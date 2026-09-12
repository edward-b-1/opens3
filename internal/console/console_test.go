package console_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"gitlab.com/Birdsall/opens3/internal/console"
	"gitlab.com/Birdsall/opens3/internal/server"
)

const (
	rootUser = "opens3root"
	rootPass = "opens3rootsecret"
)

type env struct {
	t  *testing.T
	ts *httptest.Server
	c  *http.Client // cookie-aware client
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
	return &env{t: t, ts: ts, c: newClient()}
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// do sends a console API request with the CSRF header and decodes JSON.
func (e *env) do(c *http.Client, method, path string, body any, hdr map[string]string) (*http.Response, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	req.Header.Set(console.CSRFHeader, "1")
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(raw, &out)
	} else {
		out = map[string]any{"_body": string(raw)}
	}
	return resp, out
}

func (e *env) login(c *http.Client, ak, sk string) (*http.Response, map[string]any) {
	e.t.Helper()
	return e.do(c, "POST", "/console/api/login", map[string]string{"accessKey": ak, "secretKey": sk}, nil)
}

func (e *env) mustLogin(c *http.Client, ak, sk string) {
	e.t.Helper()
	if resp, out := e.login(c, ak, sk); resp.StatusCode != 200 {
		e.t.Fatalf("login %s: %d %v", ak, resp.StatusCode, out)
	}
}

