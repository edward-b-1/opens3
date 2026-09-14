package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// metricValue reads a gauge or counter without the testutil package.
func metricValue(m prometheus.Metric) float64 {
	var out dto.Metric
	if err := m.Write(&out); err != nil {
		return -1
	}
	if out.Gauge != nil {
		return out.Gauge.GetValue()
	}
	return out.Counter.GetValue()
}

// writeCertPair writes a self-signed pair with the given validity and
// returns the certificate's fingerprint.
func writeCertPair(t *testing.T, certPath, keyPath string, notAfter time.Time) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: notAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	if err := writeFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return certFingerprint(leaf)
}

// startTLS runs ListenAndServe on a free port and returns the server and
// a stop function.
func startTLS(t *testing.T, cfg Config) (*Server, func()) {
	t.Helper()
	cfg.Address = "127.0.0.1:0"
	cfg.RootUser, cfg.RootPassword, cfg.NoFsync = "root", "rootsecret", true
	cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()
	for i := 0; i < 100 && s.Addr() == nil; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Addr() == nil {
		t.Fatal("not listening")
	}
	return s, func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("server exit: %v", err)
		}
	}
}

// servedFingerprint connects and reports the fingerprint of the
// certificate the server presented.
func servedFingerprint(t *testing.T, addr net.Addr) string {
	t.Helper()
	conn, err := tls.Dial("tcp", addr.String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	return certFingerprint(conn.ConnectionState().PeerCertificates[0])
}

func TestTLSReloadFromFiles(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	fp1 := writeCertPair(t, certPath, keyPath, time.Now().Add(24*time.Hour))
	s, stop := startTLS(t, Config{Root: filepath.Join(dir, "data"), TLSCert: certPath, TLSKey: keyPath})
	defer stop()
	if got := servedFingerprint(t, s.Addr()); got != fp1 || s.TLSFingerprint() != fp1 {
		t.Fatalf("initial certificate: served %s, reported %s, want %s", got, s.TLSFingerprint(), fp1)
	}
	if v := metricValue(s.mx.tlsNotAfter); v < float64(time.Now().Add(23*time.Hour).Unix()) {
		t.Fatalf("not_after gauge %v", v)
	}

	// A connection made before the reload stays up on the old certificate.
	old, err := tls.Dial("tcp", s.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()

	// Replace the pair and reload on request: new connections get the new
	// certificate, the old connection still works.
	fp2 := writeCertPair(t, certPath, keyPath, time.Now().Add(48*time.Hour))
	if err := s.ReloadTLS(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := servedFingerprint(t, s.Addr()); got != fp2 {
		t.Fatalf("after reload: served %s, want %s", got, fp2)
	}
	if _, err := old.Write([]byte("GET /opens3/health/live HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatalf("old connection: %v", err)
	}
	old.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	if n, _ := old.Read(buf); n == 0 {
		t.Fatal("old connection got no response after the reload")
	}
	if metricValue(s.mx.tlsReloads.WithLabelValues("ok")) != 2 { // start + reload
		t.Fatalf("ok reloads: %v", metricValue(s.mx.tlsReloads.WithLabelValues("ok")))
	}

	// A corrupt certificate is refused and the current one stays.
	os.WriteFile(certPath, []byte("not a certificate"), 0o644)
	if err := s.ReloadTLS(); err == nil {
		t.Fatal("corrupt certificate accepted")
	}
	if got := servedFingerprint(t, s.Addr()); got != fp2 {
		t.Fatalf("after failed reload: served %s, want %s", got, fp2)
	}
	// A key that does not match the certificate is refused too.
	fp3 := writeCertPair(t, certPath, keyPath, time.Now().Add(72*time.Hour))
	writeCertPair(t, filepath.Join(dir, "other.crt"), keyPath, time.Now().Add(72*time.Hour)) // overwrites the key only
	if err := s.ReloadTLS(); err == nil {
		t.Fatal("mismatched key accepted")
	}
	if got := servedFingerprint(t, s.Addr()); got != fp2 {
		t.Fatalf("after mismatched reload: served %s, want %s", got, fp2)
	}
	// An expired certificate is refused.
	writeCertPair(t, certPath, keyPath, time.Now().Add(-time.Hour))
	if err := s.ReloadTLS(); err == nil {
		t.Fatal("expired certificate accepted")
	}
	if metricValue(s.mx.tlsReloads.WithLabelValues("error")) != 3 {
		t.Fatalf("error reloads: %v", metricValue(s.mx.tlsReloads.WithLabelValues("error")))
	}
	_ = fp3

	// The poll notices a change on disk without a request.
	fp4 := writeCertPair(t, certPath, keyPath, time.Now().Add(96*time.Hour))
	s.certs.check()
	if got := servedFingerprint(t, s.Addr()); got != fp4 {
		t.Fatalf("after poll: served %s, want %s", got, fp4)
	}
	s.certs.check() // nothing changed
	if metricValue(s.mx.tlsReloads.WithLabelValues("unchanged")) != 0 {
		t.Fatal("an unchanged poll must not reload")
	}
	if err := s.ReloadTLS(); err != nil || metricValue(s.mx.tlsReloads.WithLabelValues("unchanged")) != 1 {
		t.Fatalf("requested reload of the same files: %v", err)
	}
}

func TestTLSReloadSelfSignedRenewsWhenExpired(t *testing.T) {
	root := t.TempDir()
	s, stop := startTLS(t, Config{Root: root, TLS: "self-signed"})
	defer stop()
	fp1 := s.TLSFingerprint()
	if servedFingerprint(t, s.Addr()) != fp1 {
		t.Fatal("served certificate differs from the reported one")
	}
	// Replace the pair on disk with an expired one, as if time had passed:
	// the poll regenerates it.
	dir := filepath.Join(root, selfSignedDir)
	writeCertPair(t, filepath.Join(dir, selfSignedCert), filepath.Join(dir, selfSignedKey), time.Now().Add(-time.Minute))
	s.certs.check()
	fp2 := s.TLSFingerprint()
	if fp2 == fp1 || servedFingerprint(t, s.Addr()) != fp2 {
		t.Fatalf("expired self-signed certificate not regenerated: %s -> %s", fp1, fp2)
	}
	if lc := s.certs.cur.Load(); time.Until(lc.leaf.NotAfter) < 800*24*time.Hour {
		t.Fatalf("regenerated certificate validity: %v", lc.leaf.NotAfter)
	}
	// Deleting the directory and reloading generates a fresh pair.
	os.RemoveAll(dir)
	if err := s.ReloadTLS(); err != nil {
		t.Fatal(err)
	}
	if s.TLSFingerprint() == fp2 {
		t.Fatal("deleted pair not regenerated")
	}
	// The watch loop runs: with a short interval a change is noticed
	// without calling check.
	tlsCheckInterval = 20 * time.Millisecond
	defer func() { tlsCheckInterval = time.Minute }()
	s2, stop2 := startTLS(t, Config{Root: t.TempDir(), TLS: "self-signed"})
	defer stop2()
	before := s2.TLSFingerprint()
	dir2 := filepath.Join(s2.cfg.Root, selfSignedDir)
	writeCertPair(t, filepath.Join(dir2, selfSignedCert), filepath.Join(dir2, selfSignedKey), time.Now().Add(time.Hour))
	deadline := time.Now().Add(5 * time.Second)
	for s2.TLSFingerprint() == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s2.TLSFingerprint() == before {
		t.Fatal("watch loop did not pick up the changed file")
	}
	// HSTS follows the certificate: self-signed, so not sent.
	resp, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}).Get("https://" + s2.Addr().String() + "/opens3/health/live")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS sent for a self-signed certificate")
	}
}
