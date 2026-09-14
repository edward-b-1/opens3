// Package server assembles the storage stack (KV, blobs, KMS, IAM, object
// service, S3 API) from a configuration and serves it over HTTP.
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/edward-b-1/OpenS3/internal/blob"
	"github.com/edward-b-1/OpenS3/internal/iam"
	"github.com/edward-b-1/OpenS3/internal/kms"
	"github.com/edward-b-1/OpenS3/internal/kv"
	"github.com/edward-b-1/OpenS3/internal/object"
	"github.com/edward-b-1/OpenS3/internal/s3api"
)

// Config is the server configuration.
type Config struct {
	Root         string // data directory
	Address      string // listen address, e.g. ":9000"
	Region       string
	Domains      []string // virtual-host-style domains
	RootUser     string
	RootPassword string
	// MasterKey is optional operator-supplied master key material (at least
	// 32 characters of random data). When empty a random key is generated
	// on first start and kept in <root>/meta/master.keys (mode 0600).
	MasterKey string
	TLSCert   string
	TLSKey    string
	// HSTS controls the Strict-Transport-Security header sent over TLS. By
	// default it is sent only when the certificate is not self-signed, since
	// browsers remember it for the whole host name for two years and a
	// self-signed certificate usually means an experimental setup. HSTS
	// forces it on; NoHSTS forces it off.
	HSTS          bool
	NoHSTS        bool
	EnforceRegion bool
	NoFsync       bool
	AccountID     string
	// DefaultOwnership for new buckets: BucketOwnerEnforced (AWS default,
	// ACLs disabled) or ObjectWriter / BucketOwnerPreferred (ACLs enabled).
	DefaultOwnership string
	Log              *slog.Logger
}

// Extension hooks let subsystems (lifecycle, notifications, admin API,
// console) wire themselves in from their own files without editing the
// core assembly. Register from an init() in package server.
var (
	extensions []func(*Server) error
	mounts     []func(*Server, *http.ServeMux)
	stoppers   []func(*Server)
)

// RegisterExtension adds a function run after the core stack is built.
func RegisterExtension(f func(*Server) error) { extensions = append(extensions, f) }

// RegisterMount adds a function that mounts HTTP routes on the mux before
// the S3 API catch-all.
func RegisterMount(f func(*Server, *http.ServeMux)) { mounts = append(mounts, f) }

// RegisterStopper adds a function run on Close.
func RegisterStopper(f func(*Server)) { stoppers = append(stoppers, f) }

// Server is an assembled OpenS3 node.
type Server struct {
	cfg  Config
	log  *slog.Logger
	kv   kv.Store
	blob blob.Store
	KMS  *kms.Local
	IAM  *iam.Store
	Obj  *object.Service
	API  *s3api.Server
	http *http.Server
	reg  *prometheus.Registry
	mx   *metrics
	// Registry is the Prometheus registry for subsystem metrics.
	Registry *prometheus.Registry
	// Ext holds subsystem state keyed by name (set by extensions).
	Ext map[string]any
}

// New builds the stack. It does not listen.
func New(cfg Config) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.RootUser == "" || cfg.RootPassword == "" {
		return nil, errors.New("root credentials are required: set OPENS3_ROOT_USER and OPENS3_ROOT_PASSWORD")
	}
	if cfg.Root == "" {
		return nil, errors.New("data root directory is required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if err := os.MkdirAll(cfg.Root, 0o755); err != nil {
		return nil, err
	}
	db, err := kv.OpenBolt(filepath.Join(cfg.Root, "meta", "opens3.db"))
	if err != nil {
		return nil, err
	}
	bs, err := blob.OpenFS(cfg.Root, !cfg.NoFsync)
	if err != nil {
		db.Close()
		return nil, err
	}
	var master *kms.Master
	if cfg.MasterKey != "" {
		master, err = kms.MasterFromMaterial([]byte(cfg.MasterKey))
		if err != nil {
			db.Close()
			return nil, err
		}
	} else {
		var created bool
		master, created, err = kms.LoadOrCreateMasterFile(filepath.Join(cfg.Root, "meta", "master.keys"))
		if err != nil {
			db.Close()
			return nil, err
		}
		if created {
			cfg.Log.Warn("generated a new master key; back it up, everything encrypted and every stored secret depends on it",
				"file", filepath.Join(cfg.Root, "meta", "master.keys"))
		}
	}
	k, err := kms.NewLocal(db, master)
	if err != nil {
		db.Close()
		if errors.Is(err, kms.ErrMasterMismatch) {
			return nil, fmt.Errorf("%w: the data directory was created with a different master key (source %s)", err, master.Source())
		}
		return nil, err
	}
	cfg.Log.Info("master key", "source", master.Source(), "fingerprint", master.Fingerprint(), "keys", master.Keys())
	ia, err := iam.Open(db, iam.Config{RootAccessKey: cfg.RootUser, RootSecretKey: cfg.RootPassword, Wrapper: master, AccountID: cfg.AccountID})
	if err != nil {
		db.Close()
		return nil, err
	}
	obj := object.New(db, bs, k, cfg.Region, cfg.Log)
	api := s3api.New(obj, ia, k, s3api.Config{Region: cfg.Region, Domains: cfg.Domains, EnforceRegion: cfg.EnforceRegion, HostID: hostID(), DefaultOwnership: cfg.DefaultOwnership}, cfg.Log)
	s := &Server{cfg: cfg, log: cfg.Log, kv: db, blob: bs, KMS: k, IAM: ia, Obj: obj, API: api, reg: prometheus.NewRegistry(), Ext: map[string]any{}}
	s.mx = newMetrics(s.reg)
	api.OnRequest = s.mx.observe
	s.Registry = s.reg
	for _, ext := range extensions {
		if err := ext(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

// Config returns the configuration.
func (s *Server) Config() Config { return s.cfg }

// Log returns the logger.
func (s *Server) Log() *slog.Logger { return s.log }

// KV exposes the metadata store.
func (s *Server) KV() kv.Store { return s.kv }

func hostID() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "opens3"
	}
	return h
}

// Handler returns the full HTTP handler: S3 API plus health and metrics.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/opens3/health/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/opens3/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.blob.Stats(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/opens3/metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	for _, m := range mounts {
		m(s, mux)
	}
	mux.Handle("/", s.API.Handler())
	return mux
}

