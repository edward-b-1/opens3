package admin_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/edward-b-1/OpenS3/internal/admin"
	"github.com/edward-b-1/OpenS3/internal/auth/sigv4"
	"github.com/edward-b-1/OpenS3/internal/server"
)

const (
	rootUser = "opens3root"
	rootPass = "opens3rootsecret"
)

type env struct {
	t    *testing.T
	srv  *server.Server
	ts   *httptest.Server
	root *admin.Client
	ctx  context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	srv, err := server.New(server.Config{Root: t.TempDir(), RootUser: rootUser, RootPassword: rootPass, Region: "us-east-1", NoFsync: true,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Close() })
	e := &env{t: t, srv: srv, ts: ts, ctx: context.Background()}
	e.root = e.client(rootUser, rootPass)
	return e
}

func (e *env) client(ak, sk string) *admin.Client {
	return admin.NewClient(e.ts.URL, ak, sk)
}

func (e *env) s3(ak, sk string) *s3.Client {
	return s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(ak, sk, "")})
}

func compact(t *testing.T, doc json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, doc); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func fatal(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// noErr returns a function that accepts any (value, error) call result and
// fails the test on error.
func noErr(t *testing.T) func(any, error) {
	return func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func wantStatus(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *admin.Error
	if !errors.As(err, &ae) {
		t.Fatalf("want admin error %d %s, got %v", status, code, err)
	}
	if ae.Status != status || (code != "" && ae.Code != code) {
		t.Fatalf("want %d %s, got %d %s (%s)", status, code, ae.Status, ae.Code, ae.Message)
	}
}

func TestInfoAndHealth(t *testing.T) {
	e := newEnv(t)
	if err := e.root.Health(e.ctx); err != nil {
		t.Fatal(err)
	}
	s3c := e.s3(rootUser, rootPass)
	noErr(t)(s3c.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String("bucket1")}))
	noErr(t)(s3c.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("bucket1"), Key: aws.String("k"), Body: strings.NewReader("hello")}))
	info, err := e.root.Info(e.ctx)
	fatal(t, err)
	if info.Buckets != 1 || info.Objects != 1 || info.TotalBytes != 5 || info.Region != "us-east-1" || info.Version == "" {
		t.Fatalf("bad info: %+v", info)
	}
	if info.Disk.TotalBytes == 0 {
		t.Fatalf("no disk stats: %+v", info)
	}
}

