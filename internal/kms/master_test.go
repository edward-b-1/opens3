package kms

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/Birdsall/opens3/internal/kv"
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
	// Rotation: a ring with a new current key still unwraps old data.
	rotated := &Master{keys: append([][]byte{other.keys[0]}, m.keys...)}
	if out, err := rotated.Unwrap(w, []byte("aad")); err != nil || string(out) != "secret" {
		t.Fatalf("ring unwrap: %v", err)
	}
	w2, _ := rotated.Wrap([]byte("new"), nil)
	if _, err := m.Unwrap(w2, nil); err == nil {
		t.Fatal("old ring must not unwrap data wrapped by the new key")
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
