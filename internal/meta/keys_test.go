package meta

import (
	"bytes"
	"testing"
)

func TestObjectKeyOrdering(t *testing.T) {
	k1 := ObjectKey("b", "a/b", 100)
	k2 := ObjectKey("b", "a/b", 200)
	if bytes.Compare(k2, k1) >= 0 {
		t.Fatalf("newer version must sort first: %q vs %q", k1, k2)
	}
	// A key that textually extends another must not fall inside its version range.
	other := ObjectKey("b", "a/b/c", 150)
	if !bytes.HasPrefix(other, ObjectScanPrefix("b", "a/b")) {
		t.Fatal("scan prefix should include extended key")
	}
	if bytes.HasPrefix(other, ObjectPrefix("b", "a/b")) {
		t.Fatal("version prefix must not include extended key")
	}
	b, k, seq, ok := SplitObjectKey(k1)
	if !ok || b != "b" || k != "a/b" || seq != 100 {
		t.Fatalf("split: %v %q %q %d", ok, b, k, seq)
	}
}

func TestVersionID(t *testing.T) {
	v := NewVersionID(12345)
	seq, err := SeqFromVersionID(v)
	if err != nil || seq != 12345 {
		t.Fatalf("seq=%d err=%v", seq, err)
	}
	if _, err := SeqFromVersionID("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSequencer(t *testing.T) {
	var s Sequencer
	a, b := s.Next(), s.Next()
	if b <= a {
		t.Fatal("not monotonic")
	}
}
