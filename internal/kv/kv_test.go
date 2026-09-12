package kv

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestPrefixSuccessor(t *testing.T) {
	cases := map[string]string{"a/": "a0", "abc": "abd", "\xff": "", "a\xff": "b", "": ""}
	for in, want := range cases {
		got := PrefixSuccessor([]byte(in))
		if string(got) != want {
			t.Errorf("PrefixSuccessor(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBoltRoundTrip(t *testing.T) {
	db, err := OpenBolt(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = db.Update(func(tx Txn) error {
		for _, k := range []string{"o/b/a", "o/b/b", "o/b/c", "o/c/a"} {
			if err := tx.Put([]byte(k), []byte("v"+k)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.View(func(tx Txn) error {
		v, err := tx.Get([]byte("o/b/b"))
		if err != nil || string(v) != "vo/b/b" {
			t.Fatalf("get: %v %q", err, v)
		}
		if _, err := tx.Get([]byte("nope")); err != ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
		it := tx.Seek([]byte("o/b/"))
		defer it.Close()
		var keys []string
		for ; it.Valid() && bytes.HasPrefix(it.Key(), []byte("o/b/")); it.Next() {
			keys = append(keys, string(it.Key()))
		}
		if len(keys) != 3 || keys[0] != "o/b/a" || keys[2] != "o/b/c" {
			t.Fatalf("keys=%v", keys)
		}
		it.Seek([]byte("o/b/b"))
		if !it.Valid() || string(it.Key()) != "o/b/b" {
			t.Fatalf("seek failed: %q", it.Key())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
