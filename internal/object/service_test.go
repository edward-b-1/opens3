package object

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/Birdsall/opens3/internal/blob"
	"gitlab.com/Birdsall/opens3/internal/checksum"
	"gitlab.com/Birdsall/opens3/internal/kms"
	"gitlab.com/Birdsall/opens3/internal/kv"
	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/s3err"
)

var actor = Actor{CanonicalID: "owner", DisplayName: "owner"}

func newService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	db, err := kv.OpenBolt(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	bs, err := blob.OpenFS(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	k, err := kms.NewLocal(db, kms.TestMaster())
	if err != nil {
		t.Fatal(err)
	}
	return New(db, bs, k, "us-east-1", nil)
}

func code(err error) s3err.Code {
	var e *s3err.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func readAll(t *testing.T, s *Service, bucket, key, vid string, r *Range) ([]byte, *GetResult) {
	t.Helper()
	res, err := s.GetObject(context.Background(), GetInput{Bucket: bucket, Key: key, VersionID: vid, Range: r})
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b, res
}

func TestBucketsAndBasicObjects(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	if _, err := s.CreateBucket(ctx, actor, CreateBucketInput{Name: "Bad_Name"}); code(err) != s3err.InvalidBucketName {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := s.CreateBucket(ctx, actor, CreateBucketInput{Name: "bkt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBucket(ctx, actor, CreateBucketInput{Name: "bkt"}); code(err) != s3err.BucketAlreadyOwnedByYou {
		t.Fatalf("dup: %v", err)
	}
	if _, err := s.CreateBucket(ctx, Actor{CanonicalID: "other"}, CreateBucketInput{Name: "bkt"}); code(err) != s3err.BucketAlreadyExists {
		t.Fatalf("dup other: %v", err)
	}
	data := []byte("hello world")
	sum := md5.Sum(data)
	o, err := s.PutObject(ctx, actor, PutInput{Bucket: "bkt", Key: "a/b", Body: bytes.NewReader(data), Size: int64(len(data)),
		ExpectedMD5: hex.EncodeToString(sum[:]), Attrs: ObjectAttrs{ContentType: "text/plain", UserMeta: map[string]string{"x": "y"}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.ETag != hex.EncodeToString(sum[:]) || o.VersionID != meta.NullVersionID || o.Size != 11 {
		t.Fatalf("object: %+v", o)
	}
	// Both a/b and a/b/c can coexist (unlike MinIO).
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "bkt", Key: "a/b/c", Body: bytes.NewReader(data), Size: 11}); err != nil {
		t.Fatal(err)
	}
	got, res := readAll(t, s, "bkt", "a/b", "", nil)
	if string(got) != "hello world" || res.Object.ContentType != "text/plain" {
		t.Fatalf("got %q", got)
	}
	got, res = readAll(t, s, "bkt", "a/b", "", &Range{6, -1})
	if string(got) != "world" || res.Range.Start != 6 || res.Range.End != 10 {
		t.Fatalf("range: %q %+v", got, res.Range)
	}
	got, _ = readAll(t, s, "bkt", "a/b", "", &Range{-3, 0})
	if string(got) != "rld" {
		t.Fatalf("suffix range: %q", got)
	}
	if _, err := s.GetObject(ctx, GetInput{Bucket: "bkt", Key: "a/b", Range: &Range{50, 60}}); code(err) != s3err.InvalidRange {
		t.Fatalf("invalid range: %v", err)
	}
	// Bad MD5.
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "bkt", Key: "x", Body: bytes.NewReader(data), Size: 11, ExpectedMD5: "00"}); code(err) != s3err.BadDigest {
		t.Fatalf("bad digest: %v", err)
	}
	// Short body.
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "bkt", Key: "x", Body: bytes.NewReader(data), Size: 20}); code(err) != s3err.IncompleteBody {
		t.Fatalf("incomplete: %v", err)
	}
	// Conditional read.
	if _, err := s.GetObject(ctx, GetInput{Bucket: "bkt", Key: "a/b", Conditions: Conditions{IfNoneMatch: `"` + o.ETag + `"`}}); code(err) != s3err.NotModified {
		t.Fatalf("if-none-match: %v", err)
	}
	if _, err := s.GetObject(ctx, GetInput{Bucket: "bkt", Key: "a/b", Conditions: Conditions{IfMatch: "nope"}}); code(err) != s3err.PreconditionFailed {
		t.Fatalf("if-match: %v", err)
	}
	// Conditional write.
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "bkt", Key: "a/b", Body: bytes.NewReader(data), Size: 11, Conditions: Conditions{IfNoneMatch: "*"}}); code(err) != s3err.PreconditionFailed {
		t.Fatalf("if-none-match write: %v", err)
	}
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "bkt", Key: "new", Body: bytes.NewReader(data), Size: 11, Conditions: Conditions{IfNoneMatch: "*"}}); err != nil {
		t.Fatalf("if-none-match new: %v", err)
	}
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "bkt", Key: "a/b", Body: bytes.NewReader(data), Size: 11, Conditions: Conditions{IfMatch: o.ETag}}); err != nil {
		t.Fatalf("if-match write: %v", err)
	}
	// Delete; bucket not empty; then empty.
	if err := s.DeleteBucket(ctx, "bkt"); code(err) != s3err.BucketNotEmpty {
		t.Fatalf("not empty: %v", err)
	}
	for _, k := range []string{"a/b", "a/b/c", "new"} {
		if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "bkt", Key: k}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.StatObject(ctx, "bkt", "a/b", ""); code(err) != s3err.NoSuchKey {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteBucket(ctx, "bkt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBucket(ctx, "bkt"); code(err) != s3err.NoSuchBucket {
		t.Fatal("bucket should be gone")
	}
}