func errCode(out map[string]any) string {
	if e, ok := out["error"].(map[string]any); ok {
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

func TestLoginAndSession(t *testing.T) {
	e := newEnv(t)
	// Wrong secret and unknown key both give 401 with the same code.
	for _, cred := range [][2]string{{rootUser, "wrong-secret"}, {"nobody", "whatever"}, {"", ""}} {
		resp, out := e.login(e.c, cred[0], cred[1])
		if resp.StatusCode != 401 || errCode(out) != "InvalidCredentials" {
			t.Fatalf("login %v: want 401 InvalidCredentials, got %d %v", cred, resp.StatusCode, out)
		}
	}
	resp, out := e.login(e.c, rootUser, rootPass)
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d %v", resp.StatusCode, out)
	}
	if out["user"] != "root" || out["isRoot"] != true || out["admin"] != true {
		t.Fatalf("unexpected me after login: %v", out)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == console.CookieName {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/console/" {
		t.Fatalf("session cookie missing or insecure: %+v", cookie)
	}
	resp, out = e.do(e.c, "GET", "/console/api/me", nil, nil)
	if resp.StatusCode != 200 || out["user"] != "root" {
		t.Fatalf("me: %d %v", resp.StatusCode, out)
	}
	// Logout invalidates the session key server-side.
	if resp, out = e.do(e.c, "POST", "/console/api/logout", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("logout: %d %v", resp.StatusCode, out)
	}
	stale := newClient()
	stale.Jar.SetCookies(mustURL(t, e.ts.URL+"/console/"), []*http.Cookie{cookie})
	if resp, _ = e.do(stale, "GET", "/console/api/me", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("stale session after logout: want 401, got %d", resp.StatusCode)
	}
}

func TestCookieRequired(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/console/api/me", "/console/api/buckets", "/console/api/users", "/console/api/info"} {
		resp, out := e.do(newClient(), "GET", p, nil, nil)
		if resp.StatusCode != 401 || errCode(out) != "NotLoggedIn" {
			t.Fatalf("%s without cookie: want 401 NotLoggedIn, got %d %v", p, resp.StatusCode, out)
		}
	}
	// A forged cookie is rejected too.
	c := newClient()
	c.Jar.SetCookies(mustURL(t, e.ts.URL+"/console/"), []*http.Cookie{{Name: console.CookieName, Value: "AKIAFAKE.notatoken"}})
	if resp, _ := e.do(c, "GET", "/console/api/buckets", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("forged cookie: want 401, got %d", resp.StatusCode)
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t)
	e.mustLogin(e.c, rootUser, rootPass)
	body := map[string]any{"name": "csrf-bucket"}
	// Missing custom header.
	resp, out := e.do(e.c, "POST", "/console/api/buckets", body, map[string]string{console.CSRFHeader: ""})
	if resp.StatusCode != 403 || errCode(out) != "MissingCSRFHeader" {
		t.Fatalf("no CSRF header: want 403 MissingCSRFHeader, got %d %v", resp.StatusCode, out)
	}
	// Cross-site fetch metadata.
	resp, out = e.do(e.c, "POST", "/console/api/buckets", body, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != 403 || errCode(out) != "CrossOrigin" {
		t.Fatalf("cross-site: want 403 CrossOrigin, got %d %v", resp.StatusCode, out)
	}
	// Foreign Origin.
	resp, out = e.do(e.c, "POST", "/console/api/buckets", body, map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != 403 || errCode(out) != "CrossOrigin" {
		t.Fatalf("foreign origin: want 403 CrossOrigin, got %d %v", resp.StatusCode, out)
	}
	// Login itself is protected as well.
	if resp, _ = e.do(newClient(), "POST", "/console/api/login", map[string]string{"accessKey": rootUser, "secretKey": rootPass}, map[string]string{console.CSRFHeader: ""}); resp.StatusCode != 403 {
		t.Fatalf("login without CSRF header: want 403, got %d", resp.StatusCode)
	}
	// Same-origin with the header succeeds.
	resp, out = e.do(e.c, "POST", "/console/api/buckets", body, map[string]string{"Origin": e.ts.URL, "Sec-Fetch-Site": "same-origin"})
	if resp.StatusCode != 201 {
		t.Fatalf("same-origin create: want 201, got %d %v", resp.StatusCode, out)
	}
	// GETs never need the header.
	if resp, _ = e.do(e.c, "GET", "/console/api/buckets", nil, map[string]string{console.CSRFHeader: ""}); resp.StatusCode != 200 {
		t.Fatalf("GET without header: want 200, got %d", resp.StatusCode)
	}
}

func TestBucketObjectRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.mustLogin(e.c, rootUser, rootPass)

	resp, out := e.do(e.c, "POST", "/console/api/buckets", map[string]any{"name": "photos", "versioning": true, "tags": []map[string]string{{"key": "team", "value": "web"}}}, nil)
	if resp.StatusCode != 201 || out["versioning"] != "Enabled" {
		t.Fatalf("create bucket: %d %v", resp.StatusCode, out)
	}
	if resp, out = e.do(e.c, "POST", "/console/api/buckets", map[string]any{"name": "BAD NAME"}, nil); resp.StatusCode != 400 {
		t.Fatalf("invalid bucket name: want 400, got %d %v", resp.StatusCode, out)
	}
	resp, out = e.do(e.c, "GET", "/console/api/buckets/photos", nil, nil)
	if resp.StatusCode != 200 || out["policyReadable"] != true || len(out["tags"].([]any)) != 1 {
		t.Fatalf("get bucket: %d %v", resp.StatusCode, out)
	}

	// Multipart upload of two files under a prefix.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prefix", "2026/")
	fw, _ := mw.CreateFormFile("file", "hello.txt")
	fw.Write([]byte("hello console"))
	fw, _ = mw.CreateFormFile("file", "data.bin")
	fw.Write(bytes.Repeat([]byte{7}, 100000))
	mw.Close()
	resp, out = e.do(e.c, "POST", "/console/api/buckets/photos/upload", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()})
	if resp.StatusCode != 201 || len(out["uploaded"].([]any)) != 2 {
		t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	// Raw PUT upload.
	resp, out = e.do(e.c, "PUT", "/console/api/buckets/photos/upload?key=readme.md", "# hi", map[string]string{"Content-Type": "text/markdown"})
	if resp.StatusCode != 201 {
		t.Fatalf("raw upload: %d %v", resp.StatusCode, out)
	}

	// Listing at the root shows the folder and the file.
	resp, out = e.do(e.c, "GET", "/console/api/buckets/photos/objects", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d %v", resp.StatusCode, out)
	}
	names := []string{}
	for _, en := range out["entries"].([]any) {
		m := en.(map[string]any)
		if p, _ := m["prefix"].(string); p != "" {
			names = append(names, p)
		} else {
			names = append(names, m["key"].(string))
		}
	}
	if strings.Join(names, ",") != "2026/,readme.md" {
		t.Fatalf("list entries: %v", names)
	}
	resp, out = e.do(e.c, "GET", "/console/api/buckets/photos/objects?prefix=2026/", nil, nil)
	if resp.StatusCode != 200 || len(out["entries"].([]any)) != 2 {
		t.Fatalf("list prefix: %d %v", resp.StatusCode, out)
	}

	// Head and download.
	resp, out = e.do(e.c, "GET", "/console/api/buckets/photos/object?key=2026/hello.txt", nil, nil)
	if resp.StatusCode != 200 || out["size"].(float64) != 13 || out["contentType"] != "text/plain; charset=utf-8" {
		t.Fatalf("head: %d %v", resp.StatusCode, out)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/console/api/buckets/photos/download?key=2026/hello.txt", nil)
	dl, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if dl.StatusCode != 200 || string(body) != "hello console" || !strings.HasPrefix(dl.Header.Get("Content-Disposition"), `attachment; filename="hello.txt"`) {
		t.Fatalf("download: %d %q %q", dl.StatusCode, body, dl.Header.Get("Content-Disposition"))
	}
	if resp, _ = e.do(e.c, "GET", "/console/api/buckets/photos/object?key=missing", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("head missing: want 404, got %d", resp.StatusCode)
	}

	// Versioned delete creates a delete marker; versions listing shows both.
	resp, out = e.do(e.c, "POST", "/console/api/buckets/photos/delete", map[string]any{"objects": []map[string]string{{"key": "readme.md"}}}, nil)
	if resp.StatusCode != 200 || out["failed"].(float64) != 0 {
		t.Fatalf("delete: %d %v", resp.StatusCode, out)
	}
	if res := out["results"].([]any)[0].(map[string]any); res["deleteMarker"] != true {
		t.Fatalf("expected delete marker: %v", res)
	}
	resp, out = e.do(e.c, "GET", "/console/api/buckets/photos/objects?versions=1&delimiter=", nil, nil)
	if resp.StatusCode != 200 || len(out["entries"].([]any)) != 4 {
		t.Fatalf("list versions: %d %v", resp.StatusCode, out)
	}

	// Policy set/get/delete with validation.
	pol := map[string]any{"Version": "2012-10-17", "Statement": []map[string]any{{"Effect": "Allow", "Principal": "*", "Action": "s3:GetObject", "Resource": "arn:aws:s3:::photos/*"}}}
	resp, out = e.do(e.c, "PUT", "/console/api/buckets/photos/policy", map[string]any{"policy": pol}, nil)
	if resp.StatusCode != 200 || out["public"] != true {
		t.Fatalf("put policy: %d %v", resp.StatusCode, out)
	}
	if resp, out = e.do(e.c, "PUT", "/console/api/buckets/photos/policy", map[string]any{"policy": map[string]any{"Statement": "nope"}}, nil); resp.StatusCode != 400 {
		t.Fatalf("bad policy: want 400, got %d %v", resp.StatusCode, out)
	}
	if resp, _ = e.do(e.c, "DELETE", "/console/api/buckets/photos/policy", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("delete policy: %d", resp.StatusCode)
	}

	// Bucket is not empty yet.
	if resp, out = e.do(e.c, "DELETE", "/console/api/buckets/photos", nil, nil); resp.StatusCode != 409 || errCode(out) != "BucketNotEmpty" {
		t.Fatalf("delete non-empty bucket: want 409 BucketNotEmpty, got %d %v", resp.StatusCode, out)
	}
}

func TestNonAdminDenied(t *testing.T) {
	e := newEnv(t)
	e.mustLogin(e.c, rootUser, rootPass)
	resp, out := e.do(e.c, "POST", "/console/api/users", map[string]any{"name": "alice", "secretKey": "alicesecret1", "policies": []string{"readwrite"}}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create user: %d %v", resp.StatusCode, out)
	}
	if resp, out = e.do(e.c, "POST", "/console/api/buckets", map[string]any{"name": "shared"}, nil); resp.StatusCode != 201 {
		t.Fatalf("create bucket: %d %v", resp.StatusCode, out)
	}

	alice := newClient()
	resp, out = e.login(alice, "alice", "alicesecret1")
	if resp.StatusCode != 200 || out["admin"] != false || out["isRoot"] != false {
		t.Fatalf("alice login: %d %v", resp.StatusCode, out)
	}
	for _, p := range []string{"/console/api/users", "/console/api/groups", "/console/api/policies", "/console/api/kms/keys", "/console/api/keys"} {
		if resp, out = e.do(alice, "GET", p, nil, nil); resp.StatusCode != 403 || errCode(out) != "AccessDenied" {
			t.Fatalf("alice %s: want 403 AccessDenied, got %d %v", p, resp.StatusCode, out)
		}
	}
	if resp, out = e.do(alice, "POST", "/console/api/users", map[string]any{"name": "mallory", "secretKey": "mallorysecret"}, nil); resp.StatusCode != 403 {
		t.Fatalf("alice create user: want 403, got %d %v", resp.StatusCode, out)
	}
	// readwrite grants s3:* so alice can use buckets and manage her own keys.
	if resp, out = e.do(alice, "GET", "/console/api/buckets", nil, nil); resp.StatusCode != 200 || len(out["buckets"].([]any)) != 1 {
		t.Fatalf("alice buckets: %d %v", resp.StatusCode, out)
	}
	if resp, out = e.do(alice, "PUT", "/console/api/buckets/shared/upload?key=a.txt", "x", nil); resp.StatusCode != 201 {
		t.Fatalf("alice upload: %d %v", resp.StatusCode, out)
	}
	if resp, out = e.do(alice, "GET", "/console/api/keys?user=alice", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("alice own keys: %d %v", resp.StatusCode, out)
	}
	resp, out = e.do(alice, "POST", "/console/api/keys", map[string]any{"kind": "service", "description": "ci"}, nil)
	if resp.StatusCode != 201 || out["secretKey"] == "" {
		t.Fatalf("alice service account: %d %v", resp.StatusCode, out)
	}
	if resp, out = e.do(alice, "GET", "/console/api/keys?user=root", nil, nil); resp.StatusCode != 403 {
		t.Fatalf("alice root keys: want 403, got %d %v", resp.StatusCode, out)
	}
	// Info is available but without privileged details.
	if resp, out = e.do(alice, "GET", "/console/api/info", nil, nil); resp.StatusCode != 200 || out["admin"] != false || out["disk"] != nil {
		t.Fatalf("alice info: %d %v", resp.StatusCode, out)
	}
	// Disabling alice ends her session immediately.
	if resp, out = e.do(e.c, "PATCH", "/console/api/users/alice", map[string]any{"enabled": false}, nil); resp.StatusCode != 200 {
		t.Fatalf("disable alice: %d %v", resp.StatusCode, out)
	}
	if resp, _ = e.do(alice, "GET", "/console/api/me", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("disabled user session: want 401, got %d", resp.StatusCode)
	}
}

func TestStaticAndRedirect(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.do(newClient(), "GET", "/console", nil, nil)
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/console/" {
		t.Fatalf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, out := e.do(newClient(), "GET", "/console/", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(out["_body"].(string), "OpenS3 Console") || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("index: %d %v", resp.StatusCode, resp.Header)
	}
	for _, p := range []string{"/console/app.js", "/console/style.css", "/console/views/objects.js"} {
		if resp, _ = e.do(newClient(), "GET", p, nil, nil); resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
	}
	if resp, out = e.do(newClient(), "GET", "/console/api/nothing", nil, nil); resp.StatusCode != 404 || errCode(out) != "NotFound" {
		t.Fatalf("unknown api: %d %v", resp.StatusCode, out)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