func TestUsersKeysPoliciesGroups(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	c := e.root

	// Policy.
	doc := json.RawMessage(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets","s3:ListBucket","s3:GetObject"],"Resource":["arn:aws:s3:::*"]}]}`)
	p, err := c.PutPolicy(ctx, "listers", doc)
	fatal(t, err)
	if p.Name != "listers" || p.BuiltIn || len(p.Document) == 0 {
		t.Fatalf("bad policy: %+v", p)
	}
	_, err = c.PutPolicy(ctx, "broken", json.RawMessage(`{"Statement": 1}`))
	wantStatus(t, err, 400, "InvalidArgument")
	_, err = c.PutPolicy(ctx, "readonly", doc)
	wantStatus(t, err, 400, "InvalidArgument") // built-in
	ps, err := c.ListPolicies(ctx)
	fatal(t, err)
	found := false
	for _, p := range ps {
		if p.Name == "listers" {
			found = true
		}
		if p.Document != nil {
			t.Fatalf("list must not include documents: %+v", p)
		}
	}
	if !found {
		t.Fatalf("policy not listed: %+v", ps)
	}
	got, err := c.GetPolicy(ctx, "listers")
	fatal(t, err)
	if compact(t, got.Document) != compact(t, doc) {
		t.Fatalf("document round trip: %s", got.Document)
	}

	// User.
	u, err := c.PutUser(ctx, "alice", admin.PutUserRequest{SecretKey: "alicesecret1", Policies: []string{"listers"}})
	fatal(t, err)
	if !u.Enabled || len(u.Policies) != 1 || u.Policies[0] != "listers" {
		t.Fatalf("bad user: %+v", u)
	}
	_, err = c.PutUser(ctx, "bob", admin.PutUserRequest{Policies: []string{"nope"}})
	wantStatus(t, err, 404, "NotFound")
	_, err = c.GetUser(ctx, "nobody")
	wantStatus(t, err, 404, "NotFound")
	_, err = c.PutUser(ctx, "root", admin.PutUserRequest{})
	wantStatus(t, err, 400, "")
	_, err = c.PutUser(ctx, "bad name", admin.PutUserRequest{})
	wantStatus(t, err, 400, "InvalidArgument")
	us, err := c.ListUsers(ctx)
	fatal(t, err)
	if len(us) != 1 || us[0].Name != "alice" {
		t.Fatalf("bad list: %+v", us)
	}

	// The initial key is named after the user and never exposes the secret.
	ks, err := c.ListKeys(ctx, "alice")
	fatal(t, err)
	if len(ks) != 1 || ks[0].AccessKey != "alice" || ks[0].Kind != "user" {
		t.Fatalf("bad keys: %+v", ks)
	}
	raw := rawGet(t, e, rootUser, rootPass, "/keys?user=alice")
	if strings.Contains(raw, "alicesecret1") || strings.Contains(strings.ToLower(raw), "secret") {
		t.Fatalf("secret leaked: %s", raw)
	}

	// Service key with session policy.
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	cred, err := c.CreateKey(ctx, admin.CreateKeyRequest{User: "alice", Kind: "service", Description: "ci", Expires: &exp,
		SessionPolicy: json.RawMessage(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets"],"Resource":["arn:aws:s3:::*"]}]}`)})
	fatal(t, err)
	if cred.SecretKey == "" || cred.AccessKey == "" || cred.User != "alice" || cred.Kind != "service" || cred.Expires == nil || !cred.Expires.Equal(exp) {
		t.Fatalf("bad credentials: %+v", cred)
	}
	_, err = c.CreateKey(ctx, admin.CreateKeyRequest{User: "alice", AccessKey: cred.AccessKey})
	wantStatus(t, err, 409, "AlreadyExists")
	_, err = c.CreateKey(ctx, admin.CreateKeyRequest{User: "ghost"})
	wantStatus(t, err, 404, "NotFound")
	_, err = c.CreateKey(ctx, admin.CreateKeyRequest{User: "alice", Kind: "sts"})
	wantStatus(t, err, 400, "InvalidArgument")
	_, err = c.CreateKey(ctx, admin.CreateKeyRequest{})
	wantStatus(t, err, 400, "InvalidArgument")

	// The new key works on the S3 API (session policy allows ListBuckets).
	if _, err := e.s3(cred.AccessKey, cred.SecretKey).ListBuckets(ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatalf("service key cannot list buckets: %v", err)
	}
	// Disable it: S3 rejects.
	k, err := c.SetKeyStatus(ctx, cred.AccessKey, false)
	fatal(t, err)
	if k.Enabled {
		t.Fatal("key still enabled")
	}
	if _, err := e.s3(cred.AccessKey, cred.SecretKey).ListBuckets(ctx, &s3.ListBucketsInput{}); err == nil {
		t.Fatal("disabled key accepted")
	}
	noErr(t)(c.SetKeyStatus(ctx, cred.AccessKey, true))
	// Rotate: old secret fails, new works.
	rot, err := c.RotateKey(ctx, cred.AccessKey, "")
	fatal(t, err)
	if rot.SecretKey == "" || rot.SecretKey == cred.SecretKey {
		t.Fatalf("rotate: %+v", rot)
	}
	if _, err := e.s3(cred.AccessKey, cred.SecretKey).ListBuckets(ctx, &s3.ListBucketsInput{}); err == nil {
		t.Fatal("old secret accepted after rotation")
	}
	if _, err := e.s3(cred.AccessKey, rot.SecretKey).ListBuckets(ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatalf("new secret rejected: %v", err)
	}
	_, err = c.RotateKey(ctx, cred.AccessKey, "short")
	wantStatus(t, err, 400, "InvalidArgument")
	if err := c.DeleteKey(ctx, cred.AccessKey); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, c.DeleteKey(ctx, cred.AccessKey), 404, "NotFound")

	// Groups.
	g, err := c.PutGroup(ctx, "devs", admin.PutGroupRequest{Members: []string{"alice"}, Policies: []string{"readonly"}})
	fatal(t, err)
	if len(g.Members) != 1 || g.Members[0] != "alice" || len(g.Policies) != 1 {
		t.Fatalf("bad group: %+v", g)
	}
	_, err = c.PutGroup(ctx, "ops", admin.PutGroupRequest{Members: []string{"ghost"}})
	wantStatus(t, err, 404, "NotFound")
	u, err = c.GetUser(ctx, "alice")
	fatal(t, err)
	if len(u.Groups) != 1 || u.Groups[0] != "devs" {
		t.Fatalf("group back-reference missing: %+v", u)
	}
	noErr(t)(c.PutUser(ctx, "bob", admin.PutUserRequest{SecretKey: "bobsecret12"}))
	g, err = c.UpdateGroupMembers(ctx, "devs", []string{"bob"}, []string{"alice"})
	fatal(t, err)
	if len(g.Members) != 1 || g.Members[0] != "bob" {
		t.Fatalf("members: %+v", g)
	}
	gs, err := c.ListGroups(ctx)
	fatal(t, err)
	if len(gs) != 1 {
		t.Fatalf("groups: %+v", gs)
	}
	// Updating with nil members keeps them.
	g, err = c.PutGroup(ctx, "devs", admin.PutGroupRequest{Policies: []string{"readwrite"}})
	fatal(t, err)
	if len(g.Members) != 1 || g.Policies[0] != "readwrite" {
		t.Fatalf("update: %+v", g)
	}
	if err := c.DeleteGroup(ctx, "devs"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, c.DeleteGroup(ctx, "devs"), 404, "NotFound")
	u, err = c.GetUser(ctx, "bob")
	fatal(t, err)
	if len(u.Groups) != 0 {
		t.Fatalf("stale group ref: %+v", u)
	}

	// User policy update, disable, delete.
	u, err = c.SetUserPolicies(ctx, "alice", []string{"readwrite"})
	fatal(t, err)
	if len(u.Policies) != 1 || u.Policies[0] != "readwrite" {
		t.Fatalf("policies: %+v", u)
	}
	u, err = c.SetUserStatus(ctx, "alice", false)
	fatal(t, err)
	if u.Enabled {
		t.Fatal("still enabled")
	}
	if _, err := e.s3("alice", "alicesecret1").ListBuckets(ctx, &s3.ListBucketsInput{}); err == nil {
		t.Fatal("disabled user accepted")
	}
	noErr(t)(c.SetUserStatus(ctx, "alice", true))
	// Rotating via PUT users with a new secret.
	noErr(t)(c.PutUser(ctx, "alice", admin.PutUserRequest{SecretKey: "alicesecret2"}))
	if _, err := e.s3("alice", "alicesecret2").ListBuckets(ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatalf("rotated user secret rejected: %v", err)
	}
	// Deleting the policy detaches it.
	if err := c.DeletePolicy(ctx, "listers"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, c.DeletePolicy(ctx, "readonly"), 400, "InvalidArgument")
	if err := c.DeleteUser(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, c.DeleteUser(ctx, "alice"), 404, "NotFound")
	ks, err = c.ListKeys(ctx, "alice")
	fatal(t, err)
	if len(ks) != 0 {
		t.Fatalf("keys survive user deletion: %+v", ks)
	}
}

func TestCreatedUserUsesS3(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	noErr(t)(e.root.PutUser(ctx, "carol", admin.PutUserRequest{SecretKey: "carolsecret1", Policies: []string{"readwrite"}}))
	s3c := e.s3("carol", "carolsecret1")
	noErr(t)(s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("carols")}))
	noErr(t)(s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("carols"), Key: aws.String("a"), Body: strings.NewReader("abc")}))
	out, err := s3c.ListBuckets(ctx, &s3.ListBucketsInput{})
	fatal(t, err)
	if len(out.Buckets) != 1 || *out.Buckets[0].Name != "carols" {
		t.Fatalf("buckets: %+v", out.Buckets)
	}
	bs, err := e.root.ListBuckets(ctx, true)
	fatal(t, err)
	if len(bs) != 1 || bs[0].Name != "carols" || bs[0].Objects == nil || *bs[0].Objects != 1 || *bs[0].Bytes != 3 {
		t.Fatalf("admin buckets: %+v", bs)
	}
	// Non-empty bucket: plain delete fails, force succeeds.
	wantStatus(t, e.root.DeleteBucket(ctx, "carols", false), 409, "BucketNotEmpty")
	if err := e.root.DeleteBucket(ctx, "carols", true); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.root.DeleteBucket(ctx, "carols", true), 404, "NoSuchBucket")
	bs, err = e.root.ListBuckets(ctx, false)
	fatal(t, err)
	if len(bs) != 0 {
		t.Fatalf("bucket survives: %+v", bs)
	}
}

