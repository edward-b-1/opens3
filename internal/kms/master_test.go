package kms

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/edward-b-1/opens3/internal/kv"
)

func TestMasterFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta", "master.keys")
	m, created, err := LoadOrCreateMasterFile(path)
	if err != nil || !created || m.Keys() != 1 {
		t.Fatalf("create: %v %v", err, created)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	w, err := m.Wrap([]byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	m2, created, err := LoadOrCreateMasterFile(path)
	if err != nil || created || m2.Fingerprint() != m.Fingerprint() {
		t.Fatalf("reload: %v %v", err, created)
	}
	if out, err := m2.Unwrap(w, []byte("aad")); err != nil || !bytes.Equal(out, []byte("secret")) {
		t.Fatalf("unwrap after reload: %v", err)
	}
	// A different ring cannot unwrap.
	other, _, _ := LoadOrCreateMasterFile(filepath.Join(t.TempDir(), "master.keys"))
	if _, err := other.Unwrap(w, []byte("aad")); err == nil {
		t.Fatal("foreign key unwrapped")
	}
	// Rotation: adding a key keeps old data readable and marks it as
	// wrapped by an older key; pruning drops the old keys from the file.
	info, err := m2.AddKey()
	if err != nil || info.ID != "k2" || m2.Keys() != 2 {
		t.Fatalf("add key: %v %+v", err, info)
	}
	if idx, out, err := m2.UnwrapIndex(w, []byte("aad")); err != nil || idx != 1 || string(out) != "secret" {
		t.Fatalf("unwrap index after rotation: %d %v", idx, err)
	}
	w2, _ := m2.Wrap([]byte("new"), nil)
	if idx, _, err := m2.UnwrapIndex(w2, nil); err != nil || idx != 0 {
		t.Fatalf("current key index: %d %v", idx, err)
	}
	if _, err := m.Unwrap(w2, nil); err == nil {
		t.Fatal("old ring must not unwrap data wrapped by the new key")
	}
	reloaded, err := LoadMasterFile(path)
	if err != nil || reloaded.Keys() != 2 || reloaded.Fingerprint() != m2.Fingerprint() {
		t.Fatalf("reload after rotation: %v", err)
	}
	if ids := reloaded.Info(); ids[0].ID != "k2" || ids[1].ID != "k1" || ids[0].Fingerprint == ids[1].Fingerprint {
		t.Fatalf("info: %+v", ids)
	}
	removed, err := m2.Prune()
	if err != nil || len(removed) != 1 || removed[0].ID != "k1" || m2.Keys() != 1 {
		t.Fatalf("prune: %v %+v", err, removed)
	}
	if _, err := m2.Unwrap(w, []byte("aad")); err == nil {
		t.Fatal("pruned key still unwraps")
	}
	if reloaded, err := LoadMasterFile(path); err != nil || reloaded.Keys() != 1 || reloaded.Info()[0].ID != "k2" {
		t.Fatalf("reload after prune: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
	// Fallback rings unwrap but never wrap, and ring edits are not allowed
	// on rings without a file.
	fb := m2.WithFallback(m, nil)
	if idx, out, err := fb.UnwrapIndex(w, []byte("aad")); err != nil || idx != 1 || string(out) != "secret" {
		t.Fatalf("fallback unwrap: %d %v", idx, err)
	}
	if _, err := TestMaster().AddKey(); err != ErrNotFile {
		t.Fatalf("AddKey on test ring: %v", err)
	}
	if _, err := LoadMasterFile(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestMasterFromEnv(t *testing.T) {
	t.Setenv("OPENS3_TEST_MASTER", "")
	if m, err := MasterFromEnv("OPENS3_TEST_MASTER"); m != nil || err != nil {
		t.Fatalf("unset: %v %v", m, err)
	}
	t.Setenv("OPENS3_TEST_MASTER", "short")
	if _, err := MasterFromEnv("OPENS3_TEST_MASTER"); err == nil {
		t.Fatal("short material accepted")
	}
	t.Setenv("OPENS3_TEST_MASTER", "0123456789abcdef0123456789abcdef-random-enough")
	m, err := MasterFromEnv("OPENS3_TEST_MASTER")
	if err != nil || m.Info()[0].ID != "OPENS3_TEST_MASTER" || m.Info()[0].Source != "env" {
		t.Fatalf("env ring: %v %+v", err, m.Info())
	}
	same, _ := MasterFromMaterial([]byte("0123456789abcdef0123456789abcdef-random-enough"))
	if same.Fingerprint() != m.Fingerprint() {
		t.Fatal("same material must give the same key")
	}
}

func TestMasterFromMaterialAndCheck(t *testing.T) {
	if _, err := MasterFromMaterial([]byte("password")); err == nil {
		t.Fatal("short material accepted")
	}
	m, err := MasterFromMaterial([]byte("0123456789abcdef0123456789abcdef-random-enough"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := kv.OpenBolt(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewLocal(db, m); err != nil {
		t.Fatal(err)
	}
	// Same material reopens; different material is refused.
	if _, err := NewLocal(db, m); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := NewLocal(db, TestMaster()); err != ErrMasterMismatch {
		t.Fatalf("mismatch not detected: %v", err)
	}
	l, _ := NewLocal(db, m)
	keys, _ := l.ListKeys()
	for _, k := range keys {
		if k.ID == "" || k.ID == ".master-check" {
			t.Fatalf("check entry leaked into key list: %+v", keys)
		}
	}
}
