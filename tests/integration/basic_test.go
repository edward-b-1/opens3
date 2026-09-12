package integration

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestBucketLifecycle(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("alpha")
	e.mkBucket("beta")
	lb, err := e.s3.ListBuckets(e.ctx, &s3.ListBucketsInput{})
	if err != nil || len(lb.Buckets) != 2 || *lb.Buckets[0].Name != "alpha" {
		t.Fatalf("list buckets: %v %+v", err, lb)
	}
	if _, err := e.s3.HeadBucket(e.ctx, &s3.HeadBucketInput{Bucket: aws.String("alpha")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.HeadBucket(e.ctx, &s3.HeadBucketInput{Bucket: aws.String("gamma")}); httpStatus(err) != 404 {
		t.Fatalf("head missing: %v", err)
	}
	if _, err := e.s3.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String("alpha")}); errCode(err) != "BucketAlreadyOwnedByYou" {
		t.Fatalf("dup: %v", err)
	}
	if _, err := e.s3.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String("Bad")}); errCode(err) != "InvalidBucketName" {
		t.Fatalf("bad name: %v", err)
	}
	loc, err := e.s3.GetBucketLocation(e.ctx, &s3.GetBucketLocationInput{Bucket: aws.String("alpha")})
	if err != nil || loc.LocationConstraint != "" {
		t.Fatalf("location: %v %v", err, loc.LocationConstraint)
	}
	e.put("alpha", "x", "1")
	if _, err := e.s3.DeleteBucket(e.ctx, &s3.DeleteBucketInput{Bucket: aws.String("alpha")}); errCode(err) != "BucketNotEmpty" {
		t.Fatalf("not empty: %v", err)
	}
	e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("alpha"), Key: aws.String("x")})
	if _, err := e.s3.DeleteBucket(e.ctx, &s3.DeleteBucketInput{Bucket: aws.String("alpha")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.DeleteBucket(e.ctx, &s3.DeleteBucketInput{Bucket: aws.String("alpha")}); errCode(err) != "NoSuchBucket" {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestObjectsBasic(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("objs")
	out, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt"), Body: strings.NewReader("hello world"),
		ContentType: aws.String("text/plain"), Metadata: map[string]string{"Owner": "me"}, CacheControl: aws.String("max-age=60")})
	if err != nil {
		t.Fatal(err)
	}
	if *out.ETag != `"5eb63bbbe01eeed093cb22bb8f5acdc3"` {
		t.Fatalf("etag %s", *out.ETag)
	}
	body, g := e.get("objs", "dir/hello.txt")
	if body != "hello world" || *g.ContentType != "text/plain" || g.Metadata["owner"] != "me" || *g.ContentLength != 11 || *g.CacheControl != "max-age=60" {
		t.Fatalf("get: %q %+v", body, g)
	}
	h, err := e.s3.HeadObject(e.ctx, &s3.HeadObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt")})
	if err != nil || *h.ContentLength != 11 || *h.ETag != *out.ETag {
		t.Fatalf("head: %v", err)
	}
	// Range.
	r, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt"), Range: aws.String("bytes=6-")})
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := io.ReadAll(r.Body)
	if string(rb) != "world" || *r.ContentRange != "bytes 6-10/11" {
		t.Fatalf("range: %q %v", rb, *r.ContentRange)
	}
	if _, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt"), Range: aws.String("bytes=100-200")}); errCode(err) != "InvalidRange" {
		t.Fatalf("invalid range: %v", err)
	}
	// Conditional.
	if _, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt"), IfNoneMatch: out.ETag}); httpStatus(err) != 304 {
		t.Fatalf("if-none-match: %v", err)
	}
	if _, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt"), IfMatch: aws.String(`"nope"`)}); errCode(err) != "PreconditionFailed" {
		t.Fatalf("if-match: %v", err)
	}
	// Missing key / bucket.
	if _, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("objs"), Key: aws.String("nope")}); errCode(err) != "NoSuchKey" {
		t.Fatalf("nosuchkey: %v", err)
	}
	if _, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("nobucket"), Key: aws.String("nope")}); errCode(err) != "NoSuchBucket" {
		t.Fatalf("nosuchbucket: %v", err)
	}
	if _, err := e.s3.HeadObject(e.ctx, &s3.HeadObjectInput{Bucket: aws.String("objs"), Key: aws.String("nope")}); httpStatus(err) != 404 {
		t.Fatalf("head 404: %v", err)
	}
	// Conditional write: If-None-Match: *
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt"), Body: strings.NewReader("x"), IfNoneMatch: aws.String("*")}); errCode(err) != "PreconditionFailed" {
		t.Fatalf("conditional write: %v", err)
	}
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("objs"), Key: aws.String("dir/hello.txt"), Body: strings.NewReader("x"), IfMatch: out.ETag}); err != nil {
		t.Fatalf("if-match write: %v", err)
	}
	// Coexisting keys a and a/b.
	e.put("objs", "a", "1")
	e.put("objs", "a/b", "2")
	if b, _ := e.get("objs", "a"); b != "1" {
		t.Fatal("a")
	}
	if b, _ := e.get("objs", "a/b"); b != "2" {
		t.Fatal("a/b")
	}
	// Large body (multiple chunks through aws-chunked).
	big := make([]byte, 3*1024*1024+17)
	rand.Read(big)
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("objs"), Key: aws.String("big"), Body: bytes.NewReader(big)}); err != nil {
		t.Fatal(err)
	}
	gb, _ := e.get("objs", "big")
	if !bytes.Equal([]byte(gb), big) {
		t.Fatal("big body mismatch")
	}
	// Delete.
	if _, err := e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("objs"), Key: aws.String("big")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("objs"), Key: aws.String("big")}); err != nil {
		t.Fatal("delete missing must succeed")
	}
	// DeleteObjects.
	do, err := e.s3.DeleteObjects(e.ctx, &s3.DeleteObjectsInput{Bucket: aws.String("objs"), Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: aws.String("a")}, {Key: aws.String("a/b")}, {Key: aws.String("zzz")}}}})
	if err != nil || len(do.Deleted) != 3 || len(do.Errors) != 0 {
		t.Fatalf("delete objects: %v %+v", err, do)
	}
}