func TestAuthorization(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	noErr(t)(e.root.PutUser(ctx, "plain", admin.PutUserRequest{SecretKey: "plainsecret1", Policies: []string{"readwrite"}}))
	noErr(t)(e.root.PutUser(ctx, "diag", admin.PutUserRequest{SecretKey: "diagsecret12", Policies: []string{"diagnostics"}}))
	noErr(t)(e.root.PutUser(ctx, "adm", admin.PutUserRequest{SecretKey: "admsecret123", Policies: []string{"consoleAdmin"}}))

	// A non-admin user is denied everything, even though it has s3:*.
	plain := e.client("plain", "plainsecret1")
	_, err := plain.ListUsers(ctx)
	wantStatus(t, err, 403, "AccessDenied")
	_, err = plain.Info(ctx)
	wantStatus(t, err, 403, "AccessDenied")
	wantStatus(t, plain.DeleteUser(ctx, "adm"), 403, "AccessDenied")

	// diagnostics grants only info/health.
	diag := e.client("diag", "diagsecret12")
	if _, err := diag.Info(ctx); err != nil {
		t.Fatal(err)
	}
	if err := diag.Health(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = diag.ListUsers(ctx)
	wantStatus(t, err, 403, "AccessDenied")

	// consoleAdmin grants everything.
	adm := e.client("adm", "admsecret123")
	if _, err := adm.ListUsers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.PutUser(ctx, "dave", admin.PutUserRequest{}); err != nil {
		t.Fatal(err)
	}

	// Unknown key and wrong secret.
	_, err = e.client("nobody", "nobodysecret").ListUsers(ctx)
	wantStatus(t, err, 403, "InvalidAccessKeyId")
	_, err = e.client("adm", "wrongsecret1").ListUsers(ctx)
	wantStatus(t, err, 403, "SignatureDoesNotMatch")

	// A disabled admin is rejected.
	noErr(t)(e.root.SetUserStatus(ctx, "adm", false))
	_, err = adm.ListUsers(ctx)
	wantStatus(t, err, 403, "InvalidAccessKeyId")
}

func TestUnauthenticated(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/info", "/users", "/policies/readonly", "/nothing"} {
		resp, err := http.Get(e.ts.URL + admin.Prefix + p)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Fatalf("%s: want 403, got %d %s", p, resp.StatusCode, body)
		}
		var ae admin.Error
		if json.Unmarshal(body, &ae) != nil || ae.Code != "AccessDenied" {
			t.Fatalf("%s: bad error body %s", p, body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("content type %q", ct)
		}
	}
	// Unknown endpoint with valid auth: 404 JSON.
	r, _ := http.NewRequest(http.MethodGet, e.ts.URL+admin.Prefix+"/nothing", nil)
	sigv4.Sign(r, rootUser, rootPass, "us-east-1", time.Now(), sigv4.EmptySHA256)
	resp, err := http.DefaultClient.Do(r)
	fatal(t, err)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestPayloadMustBeSigned(t *testing.T) {
	e := newEnv(t)
	body := `{"policies":["readwrite"]}`
	send := func(hash string, mutate func(*http.Request)) *http.Response {
		r, _ := http.NewRequest(http.MethodPut, e.ts.URL+admin.Prefix+"/users/eve", strings.NewReader(body))
		sigv4.Sign(r, rootUser, rootPass, "us-east-1", time.Now(), hash)
		if mutate != nil {
			mutate(r)
		}
		resp, err := http.DefaultClient.Do(r)
		fatal(t, err)
		resp.Body.Close()
		return resp
	}
	sum := sha256.Sum256([]byte(body))
	good := hex.EncodeToString(sum[:])
	// Unsigned payload with a body is refused.
	if resp := send(sigv4.UnsignedPayload, nil); resp.StatusCode != 400 {
		t.Fatalf("unsigned payload: want 400, got %d", resp.StatusCode)
	}
	// A signed hash for a different body is refused.
	other := sha256.Sum256([]byte(`{"policies":[]}`))
	if resp := send(hex.EncodeToString(other[:]), nil); resp.StatusCode != 400 {
		t.Fatalf("wrong hash: want 400, got %d", resp.StatusCode)
	}
	// Tampering with the body after signing is refused.
	if resp := send(good, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(`{"policies":["consoleAdmin"]}`))
		r.ContentLength = int64(len(`{"policies":["consoleAdmin"]}`))
	}); resp.StatusCode != 400 {
		t.Fatalf("tampered body: want 400, got %d", resp.StatusCode)
	}
	if _, err := e.root.GetUser(e.ctx, "eve"); err == nil {
		t.Fatal("user created by rejected request")
	}
	if resp := send(good, nil); resp.StatusCode != 200 {
		t.Fatalf("good request: want 200, got %d", resp.StatusCode)
	}
	u, err := e.root.GetUser(e.ctx, "eve")
	fatal(t, err)
	if len(u.Policies) != 1 || u.Policies[0] != "readwrite" {
		t.Fatalf("user: %+v", u)
	}
}

