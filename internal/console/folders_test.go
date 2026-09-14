package console_test

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/edward-b-1/OpenS3/internal/console"
)

func TestFolderDeleteAndZip(t *testing.T) {
	e := newEnv(t)
	e.mustLogin(e.c, rootUser, rootPass)
	if resp, out := e.do(e.c, "POST", "/console/api/buckets", map[string]any{"name": "fold"}, nil); resp.StatusCode != 201 && resp.StatusCode != 200 {
		t.Fatalf("create bucket: %d %v", resp.StatusCode, out)
	}
	put := func(key, body string) {
		t.Helper()
		req, _ := http.NewRequest("PUT", e.ts.URL+"/console/api/buckets/fold/upload?key="+key, strings.NewReader(body))
		req.Header.Set(console.CSRFHeader, "1")
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := e.c.Do(req)
		if err != nil || resp.StatusCode/100 != 2 {
			t.Fatalf("upload %s: %v %v", key, err, resp)
		}
		resp.Body.Close()
	}
	put("photos/2024/a.txt", "aaa")
	put("photos/2024/b.txt", "bbbb")
	put("photos/2024/sub/c.txt", "c")
	put("photos/2024/", "")
	put("photos/2025/d.txt", "dd")
	put("other.txt", "o")

	// Zip of a folder: entries relative to the parent of the prefix.
	req, _ := http.NewRequest("GET", e.ts.URL+"/console/api/buckets/fold/zip?prefix=photos/2024/", nil)
	resp, err := e.c.Do(req)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip: %v %v", err, resp)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="2024.zip"`) {
		t.Fatalf("disposition %q", cd)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	want := map[string]string{"2024/a.txt": "aaa", "2024/b.txt": "bbbb", "2024/sub/c.txt": "c"}
	if len(got) != len(want) {
		t.Fatalf("zip entries: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("entry %s = %q", k, got[k])
		}
	}
	// Whole-bucket zip.
	req, _ = http.NewRequest("GET", e.ts.URL+"/console/api/buckets/fold/zip", nil)
	resp, _ = e.c.Do(req)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, _ = zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if len(zr.File) != 5 || !strings.Contains(resp.Header.Get("Content-Disposition"), "fold.zip") {
		t.Fatalf("bucket zip: %d files", len(zr.File))
	}

	// Dry run counts; real delete removes the subtree only.
	_, out := e.do(e.c, "POST", "/console/api/buckets/fold/delete-prefix", map[string]any{"prefix": "photos/2024/", "dryRun": true}, nil)
	if out["matched"].(float64) != 4 || out["deleted"].(float64) != 0 || out["bytes"].(float64) != 8 {
		t.Fatalf("dry run: %v", out)
	}
	_, out = e.do(e.c, "POST", "/console/api/buckets/fold/delete-prefix", map[string]any{"prefix": "photos/2024/"}, nil)
	if out["deleted"].(float64) != 4 || out["failed"].(float64) != 0 {
		t.Fatalf("delete: %v", out)
	}
	_, list := e.do(e.c, "GET", "/console/api/buckets/fold/objects?prefix=photos/", nil, nil)
	entries := list["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["prefix"] != "photos/2025/" {
		t.Fatalf("after delete: %v", entries)
	}
	if resp, out := e.do(e.c, "POST", "/console/api/buckets/fold/delete-prefix", map[string]any{"prefix": ""}, nil); resp.StatusCode != 400 {
		t.Fatalf("empty prefix must be rejected: %d %v", resp.StatusCode, out)
	}

	// Versioned: delete without versions creates markers; with versions purges.
	e.do(e.c, "PUT", "/console/api/buckets/fold/versioning", map[string]any{"status": "Enabled"}, nil)
	put("photos/2025/d.txt", "dd2")
	_, out = e.do(e.c, "POST", "/console/api/buckets/fold/delete-prefix", map[string]any{"prefix": "photos/2025/"}, nil)
	if out["deleted"].(float64) != 1 {
		t.Fatalf("versioned delete: %v", out)
	}
	_, out = e.do(e.c, "POST", "/console/api/buckets/fold/delete-prefix", map[string]any{"prefix": "photos/2025/", "versions": true, "dryRun": true}, nil)
	if out["matched"].(float64) != 3 { // two versions + delete marker
		t.Fatalf("versions dry run: %v", out)
	}
	_, out = e.do(e.c, "POST", "/console/api/buckets/fold/delete-prefix", map[string]any{"prefix": "photos/2025/", "versions": true}, nil)
	if out["deleted"].(float64) != 3 || out["failed"].(float64) != 0 {
		t.Fatalf("versions purge: %v", out)
	}
	_, out = e.do(e.c, "GET", "/console/api/buckets/fold/objects?prefix=photos/&versions=1", nil, nil)
	if len(out["entries"].([]any)) != 0 {
		t.Fatalf("not purged: %v", out["entries"])
	}
}

func TestBucketDefaultEncryption(t *testing.T) {
	e := newEnv(t)
	e.mustLogin(e.c, rootUser, rootPass)
	e.do(e.c, "POST", "/console/api/buckets", map[string]any{"name": "encb"}, nil)
	if resp, out := e.do(e.c, "PUT", "/console/api/buckets/encb/encryption", map[string]any{"algorithm": "aws:kms", "kmsKeyId": "nope"}, nil); resp.StatusCode != 400 {
		t.Fatalf("unknown key: %d %v", resp.StatusCode, out)
	}
	e.do(e.c, "POST", "/console/api/kms/keys", map[string]any{"id": "payroll"}, nil)
	if resp, out := e.do(e.c, "PUT", "/console/api/buckets/encb/encryption", map[string]any{"algorithm": "aws:kms", "kmsKeyId": "payroll"}, nil); resp.StatusCode != 200 {
		t.Fatalf("set: %d %v", resp.StatusCode, out)
	}
	_, det := e.do(e.c, "GET", "/console/api/buckets/encb", nil, nil)
	enc, _ := det["encryption"].(map[string]any)
	if enc == nil || enc["kmsKeyId"] != "payroll" {
		t.Fatalf("detail: %v", det)
	}
	req, _ := http.NewRequest("PUT", e.ts.URL+"/console/api/buckets/encb/upload?key=doc", strings.NewReader("secret"))
	req.Header.Set(console.CSRFHeader, "1")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, _ := e.c.Do(req)
	resp.Body.Close()
	_, obj := e.do(e.c, "GET", "/console/api/buckets/encb/object?key=doc", nil, nil)
	sse, _ := obj["sse"].(map[string]any)
	if sse == nil || sse["type"] != "aws:kms" || sse["kmsKeyId"] != "payroll" {
		t.Fatalf("uploaded object not encrypted with the bucket default: %v", obj)
	}
	if resp, _ := e.do(e.c, "PUT", "/console/api/buckets/encb/encryption", map[string]any{"algorithm": ""}, nil); resp.StatusCode != 200 {
		t.Fatal("clear")
	}
	_, det = e.do(e.c, "GET", "/console/api/buckets/encb", nil, nil)
	if _, has := det["encryption"]; has && det["encryption"] != nil {
		t.Fatalf("still set: %v", det["encryption"])
	}
}
