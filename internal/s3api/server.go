// Package s3api implements the S3 REST API over the object service:
// request routing (path- and virtual-host-style), SigV4 authentication,
// authorisation, XML marshalling and the exact AWS error responses.
package s3api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/iam"
	"gitlab.com/Birdsall/opens3/internal/kms"
	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/object"
)

// Config for the API server.
type Config struct {
	Region string
	// Domains enables virtual-host-style addressing: a Host of
	// "<bucket>.<domain>" selects the bucket.
	Domains []string
	// EnforceRegion rejects signatures whose scope region differs.
	EnforceRegion bool
	// HostID is returned in error responses.
	HostID string
	// RequireTLSForSSEC rejects SSE-C over plaintext connections (AWS does).
	RequireTLSForSSEC bool
}

// Server serves the S3 API.
type Server struct {
	obj *object.Service
	iam *iam.Store
	kms *kms.Local
	cfg Config
	log *slog.Logger
	// Metrics hooks (set by the server package); nil-safe.
	OnRequest func(op string, status int, dur time.Duration, bytesIn, bytesOut int64)
	// ValidateTarget checks that a notification destination ARN is
	// configured (set by the notification subsystem); nil accepts all.
	ValidateTarget func(arn string) error
}

// New creates the API server.
func New(obj *object.Service, ia *iam.Store, k *kms.Local, cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Region == "" {
		cfg.Region = obj.Region()
	}
	if cfg.HostID == "" {
		cfg.HostID = "opens3"
	}
	return &Server{obj: obj, iam: ia, kms: k, cfg: cfg, log: log}
}

// ctxKey is the context key type.
type ctxKey int

const (
	ctxReqID ctxKey = iota
	ctxIdentity
)

// reqCtx is the per-request state passed to handlers.
type reqCtx struct {
	w        http.ResponseWriter
	r        *http.Request
	id       string // request id
	bucket   string
	key      string
	op       *operation
	identity *iam.Identity // nil = anonymous
	bkt      *meta.Bucket  // loaded bucket (nil for service-level ops)
	authType string        // "" | "REST-HEADER" | "REST-QUERY-STRING" | "POST"
	sigVer   string        // "AWS4-HMAC-SHA256" | "AWS"
	sigAge   time.Duration
	// body is the request body after decoding aws-chunked / trailers.
	body      *bodyReader
	startTime time.Time
	// Extra values for the object-level authorisation.
	objMeta *meta.Object
	status  int
	written int64
}

// actor returns the object.Actor for the caller.
func (c *reqCtx) actor() object.Actor {
	if c.identity == nil {
		return object.Actor{CanonicalID: "anonymous", DisplayName: "anonymous"}
	}
	return object.Actor{CanonicalID: c.identity.CanonicalID(), DisplayName: c.identity.Name()}
}

func newRequestID() string {
	var b [8]byte
	rand.Read(b[:])
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

// statusWriter records the status and bytes written.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w}
	c := &reqCtx{w: sw, r: r, id: newRequestID(), startTime: start}
	sw.Header().Set("x-amz-request-id", c.id)
	sw.Header().Set("x-amz-id-2", s.cfg.HostID)
	sw.Header().Set("Server", "OpenS3")
	sw.Header().Set("Accept-Ranges", "bytes")
	r = r.WithContext(context.WithValue(r.Context(), ctxReqID, c.id))
	c.r = r

	s.parseTarget(c)
	c.op = route(c)
	opName := "Unknown"
	if c.op != nil {
		opName = c.op.name
	}
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("panic", "op", opName, "err", rec)
			s.writeError(c, errInternal())
		}
		if s.OnRequest != nil {
			s.OnRequest(opName, sw.status, time.Since(start), c.bytesIn(), sw.written)
		}
	}()

	if c.op == nil {
		s.writeError(c, errNotImplemented("operation not supported"))
		return
	}
	// CORS preflight needs no auth.
	if c.op.name == "PreflightOptions" {
		s.handlePreflight(c)
		return
	}
	if err := s.authenticate(c); err != nil {
		s.writeError(c, err)
		return
	}
	if err := s.authorize(c); err != nil {
		s.writeError(c, err)
		return
	}
	s.applyCORS(c)
	if err := c.op.handler(s, c); err != nil {
		s.writeError(c, err)
	}
}

func (c *reqCtx) bytesIn() int64 {
	if c.body != nil {
		return c.body.n
	}
	return 0
}

// parseTarget extracts bucket and key from host and path.
func (s *Server) parseTarget(c *reqCtx) {
	host := c.r.Host
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	path := c.r.URL.Path
	for _, d := range s.cfg.Domains {
		if strings.HasSuffix(host, "."+d) {
			c.bucket = strings.TrimSuffix(host, "."+d)
			c.key = strings.TrimPrefix(path, "/")
			return
		}
	}
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return
	}
	if i := strings.IndexByte(path, '/'); i >= 0 {
		c.bucket = path[:i]
		c.key = path[i+1:]
	} else {
		c.bucket = path
	}
}
