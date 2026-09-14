package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Certificate reload without a restart. The TLS config hands out the
// certificate through a callback that reads an atomic pointer, so a swap
// is one pointer write. Reloads happen when the certificate or key file
// changes on disk (polled every tlsCheckInterval), when a self-signed
// certificate expires (it is regenerated), and on demand (SIGHUP, via
// Server.ReloadTLS). A new pair is validated before it is swapped in; a
// bad file is logged and the current certificate stays in service.

// tlsCheckInterval is how often the certificate files are checked for
// changes (a variable so tests can shorten it).
var tlsCheckInterval = time.Minute

// loadedCert is one certificate in service with what the server derives
// from it.
type loadedCert struct {
	cert        tls.Certificate
	leaf        *x509.Certificate
	selfSigned  bool
	fingerprint string
	stat        fileStat
}

// fileStat identifies a version of the certificate and key files.
type fileStat struct {
	certSize, keySize   int64
	certMod, keyMod     time.Time
	certMissing, keyErr bool
}

func statPair(certPath, keyPath string) fileStat {
	var fs fileStat
	if st, err := os.Stat(certPath); err == nil {
		fs.certSize, fs.certMod = st.Size(), st.ModTime()
	} else {
		fs.certMissing = true
	}
	if st, err := os.Stat(keyPath); err == nil {
		fs.keySize, fs.keyMod = st.Size(), st.ModTime()
	} else {
		fs.keyErr = true
	}
	return fs
}

// certSource owns the certificate in service.
type certSource struct {
	s        *Server
	mu       sync.Mutex // serialises reloads
	cur      atomic.Pointer[loadedCert]
	interval time.Duration // poll interval, fixed at creation
}

func newCertSource(s *Server) *certSource { return &certSource{s: s, interval: tlsCheckInterval} }

// paths returns the certificate and key file paths for the configured mode.
func (c *certSource) paths() (certPath, keyPath string) {
	if c.s.cfg.TLSCert != "" {
		return c.s.cfg.TLSCert, c.s.cfg.TLSKey
	}
	dir := filepath.Join(c.s.cfg.Root, selfSignedDir)
	return filepath.Join(dir, selfSignedCert), filepath.Join(dir, selfSignedKey)
}

// load reads (or, for self-signed, generates) the certificate and
// validates it. reason says why, for the log.
func (c *certSource) load(reason string) (*loadedCert, error) {
	s := c.s
	certPath, keyPath := c.paths()
	now := time.Now()
	lc := &loadedCert{}
	switch {
	case s.cfg.TLSCert != "":
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, err
		}
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", certPath, err)
		}
		if now.After(leaf.NotAfter) {
			return nil, fmt.Errorf("%s: certificate expired on %s", certPath, leaf.NotAfter.UTC().Format(time.RFC3339))
		}
		lc.cert, lc.leaf = cert, leaf
		lc.selfSigned = bytes.Equal(leaf.RawIssuer, leaf.RawSubject)
		lc.fingerprint = certFingerprint(leaf)
		lc.stat = statPair(certPath, keyPath)
		s.log.Info("tls certificate", "source", "files", "reason", reason, "certificate", certPath, "sha256_fingerprint", lc.fingerprint,
			"self_signed", lc.selfSigned, "not_after", leaf.NotAfter.UTC().Format(time.RFC3339))
	case s.cfg.TLS == "self-signed":
		ss, err := loadOrCreateSelfSigned(filepath.Dir(certPath), s.cfg.Address, s.cfg.Domains, now)
		if err != nil {
			return nil, err
		}
		lc.cert, lc.leaf, lc.selfSigned, lc.fingerprint = ss.cert, ss.leaf, true, ss.fingerprint
		lc.stat = statPair(certPath, keyPath)
		attrs := []any{"source", "self-signed", "reason", reason, "certificate", ss.certPath, "sha256_fingerprint", ss.fingerprint,
			"names", ss.names(), "not_after", ss.leaf.NotAfter.UTC().Format(time.RFC3339)}
		switch {
		case ss.created:
			s.log.Warn("generated a self-signed TLS certificate; clients must trust this file or pin the fingerprint", attrs...)
		case ss.renewed:
			s.log.Warn("the self-signed TLS certificate had expired and was replaced; clients that pinned the old fingerprint must update", attrs...)
		default:
			s.log.Info("tls certificate", attrs...)
		}
		if left := time.Until(ss.leaf.NotAfter); left < selfSignedWarnDays*24*time.Hour {
			s.log.Warn("the self-signed TLS certificate expires soon and will be replaced when it does", "days_left", int(left.Hours()/24))
		}
		if len(ss.missing) > 0 {
			s.log.Warn("the self-signed TLS certificate does not cover every name this server answers to; delete it to generate a new one", "missing", ss.missing)
		}
	default:
		return nil, errors.New("tls is not enabled")
	}
	return lc, nil
}

