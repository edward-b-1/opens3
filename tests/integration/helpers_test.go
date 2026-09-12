// Package integration drives a real aws-sdk-go-v2 client against an
// in-process OpenS3 server. These tests are the first conformance line:
// if the official SDK cannot do something here, a user cannot either.
package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"gitlab.com/Birdsall/opens3/internal/server"
)

const (
	rootUser = "opens3root"
	rootPass = "opens3rootsecret"
)

type env struct {
	t   *testing.T
	srv *server.Server
	ts  *httptest.Server
	s3  *s3.Client
	ctx context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	lvl := slog.LevelWarn
	if os.Getenv("OPENS3_TEST_DEBUG") != "" {
		lvl = slog.LevelDebug
	}
	srv, err := server.New(server.Config{Root: root, RootUser: rootUser, RootPassword: rootPass, Region: "us-east-1", NoFsync: true,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	e := &env{t: t, srv: srv, ts: ts, ctx: context.Background()}
	e.s3 = e.client(rootUser, rootPass, "")
	return e
}

func (e *env) client(ak, sk, token string) *s3.Client {
	return s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(e.ts.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(ak, sk, token),
		HTTPClient:   &http.Client{Transport: dumpTransport{http.DefaultTransport}},
	})
}

func (e *env) anon() *s3.Client {
	return s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), UsePathStyle: true, Credentials: aws.AnonymousCredentials{}})
}

func (e *env) mkBucket(name string) {
	e.t.Helper()
	if _, err := e.s3.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String(name)}); err != nil {
		e.t.Fatalf("create bucket %s: %v", name, err)
	}
}

func (e *env) put(bucket, key, body string) *s3.PutObjectOutput {
	e.t.Helper()
	out, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(body)})
	if err != nil {
		e.t.Fatalf("put %s/%s: %v", bucket, key, err)
	}
	return out
}

func (e *env) get(bucket, key string) (string, *s3.GetObjectOutput) {
	e.t.Helper()
	out, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		e.t.Fatalf("get %s/%s: %v", bucket, key, err)
	}
	defer out.Body.Close()
	b, _ := io.ReadAll(out.Body)
	return string(b), out
}

// errCode extracts the S3 error code from an SDK error.
func errCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	if err == nil {
		return ""
	}
	return "unknown: " + err.Error()
}

func httpStatus(err error) int {
	var re *smithyHTTPResponseError
	_ = re
	var rerr interface{ HTTPStatusCode() int }
	if errors.As(err, &rerr) {
		return rerr.HTTPStatusCode()
	}
	return 0
}

type smithyHTTPResponseError struct{}

func rawGet(url string) (*http.Response, string) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}