// ListenAndServe runs the HTTP(S) server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return err
	}
	handler := s.Handler()
	var cert tls.Certificate
	if s.cfg.TLSCert != "" {
		var err error
		cert, err = tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
		if err != nil {
			return err
		}
		selfSigned := false
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			selfSigned = bytes.Equal(leaf.RawIssuer, leaf.RawSubject)
		}
		sendHSTS := (s.cfg.HSTS || !selfSigned) && !s.cfg.NoHSTS
		s.log.Info("tls", "certificate", s.cfg.TLSCert, "self_signed", selfSigned, "hsts", sendHSTS)
		if sendHSTS {
			handler = hsts(handler)
		}
	}
	s.http = &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	if s.cfg.TLSCert != "" {
		s.http.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}
		// Plain-HTTP connections on the same port are redirected to https.
		ln = newMuxListener(ln, s.http.TLSConfig, s.log)
	}
	s.log.Info("opens3 listening", "address", ln.Addr().String(), "root", s.cfg.Root, "tls", s.cfg.TLSCert != "")
	errc := make(chan error, 1)
	go func() { errc <- s.http.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.http.Shutdown(shutdown)
		return s.Close()
	case err := <-errc:
		s.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Close releases resources.
func (s *Server) Close() error {
	for _, st := range stoppers {
		st(s)
	}
	err := s.kv.Close()
	if berr := s.blob.Close(); err == nil {
		err = berr
	}
	return err
}

// ConfigFromEnv fills unset fields from OPENS3_* environment variables.
func ConfigFromEnv(cfg Config) Config {
	get := func(k, def string) string {
		if v := os.Getenv("OPENS3_" + k); v != "" {
			return v
		}
		return def
	}
	cfg.RootUser = get("ROOT_USER", cfg.RootUser)
	cfg.RootPassword = get("ROOT_PASSWORD", cfg.RootPassword)
	cfg.MasterKey = get("MASTER_KEY", cfg.MasterKey)
	cfg.Region = get("REGION", cfg.Region)
	cfg.Address = get("ADDRESS", cfg.Address)
	cfg.Root = get("ROOT", cfg.Root)
	cfg.TLSCert = get("TLS_CERT", cfg.TLSCert)
	cfg.TLSKey = get("TLS_KEY", cfg.TLSKey)
	if v := get("NO_HSTS", ""); v == "1" || strings.EqualFold(v, "true") {
		cfg.NoHSTS = true
	}
	if v := get("HSTS", ""); v == "1" || strings.EqualFold(v, "true") {
		cfg.HSTS = true
	}
	cfg.AccountID = get("ACCOUNT_ID", cfg.AccountID)
	cfg.DefaultOwnership = get("DEFAULT_OBJECT_OWNERSHIP", cfg.DefaultOwnership)
	if d := get("DOMAINS", ""); d != "" {
		cfg.Domains = strings.Split(d, ",")
	}
	return cfg
}

// String describes the config without secrets.
func (c Config) String() string {
	return fmt.Sprintf("root=%s address=%s region=%s domains=%v tls=%v", c.Root, c.Address, c.Region, c.Domains, c.TLSCert != "")
}