func TestListing(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("list")
	for _, k := range []string{"a.txt", "b.txt", "photos/2024/a.jpg", "photos/2024/b.jpg", "photos/2025/c.jpg", "videos/v.mp4"} {
		e.put("list", k, k)
	}
	l, err := e.s3.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("list"), Delimiter: aws.String("/")})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Contents) != 2 || len(l.CommonPrefixes) != 2 || *l.CommonPrefixes[0].Prefix != "photos/" || *l.KeyCount != 4 {
		t.Fatalf("v2 delimiter: %+v", l)
	}
	l, _ = e.s3.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("list"), Prefix: aws.String("photos/"), Delimiter: aws.String("/")})
	if len(l.Contents) != 0 || len(l.CommonPrefixes) != 2 {
		t.Fatalf("v2 prefix: %+v", l)
	}
	// Pagination.
	var keys []string
	var token *string
	for {
		p, err := e.s3.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("list"), MaxKeys: aws.Int32(2), ContinuationToken: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range p.Contents {
			keys = append(keys, *o.Key)
		}
		if !*p.IsTruncated {
			break
		}
		token = p.NextContinuationToken
	}
	if len(keys) != 6 || keys[0] != "a.txt" || keys[5] != "videos/v.mp4" {
		t.Fatalf("paginated: %v", keys)
	}
	// start-after.
	l, _ = e.s3.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("list"), StartAfter: aws.String("photos/2024/b.jpg")})
	if len(l.Contents) != 2 || *l.Contents[0].Key != "photos/2025/c.jpg" {
		t.Fatalf("start-after: %+v", l.Contents)
	}
	// V1 with marker.
	v1, err := e.s3.ListObjects(e.ctx, &s3.ListObjectsInput{Bucket: aws.String("list"), Marker: aws.String("b.txt"), MaxKeys: aws.Int32(2), Delimiter: aws.String("/")})
	if err != nil {
		t.Fatal(err)
	}
	if len(v1.CommonPrefixes) != 2 || v1.IsTruncated == nil || *v1.IsTruncated {
		t.Fatalf("v1: %+v", v1)
	}
	// Owner + fetch-owner.
	l, _ = e.s3.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("list"), FetchOwner: aws.Bool(true), MaxKeys: aws.Int32(1)})
	if l.Contents[0].Owner == nil || l.Contents[0].Owner.ID == nil {
		t.Fatal("owner")
	}
	// encoding-type=url.
	e.put("list", "sp ace+plus", "x")
	l, _ = e.s3.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("list"), Prefix: aws.String("sp"), EncodingType: types.EncodingTypeUrl})
	if len(l.Contents) != 1 || (*l.Contents[0].Key != "sp ace+plus" && *l.Contents[0].Key != "sp+ace%2Bplus") {
		t.Fatalf("encoding: %q", *l.Contents[0].Key)
	}
}

