package integration

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/edward-b-1/opens3/internal/server"
)

// TestSelfSignedTLS starts the real listener with `--tls self-signed`,
// trusts the generated certificate file the way a client would, and
// drives the S3 API over HTTPS; a second start reuses the certificate.
func TestSelfSignedTLS(t *testing.T) {
	root := t.TempDir()
	start := func() (*server.Server, context.CancelFunc) {
		srv, err := server.New(server.Config{Root: root, Address: "127.0.0.1:0", TLS: "self-signed", RootUser: rootUser, RootPassword: rootPass, NoFsync: true,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- srv.ListenAndServe(ctx) }()
		for i := 0; i < 100 && srv.Addr() == nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if srv.Addr() == nil {
			t.Fatal("server did not start listening")
		}
		return srv, func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("server exit: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("server did not stop")
			}
		}
	}

	srv, stop := start()
	certPath := filepath.Join(root, "tls", "tls.crt")
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("certificate file: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("certificate file is not PEM")
	}
	if len(srv.TLSFingerprint()) != 95 {
		t.Fatalf("fingerprint %q", srv.TLSFingerprint())
	}
	url := "https://" + srv.Addr().String()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	// Health over HTTPS; no HSTS for a self-signed certificate.
	resp, err := client.Get(url + "/opens3/health/ready")
	if err != nil {
		t.Fatalf("https health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Strict-Transport-Security") != "" {
		t.Fatalf("health: %d hsts=%q", resp.StatusCode, resp.Header.Get("Strict-Transport-Security"))
	}
	// Without trusting the file the handshake fails, as it must.
	if _, err := http.Get(url + "/opens3/health/ready"); err == nil {
		t.Fatal("untrusted client succeeded")
	}
	// Plain HTTP on the TLS port gets the API error, not a hang.
	plain, err := http.Get("http://" + srv.Addr().String() + "/bucket")
	if err != nil {
		t.Fatalf("plain http: %v", err)
	}
	body, _ := io.ReadAll(plain.Body)
	plain.Body.Close()
	if plain.StatusCode != 400 || !strings.Contains(string(body), "InvalidRequest") {
		t.Fatalf("plain http on tls port: %d %s", plain.StatusCode, body)
	}

	// The S3 API over HTTPS with the SDK.
	s3c := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(url), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(rootUser, rootPass, ""), HTTPClient: client})
	ctx := context.Background()
	if _, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("tls")}); err != nil {
		t.Fatalf("create bucket over https: %v", err)
	}
	if _, err := s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("tls"), Key: aws.String("k"), Body: strings.NewReader("over tls")}); err != nil {
		t.Fatalf("put over https: %v", err)
	}
	got, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("tls"), Key: aws.String("k")})
	if err != nil {
		t.Fatalf("get over https: %v", err)
	}
	b, _ := io.ReadAll(got.Body)
	if string(b) != "over tls" {
		t.Fatalf("body %q", b)
	}
	fp := srv.TLSFingerprint()
	stop()

	// A restart reuses the same certificate, so a client that trusted the
	// file keeps working.
	srv, stop = start()
	defer stop()
	if srv.TLSFingerprint() != fp {
		t.Fatalf("certificate changed across restart: %s vs %s", srv.TLSFingerprint(), fp)
	}
	resp, err = client.Get("https://" + srv.Addr().String() + "/opens3/health/ready")
	if err != nil {
		t.Fatalf("https after restart: %v", err)
	}
	resp.Body.Close()
}
