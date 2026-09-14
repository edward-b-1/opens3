package notify

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
)

func keysOf(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRecordJSON(t *testing.T) {
	ev := object.Event{
		Name:      "s3:ObjectCreated:Put",
		Bucket:    &meta.Bucket{Name: "photos", Owner: "owner-1", Region: "eu-central-1"},
		Object:    &meta.Object{Bucket: "photos", Key: "dir/my cat.jpg", VersionID: "null", Seq: 0x42, Size: 1024, ETag: "abc", ContentType: "image/jpeg", UserMeta: map[string]string{"camera": "x100"}},
		Key:       "dir/my cat.jpg",
		VersionID: "null",
		Time:      time.Date(2026, 9, 12, 10, 15, 30, 123456789, time.UTC),
		Actor:     object.Actor{CanonicalID: "user-1"},
	}
	rec := BuildRecord(ev, RecordOptions{ConfigurationID: "cfg-1", SourceIP: "10.0.0.1"})
	data, err := json.Marshal(Message{Records: []Record{rec}})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keysOf(doc), []string{"Records"}) {
		t.Fatalf("top keys %v", keysOf(doc))
	}
	r := doc["Records"].([]any)[0].(map[string]any)
	want := []string{"awsRegion", "eventName", "eventSource", "eventTime", "eventVersion", "requestParameters", "responseElements", "s3", "userIdentity"}
	if got := keysOf(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("record keys %v", got)
	}
	s3 := r["s3"].(map[string]any)
	if got := keysOf(s3); !reflect.DeepEqual(got, []string{"bucket", "configurationId", "object", "s3SchemaVersion"}) {
		t.Fatalf("s3 keys %v", got)
	}
	bkt := s3["bucket"].(map[string]any)
	if got := keysOf(bkt); !reflect.DeepEqual(got, []string{"arn", "name", "ownerIdentity"}) {
		t.Fatalf("bucket keys %v", got)
	}
	obj := s3["object"].(map[string]any)
	if got := keysOf(obj); !reflect.DeepEqual(got, []string{"contentType", "eTag", "key", "sequencer", "size", "userMetadata", "versionId"}) {
		t.Fatalf("object keys %v", got)
	}
	if got := keysOf(r["responseElements"].(map[string]any)); !reflect.DeepEqual(got, []string{"x-amz-id-2", "x-amz-request-id"}) {
		t.Fatalf("responseElements keys %v", got)
	}
	checks := map[string]any{
		"eventVersion": "2.1", "eventSource": "aws:s3", "awsRegion": "eu-central-1", "eventTime": "2026-09-12T10:15:30.123Z", "eventName": "ObjectCreated:Put",
	}
	for k, v := range checks {
		if r[k] != v {
			t.Errorf("%s = %v want %v", k, r[k], v)
		}
	}
	if r["userIdentity"].(map[string]any)["principalId"] != "user-1" || r["requestParameters"].(map[string]any)["sourceIPAddress"] != "10.0.0.1" {
		t.Fatalf("identity: %v", r)
	}
	if s3["s3SchemaVersion"] != "1.0" || s3["configurationId"] != "cfg-1" || bkt["name"] != "photos" || bkt["arn"] != "arn:aws:s3:::photos" || bkt["ownerIdentity"].(map[string]any)["principalId"] != "owner-1" {
		t.Fatalf("s3: %v", s3)
	}
	if obj["key"] != "dir/my+cat.jpg" || obj["size"] != float64(1024) || obj["eTag"] != "abc" || obj["versionId"] != "null" || obj["sequencer"] != "0000000000000042" || obj["contentType"] != "image/jpeg" {
		t.Fatalf("object: %v", obj)
	}
	if obj["userMetadata"].(map[string]any)["X-Amz-Meta-Camera"] != "x100" {
		t.Fatalf("userMetadata: %v", obj["userMetadata"])
	}
	// Region fallback and delete markers (no object attributes).
	rec = BuildRecord(object.Event{Name: "s3:ObjectRemoved:DeleteMarkerCreated", Bucket: &meta.Bucket{Name: "b"}, Key: "k", VersionID: "v1"}, RecordOptions{Region: "us-west-2"})
	if rec.AWSRegion != "us-west-2" || rec.S3.Object.Key != "k" || rec.S3.Object.VersionID != "v1" || rec.S3.Object.UserMetadata == nil || rec.ResponseElements["x-amz-request-id"] == "" {
		t.Fatalf("fallback record: %+v", rec)
	}
	if EncodeKey("a b/c&d=é") != "a+b/c%26d%3D%C3%A9" {
		t.Fatalf("encode: %q", EncodeKey("a b/c&d=é"))
	}
}