func TestCopyAndMultipart(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("mpu")
	e.put("mpu", "src", "source data")
	cp, err := e.s3.CopyObject(e.ctx, &s3.CopyObjectInput{Bucket: aws.String("mpu"), Key: aws.String("dst"), CopySource: aws.String("mpu/src"),
		MetadataDirective: types.MetadataDirectiveReplace, ContentType: aws.String("x/y")})
	if err != nil {
		t.Fatal(err)
	}
	b, g := e.get("mpu", "dst")
	if b != "source data" || *g.ContentType != "x/y" || *cp.CopyObjectResult.ETag != *g.ETag {
		t.Fatalf("copy: %q %v", b, *g.ContentType)
	}
	if _, err := e.s3.CopyObject(e.ctx, &s3.CopyObjectInput{Bucket: aws.String("mpu"), Key: aws.String("dst2"), CopySource: aws.String("mpu/nope")}); errCode(err) != "NoSuchKey" {
		t.Fatalf("copy missing: %v", err)
	}
	// Multipart.
	cm, err := e.s3.CreateMultipartUpload(e.ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("mpu"), Key: aws.String("big"), ContentType: aws.String("application/x-big")})
	if err != nil {
		t.Fatal(err)
	}
	p1 := make([]byte, 5*1024*1024+1)
	rand.Read(p1)
	p2 := []byte("the end")
	var parts []types.CompletedPart
	for i, d := range [][]byte{p1, p2} {
		up, err := e.s3.UploadPart(e.ctx, &s3.UploadPartInput{Bucket: aws.String("mpu"), Key: aws.String("big"), UploadId: cm.UploadId, PartNumber: aws.Int32(int32(i + 1)), Body: bytes.NewReader(d)})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(int32(i + 1)), ETag: up.ETag})
	}
	// UploadPartCopy as part 3.
	upc, err := e.s3.UploadPartCopy(e.ctx, &s3.UploadPartCopyInput{Bucket: aws.String("mpu"), Key: aws.String("big"), UploadId: cm.UploadId, PartNumber: aws.Int32(3), CopySource: aws.String("mpu/src"), CopySourceRange: aws.String("bytes=0-5")})
	if err != nil {
		t.Fatal(err)
	}
	parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(3), ETag: upc.CopyPartResult.ETag})
	lp, err := e.s3.ListParts(e.ctx, &s3.ListPartsInput{Bucket: aws.String("mpu"), Key: aws.String("big"), UploadId: cm.UploadId})
	if err != nil || len(lp.Parts) != 3 {
		t.Fatalf("list parts: %v %d", err, len(lp.Parts))
	}
	lu, err := e.s3.ListMultipartUploads(e.ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String("mpu")})
	if err != nil || len(lu.Uploads) != 1 || *lu.Uploads[0].Key != "big" {
		t.Fatalf("list uploads: %v", err)
	}
	// Part 2 is too small and not last: EntityTooSmall.
	if _, err := e.s3.CompleteMultipartUpload(e.ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String("mpu"), Key: aws.String("big"), UploadId: cm.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}}); errCode(err) != "EntityTooSmall" {
		t.Fatalf("too small: %v", err)
	}
	comp, err := e.s3.CompleteMultipartUpload(e.ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String("mpu"), Key: aws.String("big"), UploadId: cm.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts[:2]}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(*comp.ETag, `-2"`) {
		t.Fatalf("etag %s", *comp.ETag)
	}
	got, g := e.get("mpu", "big")
	if !bytes.Equal([]byte(got), append(append([]byte{}, p1...), p2...)) || *g.ContentType != "application/x-big" {
		t.Fatal("multipart content")
	}
	if g.PartsCount == nil || *g.PartsCount != 2 {
		t.Fatalf("parts count %v", g.PartsCount)
	}
	pr, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("mpu"), Key: aws.String("big"), PartNumber: aws.Int32(2)})
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := io.ReadAll(pr.Body)
	if string(pb) != "the end" {
		t.Fatalf("part 2: %q", pb)
	}
	// Abort.
	cm2, _ := e.s3.CreateMultipartUpload(e.ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("mpu"), Key: aws.String("abort")})
	if _, err := e.s3.AbortMultipartUpload(e.ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String("mpu"), Key: aws.String("abort"), UploadId: cm2.UploadId}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.AbortMultipartUpload(e.ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String("mpu"), Key: aws.String("abort"), UploadId: cm2.UploadId}); errCode(err) != "NoSuchUpload" {
		t.Fatalf("abort twice: %v", err)
	}
	// GetObjectAttributes.
	attr, err := e.s3.GetObjectAttributes(e.ctx, &s3.GetObjectAttributesInput{Bucket: aws.String("mpu"), Key: aws.String("big"),
		ObjectAttributes: []types.ObjectAttributes{types.ObjectAttributesEtag, types.ObjectAttributesObjectSize, types.ObjectAttributesObjectParts, types.ObjectAttributesStorageClass}})
	if err != nil || *attr.ObjectSize != int64(len(p1)+len(p2)) || attr.ObjectParts == nil || *attr.ObjectParts.TotalPartsCount != 2 {
		t.Fatalf("attributes: %v %+v", err, attr)
	}
}

func TestVersioning(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("ver")
	e.put("ver", "k", "v0")
	if _, err := e.s3.PutBucketVersioning(e.ctx, &s3.PutBucketVersioningInput{Bucket: aws.String("ver"), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	gv, _ := e.s3.GetBucketVersioning(e.ctx, &s3.GetBucketVersioningInput{Bucket: aws.String("ver")})
	if gv.Status != types.BucketVersioningStatusEnabled {
		t.Fatal("versioning status")
	}
	v1 := e.put("ver", "k", "v1")
	v2 := e.put("ver", "k", "v2")
	if v1.VersionId == nil || *v1.VersionId == *v2.VersionId {
		t.Fatal("version ids")
	}
	o, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("ver"), Key: aws.String("k"), VersionId: v1.VersionId})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(o.Body)
	if string(b) != "v1" || *o.VersionId != *v1.VersionId {
		t.Fatal("get by version")
	}
	o, _ = e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("ver"), Key: aws.String("k"), VersionId: aws.String("null")})
	b, _ = io.ReadAll(o.Body)
	if string(b) != "v0" {
		t.Fatal("null version")
	}
	lv, err := e.s3.ListObjectVersions(e.ctx, &s3.ListObjectVersionsInput{Bucket: aws.String("ver")})
	if err != nil || len(lv.Versions) != 3 || !*lv.Versions[0].IsLatest || *lv.Versions[2].VersionId != "null" {
		t.Fatalf("list versions: %v %+v", err, lv.Versions)
	}
	d, err := e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("ver"), Key: aws.String("k")})
	if err != nil || d.DeleteMarker == nil || !*d.DeleteMarker || d.VersionId == nil {
		t.Fatalf("delete marker: %v %+v", err, d)
	}
	if _, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("ver"), Key: aws.String("k")}); errCode(err) != "NoSuchKey" {
		t.Fatalf("after marker: %v", err)
	}
	if _, err := e.s3.HeadObject(e.ctx, &s3.HeadObjectInput{Bucket: aws.String("ver"), Key: aws.String("k")}); httpStatus(err) != 404 {
		t.Fatalf("head after marker: %v", err)
	}
	lv, _ = e.s3.ListObjectVersions(e.ctx, &s3.ListObjectVersionsInput{Bucket: aws.String("ver")})
	if len(lv.DeleteMarkers) != 1 || !*lv.DeleteMarkers[0].IsLatest {
		t.Fatal("delete marker listed")
	}
	l, _ := e.s3.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("ver")})
	if len(l.Contents) != 0 {
		t.Fatal("deleted key must not list")
	}
	// Remove the marker.
	if _, err := e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("ver"), Key: aws.String("k"), VersionId: d.VersionId}); err != nil {
		t.Fatal(err)
	}
	b2, _ := e.get("ver", "k")
	if b2 != "v2" {
		t.Fatal("restored latest")
	}
	// Suspend.
	e.s3.PutBucketVersioning(e.ctx, &s3.PutBucketVersioningInput{Bucket: aws.String("ver"), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusSuspended}})
	s := e.put("ver", "k", "susp")
	if *s.VersionId != "null" {
		t.Fatalf("suspended version id %s", *s.VersionId)
	}
	lv, _ = e.s3.ListObjectVersions(e.ctx, &s3.ListObjectVersionsInput{Bucket: aws.String("ver")})
	if len(lv.Versions) != 3 || *lv.Versions[0].VersionId != "null" {
		t.Fatalf("suspended list: %+v", lv.Versions)
	}
	// Paginate versions.
	first, _ := e.s3.ListObjectVersions(e.ctx, &s3.ListObjectVersionsInput{Bucket: aws.String("ver"), MaxKeys: aws.Int32(1)})
	second, _ := e.s3.ListObjectVersions(e.ctx, &s3.ListObjectVersionsInput{Bucket: aws.String("ver"), KeyMarker: first.NextKeyMarker, VersionIdMarker: first.NextVersionIdMarker})
	if len(first.Versions) != 1 || len(second.Versions) != 2 || *second.Versions[0].VersionId != *v2.VersionId {
		t.Fatalf("version pagination: %+v / %+v", first.Versions, second.Versions)
	}
}

