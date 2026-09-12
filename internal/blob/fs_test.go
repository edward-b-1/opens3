package blob

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFSRoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := OpenFS(root, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w, err := s.Create(ctx, "bk")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("hello "))
	w.Write([]byte("world"))
	id, n, err := w.Commit()
	if err != nil || n != 11 {
		t.Fatalf("commit: %v %d", err, n)
	}
	if _, err := os.Stat(filepath.Join(root, "tmp", id)); !os.IsNotExist(err) {
		t.Fatal("tmp file should be gone")
	}
	r, err := s.Open(ctx, "bk", id, 6, 3)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "wor" {
		t.Fatalf("range read: %q", b)
	}
	r, _ = s.Open(ctx, "bk", id, 0, -1)
	b, _ = io.ReadAll(r)
	r.Close()
	if string(b) != "hello world" {
		t.Fatalf("full read: %q", b)
	}
	if sz, _ := s.Size(ctx, "bk", id); sz != 11 {
		t.Fatal("size")
	}
	if err := s.Delete(ctx, "bk", id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "bk", id, 0, -1); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
	if err := s.Delete(ctx, "bk", id); err != nil {
		t.Fatal("double delete should be nil")
	}
	// Abort leaves nothing behind.
	w, _ = s.Create(ctx, "bk")
	w.Write([]byte("x"))
	w.Abort()
	entries, _ := os.ReadDir(filepath.Join(root, "tmp"))
	if len(entries) != 0 {
		t.Fatal("abort left tmp file")
	}
	if _, err := s.Stats(ctx); err != nil {
		t.Fatal(err)
	}
}
