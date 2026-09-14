package fsck

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edward-b-1/opens3/internal/blob"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
)

type fixture struct {
	root string
	db   kv.Store
	svc  *object.Service
	ctx  context.Context
	t    *testing.T
}

var actor = object.Actor{CanonicalID: "me", DisplayName: "me"}

// newFixture builds a data directory through the object service: a
// plain object, a multipart object, an SSE-S3 object, an SSE-C object, a
// versioned key with two versions and a delete marker, a directory
// marker, and an upload in progress.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	db, err := kv.OpenBolt(filepath.Join(root, "meta", "opens3.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bs, err := blob.OpenFS(root, false)
	if err != nil {
		t.Fatal(err)
	}
	k, err := kms.NewLocal(db, kms.TestMaster())
	if err != nil {
		t.Fatal(err)
	}
	svc := object.New(db, bs, k, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	f := &fixture{root: root, db: db, svc: svc, ctx: context.Background(), t: t}
	f.must(svc.CreateBucket(f.ctx, actor, object.CreateBucketInput{Name: "docs"}))
	f.put("docs", "plain.txt", "hello plain")
	f.put("docs", "dir/", "")
	f.put("docs", "dir/nested.txt", "nested")
	if _, err := svc.PutObject(f.ctx, actor, object.PutInput{Bucket: "docs", Key: "sse.txt", Body: strings.NewReader("hello sse"), Size: 9, SSE: object.SSERequest{Type: "AES256"}}); err != nil {
		t.Fatal(err)
	}
	ck := []byte("0123456789abcdef0123456789abcdef")
	if _, err := svc.PutObject(f.ctx, actor, object.PutInput{Bucket: "docs", Key: "ssec.txt", Body: strings.NewReader("hello ssec"), Size: 10,
		SSE: object.SSERequest{Type: "SSE-C", CustomerKey: ck, CustomerKeyMD5: md5b64(ck)}}); err != nil {
		t.Fatal(err)
	}
	u, err := svc.CreateUpload(f.ctx, actor, object.CreateUploadInput{Bucket: "docs", Key: "big.bin"})
	if err != nil {
		t.Fatal(err)
	}
	p1, err := svc.UploadPart(f.ctx, object.UploadPartInput{Bucket: "docs", Key: "big.bin", UploadID: u.UploadID, PartNumber: 1, Body: strings.NewReader(strings.Repeat("a", 5<<20)), Size: 5 << 20})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := svc.UploadPart(f.ctx, object.UploadPartInput{Bucket: "docs", Key: "big.bin", UploadID: u.UploadID, PartNumber: 2, Body: strings.NewReader("tail"), Size: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteUpload(f.ctx, actor, object.CompleteInput{Bucket: "docs", Key: "big.bin", UploadID: u.UploadID, ObjectSize: -1,
		Parts: []object.CompletePart{{PartNumber: 1, ETag: p1.ETag}, {PartNumber: 2, ETag: p2.ETag}}}); err != nil {
		t.Fatal(err)
	}
	// An upload left in progress.
	pu, err := svc.CreateUpload(f.ctx, actor, object.CreateUploadInput{Bucket: "docs", Key: "pending.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadPart(f.ctx, object.UploadPartInput{Bucket: "docs", Key: "pending.bin", UploadID: pu.UploadID, PartNumber: 1, Body: strings.NewReader("part"), Size: 4}); err != nil {
		t.Fatal(err)
	}
	// A versioned bucket with history.
	f.must(svc.CreateBucket(f.ctx, actor, object.CreateBucketInput{Name: "ver"}))
	if _, err := svc.UpdateBucket(f.ctx, "ver", func(b *meta.Bucket) error { b.Versioning = "Enabled"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.put("ver", "k", "v1")
	f.put("ver", "k", "v2 is longer")
	f.put("ver", "gone", "was here")
	if _, err := svc.DeleteObject(f.ctx, actor, object.DeleteInput{Bucket: "ver", Key: "gone"}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) must(_ any, err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) put(bucket, key, body string) {
	f.t.Helper()
	if _, err := f.svc.PutObject(f.ctx, actor, object.PutInput{Bucket: bucket, Key: key, Body: strings.NewReader(body), Size: int64(len(body))}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) check(verify bool) *Report {
	f.t.Helper()
	rep, err := Check(f.ctx, f.db, f.root, Options{Verify: verify, Objects: f.svc})
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

func (f *fixture) blobOf(bucket, key string) (string, string) {
	o, err := f.svc.StatObject(f.ctx, bucket, key, "")
	if err != nil {
		f.t.Fatal(err)
	}
	id := o.Parts[0].Blob
	return id, filepath.Join(f.root, "data", bucket, id[:2], id)
}

func TestCheckCleanDirectory(t *testing.T) {
	f := newFixture(t)
	rep := f.check(true)
	if len(rep.Problems) != 1 || rep.Problems[0].Kind != Unverifiable || !strings.Contains(rep.Problems[0].Item, "ssec.txt") {
		t.Fatalf("clean directory: %+v", rep.Problems)
	}
	if rep.Buckets != 2 || rep.Uploads != 1 || rep.Parts != 1 || rep.Verified < 6 {
		t.Fatalf("counts: %+v", rep)
	}
	// Without decryption, encrypted objects are unverifiable but still counted.
	rep2, _ := Check(f.ctx, f.db, f.root, Options{Verify: true})
	if rep2.Count(Unverifiable) != 2 || rep2.Count(ContentMismatch) != 0 {
		t.Fatalf("no key: %+v", rep2.Problems)
	}
}

func TestCheckAndRepair(t *testing.T) {
	f := newFixture(t)
	// Damage of every kind.
	_, plainPath := f.blobOf("docs", "plain.txt")
	os.Remove(plainPath) // missing file (not repairable)
	_, ssePath := f.blobOf("docs", "sse.txt")
	os.Truncate(ssePath, 3) // size mismatch (not repairable)
	_, nestedPath := f.blobOf("docs", "dir/nested.txt")
	os.WriteFile(nestedPath, []byte("nestXd"), 0o600) // same size, wrong content (verify only)
	os.MkdirAll(filepath.Join(f.root, "data", "docs", "zz"), 0o700)
	os.WriteFile(filepath.Join(f.root, "data", "docs", "zz", "zz00orphan"), []byte("orphan"), 0o600)
	os.MkdirAll(filepath.Join(f.root, "data", "nobucket", "aa"), 0o700)
	os.WriteFile(filepath.Join(f.root, "data", "nobucket", "aa", "aa00stray"), []byte("stray"), 0o600)
	os.WriteFile(filepath.Join(f.root, "tmp", "leftover"), []byte("x"), 0o600)
	var pendingID string
	f.db.Update(func(tx kv.Txn) error {
		// Dangling null pointer and missing null pointer.
		tx.Put(meta.NullKey("docs", "ghost"), []byte("0000000000000000"))
		tx.Delete(meta.NullKey("docs", "plain.txt"))
		// Upload index problems.
		meta.ScanBucketUploads(tx, "docs", func(u *meta.Upload) bool { pendingID = u.UploadID; return false })
		tx.Delete(meta.UploadIDKey("docs", pendingID))
		tx.Put(meta.UploadIDKey("docs", "deadbeef"), []byte("nothing"))
		// Orphan part records for an upload that does not exist.
		tx.Put(meta.PartKey("docs", "orphanupload", 1), []byte(`{"n":1,"b":"zz00orphanpart","s":6,"e":"x"}`))
		// A part record whose file is gone.
		tx.Put(meta.PartKey("docs", pendingID, 2), []byte(`{"n":2,"b":"zz00nofile","s":6,"e":"x"}`))
		// Buckets stuck in deletion (one empty, one that still holds objects)
		// and records without a bucket record.
		meta.CreateBucket(tx, &meta.Bucket{Name: "stuck", Owner: "me", Deleting: true})
		b, _ := meta.GetBucket(tx, "ver")
		b.Deleting = true
		meta.PutBucket(tx, b)
		x := "9dd4e461268c8034f5c8564e155c67a6" // md5("x")
		o := &meta.Object{Bucket: "lost", Key: "k", VersionID: meta.NullVersionID, Seq: 5, Size: 1, ETag: x, Parts: []meta.Part{{Number: 1, Blob: "aa00lost", Size: 1, ETag: x}}}
		meta.PutObject(tx, o)
		return nil
	})
	os.MkdirAll(filepath.Join(f.root, "data", "stuck", "aa"), 0o700)
	os.WriteFile(filepath.Join(f.root, "data", "stuck", "aa", "aa00stuck"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(f.root, "data", "lost", "aa"), 0o700)
	os.WriteFile(filepath.Join(f.root, "data", "lost", "aa", "aa00lost"), []byte("x"), 0o600)

	rep := f.check(true)
	want := map[string]int{MissingFile: 1, SizeMismatch: 1, ContentMismatch: 1, Unverifiable: 1, RecordWithoutBkt: 1, OrphanFile: 3,
		DanglingNullPtr: 1, MissingNullPtr: 1, DanglingUploadIdx: 1, MissingUploadIdx: 1, OrphanPartRecords: 1, PartMissingFile: 1, BucketDeleting: 2, TempFile: 1}
	for kind, n := range want {
		if got := rep.Count(kind); got != n {
			t.Errorf("%s: %d, want %d", kind, got, n)
		}
	}
	if t.Failed() {
		for _, p := range rep.Problems {
			t.Logf("%s | %s | %s | %s", p.Kind, p.Bucket, p.Item, p.Detail)
		}
		t.FailNow()
	}

	// Dry run changes nothing.
	dry, err := Repair(f.ctx, f.db, f.root, rep, true)
	if err != nil || len(dry.Actions) != rep.Repairable() {
		t.Fatalf("dry run: %v %d/%d", err, len(dry.Actions), rep.Repairable())
	}
	if again := f.check(false); len(again.Problems) != len(rep.Problems)-rep.Count(ContentMismatch)-rep.Count(Unverifiable) {
		t.Fatalf("dry run changed something: %d problems", len(again.Problems))
	}
	// Real repair leaves only what needs a backup.
	res, err := Repair(f.ctx, f.db, f.root, rep, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != rep.Repairable() {
		t.Fatalf("actions: %d", len(res.Actions))
	}
	after := f.check(true)
	for _, p := range after.Problems {
		if Repairable(p.Kind) {
			t.Errorf("still present after repair: %s %s/%s", p.Kind, p.Bucket, p.Item)
		}
	}
	if after.Count(MissingFile) != 1 || after.Count(SizeMismatch) != 1 || after.Count(ContentMismatch) != 1 {
		t.Fatalf("unrepairable problems changed: %+v", after.Problems)
	}
	// The lost bucket is back with its object; the empty deleting bucket is
	// gone; the deleting bucket that held objects is restored.
	if _, err := f.svc.GetBucket(f.ctx, "lost"); err != nil {
		t.Fatalf("recreated bucket: %v", err)
	}
	if _, err := f.svc.GetBucket(f.ctx, "stuck"); err == nil {
		t.Fatal("empty deleting bucket survived")
	}
	if b, err := f.svc.GetBucket(f.ctx, "ver"); err != nil || b.Deleting {
		t.Fatalf("deleting bucket with objects not restored: %v", err)
	}
	if _, err := f.svc.StatObject(f.ctx, "ver", "k", ""); err != nil {
		t.Fatalf("restored bucket's object: %v", err)
	}
	// The null pointer was recreated: the plain object is addressable again.
	if _, err := f.svc.StatObject(f.ctx, "docs", "plain.txt", meta.NullVersionID); err != nil {
		t.Fatalf("null pointer not restored: %v", err)
	}
	// Bucket-scoped check.
	one, _ := Check(f.ctx, f.db, f.root, Options{Bucket: "docs"})
	if one.Buckets != 1 || one.Count(MissingFile) != 1 {
		t.Fatalf("bucket scope: %+v", one)
	}
}

func TestExport(t *testing.T) {
	f := newFixture(t)
	out := filepath.Join(t.TempDir(), "out")
	res, err := Export(f.ctx, f.svc, out, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// docs: plain, dir/ (marker), dir/nested, sse, ssec (skipped), big; ver: k (current only).
	if res.Buckets != 2 || res.Exported != 5 || res.Skipped != 2 {
		t.Fatalf("result: %+v", res)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return string(b)
	}
	if read("docs/plain.txt") != "hello plain" || read("docs/sse.txt") != "hello sse" || read("docs/dir/nested.txt") != "nested" || read("ver/k") != "v2 is longer" {
		t.Fatal("exported content")
	}
	if b := read("docs/big.bin"); len(b) != 5<<20+4 || !strings.HasSuffix(b, "tail") {
		t.Fatalf("multipart export: %d bytes", len(b))
	}
	if _, err := os.Stat(filepath.Join(out, "docs", "ssec.txt")); !os.IsNotExist(err) {
		t.Fatal("SSE-C object written")
	}
	if st, err := os.Stat(filepath.Join(out, "docs", "dir")); err != nil || !st.IsDir() {
		t.Fatal("directory marker")
	}
	manifest := read("manifest.jsonl")
	if !strings.Contains(manifest, `"key":"ssec.txt"`) || !strings.Contains(manifest, "customer-provided key") || !strings.Contains(manifest, `"encryption":"AES256"`) {
		t.Fatalf("manifest: %s", manifest)
	}
	// All versions: the older version sits beside the current one; the
	// delete marker is listed, not written; files already present are kept.
	out2 := filepath.Join(t.TempDir(), "out2")
	res, err = Export(f.ctx, f.svc, out2, ExportOptions{Bucket: "ver", AllVersions: true})
	if err != nil || res.Buckets != 1 || res.Exported != 3 || res.Skipped != 1 {
		t.Fatalf("all versions: %v %+v", err, res)
	}
	entries, _ := os.ReadDir(filepath.Join(out2, "ver"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 3 || names[0] != "gone@"+versionOf(f, "ver", "gone") {
		t.Fatalf("all-versions files: %v", names)
	}
	if _, err := Export(f.ctx, f.svc, out2, ExportOptions{Bucket: "nope"}); err == nil {
		t.Fatal("missing bucket accepted")
	}
	// Unsafe keys go under _unsafe.
	f.put("docs", "../escape", "x")
	res, err = Export(f.ctx, f.svc, out, ExportOptions{Bucket: "docs", Prefix: ".."})
	if err != nil || res.Exported != 1 {
		t.Fatalf("unsafe key: %v %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join(out, "docs", "_unsafe")); err != nil {
		t.Fatal("unsafe key not redirected")
	}
	if _, err := os.Stat(filepath.Join(out, "escape")); !os.IsNotExist(err) {
		t.Fatal("unsafe key escaped the output directory")
	}
}

// versionOf returns the version id of the oldest version of key.
func versionOf(f *fixture, bucket, key string) string {
	var vid string
	f.db.View(func(tx kv.Txn) error {
		meta.ScanBucketObjects(tx, bucket, func(o *meta.Object) bool {
			if o.Key == key && !o.DeleteMarker {
				vid = o.VersionID
			}
			return true
		})
		return nil
	})
	return vid
}

func md5b64(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}