func TestTaggingAndBucketConfigs(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("cfg")
	e.put("cfg", "k", "x")
	if _, err := e.s3.PutObjectTagging(e.ctx, &s3.PutObjectTaggingInput{Bucket: aws.String("cfg"), Key: aws.String("k"), Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("a"), Value: aws.String("1")}}}}); err != nil {
		t.Fatal(err)
	}
	gt, _ := e.s3.GetObjectTagging(e.ctx, &s3.GetObjectTaggingInput{Bucket: aws.String("cfg"), Key: aws.String("k")})
	if len(gt.TagSet) != 1 || *gt.TagSet[0].Value != "1" {
		t.Fatal("object tags")
	}
	_, h := e.get("cfg", "k")
	if h.TagCount == nil || *h.TagCount != 1 {
		t.Fatal("tag count header")
	}
	e.s3.DeleteObjectTagging(e.ctx, &s3.DeleteObjectTaggingInput{Bucket: aws.String("cfg"), Key: aws.String("k")})
	gt, _ = e.s3.GetObjectTagging(e.ctx, &s3.GetObjectTaggingInput{Bucket: aws.String("cfg"), Key: aws.String("k")})
	if len(gt.TagSet) != 0 {
		t.Fatal("tags cleared")
	}
	// Put with tagging header.
	e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("cfg"), Key: aws.String("t"), Body: strings.NewReader("x"), Tagging: aws.String("x=1&y=2")})
	gt, _ = e.s3.GetObjectTagging(e.ctx, &s3.GetObjectTaggingInput{Bucket: aws.String("cfg"), Key: aws.String("t")})
	if len(gt.TagSet) != 2 {
		t.Fatal("tagging header")
	}
	// Bucket tagging.
	if _, err := e.s3.GetBucketTagging(e.ctx, &s3.GetBucketTaggingInput{Bucket: aws.String("cfg")}); errCode(err) != "NoSuchTagSet" {
		t.Fatalf("no tagset: %v", err)
	}
	e.s3.PutBucketTagging(e.ctx, &s3.PutBucketTaggingInput{Bucket: aws.String("cfg"), Tagging: &types.Tagging{TagSet: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}}})
	bt, err := e.s3.GetBucketTagging(e.ctx, &s3.GetBucketTaggingInput{Bucket: aws.String("cfg")})
	if err != nil || len(bt.TagSet) != 1 {
		t.Fatal("bucket tags")
	}
	// CORS.
	if _, err := e.s3.GetBucketCors(e.ctx, &s3.GetBucketCorsInput{Bucket: aws.String("cfg")}); errCode(err) != "NoSuchCORSConfiguration" {
		t.Fatalf("no cors: %v", err)
	}
	_, err = e.s3.PutBucketCors(e.ctx, &s3.PutBucketCorsInput{Bucket: aws.String("cfg"), CORSConfiguration: &types.CORSConfiguration{CORSRules: []types.CORSRule{
		{AllowedOrigins: []string{"https://example.com"}, AllowedMethods: []string{"GET", "PUT"}, AllowedHeaders: []string{"*"}, ExposeHeaders: []string{"ETag"}, MaxAgeSeconds: aws.Int32(300)}}}})
	if err != nil {
		t.Fatal(err)
	}
	gc, err := e.s3.GetBucketCors(e.ctx, &s3.GetBucketCorsInput{Bucket: aws.String("cfg")})
	if err != nil || len(gc.CORSRules) != 1 || gc.CORSRules[0].AllowedOrigins[0] != "https://example.com" {
		t.Fatalf("get cors: %v", err)
	}
	// Preflight.
	req, _ := http.NewRequest("OPTIONS", e.ts.URL+"/cfg/k", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "https://example.com" || resp.Header.Get("Access-Control-Max-Age") != "300" {
		t.Fatalf("preflight: %v %v %v", err, resp.StatusCode, resp.Header)
	}
	req.Header.Set("Origin", "https://evil.com")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatal("preflight evil origin")
	}
	// Lifecycle.
	_, err = e.s3.PutBucketLifecycleConfiguration(e.ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: aws.String("cfg"), LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: []types.LifecycleRule{
		{ID: aws.String("expire"), Status: types.ExpirationStatusEnabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String("tmp/")}, Expiration: &types.LifecycleExpiration{Days: aws.Int32(30)}},
		{ID: aws.String("mpu"), Status: types.ExpirationStatusEnabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")}, AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{DaysAfterInitiation: aws.Int32(7)}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	gl, err := e.s3.GetBucketLifecycleConfiguration(e.ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String("cfg")})
	if err != nil || len(gl.Rules) != 2 || *gl.Rules[0].Expiration.Days != 30 {
		t.Fatalf("lifecycle: %v", err)
	}
	e.s3.DeleteBucketLifecycle(e.ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String("cfg")})
	if _, err := e.s3.GetBucketLifecycleConfiguration(e.ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String("cfg")}); errCode(err) != "NoSuchLifecycleConfiguration" {
		t.Fatalf("lifecycle deleted: %v", err)
	}
	// Encryption config.
	if _, err := e.s3.GetBucketEncryption(e.ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String("cfg")}); errCode(err) != "ServerSideEncryptionConfigurationNotFoundError" {
		t.Fatalf("no encryption: %v", err)
	}
	_, err = e.s3.PutBucketEncryption(e.ctx, &s3.PutBucketEncryptionInput{Bucket: aws.String("cfg"), ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{Rules: []types.ServerSideEncryptionRule{
		{ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{SSEAlgorithm: types.ServerSideEncryptionAes256}}}}})
	if err != nil {
		t.Fatal(err)
	}
	ge, _ := e.s3.GetBucketEncryption(e.ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String("cfg")})
	if ge.ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm != types.ServerSideEncryptionAes256 {
		t.Fatal("encryption config")
	}
	po := e.put("cfg", "enc", "secret")
	if po.ServerSideEncryption != types.ServerSideEncryptionAes256 {
		t.Fatal("default encryption applied")
	}
	if b, _ := e.get("cfg", "enc"); b != "secret" {
		t.Fatal("encrypted read")
	}
	// Public access block / ownership / policy status.
	e.s3.PutPublicAccessBlock(e.ctx, &s3.PutPublicAccessBlockInput{Bucket: aws.String("cfg"), PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{BlockPublicPolicy: aws.Bool(true)}})
	pab, err := e.s3.GetPublicAccessBlock(e.ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String("cfg")})
	if err != nil || !*pab.PublicAccessBlockConfiguration.BlockPublicPolicy {
		t.Fatal("pab")
	}
	pubPolicy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"}]}`, "cfg")
	if _, err := e.s3.PutBucketPolicy(e.ctx, &s3.PutBucketPolicyInput{Bucket: aws.String("cfg"), Policy: aws.String(pubPolicy)}); errCode(err) != "AccessDenied" {
		t.Fatalf("blocked public policy: %v", err)
	}
	e.s3.DeletePublicAccessBlock(e.ctx, &s3.DeletePublicAccessBlockInput{Bucket: aws.String("cfg")})
	oc, _ := e.s3.GetBucketOwnershipControls(e.ctx, &s3.GetBucketOwnershipControlsInput{Bucket: aws.String("cfg")})
	if oc.OwnershipControls.Rules[0].ObjectOwnership != types.ObjectOwnershipBucketOwnerEnforced {
		t.Fatal("ownership default")
	}
	// Named configs round-trip.
	e.s3.PutBucketMetricsConfiguration(e.ctx, &s3.PutBucketMetricsConfigurationInput{Bucket: aws.String("cfg"), Id: aws.String("m1"), MetricsConfiguration: &types.MetricsConfiguration{Id: aws.String("m1")}})
	gm, err := e.s3.GetBucketMetricsConfiguration(e.ctx, &s3.GetBucketMetricsConfigurationInput{Bucket: aws.String("cfg"), Id: aws.String("m1")})
	if err != nil || *gm.MetricsConfiguration.Id != "m1" {
		t.Fatalf("metrics config: %v", err)
	}
	lm, _ := e.s3.ListBucketMetricsConfigurations(e.ctx, &s3.ListBucketMetricsConfigurationsInput{Bucket: aws.String("cfg")})
	if len(lm.MetricsConfigurationList) != 1 {
		t.Fatal("list metrics configs")
	}
	// Website.
	e.s3.PutBucketWebsite(e.ctx, &s3.PutBucketWebsiteInput{Bucket: aws.String("cfg"), WebsiteConfiguration: &types.WebsiteConfiguration{IndexDocument: &types.IndexDocument{Suffix: aws.String("index.html")}}})
	gw, err := e.s3.GetBucketWebsite(e.ctx, &s3.GetBucketWebsiteInput{Bucket: aws.String("cfg")})
	if err != nil || *gw.IndexDocument.Suffix != "index.html" {
		t.Fatal("website")
	}
	// Notification.
	gn, err := e.s3.GetBucketNotificationConfiguration(e.ctx, &s3.GetBucketNotificationConfigurationInput{Bucket: aws.String("cfg")})
	if err != nil || len(gn.QueueConfigurations) != 0 {
		t.Fatalf("empty notification: %v", err)
	}
	_ = time.Now
}

