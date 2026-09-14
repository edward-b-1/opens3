package masterkey

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/sse"
)

// fixture builds a data directory the way the server does, with one
// record of every protected kind plus records the ring does not wrap.
type fixture struct {
	db     kv.Store
	path   string
	ring   *kms.Master
	objDEK []byte // data key of the first SSE-S3 object
	objKey []byte // its record key
}

func newFixture(t *testing.T, sseObjects int) *fixture {
	t.Helper()
	dir := t.TempDir()
	db, err := kv.OpenBolt(filepath.Join(dir, "meta", "opens3.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fixture{db: db, path: filepath.Join(dir, "meta", "master.keys")}
	f.ring, _, err = kms.LoadOrCreateMasterFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := kms.NewLocal(db, f.ring)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.CreateKey("payroll"); err != nil {
		t.Fatal(err)
	}
	ia, err := iam.Open(db, iam.Config{RootAccessKey: "root", RootSecretKey: "rootsecret", Wrapper: f.ring})
	if err != nil {
		t.Fatal(err)
	}
	if err := ia.CreateUser("alice", "alicesecret0123456789", nil); err != nil {
		t.Fatal(err)
	}
	put := func(key []byte, v any) {
		b, _ := json.Marshal(v)
		if err := db.Update(func(tx kv.Txn) error { return tx.Put(key, b) }); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < sseObjects; i++ {
		dek := sse.NewDEK()
		w, err := l.Wrap("", dek, map[string]string{"bucket": "b"})
		if err != nil {
			t.Fatal(err)
		}
		k := meta.ObjectKey("b", "sse-"+string(rune('a'+i)), uint64(100+i))
		put(k, &meta.Object{Bucket: "b", Key: "sse", SSE: &meta.SSE{Type: "AES256", WrappedKey: w}})
		if i == 0 {
			f.objDEK, f.objKey = dek, k
		}
	}
	// Not wrapped by the ring: plaintext, SSE-KMS (named key) and SSE-C.
	put(meta.ObjectKey("b", "plain", 200), &meta.Object{Bucket: "b", Key: "plain"})
	wk, _ := l.Wrap("payroll", sse.NewDEK(), nil)
	put(meta.ObjectKey("b", "kms", 201), &meta.Object{Bucket: "b", Key: "kms", SSE: &meta.SSE{Type: "aws:kms", KMSKeyID: "payroll", WrappedKey: wk}})
	wc, _ := sse.Wrap(sse.NewDEK(), sse.NewDEK(), []byte("ssec"))
	put(meta.ObjectKey("b", "ssec", 202), &meta.Object{Bucket: "b", Key: "ssec", SSE: &meta.SSE{Type: "SSE-C", WrappedKey: wc}})
	// One SSE-S3 multipart upload in progress and one plaintext upload.
	wu, _ := l.Wrap("", sse.NewDEK(), map[string]string{"bucket": "b"})
	put(meta.UploadKey("b", "big", "upload1"), &meta.Upload{Bucket: "b", Key: "big", UploadID: "upload1", SSE: &meta.SSE{Type: "AES256", WrappedKey: wu}})
	put(meta.UploadKey("b", "plainbig", "upload2"), &meta.Upload{Bucket: "b", Key: "plainbig", UploadID: "upload2"})
	return f
}

func totals(rep Report) map[string]Kind {
	m := map[string]Kind{}
	for _, k := range rep.Kinds {
		m[k.Name] = k
	}
	return m
}

func TestRewrapAfterRotation(t *testing.T) {
	f := newFixture(t, 5)
	rep, err := Run(f.db, f.ring, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"key-check value": 1, "encryption keys": 2, "access-key secrets": 1, "objects (SSE-S3)": 5, "multipart uploads (SSE-S3)": 1}
	for name, n := range want {
		k := totals(rep)[name]
		if k.Total != n || k.Current() != n || k.Stale() != 0 || k.Unreadable != 0 {
			t.Fatalf("%s before rotation: %+v", name, k)
		}
	}
	if _, err := f.ring.AddKey(); err != nil {
		t.Fatal(err)
	}
	// Dry run after rotation: everything is stale, nothing written.
	rep, err = Run(f.db, f.ring, Options{DryRun: true, Batch: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stale() != 10 || rep.Rewrapped() != 0 || rep.ByKey(0) != 0 || rep.ByKey(1) != 10 {
		t.Fatalf("dry run after rotation: stale %d rewrapped %d by key %d/%d", rep.Stale(), rep.Rewrapped(), rep.ByKey(0), rep.ByKey(1))
	}
	// Real run in small batches (exercises the batch boundary), then a
	// second run finds nothing to do.
	var progress int
	rep, err = Run(f.db, f.ring, Options{Batch: 2, Progress: func(string, int) { progress++ }})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rewrapped() != 10 || rep.Stale() != 0 || rep.ByKey(0) != 10 || progress == 0 {
		t.Fatalf("rewrap: rewrapped %d stale %d current %d progress %d", rep.Rewrapped(), rep.Stale(), rep.ByKey(0), progress)
	}
	rep, err = Run(f.db, f.ring, Options{})
	if err != nil || rep.Rewrapped() != 0 || rep.Stale() != 0 {
		t.Fatalf("second run: %v %+v", err, rep)
	}
	// With the old key pruned, the server-side stack still opens and every
	// record is readable.
	if _, err := f.ring.Prune(); err != nil {
		t.Fatal(err)
	}
	pruned, err := kms.LoadMasterFile(f.path)
	if err != nil || pruned.Keys() != 1 {
		t.Fatalf("reload: %v", err)
	}
	if err := Check(f.db, pruned); err != nil {
		t.Fatal(err)
	}
	l, err := kms.NewLocal(f.db, pruned)
	if err != nil {
		t.Fatalf("kms with pruned ring: %v", err)
	}
	if !l.KeyExists("payroll") {
		t.Fatal("named key unreadable after rewrap")
	}
	ia, err := iam.Open(f.db, iam.Config{RootAccessKey: "root", RootSecretKey: "rootsecret", Wrapper: pruned})
	if err != nil {
		t.Fatal(err)
	}
	if sk, err := ia.LookupSecret("alice"); err != nil || sk != "alicesecret0123456789" {
		t.Fatalf("access-key secret after rewrap: %q %v", sk, err)
	}
	var o meta.Object
	f.db.View(func(tx kv.Txn) error { b, _ := tx.Get(f.objKey); return json.Unmarshal(b, &o) })
	if dek, err := l.Unwrap("", o.SSE.WrappedKey, map[string]string{"bucket": "b"}); err != nil || string(dek) != string(f.objDEK) {
		t.Fatalf("object data key after rewrap: %v", err)
	}
	// A record the ring cannot read is counted and left alone.
	foreign, _ := kms.TestMaster().Wrap(sse.NewDEK(), kms.ContextAAD(map[string]string{"bucket": "b"}))
	fb, _ := json.Marshal(&meta.Object{Bucket: "b", Key: "foreign", SSE: &meta.SSE{Type: "AES256", WrappedKey: foreign}})
	f.db.Update(func(tx kv.Txn) error { return tx.Put(meta.ObjectKey("b", "foreign", 300), fb) })
	rep, err = Run(f.db, pruned, Options{})
	if err != nil || rep.Unreadable() != 1 || totals(rep)["objects (SSE-S3)"].Total != 6 {
		t.Fatalf("unreadable: %v %+v", err, rep)
	}
	if err := Check(f.db, kms.TestMaster()); err == nil {
		t.Fatal("check passed with a foreign ring")
	}
}

func TestRewrapMovesBetweenSources(t *testing.T) {
	// Data wrapped under an environment key moves to a file ring given as
	// primary with the environment key as fallback.
	f := newFixture(t, 3)
	env, _ := kms.MasterFromMaterial([]byte("0123456789abcdef0123456789abcdef-env-material"))
	if _, err := Run(f.db, env.WithFallback(f.ring), Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Check(f.db, env); err != nil {
		t.Fatalf("after moving to the environment key: %v", err)
	}
	rep, err := Run(f.db, env, Options{DryRun: true})
	if err != nil || rep.Stale() != 0 || rep.Unreadable() != 0 || rep.ByKey(0) != 8 {
		t.Fatalf("environment ring alone: %v %+v", err, rep)
	}
	if _, err := Run(f.db, f.ring, Options{DryRun: true}); err != nil {
		t.Fatal(err)
	}
}