func TestKMS(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	ks, err := e.root.ListKMSKeys(ctx)
	fatal(t, err)
	if len(ks) != 1 || ks[0].ID != "opens3-default-key" {
		t.Fatalf("initial keys: %+v", ks)
	}
	k, err := e.root.CreateKMSKey(ctx, "projects")
	fatal(t, err)
	if k.ID != "projects" || k.Created.IsZero() {
		t.Fatalf("created: %+v", k)
	}
	_, err = e.root.CreateKMSKey(ctx, "projects")
	wantStatus(t, err, 409, "AlreadyExists")
	_, err = e.root.CreateKMSKey(ctx, "")
	wantStatus(t, err, 400, "InvalidArgument")
	ks, err = e.root.ListKMSKeys(ctx)
	fatal(t, err)
	if len(ks) != 2 {
		t.Fatalf("keys: %+v", ks)
	}
	// The key is usable for SSE-KMS.
	s3c := e.s3(rootUser, rootPass)
	noErr(t)(s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("enc")}))
	noErr(t)(s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("enc"), Key: aws.String("k"), Body: strings.NewReader("x"),
		ServerSideEncryption: "aws:kms", SSEKMSKeyId: aws.String("projects")}))
	wantStatus(t, e.root.DeleteKMSKey(ctx, "opens3-default-key"), 400, "InvalidArgument")
	if err := e.root.DeleteKMSKey(ctx, "projects"); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.root.DeleteKMSKey(ctx, "projects"), 404, "NotFound")
}

// rawGet performs a signed GET and returns the response body.
func rawGet(t *testing.T, e *env, ak, sk, path string) string {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet, e.ts.URL+admin.Prefix+path, nil)
	sigv4.Sign(r, ak, sk, "us-east-1", time.Now(), sigv4.EmptySHA256)
	resp, err := http.DefaultClient.Do(r)
	fatal(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	return string(b)
}