func TestPolicyAndAnonymousAccess(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("pub")
	e.put("pub", "file", "public data")
	anon := e.anon()
	if _, err := anon.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("pub"), Key: aws.String("file")}); errCode(err) != "AccessDenied" {
		t.Fatalf("anon before policy: %v", err)
	}
	if _, err := e.s3.GetBucketPolicy(e.ctx, &s3.GetBucketPolicyInput{Bucket: aws.String("pub")}); errCode(err) != "NoSuchBucketPolicy" {
		t.Fatalf("no policy: %v", err)
	}
	pol := `{"Version":"2012-10-17","Statement":[{"Sid":"public","Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":"arn:aws:s3:::pub/*"}]}`
	if _, err := e.s3.PutBucketPolicy(e.ctx, &s3.PutBucketPolicyInput{Bucket: aws.String("pub"), Policy: aws.String(pol)}); err != nil {
		t.Fatal(err)
	}
	gp, _ := e.s3.GetBucketPolicy(e.ctx, &s3.GetBucketPolicyInput{Bucket: aws.String("pub")})
	if *gp.Policy != pol {
		t.Fatal("policy round trip")
	}
	ps, _ := e.s3.GetBucketPolicyStatus(e.ctx, &s3.GetBucketPolicyStatusInput{Bucket: aws.String("pub")})
	if !*ps.PolicyStatus.IsPublic {
		t.Fatal("policy status")
	}
	o, err := anon.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("pub"), Key: aws.String("file")})
	if err != nil {
		t.Fatalf("anon get: %v", err)
	}
	b, _ := io.ReadAll(o.Body)
	if string(b) != "public data" {
		t.Fatal("anon body")
	}
	// Plain HTTP GET.
	resp, body := rawGet(e.ts.URL + "/pub/file")
	if resp.StatusCode != 200 || body != "public data" {
		t.Fatalf("raw get: %d %s", resp.StatusCode, body)
	}
	if _, err := anon.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("pub"), Key: aws.String("x"), Body: strings.NewReader("x")}); errCode(err) != "AccessDenied" {
		t.Fatalf("anon put: %v", err)
	}
	if _, err := anon.ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("pub")}); errCode(err) != "AccessDenied" {
		t.Fatalf("anon list: %v", err)
	}
	// Bad policy.
	if _, err := e.s3.PutBucketPolicy(e.ctx, &s3.PutBucketPolicyInput{Bucket: aws.String("pub"), Policy: aws.String(`{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::other/*"}]}`)}); errCode(err) != "MalformedPolicy" {
		t.Fatalf("bad policy: %v", err)
	}
	e.s3.DeleteBucketPolicy(e.ctx, &s3.DeleteBucketPolicyInput{Bucket: aws.String("pub")})
	if _, err := anon.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("pub"), Key: aws.String("file")}); errCode(err) != "AccessDenied" {
		t.Fatalf("anon after delete: %v", err)
	}
	// IAM user with readonly policy.
	if err := e.srv.IAM.CreateUser("reader", "readersecret", []string{"readonly"}); err != nil {
		t.Fatal(err)
	}
	rc := e.client("reader", "readersecret", "")
	if _, err := rc.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("pub"), Key: aws.String("file")}); err != nil {
		t.Fatalf("reader get: %v", err)
	}
	if _, err := rc.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("pub"), Key: aws.String("x"), Body: strings.NewReader("x")}); errCode(err) != "AccessDenied" {
		t.Fatalf("reader put: %v", err)
	}
	lb, err := rc.ListBuckets(e.ctx, &s3.ListBucketsInput{})
	if err != nil || len(lb.Buckets) != 1 {
		t.Fatalf("reader list buckets: %v %d", err, len(lb.Buckets))
	}
	// Bad credentials.
	bad := e.client("nobody", "nobodysecret", "")
	if _, err := bad.ListBuckets(e.ctx, &s3.ListBucketsInput{}); errCode(err) != "InvalidAccessKeyId" {
		t.Fatalf("bad key: %v", err)
	}
	wrong := e.client(rootUser, "wrongsecret", "")
	if _, err := wrong.ListBuckets(e.ctx, &s3.ListBucketsInput{}); errCode(err) != "SignatureDoesNotMatch" {
		t.Fatalf("wrong secret: %v", err)
	}
}

