// Package server assembles the storage stack (KV, blobs, KMS, IAM, object
// service, S3 API) from a configuration and serves it over HTTP.
package server

import (
	"context"
	"crypto/tls"
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

	"gitlab.com/Birdsall/opens3/internal/blob"
	"gitlab.com/Birdsall/opens3/internal/iam"
	"gitlab.com/Birdsall/opens3/internal/kms"
	"gitlab.com/Birdsall/opens3/internal/kv"
	"gitlab.com/Birdsall/opens3/internal/object"
	"gitlab.com/Birdsall/opens3/internal/s3api"
)

// Config is the server configuration.
type Config struct {
	Root          string // data directory
	Address       string // listen address, e.g. ":9000"
	Region        string
	Domains       []string // virtual-host-style domains
	RootUser      string
	RootPassword  string
	MasterKey     string // KMS master key material (defaults to derived from root password; set explicitly in production)
	TLSCert       string
	TLSKey        string
	EnforceRegion bool
	NoFsync       bool
	AccountID     string
	Log           *slog.Logger
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
	master := cfg.MasterKey
	if master == "" {
		master = "derived:" + cfg.RootPassword
	}
	k, err := kms.NewLocal(db, []byte(master))
	if err != nil {
		db.Close()
		return nil, err
	}
	ia, err := iam.Open(db, iam.Config{RootAccessKey: cfg.RootUser, RootSecretKey: cfg.RootPassword, MasterKey: k.MasterKey(), AccountID: cfg.AccountID})
	if err != nil {
		db.Close()
		return nil, err
	}
	obj := object.New(db, bs, k, cfg.Region, cfg.Log)
	api := s3api.New(obj, ia, k, s3api.Config{Region: cfg.Region, Domains: cfg.Domains, EnforceRegion: cfg.EnforceRegion, HostID: hostID()}, cfg.Log)
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
	s.http = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	if s.cfg.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
		if err != nil {
			return err
		}
		s.http.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		ln = tls.NewListener(ln, s.http.TLSConfig)
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
	cfg.AccountID = get("ACCOUNT_ID", cfg.AccountID)
	if d := get("DOMAINS", ""); d != "" {
		cfg.Domains = strings.Split(d, ",")
	}
	return cfg
}

// String describes the config without secrets.
func (c Config) String() string {
	return fmt.Sprintf("root=%s address=%s region=%s domains=%v tls=%v", c.Root, c.Address, c.Region, c.Domains, c.TLSCert != "")
}