// reload loads and, if that succeeds, swaps the certificate. It reports
// "ok", "unchanged" or "error" in the reload metric.
func (c *certSource) reload(reason string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	lc, err := c.load(reason)
	if err != nil {
		c.s.mx.tlsReloads.WithLabelValues("error").Inc()
		c.s.log.Error("tls certificate reload failed; the current certificate stays in service", "reason", reason, "error", err)
		return err
	}
	if old := c.cur.Load(); old != nil && old.fingerprint == lc.fingerprint {
		// Same certificate (perhaps rewritten files): remember the new file
		// identity without touching the record readers may hold.
		n := *old
		n.stat = lc.stat
		c.cur.Store(&n)
		c.s.mx.tlsReloads.WithLabelValues("unchanged").Inc()
		return nil
	}
	c.cur.Store(lc)
	c.s.mx.tlsNotAfter.Set(float64(lc.leaf.NotAfter.Unix()))
	c.s.mx.tlsReloads.WithLabelValues("ok").Inc()
	return nil
}

// get is the tls.Config GetCertificate callback.
func (c *certSource) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	lc := c.cur.Load()
	if lc == nil {
		return nil, errors.New("tls: no certificate loaded")
	}
	return &lc.cert, nil
}

// sendHSTS decides per request, since a reload can change whether the
// certificate is self-signed.
func (c *certSource) sendHSTS() bool {
	lc := c.cur.Load()
	if lc == nil {
		return false
	}
	return (c.s.cfg.HSTS || !lc.selfSigned) && !c.s.cfg.NoHSTS
}

// watch polls the files until ctx ends and reloads on a change; a
// self-signed certificate is also reloaded (regenerated) once expired.
func (c *certSource) watch(ctx context.Context) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.check()
	}
}

// check runs one poll iteration (exposed for tests).
func (c *certSource) check() {
	lc := c.cur.Load()
	if lc == nil {
		return
	}
	certPath, keyPath := c.paths()
	now := statPair(certPath, keyPath)
	switch {
	case now != lc.stat:
		_ = c.reload("files changed")
	case c.s.cfg.TLS == "self-signed" && time.Now().After(lc.leaf.NotAfter):
		_ = c.reload("expired")
	}
}

// ReloadTLS reloads the certificate from its source (the operator's
// files, or the self-signed pair under the data root, regenerating it if
// missing or expired). It is what SIGHUP triggers. With TLS off it does
// nothing.
func (s *Server) ReloadTLS() error {
	if s.certs == nil {
		return nil
	}
	return s.certs.reload("requested")
}

// TLSFingerprint is the SHA-256 fingerprint of the certificate in
// service, or "" when TLS is off.
func (s *Server) TLSFingerprint() string {
	if s.certs == nil {
		return ""
	}
	if lc := s.certs.cur.Load(); lc != nil {
		return lc.fingerprint
	}
	return ""
}

// hstsIf sends Strict-Transport-Security when decide says so.
func hstsIf(next http.Handler, decide func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if decide() {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000")
		}
		next.ServeHTTP(w, r)
	})
}