func TestPresigned(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("pre")
	e.put("pre", "k", "presigned data")
	ps := s3.NewPresignClient(e.s3)
	req, err := ps.PresignGetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("pre"), Key: aws.String("k")}, s3.WithPresignExpires(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	resp, body := rawGet(req.URL)
	if resp.StatusCode != 200 || body != "presigned data" {
		t.Fatalf("presigned get: %d %s", resp.StatusCode, body)
	}
	// Tampered.
	resp, _ = rawGet(strings.Replace(req.URL, "X-Amz-Signature=", "X-Amz-Signature=0", 1))
	if resp.StatusCode != 403 {
		t.Fatalf("tampered: %d", resp.StatusCode)
	}
	// Presigned PUT.
	preq, err := ps.PresignPutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("pre"), Key: aws.String("up"), ContentType: aws.String("text/plain")}, s3.WithPresignExpires(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	hreq, _ := http.NewRequest("PUT", preq.URL, strings.NewReader("uploaded"))
	for k, v := range preq.SignedHeader {
		hreq.Header[k] = v
	}
	hresp, err := http.DefaultClient.Do(hreq)
	if err != nil || hresp.StatusCode != 200 {
		t.Fatalf("presigned put: %v %v", err, hresp)
	}
	if b, _ := e.get("pre", "up"); b != "uploaded" {
		t.Fatal("presigned upload content")
	}
}

