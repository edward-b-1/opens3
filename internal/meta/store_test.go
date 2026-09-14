package meta

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/edward-b-1/OpenS3/internal/kv"
)

func openTestDB(t *testing.T) kv.Store {
	t.Helper()
	db, err := kv.OpenBolt(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func put(t *testing.T, db kv.Store, seq *Sequencer, bucket, key, vid string, dm bool) *Object {
	t.Helper()
	o := &Object{Bucket: bucket, Key: key, Seq: seq.Next(), DeleteMarker: dm, ModTime: time.Now()}
	if vid == "" {
		o.VersionID = NewVersionID(o.Seq)
	} else {
		o.VersionID = vid
	}
	if err := db.Update(func(tx kv.Txn) error { _, err := PutObject(tx, o); return err }); err != nil {
		t.Fatal(err)
	}
	return o
}

func keys(res *ListResult) []string {
	var out []string
	for _, e := range res.Entries {
		if e.Object != nil {
			out = append(out, e.Object.Key)
		} else {
			out = append(out, e.CommonPrefix)
		}
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListObjectsDelimiterAndMarkers(t *testing.T) {
	db := openTestDB(t)
	var seq Sequencer
	for _, k := range []string{"a", "ab", "b/1", "b/2", "b/c/3", "c", "d/1"} {
		put(t, db, &seq, "bk", k, "", false)
	}
	// Overwrite "c" with a newer version and mark "d/1" deleted.
	put(t, db, &seq, "bk", "c", "", false)
	put(t, db, &seq, "bk", "d/1", "", true)

	db.View(func(tx kv.Txn) error {
		r, err := ListObjects(tx, "bk", ListOptions{Delimiter: "/"})
		if err != nil {
			t.Fatal(err)
		}
		if got := keys(r); !eq(got, []string{"a", "ab", "b/", "c"}) {
			t.Fatalf("delimiter listing: %v", got)
		}
		// "d/" is not listed: its only key is a delete marker. In the
		// versions listing it is.
		rvd, _ := ListVersions(tx, "bk", ListOptions{Delimiter: "/"})
		if got := keys(rvd); !eq(got, []string{"a", "ab", "b/", "c", "c", "d/"}) {
			t.Fatalf("delimiter version listing: %v", got)
		}
		r, _ = ListObjects(tx, "bk", ListOptions{})
		if got := keys(r); !eq(got, []string{"a", "ab", "b/1", "b/2", "b/c/3", "c"}) {
			t.Fatalf("flat listing: %v", got)
		}
		r, _ = ListObjects(tx, "bk", ListOptions{Prefix: "b/", Delimiter: "/"})
		if got := keys(r); !eq(got, []string{"b/1", "b/2", "b/c/"}) {
			t.Fatalf("prefix listing: %v", got)
		}
		r, _ = ListObjects(tx, "bk", ListOptions{StartAfter: "a"})
		if got := keys(r); !eq(got, []string{"ab", "b/1", "b/2", "b/c/3", "c"}) {
			t.Fatalf("start-after a: %v", got)
		}
		r, _ = ListObjects(tx, "bk", ListOptions{Delimiter: "/", StartAfter: "b/"})
		if got := keys(r); !eq(got, []string{"c"}) {
			t.Fatalf("start-after common prefix: %v", got)
		}
		r, _ = ListObjects(tx, "bk", ListOptions{MaxKeys: 2})
		if got := keys(r); !eq(got, []string{"a", "ab"}) || !r.IsTruncated || r.NextKey != "ab" {
			t.Fatalf("pagination: %v trunc=%v next=%q", got, r.IsTruncated, r.NextKey)
		}
		r, _ = ListObjects(tx, "bk", ListOptions{MaxKeys: 2, StartAfter: r.NextKey})
		if got := keys(r); !eq(got, []string{"b/1", "b/2"}) {
			t.Fatalf("page 2: %v", got)
		}
		// Versions: "c" has two, "d/1" has two (object + marker).
		rv, _ := ListVersions(tx, "bk", ListOptions{Prefix: "c"})
		if len(rv.Entries) != 2 || !rv.Entries[0].IsLatest || rv.Entries[1].IsLatest {
			t.Fatalf("versions: %+v", rv.Entries)
		}
		rv, _ = ListVersions(tx, "bk", ListOptions{MaxKeys: 1, Prefix: "c"})
		rv2, _ := ListVersions(tx, "bk", ListOptions{Prefix: "c", KeyMarker: rv.NextKey, VersionMarker: rv.NextVersion})
		if len(rv2.Entries) != 1 || rv2.Entries[0].Object.VersionID == rv.NextVersion {
			t.Fatalf("version marker: %+v", rv2.Entries)
		}
		return nil
	})
}

func TestNullVersion(t *testing.T) {
	db := openTestDB(t)
	var seq Sequencer
	o1 := put(t, db, &seq, "bk", "k", NullVersionID, false)
	o2 := put(t, db, &seq, "bk", "k", NullVersionID, false)
	db.View(func(tx kv.Txn) error {
		got, err := GetVersion(tx, "bk", "k", NullVersionID)
		if err != nil || got.Seq != o2.Seq {
			t.Fatalf("null lookup: %v %+v", err, got)
		}
		if _, err := tx.Get(ObjectKey("bk", "k", o1.Seq)); err == nil {
			t.Fatal("old null version should have been replaced")
		}
		rv, _ := ListVersions(tx, "bk", ListOptions{})
		if len(rv.Entries) != 1 {
			t.Fatalf("expected one version, got %d", len(rv.Entries))
		}
		return nil
	})
	// Versioned write on top of null keeps both.
	o3 := put(t, db, &seq, "bk", "k", "", false)
	db.View(func(tx kv.Txn) error {
		l, _ := GetLatest(tx, "bk", "k")
		if l.Seq != o3.Seq {
			t.Fatal("latest should be versioned write")
		}
		n, _ := GetVersion(tx, "bk", "k", NullVersionID)
		if n.Seq != o2.Seq {
			t.Fatal("null still resolvable")
		}
		v, err := GetVersion(tx, "bk", "k", o3.VersionID)
		if err != nil || v.Seq != o3.Seq {
			t.Fatalf("by id: %v", err)
		}
		return nil
	})
}

func TestUploadsAndParts(t *testing.T) {
	db := openTestDB(t)
	u := &Upload{Bucket: "bk", Key: "big", UploadID: NewID(), Initiated: time.Now()}
	db.Update(func(tx kv.Txn) error {
		if err := PutUpload(tx, u); err != nil {
			return err
		}
		for _, n := range []int{3, 1, 2} {
			if _, err := PutPart(tx, "bk", u.UploadID, Part{Number: n, Blob: NewID(), Size: 10}); err != nil {
				return err
			}
		}
		old, err := PutPart(tx, "bk", u.UploadID, Part{Number: 2, Blob: "new", Size: 11})
		if err != nil || old == nil || old.Size != 10 {
			t.Fatalf("replace part: %v %+v", err, old)
		}
		return nil
	})
	db.View(func(tx kv.Txn) error {
		got, err := GetUpload(tx, "bk", "big", u.UploadID)
		if err != nil || got.Key != "big" {
			t.Fatalf("get upload: %v", err)
		}
		if _, err := GetUpload(tx, "bk", "other", u.UploadID); err == nil {
			t.Fatal("key mismatch should fail")
		}
		parts, _ := ListParts(tx, "bk", u.UploadID, 0, 0)
		if len(parts) != 3 || parts[0].Number != 1 || parts[2].Number != 3 || parts[1].Blob != "new" {
			t.Fatalf("parts: %+v", parts)
		}
		parts, _ = ListParts(tx, "bk", u.UploadID, 1, 1)
		if len(parts) != 1 || parts[0].Number != 2 {
			t.Fatalf("parts after 1 max 1: %+v", parts)
		}
		lu, _ := ListUploads(tx, "bk", ListOptions{})
		if len(lu.Uploads) != 1 {
			t.Fatal("list uploads")
		}
		return nil
	})
	db.Update(func(tx kv.Txn) error {
		parts, err := DeleteUpload(tx, u)
		if err != nil || len(parts) != 3 {
			t.Fatalf("delete upload: %v %d", err, len(parts))
		}
		if _, err := GetUpload(tx, "bk", "", u.UploadID); err == nil {
			t.Fatal("upload should be gone")
		}
		return nil
	})
}
