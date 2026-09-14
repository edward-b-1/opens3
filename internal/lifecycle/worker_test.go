package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edward-b-1/opens3/internal/blob"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
)

var owner = object.Actor{CanonicalID: "owner", DisplayName: "owner"}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newService(t *testing.T) *object.Service {
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
	return object.New(db, bs, k, "us-east-1", nil)
}

type fixture struct {
	t      *testing.T
	s      *object.Service
	ctx    context.Context
	events []string
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, s: newService(t), ctx: context.Background()}
	f.s.Notify = func(e object.Event) {
		if strings.HasPrefix(e.Name, "s3:Lifecycle") {
			if e.Actor != Actor {
				t.Errorf("event %s has actor %+v", e.Name, e.Actor)
			}
			f.events = append(f.events, e.Name+" "+e.Key)
		}
	}
	return f
}

func (f *fixture) bucket(name, lifecycleXML string, versioning string, lock bool) {
	f.t.Helper()
	if _, err := f.s.CreateBucket(f.ctx, owner, object.CreateBucketInput{Name: name, ObjectLockEnabled: lock}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := Parse([]byte(lifecycleXML)); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.s.UpdateBucket(f.ctx, name, func(b *meta.Bucket) error {
		b.LifecycleXML = []byte(lifecycleXML)
		if versioning != "" {
			b.Versioning = versioning
		}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

// put writes an object and backdates its ModTime by age.
func (f *fixture) put(bucket, key string, age time.Duration, attrs object.ObjectAttrs) *meta.Object {
	f.t.Helper()
	body := []byte("content of " + key)
	o, err := f.s.PutObject(f.ctx, owner, object.PutInput{Bucket: bucket, Key: key, Body: bytes.NewReader(body), Size: int64(len(body)), Attrs: attrs})
	if err != nil {
		f.t.Fatalf("put %s: %v", key, err)
	}
	if age > 0 {
		o = f.backdate(bucket, key, o.VersionID, age)
	}
	return o
}

func (f *fixture) backdate(bucket, key, vid string, age time.Duration) *meta.Object {
	f.t.Helper()
	o, err := f.s.UpdateObjectMeta(f.ctx, bucket, key, vid, func(o *meta.Object) error {
		o.ModTime = time.Now().UTC().Add(-age)
		return nil
	})
	if err != nil {
		f.t.Fatalf("backdate %s: %v", key, err)
	}
	return o
}

// versions lists "key@versionID[:dm][:class]" for the bucket.
func (f *fixture) versions(bucket string) []string {
	f.t.Helper()
	res, err := f.s.ListVersions(f.ctx, bucket, meta.ListOptions{})
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range res.Entries {
		s := e.Object.Key
		if e.Object.DeleteMarker {
			s += ":dm"
		}
		if e.Object.StorageClass != "" && e.Object.StorageClass != "STANDARD" {
			s += ":" + e.Object.StorageClass
		}
		out = append(out, s)
	}
	return out
}

func (f *fixture) run() Stats {
	f.t.Helper()
	w := New(f.s, quiet, Options{BatchSize: 2, MaxScan: 3})
	st, err := w.RunOnce(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	g, w := append([]string(nil), got...), append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		t.Fatalf("%s: got %v want %v", what, got, want)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("%s: got %v want %v", what, got, want)
		}
	}
}

const day = 24 * time.Hour

func TestWorkerUnversioned(t *testing.T) {
	f := newFixture(t)
	f.bucket("unv", `<LifecycleConfiguration>
	<Rule><ID>tmp</ID><Status>Enabled</Status><Filter><Prefix>tmp/</Prefix></Filter><Expiration><Days>1</Days></Expiration></Rule>
	<Rule><ID>cold</ID><Status>Enabled</Status><Filter><Prefix>cold/</Prefix></Filter><Transition><Days>1</Days><StorageClass>STANDARD_IA</StorageClass></Transition><Transition><Days>5</Days><StorageClass>GLACIER</StorageClass></Transition></Rule>
	<Rule><ID>tagged</ID><Status>Enabled</Status><Filter><Tag><Key>tier</Key><Value>scratch</Value></Tag></Filter><Expiration><Days>1</Days></Expiration></Rule>
	<Rule><ID>off</ID><Status>Disabled</Status><Filter><Prefix>keep/</Prefix></Filter><Expiration><Days>1</Days></Expiration></Rule>
	<Rule><ID>mpu</ID><Status>Enabled</Status><Filter></Filter><AbortIncompleteMultipartUpload><DaysAfterInitiation>2</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>
	</LifecycleConfiguration>`, "", false)
	// A bucket without lifecycle configuration is left alone.
	f.s.CreateBucket(f.ctx, owner, object.CreateBucketInput{Name: "plain"})
	f.put("plain", "tmp/old", 10*day, object.ObjectAttrs{})

	f.put("unv", "tmp/old1", 3*day, object.ObjectAttrs{})
	f.put("unv", "tmp/old2", 3*day, object.ObjectAttrs{})
	f.put("unv", "tmp/old3", 3*day, object.ObjectAttrs{})
	f.put("unv", "tmp/new", 0, object.ObjectAttrs{})
	f.put("unv", "cold/mid", 3*day, object.ObjectAttrs{})
	f.put("unv", "cold/old", 10*day, object.ObjectAttrs{})
	f.put("unv", "cold/archived", 10*day, object.ObjectAttrs{StorageClass: "DEEP_ARCHIVE"})
	f.put("unv", "cold/new", 0, object.ObjectAttrs{})
	f.put("unv", "keep/old", 10*day, object.ObjectAttrs{})
	f.put("unv", "other/old", 10*day, object.ObjectAttrs{})
	f.put("unv", "scratch/old", 10*day, object.ObjectAttrs{Tags: []meta.Tag{{Key: "tier", Value: "scratch"}}})
	f.put("unv", "scratch/other", 10*day, object.ObjectAttrs{Tags: []meta.Tag{{Key: "tier", Value: "keep"}}})

	// Two uploads: one stale, one fresh.
	stale, err := f.s.CreateUpload(f.ctx, owner, object.CreateUploadInput{Bucket: "unv", Key: "mpu/stale"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.UploadPart(f.ctx, object.UploadPartInput{Bucket: "unv", Key: "mpu/stale", UploadID: stale.UploadID, PartNumber: 1, Body: bytes.NewReader([]byte("part")), Size: 4}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.KV().Update(func(tx kv.Txn) error {
		u, err := meta.GetUpload(tx, "unv", "mpu/stale", stale.UploadID)
		if err != nil {
			return err
		}
		u.Initiated = time.Now().UTC().Add(-5 * day)
		return meta.PutUpload(tx, u)
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.s.CreateUpload(f.ctx, owner, object.CreateUploadInput{Bucket: "unv", Key: "mpu/fresh"})
	if err != nil {
		t.Fatal(err)
	}

	st := f.run()
	if st.Buckets != 1 || st.Expired != 4 || st.Transitioned != 2 || st.Aborted != 1 || st.Errors != 0 || st.Skipped != 0 || st.DeleteMarkersCreated != 0 {
		t.Fatalf("stats: %+v", st)
	}
	eq(t, "versions", f.versions("unv"), []string{"tmp/new", "cold/mid:STANDARD_IA", "cold/old:GLACIER", "cold/archived:DEEP_ARCHIVE", "cold/new",
		"keep/old", "other/old", "scratch/other"})
	eq(t, "plain", f.versions("plain"), []string{"tmp/old"})
	ups, err := f.s.ListUploads(f.ctx, "unv", meta.ListOptions{})
	if err != nil || len(ups.Uploads) != 1 || ups.Uploads[0].UploadID != fresh.UploadID {
		t.Fatalf("uploads: %+v %v", ups, err)
	}
	if _, err := f.s.GetObject(f.ctx, object.GetInput{Bucket: "unv", Key: "tmp/old1"}); err == nil {
		t.Fatal("tmp/old1 still readable")
	}
	eq(t, "events", f.events, []string{
		EventExpirationDelete + " tmp/old1", EventExpirationDelete + " tmp/old2", EventExpirationDelete + " tmp/old3", EventExpirationDelete + " scratch/old",
		EventTransition + " cold/mid", EventTransition + " cold/old"})

	// A second pass is a no-op.
	f.events = nil
	st = f.run()
	if st.Expired+st.Transitioned+st.Aborted+st.Errors+st.Skipped != 0 || len(f.events) != 0 {
		t.Fatalf("second pass: %+v %v", st, f.events)
	}
}

func TestWorkerVersioned(t *testing.T) {
	f := newFixture(t)
	f.bucket("ver", `<LifecycleConfiguration>
	<Rule><ID>cur</ID><Status>Enabled</Status><Filter><Prefix>a</Prefix></Filter><Expiration><Days>2</Days></Expiration>
	  <NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays><NewerNoncurrentVersions>1</NewerNoncurrentVersions></NoncurrentVersionExpiration></Rule>
	<Rule><ID>nct</ID><Status>Enabled</Status><Filter><Prefix>t</Prefix></Filter><NoncurrentVersionTransition><NoncurrentDays>1</NoncurrentDays><StorageClass>GLACIER</StorageClass></NoncurrentVersionTransition></Rule>
	<Rule><ID>dm</ID><Status>Enabled</Status><Filter><Prefix>dm/</Prefix></Filter><Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration></Rule>
	</LifecycleConfiguration>`, "Enabled", false)

	// Key "a": four versions, all old. The current one expires (delete
	// marker); of the noncurrent ones the newest is retained by
	// NewerNoncurrentVersions=1 and the two older ones are deleted.
	v1 := f.put("ver", "a", 10*day, object.ObjectAttrs{})
	v2 := f.put("ver", "a", 9*day, object.ObjectAttrs{})
	v3 := f.put("ver", "a", 8*day, object.ObjectAttrs{})
	v4 := f.put("ver", "a", 7*day, object.ObjectAttrs{})
	// Key "afresh": old noncurrent version but the current one is new: the
	// noncurrent version became noncurrent only now, so nothing happens.
	f.put("ver", "afresh", 10*day, object.ObjectAttrs{})
	f.put("ver", "afresh", 0, object.ObjectAttrs{})
	// Key "t": noncurrent transition, current untouched.
	t1 := f.put("ver", "t", 10*day, object.ObjectAttrs{})
	f.put("ver", "t", 5*day, object.ObjectAttrs{})
	// Key "dm/x": only an expired delete marker remains.
	o := f.put("ver", "dm/x", 0, object.ObjectAttrs{})
	if _, err := f.s.DeleteObject(f.ctx, owner, object.DeleteInput{Bucket: "ver", Key: "dm/x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DeleteObject(f.ctx, owner, object.DeleteInput{Bucket: "ver", Key: "dm/x", VersionID: o.VersionID}); err != nil {
		t.Fatal(err)
	}
	// Key "dm/y": delete marker with a live older version: kept.
	f.put("ver", "dm/y", 0, object.ObjectAttrs{})
	if _, err := f.s.DeleteObject(f.ctx, owner, object.DeleteInput{Bucket: "ver", Key: "dm/y"}); err != nil {
		t.Fatal(err)
	}

	st := f.run()
	if st.DeleteMarkersCreated != 1 || st.Expired != 2 || st.DeleteMarkersRemoved != 1 || st.Transitioned != 1 || st.Errors != 0 || st.Skipped != 0 {
		t.Fatalf("stats: %+v", st)
	}
	eq(t, "versions", f.versions("ver"), []string{"a:dm", "a", "a", "afresh", "afresh", "t", "t:GLACIER", "dm/y:dm", "dm/y"})
	for _, vid := range []string{v1.VersionID, v2.VersionID} {
		if _, err := f.s.StatObject(f.ctx, "ver", "a", vid); err == nil {
			t.Fatalf("version %s survived", vid)
		}
	}
	for _, vid := range []string{v3.VersionID, v4.VersionID} {
		if _, err := f.s.StatObject(f.ctx, "ver", "a", vid); err != nil {
			t.Fatalf("version %s gone: %v", vid, err)
		}
	}
	if st, err := f.s.StatObject(f.ctx, "ver", "t", t1.VersionID); err != nil || st.StorageClass != "GLACIER" {
		t.Fatalf("t1: %+v %v", st, err)
	}
	eq(t, "events", f.events, []string{EventExpirationDeleteMarkerCreate + " a", EventExpirationDelete + " a", EventExpirationDelete + " a",
		EventExpirationDelete + " dm/x", EventTransition + " t"})

	// Second pass: the delete marker made v4 noncurrent, so v3 now has a
	// newer noncurrent version and, having been noncurrent since v4 was
	// written 7 days ago, expires. v4 itself is retained by
	// NewerNoncurrentVersions=1.
	st = f.run()
	if st.Expired != 1 || st.DeleteMarkersCreated+st.DeleteMarkersRemoved+st.Transitioned+st.Errors != 0 {
		t.Fatalf("second pass: %+v", st)
	}
	if _, err := f.s.StatObject(f.ctx, "ver", "a", v3.VersionID); err == nil {
		t.Fatal("v3 survived second pass")
	}
	eq(t, "versions", f.versions("ver"), []string{"a:dm", "a", "afresh", "afresh", "t", "t:GLACIER", "dm/y:dm", "dm/y"})
	// Even with an old delete marker v4 stays (retained count) and the
	// marker is not an expired object delete marker while v4 exists.
	f.s.KV().Update(func(tx kv.Txn) error {
		m, err := meta.GetLatest(tx, "ver", "a")
		if err != nil {
			return err
		}
		m.ModTime = m.ModTime.Add(-3 * day)
		_, err = meta.PutObject(tx, m)
		return err
	})
	st = f.run()
	if st.Expired+st.DeleteMarkersCreated+st.DeleteMarkersRemoved+st.Transitioned+st.Errors != 0 {
		t.Fatalf("third pass: %+v", st)
	}
	eq(t, "versions", f.versions("ver"), []string{"a:dm", "a", "afresh", "afresh", "t", "t:GLACIER", "dm/y:dm", "dm/y"})
}

func TestWorkerSuspended(t *testing.T) {
	f := newFixture(t)
	f.bucket("sus", `<LifecycleConfiguration><Rule><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration></Rule></LifecycleConfiguration>`, "Suspended", false)
	f.put("sus", "k", 5*day, object.ObjectAttrs{})
	st := f.run()
	if st.DeleteMarkersCreated != 1 || st.Expired != 0 {
		t.Fatalf("stats: %+v", st)
	}
	eq(t, "versions", f.versions("sus"), []string{"k:dm"})
}

func TestWorkerObjectLock(t *testing.T) {
	f := newFixture(t)
	f.bucket("lck", `<LifecycleConfiguration><Rule><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration>
	<NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays></NoncurrentVersionExpiration></Rule></LifecycleConfiguration>`, "", true)
	ret := &meta.Retention{Mode: "COMPLIANCE", RetainUntil: time.Now().Add(365 * day)}
	locked := f.put("lck", "k", 10*day, object.ObjectAttrs{Retention: ret})
	f.put("lck", "k", 5*day, object.ObjectAttrs{})
	st := f.run()
	// The current version gets a delete marker (allowed under lock); the
	// locked noncurrent version is refused and skipped.
	if st.DeleteMarkersCreated != 1 || st.Expired != 0 || st.Skipped != 1 || st.Errors != 0 {
		t.Fatalf("stats: %+v", st)
	}
	if _, err := f.s.StatObject(f.ctx, "lck", "k", locked.VersionID); err != nil {
		t.Fatalf("locked version deleted: %v", err)
	}
}

func TestWorkerInvalidConfigSkipped(t *testing.T) {
	f := newFixture(t)
	f.bucket("bad", `<LifecycleConfiguration><Rule><Status>Enabled</Status></Rule></LifecycleConfiguration>`, "", false)
	f.put("bad", "k", 10*day, object.ObjectAttrs{})
	st := f.run()
	if st.Buckets != 0 || st.Expired != 0 {
		t.Fatalf("stats: %+v", st)
	}
}

func TestWorkerStartStop(t *testing.T) {
	f := newFixture(t)
	f.bucket("run", `<LifecycleConfiguration><Rule><Status>Enabled</Status><Filter></Filter><Expiration><Days>1</Days></Expiration></Rule></LifecycleConfiguration>`, "", false)
	f.put("run", "old", 5*day, object.ObjectAttrs{})
	w := New(f.s, quiet, Options{Interval: time.Hour})
	w.Start()
	w.Start() // idempotent
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := f.s.StatObject(f.ctx, "run", "old", "")
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.Stop()
	w.Stop() // idempotent
	// Cancelled contexts stop RunOnce promptly.
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, err := w.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