func TestSSEAndChecksums(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("sse")
	// SSE-S3 and SSE-KMS.
	o, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sse"), Key: aws.String("s3"), Body: strings.NewReader("aes"), ServerSideEncryption: types.ServerSideEncryptionAes256})
	if err != nil || o.ServerSideEncryption != types.ServerSideEncryptionAes256 {
		t.Fatalf("sse-s3: %v", err)
	}
	o, err = e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sse"), Key: aws.String("kms"), Body: strings.NewReader("kms"), ServerSideEncryption: types.ServerSideEncryptionAwsKms})
	if err != nil || o.ServerSideEncryption != types.ServerSideEncryptionAwsKms || o.SSEKMSKeyId == nil {
		t.Fatalf("sse-kms: %v", err)
	}
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sse"), Key: aws.String("kms2"), Body: strings.NewReader("kms"), ServerSideEncryption: types.ServerSideEncryptionAwsKms, SSEKMSKeyId: aws.String("missing")}); errCode(err) != "KMSKeyNotFoundException" {
		t.Fatalf("missing kms key: %v", err)
	}
	if b, g := e.get("sse", "kms"); b != "kms" || g.ServerSideEncryption != types.ServerSideEncryptionAwsKms {
		t.Fatal("kms read")
	}
	// SSE-C.
	key := make([]byte, 32)
	rand.Read(key)
	keyB64 := b64(key)
	keyMD5 := b64md5(key)
	_, err = e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sse"), Key: aws.String("c"), Body: strings.NewReader("customer"),
		SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(keyB64), SSECustomerKeyMD5: aws.String(keyMD5)})
	if err != nil {
		t.Fatalf("sse-c put: %v", err)
	}
	if _, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("sse"), Key: aws.String("c")}); httpStatus(err) != 400 {
		t.Fatalf("sse-c get without key: %v", err)
	}
	g, err := e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("sse"), Key: aws.String("c"), SSECustomerAlgorithm: aws.String("AES256"), SSECustomerKey: aws.String(keyB64), SSECustomerKeyMD5: aws.String(keyMD5)})
	if err != nil {
		t.Fatalf("sse-c get: %v", err)
	}
	b, _ := io.ReadAll(g.Body)
	if string(b) != "customer" || *g.SSECustomerKeyMD5 != keyMD5 {
		t.Fatal("sse-c content")
	}
	// Explicit checksums.
	data := "checksum me"
	o, err = e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sse"), Key: aws.String("ck"), Body: strings.NewReader(data), ChecksumAlgorithm: types.ChecksumAlgorithmSha256})
	if err != nil || o.ChecksumSHA256 == nil {
		t.Fatalf("checksum put: %v", err)
	}
	g, err = e.s3.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("sse"), Key: aws.String("ck"), ChecksumMode: types.ChecksumModeEnabled})
	if err != nil || g.ChecksumSHA256 == nil || *g.ChecksumSHA256 != *o.ChecksumSHA256 {
		t.Fatalf("checksum get: %v", err)
	}
	// Wrong precomputed checksum.
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sse"), Key: aws.String("bad"), Body: strings.NewReader(data), ChecksumCRC32: aws.String("AAAAAA==")}); errCode(err) != "BadDigest" && errCode(err) != "InvalidRequest" {
		t.Fatalf("bad checksum: %v", err)
	}
	// Multipart with CRC32C full-object checksum.
	cm, _ := e.s3.CreateMultipartUpload(e.ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("sse"), Key: aws.String("mpck"), ChecksumAlgorithm: types.ChecksumAlgorithmCrc32c, ChecksumType: types.ChecksumTypeFullObject})
	p1 := make([]byte, 5*1024*1024)
	rand.Read(p1)
	var parts []types.CompletedPart
	for i, d := range [][]byte{p1, []byte("tail")} {
		up, err := e.s3.UploadPart(e.ctx, &s3.UploadPartInput{Bucket: aws.String("sse"), Key: aws.String("mpck"), UploadId: cm.UploadId, PartNumber: aws.Int32(int32(i + 1)), Body: bytes.NewReader(d), ChecksumAlgorithm: types.ChecksumAlgorithmCrc32c})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(int32(i + 1)), ETag: up.ETag, ChecksumCRC32C: up.ChecksumCRC32C})
	}
	comp, err := e.s3.CompleteMultipartUpload(e.ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String("sse"), Key: aws.String("mpck"), UploadId: cm.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	if err != nil || comp.ChecksumCRC32C == nil || comp.ChecksumType != types.ChecksumTypeFullObject {
		t.Fatalf("mp checksum: %v %+v", err, comp)
	}
}