func TestVersioning(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	s.CreateBucket(ctx, actor, CreateBucketInput{Name: "ver"})
	put := func(key, body string) *meta.Object {
		o, err := s.PutObject(ctx, actor, PutInput{Bucket: "ver", Key: key, Body: bytes.NewReader([]byte(body)), Size: int64(len(body))})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	put("k", "one") // null version
	s.UpdateBucket(ctx, "ver", func(b *meta.Bucket) error { b.Versioning = "Enabled"; return nil })
	v2 := put("k", "two")
	v3 := put("k", "three")
	if v2.VersionID == meta.NullVersionID || v3.VersionID == v2.VersionID {
		t.Fatal("version ids")
	}
	if got, _ := readAll(t, s, "ver", "k", "", nil); string(got) != "three" {
		t.Fatal("latest")
	}
	if got, _ := readAll(t, s, "ver", "k", meta.NullVersionID, nil); string(got) != "one" {
		t.Fatal("null version")
	}
	if got, _ := readAll(t, s, "ver", "k", v2.VersionID, nil); string(got) != "two" {
		t.Fatal("v2")
	}
	// Delete → marker.
	dr, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "ver", Key: "k"})
	if err != nil || !dr.DeleteMarker {
		t.Fatalf("marker: %v %+v", err, dr)
	}
	if _, err := s.GetObject(ctx, GetInput{Bucket: "ver", Key: "k"}); code(err) != s3err.NoSuchKey {
		t.Fatalf("after marker: %v", err)
	}
	if _, err := s.GetObject(ctx, GetInput{Bucket: "ver", Key: "k", VersionID: dr.VersionID}); code(err) != s3err.MethodNotAllowed {
		t.Fatalf("get marker by id: %v", err)
	}
	lv, _ := s.ListVersions(ctx, "ver", meta.ListOptions{})
	if len(lv.Entries) != 4 || !lv.Entries[0].Object.DeleteMarker || !lv.Entries[0].IsLatest {
		t.Fatalf("versions: %d", len(lv.Entries))
	}
	lo, _ := s.ListObjects(ctx, "ver", meta.ListOptions{})
	if len(lo.Entries) != 0 {
		t.Fatal("list should hide deleted key")
	}
	// Remove marker → v3 visible again.
	if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "ver", Key: "k", VersionID: dr.VersionID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := readAll(t, s, "ver", "k", "", nil); string(got) != "three" {
		t.Fatal("after marker removal")
	}
	// Suspend: put replaces null; delete removes null and adds null marker.
	s.UpdateBucket(ctx, "ver", func(b *meta.Bucket) error { b.Versioning = "Suspended"; return nil })
	n2 := put("k", "null2")
	if n2.VersionID != meta.NullVersionID {
		t.Fatal("suspended put must be null")
	}
	if got, _ := readAll(t, s, "ver", "k", meta.NullVersionID, nil); string(got) != "null2" {
		t.Fatal("null replaced")
	}
	lv, _ = s.ListVersions(ctx, "ver", meta.ListOptions{})
	if len(lv.Entries) != 3 { // null2, v3, v2
		t.Fatalf("versions after suspend: %d", len(lv.Entries))
	}
	dr, _ = s.DeleteObject(ctx, actor, DeleteInput{Bucket: "ver", Key: "k"})
	if !dr.DeleteMarker || dr.VersionID != meta.NullVersionID {
		t.Fatalf("suspended delete: %+v", dr)
	}
	lv, _ = s.ListVersions(ctx, "ver", meta.ListOptions{})
	if len(lv.Entries) != 3 || !lv.Entries[0].Object.DeleteMarker || lv.Entries[0].Object.VersionID != meta.NullVersionID {
		t.Fatalf("versions after suspended delete: %+v", lv.Entries[0].Object)
	}
	// Permanent delete of v3 and v2 and the marker.
	for _, e := range lv.Entries {
		if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "ver", Key: "k", VersionID: e.Object.VersionID}); err != nil {
			t.Fatal(err)
		}
	}
	lv, _ = s.ListVersions(ctx, "ver", meta.ListOptions{})
	if len(lv.Entries) != 0 {
		t.Fatal("all gone")
	}
	if err := s.DeleteBucket(ctx, "ver"); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartAndChecksums(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	s.CreateBucket(ctx, actor, CreateBucketInput{Name: "mpb"})
	u, err := s.CreateUpload(ctx, actor, CreateUploadInput{Bucket: "mpb", Key: "big", ChecksumAlgorithm: "CRC32C", ChecksumType: "FULL_OBJECT",
		Attrs: ObjectAttrs{ContentType: "application/octet-stream"}})
	if err != nil {
		t.Fatal(err)
	}
	p1 := make([]byte, MinPartSize+10)
	rand.Read(p1)
	p2 := []byte("last part")
	var cps []CompletePart
	for i, d := range [][]byte{p1, p2} {
		p, err := s.UploadPart(ctx, UploadPartInput{Bucket: "mpb", Key: "big", UploadID: u.UploadID, PartNumber: i + 1, Body: bytes.NewReader(d), Size: int64(len(d))})
		if err != nil {
			t.Fatal(err)
		}
		cps = append(cps, CompletePart{PartNumber: i + 1, ETag: p.ETag})
	}
	// Wrong order / bad etag / too small.
	if _, err := s.CompleteUpload(ctx, actor, CompleteInput{Bucket: "mpb", Key: "big", UploadID: u.UploadID, Parts: []CompletePart{cps[1], cps[0]}, ObjectSize: -1}); code(err) != s3err.InvalidPartOrder {
		t.Fatalf("order: %v", err)
	}
	if _, err := s.CompleteUpload(ctx, actor, CompleteInput{Bucket: "mpb", Key: "big", UploadID: u.UploadID, Parts: []CompletePart{{1, "bad", ""}}, ObjectSize: -1}); code(err) != s3err.InvalidPart {
		t.Fatalf("etag: %v", err)
	}
	o, err := s.CompleteUpload(ctx, actor, CompleteInput{Bucket: "mpb", Key: "big", UploadID: u.UploadID, Parts: cps, ObjectSize: int64(len(p1) + len(p2))})
	if err != nil {
		t.Fatal(err)
	}
	if o.Size != int64(len(p1)+len(p2)) || o.ETag[len(o.ETag)-2:] != "-2" || o.Checksum == nil || o.Checksum.Type != "FULL_OBJECT" {
		t.Fatalf("completed: %+v", o)
	}
	h, _ := checksum.New("CRC32C")
	h.Write(p1)
	h.Write(p2)
	if o.Checksum.Value != checksum.Encode(h.Sum(nil)) {
		t.Fatal("full-object crc32c mismatch")
	}
	all, res := readAll(t, s, "mpb", "big", "", nil)
	if !bytes.Equal(all, append(append([]byte{}, p1...), p2...)) || res.PartsCount != 2 {
		t.Fatal("multipart read")
	}
	// Range straddling the part boundary.
	got, _ := readAll(t, s, "mpb", "big", "", &Range{int64(len(p1)) - 3, int64(len(p1)) + 3})
	want := append(append([]byte{}, p1[len(p1)-3:]...), p2[:4]...)
	if !bytes.Equal(got, want) {
		t.Fatalf("straddle: %x vs %x", got, want)
	}
	// Part number read.
	pr, _ := s.GetObject(ctx, GetInput{Bucket: "mpb", Key: "big", PartNumber: 2})
	pb, _ := io.ReadAll(pr.Body)
	if string(pb) != "last part" {
		t.Fatal("part read")
	}
	if _, err := s.GetUpload(ctx, "mpb", "big", u.UploadID); code(err) != s3err.NoSuchUpload {
		t.Fatal("upload should be gone")
	}
	// Single PUT with SHA256 checksum, wrong value.
	data := []byte("checksummed")
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "mpb", Key: "c", Body: bytes.NewReader(data), Size: int64(len(data)),
		Checksum: &ChecksumRequest{Algorithm: "SHA256", Value: base64.StdEncoding.EncodeToString(make([]byte, 32))}}); code(err) != s3err.BadDigest {
		t.Fatalf("bad checksum: %v", err)
	}
	// Trailing checksum.
	hh, _ := checksum.New("CRC64NVME")
	hh.Write(data)
	trail := checksum.Encode(hh.Sum(nil))
	o2, err := s.PutObject(ctx, actor, PutInput{Bucket: "mpb", Key: "c", Body: bytes.NewReader(data), Size: -1,
		Checksum: &ChecksumRequest{Algorithm: "CRC64NVME", Trailer: func() (string, string) { return "crc64nvme", trail }}})
	if err != nil || o2.Checksum.Value != trail {
		t.Fatalf("trailer: %v", err)
	}
	// Abort.
	u2, _ := s.CreateUpload(ctx, actor, CreateUploadInput{Bucket: "mpb", Key: "abort"})
	s.UploadPart(ctx, UploadPartInput{Bucket: "mpb", Key: "abort", UploadID: u2.UploadID, PartNumber: 1, Body: bytes.NewReader(data), Size: int64(len(data))})
	lu, _ := s.ListUploads(ctx, "mpb", meta.ListOptions{})
	if len(lu.Uploads) != 1 {
		t.Fatal("list uploads")
	}
	if err := s.AbortUpload(ctx, "mpb", "abort", u2.UploadID); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortUpload(ctx, "mpb", "abort", u2.UploadID); code(err) != s3err.NoSuchUpload {
		t.Fatal("double abort")
	}
}

