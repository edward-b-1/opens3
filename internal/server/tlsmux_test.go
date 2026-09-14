package server

import (
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
	"strings"
	"testing"
	"time"
)

func writeSelfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	cp, kp := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cp, kp
}

func TestTLSListenerRedirectsPlainHTTP(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	s, err := New(Config{Root: dir, Address: addr, RootUser: "rootuser", RootPassword: "rootsecret", NoFsync: true, TLSCert: cert, TLSKey: key,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := contextWithCancel()
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx) }()
	waitForPort(t, addr)

	// HTTPS works and carries HSTS.
	tc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := tc.Get("https://" + addr + "/opens3/health/ready")
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("https: %v %v", err, resp)
	}
	resp.Body.Close()

	// Plain HTTP on the same port: browsers are redirected.
	pc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = pc.Get("http://" + addr + "/console/?x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "https://"+addr+"/console/?x=1" {
		t.Fatalf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	req, _ := http.NewRequest("PUT", "http://"+addr+"/console/api/x", nil)
	req.Header.Set("Accept", "text/html")
	resp, err = pc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 308 {
		t.Fatalf("PUT redirect: %d", resp.StatusCode)
	}
	// S3 clients get an S3 error naming the https URL, never a redirect.
	req, _ = http.NewRequest("GET", "http://"+addr+"/bucket/?list-type=2", nil)
	resp, err = pc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(body), "<Code>InvalidRequest</Code>") || !strings.Contains(string(body), "https://"+addr+"/bucket/") {
		t.Fatalf("s3 client: %d %s", resp.StatusCode, body)
	}
	// Garbage is dropped without a panic.
	c, _ := net.Dial("tcp", addr)
	c.Write([]byte("\x00\x01garbage"))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("garbage should get no response")
	}
	c.Close()

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitForPort(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not start")
}