func TestObjectLock(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s3.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String("lock"), ObjectLockEnabledForBucket: aws.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	gl, err := e.s3.GetObjectLockConfiguration(e.ctx, &s3.GetObjectLockConfigurationInput{Bucket: aws.String("lock")})
	if err != nil || gl.ObjectLockConfiguration.ObjectLockEnabled != types.ObjectLockEnabledEnabled {
		t.Fatalf("lock config: %v", err)
	}
	until := time.Now().Add(2 * time.Hour).UTC()
	o, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("lock"), Key: aws.String("k"), Body: strings.NewReader("x"), ObjectLockMode: types.ObjectLockModeGovernance, ObjectLockRetainUntilDate: &until})
	if err != nil {
		t.Fatal(err)
	}
	gr, err := e.s3.GetObjectRetention(e.ctx, &s3.GetObjectRetentionInput{Bucket: aws.String("lock"), Key: aws.String("k")})
	if err != nil || gr.Retention.Mode != types.ObjectLockRetentionModeGovernance {
		t.Fatalf("retention: %v", err)
	}
	if _, err := e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("lock"), Key: aws.String("k"), VersionId: o.VersionId}); errCode(err) != "AccessDenied" {
		t.Fatalf("locked delete: %v", err)
	}
	if _, err := e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("lock"), Key: aws.String("k"), VersionId: o.VersionId, BypassGovernanceRetention: aws.Bool(true)}); err != nil {
		t.Fatalf("bypass delete: %v", err)
	}
	// Legal hold.
	o, _ = e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("lock"), Key: aws.String("h"), Body: strings.NewReader("x")})
	e.s3.PutObjectLegalHold(e.ctx, &s3.PutObjectLegalHoldInput{Bucket: aws.String("lock"), Key: aws.String("h"), LegalHold: &types.ObjectLockLegalHold{Status: types.ObjectLockLegalHoldStatusOn}})
	lh, _ := e.s3.GetObjectLegalHold(e.ctx, &s3.GetObjectLegalHoldInput{Bucket: aws.String("lock"), Key: aws.String("h")})
	if lh.LegalHold.Status != types.ObjectLockLegalHoldStatusOn {
		t.Fatal("legal hold")
	}
	if _, err := e.s3.DeleteObject(e.ctx, &s3.DeleteObjectInput{Bucket: aws.String("lock"), Key: aws.String("h"), VersionId: o.VersionId}); errCode(err) != "AccessDenied" {
		t.Fatalf("hold delete: %v", err)
	}
	// Default retention via config.
	_, err = e.s3.PutObjectLockConfiguration(e.ctx, &s3.PutObjectLockConfigurationInput{Bucket: aws.String("lock"), ObjectLockConfiguration: &types.ObjectLockConfiguration{
		ObjectLockEnabled: types.ObjectLockEnabledEnabled, Rule: &types.ObjectLockRule{DefaultRetention: &types.DefaultRetention{Mode: types.ObjectLockRetentionModeCompliance, Days: aws.Int32(1)}}}})
	if err != nil {
		t.Fatal(err)
	}
	h, _ := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("lock"), Key: aws.String("d"), Body: strings.NewReader("x")})
	ho, _ := e.s3.HeadObject(e.ctx, &s3.HeadObjectInput{Bucket: aws.String("lock"), Key: aws.String("d")})
	if ho.ObjectLockMode != types.ObjectLockModeCompliance {
		t.Fatalf("default retention: %v", ho.ObjectLockMode)
	}
	_ = h
}

func TestACLs(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s3.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String("acl"), ObjectOwnership: types.ObjectOwnershipObjectWriter}); err != nil {
		t.Fatal(err)
	}
	e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("acl"), Key: aws.String("pub"), Body: strings.NewReader("public"), ACL: types.ObjectCannedACLPublicRead})
	ga, err := e.s3.GetObjectAcl(e.ctx, &s3.GetObjectAclInput{Bucket: aws.String("acl"), Key: aws.String("pub")})
	if err != nil || len(ga.Grants) != 2 {
		t.Fatalf("object acl: %v %+v", err, ga)
	}
	resp, body := rawGet(e.ts.URL + "/acl/pub")
	if resp.StatusCode != 200 || body != "public" {
		t.Fatalf("public-read: %d", resp.StatusCode)
	}
	e.s3.PutObjectAcl(e.ctx, &s3.PutObjectAclInput{Bucket: aws.String("acl"), Key: aws.String("pub"), ACL: types.ObjectCannedACLPrivate})
	resp, _ = rawGet(e.ts.URL + "/acl/pub")
	if resp.StatusCode != 403 {
		t.Fatalf("after private: %d", resp.StatusCode)
	}
	// Bucket ACL public-read allows anonymous listing.
	e.s3.PutBucketAcl(e.ctx, &s3.PutBucketAclInput{Bucket: aws.String("acl"), ACL: types.BucketCannedACLPublicRead})
	if _, err := e.anon().ListObjectsV2(e.ctx, &s3.ListObjectsV2Input{Bucket: aws.String("acl")}); err != nil {
		t.Fatalf("anon list via bucket acl: %v", err)
	}
	// Enforced bucket rejects ACLs.
	e.mkBucket("enforced")
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("enforced"), Key: aws.String("x"), Body: strings.NewReader("x"), ACL: types.ObjectCannedACLPublicRead}); errCode(err) != "AccessControlListNotSupported" {
		t.Fatalf("enforced: %v", err)
	}
}

func TestSTS(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("sts")
	e.put("sts", "k", "x")
	// Assume role via form POST signed as root.
	form := "Action=AssumeRole&Version=2011-06-15&DurationSeconds=3600&RoleSessionName=test&Policy=" +
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::sts/*"}]}`
	req, _ := http.NewRequest("POST", e.ts.URL+"/", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signRequest(req, rootUser, rootPass, []byte(form))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("sts: %d %s", resp.StatusCode, body)
	}
	ak := between(string(body), "<AccessKeyId>", "</AccessKeyId>")
	sk := between(string(body), "<SecretAccessKey>", "</SecretAccessKey>")
	tok := between(string(body), "<SessionToken>", "</SessionToken>")
	tc := e.client(ak, sk, tok)
	if _, err := tc.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("sts"), Key: aws.String("k")}); err != nil {
		t.Fatalf("temp creds get: %v", err)
	}
	if _, err := tc.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sts"), Key: aws.String("k2"), Body: strings.NewReader("x")}); errCode(err) != "AccessDenied" {
		t.Fatalf("session policy should restrict: %v", err)
	}
	bad := e.client(ak, sk, "wrongtoken")
	if _, err := bad.GetObject(e.ctx, &s3.GetObjectInput{Bucket: aws.String("sts"), Key: aws.String("k")}); errCode(err) != "InvalidToken" {
		t.Fatalf("bad token: %v", err)
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		return ""
	}
	return s[:j]
}