func TestSSEAndCopy(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	s.CreateBucket(ctx, actor, CreateBucketInput{Name: "enc"})
	data := make([]byte, 200000)
	rand.Read(data)
	ck := make([]byte, 32)
	rand.Read(ck)
	ckmd5 := md5.Sum(ck)
	cases := []SSERequest{{Type: "AES256"}, {Type: "aws:kms"}, {Type: "SSE-C", CustomerKey: ck, CustomerKeyMD5: base64.StdEncoding.EncodeToString(ckmd5[:])}}
	for _, c := range cases {
		o, err := s.PutObject(ctx, actor, PutInput{Bucket: "enc", Key: "k-" + c.Type, Body: bytes.NewReader(data), Size: int64(len(data)), SSE: c})
		if err != nil {
			t.Fatalf("%s: %v", c.Type, err)
		}
		if o.SSE == nil || o.SSE.Type != c.Type {
			t.Fatal("sse info")
		}
		res, err := s.GetObject(ctx, GetInput{Bucket: "enc", Key: "k-" + c.Type, Range: &Range{70000, 130000}, SSE: c})
		if err != nil {
			t.Fatalf("%s get: %v", c.Type, err)
		}
		got, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !bytes.Equal(got, data[70000:130001]) {
			t.Fatalf("%s: decrypted range mismatch", c.Type)
		}
		// Blob on disk must not contain plaintext.
		rc, _ := s.blobs.Open(ctx, "enc", o.Parts[0].Blob, 0, -1)
		raw, _ := io.ReadAll(rc)
		rc.Close()
		if bytes.Contains(raw, data[1000:1100]) {
			t.Fatal("plaintext on disk")
		}
	}
	// SSE-C without key fails; wrong key fails.
	if _, err := s.GetObject(ctx, GetInput{Bucket: "enc", Key: "k-SSE-C"}); code(err) != s3err.InvalidRequest {
		t.Fatalf("ssec no key: %v", err)
	}
	wrong := make([]byte, 32)
	wm := md5.Sum(wrong)
	if _, err := s.GetObject(ctx, GetInput{Bucket: "enc", Key: "k-SSE-C", SSE: SSERequest{Type: "SSE-C", CustomerKey: wrong, CustomerKeyMD5: base64.StdEncoding.EncodeToString(wm[:])}}); code(err) != s3err.InvalidRequest && code(err) != s3err.AccessDenied {
		t.Fatalf("ssec wrong key: %v", err)
	}
	// Bucket default encryption.
	s.UpdateBucket(ctx, "enc", func(b *meta.Bucket) error { b.Encryption = &meta.EncryptionRule{Algorithm: "AES256"}; return nil })
	o, _ := s.PutObject(ctx, actor, PutInput{Bucket: "enc", Key: "def", Body: bytes.NewReader(data[:10]), Size: 10})
	if o.SSE == nil || o.SSE.Type != "AES256" {
		t.Fatal("bucket default encryption not applied")
	}
	// Copy SSE-S3 → SSE-KMS with metadata replace.
	dst, src, err := s.CopyObject(ctx, actor, CopyInput{SrcBucket: "enc", SrcKey: "k-AES256", DstBucket: "enc", DstKey: "copy", MetadataDirective: "REPLACE",
		Attrs: ObjectAttrs{ContentType: "x/y"}, SSE: SSERequest{Type: "aws:kms"}})
	if err != nil || src.Key != "k-AES256" || dst.SSE.Type != "aws:kms" || dst.ContentType != "x/y" {
		t.Fatalf("copy: %v", err)
	}
	got, _ := readAll(t, s, "enc", "copy", "", nil)
	if !bytes.Equal(got, data) {
		t.Fatal("copy content")
	}
	if _, _, err := s.CopyObject(ctx, actor, CopyInput{SrcBucket: "enc", SrcKey: "copy", DstBucket: "enc", DstKey: "copy"}); code(err) != s3err.InvalidRequest {
		t.Fatalf("self copy: %v", err)
	}
	// Encrypted multipart.
	u, _ := s.CreateUpload(ctx, actor, CreateUploadInput{Bucket: "enc", Key: "empart", SSE: SSERequest{Type: "AES256"}})
	big := make([]byte, MinPartSize)
	rand.Read(big)
	pa, _ := s.UploadPart(ctx, UploadPartInput{Bucket: "enc", Key: "empart", UploadID: u.UploadID, PartNumber: 1, Body: bytes.NewReader(big), Size: int64(len(big))})
	pc, _, err := s.UploadPartCopy(ctx, UploadPartCopyInput{SrcBucket: "enc", SrcKey: "k-AES256", SrcRange: &Range{10, 19}, Bucket: "enc", Key: "empart", UploadID: u.UploadID, PartNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	eo, err := s.CompleteUpload(ctx, actor, CompleteInput{Bucket: "enc", Key: "empart", UploadID: u.UploadID, ObjectSize: -1, Parts: []CompletePart{{1, pa.ETag, ""}, {2, pc.ETag, ""}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = readAll(t, s, "enc", "empart", "", &Range{int64(len(big)) - 2, -1})
	if !bytes.Equal(got, append(big[len(big)-2:], data[10:20]...)) || eo.Size != int64(len(big))+10 {
		t.Fatal("encrypted multipart read")
	}
}

func TestObjectLockAndTags(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	s.CreateBucket(ctx, actor, CreateBucketInput{Name: "lock", ObjectLockEnabled: true})
	b, _ := s.GetBucket(ctx, "lock")
	if b.Versioning != "Enabled" {
		t.Fatal("lock implies versioning")
	}
	data := []byte("x")
	until := time.Now().Add(time.Hour)
	o, err := s.PutObject(ctx, actor, PutInput{Bucket: "lock", Key: "k", Body: bytes.NewReader(data), Size: 1,
		Attrs: ObjectAttrs{Retention: &meta.Retention{Mode: "GOVERNANCE", RetainUntil: until}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "lock", Key: "k", VersionID: o.VersionID}); code(err) != s3err.AccessDenied {
		t.Fatalf("locked delete: %v", err)
	}
	// Delete marker creation is still allowed.
	if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "lock", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	// Shorten governance without bypass fails; with bypass ok.
	if _, err := s.PutObjectRetention(ctx, actor, "lock", "k", o.VersionID, nil, false); code(err) != s3err.AccessDenied {
		t.Fatalf("shorten: %v", err)
	}
	if _, err := s.PutObjectRetention(ctx, actor, "lock", "k", o.VersionID, &meta.Retention{Mode: "COMPLIANCE", RetainUntil: until.Add(time.Hour)}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObjectRetention(ctx, actor, "lock", "k", o.VersionID, &meta.Retention{Mode: "GOVERNANCE", RetainUntil: until.Add(time.Hour)}, true); code(err) != s3err.AccessDenied {
		t.Fatal("compliance cannot be weakened")
	}
	if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "lock", Key: "k", VersionID: o.VersionID, BypassGovernance: true}); code(err) != s3err.AccessDenied {
		t.Fatal("compliance delete must fail even with bypass")
	}
	// Legal hold.
	o2, _ := s.PutObject(ctx, actor, PutInput{Bucket: "lock", Key: "h", Body: bytes.NewReader(data), Size: 1, Attrs: ObjectAttrs{LegalHold: true, LegalHoldSet: true}})
	if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "lock", Key: "h", VersionID: o2.VersionID}); code(err) != s3err.AccessDenied {
		t.Fatal("legal hold")
	}
	s.PutObjectLegalHold(ctx, actor, "lock", "h", o2.VersionID, false)
	if _, err := s.DeleteObject(ctx, actor, DeleteInput{Bucket: "lock", Key: "h", VersionID: o2.VersionID}); err != nil {
		t.Fatal(err)
	}
	// Retention on a bucket without lock config.
	s.CreateBucket(ctx, actor, CreateBucketInput{Name: "nolock"})
	if _, err := s.PutObject(ctx, actor, PutInput{Bucket: "nolock", Key: "k", Body: bytes.NewReader(data), Size: 1,
		Attrs: ObjectAttrs{Retention: &meta.Retention{Mode: "GOVERNANCE", RetainUntil: until}}}); code(err) != s3err.InvalidRequest {
		t.Fatalf("retention without lock: %v", err)
	}
	// Tags.
	s.PutObject(ctx, actor, PutInput{Bucket: "nolock", Key: "t", Body: bytes.NewReader(data), Size: 1})
	if _, err := s.PutObjectTagging(ctx, actor, "nolock", "t", "", []meta.Tag{{Key: "a", Value: "1"}, {Key: "a", Value: "2"}}); code(err) != s3err.InvalidTag {
		t.Fatal("dup tags")
	}
	to, _ := s.PutObjectTagging(ctx, actor, "nolock", "t", "", []meta.Tag{{Key: "a", Value: "1"}})
	if len(to.Tags) != 1 {
		t.Fatal("tags")
	}
	st, _ := s.StatObject(ctx, "nolock", "t", "")
	if len(st.Tags) != 1 || st.Tags[0].Value != "1" {
		t.Fatal("tags persisted")
	}
	s.DeleteObjectTagging(ctx, actor, "nolock", "t", "")
	st, _ = s.StatObject(ctx, "nolock", "t", "")
	if len(st.Tags) != 0 {
		t.Fatal("tags cleared")
	}
}

func TestEvents(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	var names []string
	s.Notify = func(e Event) { names = append(names, e.Name) }
	s.CreateBucket(ctx, actor, CreateBucketInput{Name: "evt"})
	s.PutObject(ctx, actor, PutInput{Bucket: "evt", Key: "k", Body: bytes.NewReader([]byte("x")), Size: 1})
	s.DeleteObject(ctx, actor, DeleteInput{Bucket: "evt", Key: "k"})
	if len(names) != 2 || names[0] != "s3:ObjectCreated:Put" || names[1] != "s3:ObjectRemoved:Delete" {
		t.Fatalf("events: %v", names)
	}
}
