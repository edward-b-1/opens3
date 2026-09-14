package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fsckCmd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := runFsckIO(args, &out, &out)
	return code, out.String()
}

func TestFsckAndExportCommands(t *testing.T) {
	t.Setenv("OPENS3_MASTER_KEY", "")
	root := t.TempDir()
	initRoot(t, root, "")

	code, out := fsckCmd(t, "check", "--root", root)
	expect(t, code, out, 0, "No problems found", "0 buckets")

	// An orphan file: check reports it (exit 1), repair removes it.
	os.MkdirAll(filepath.Join(root, "data", "nobucket", "ab"), 0o700)
	os.WriteFile(filepath.Join(root, "data", "nobucket", "ab", "ab00orphan"), []byte("x"), 0o600)
	code, out = fsckCmd(t, "check", "--root", root)
	expect(t, code, out, 1, "orphan file", "1 repairable")
	code, out = fsckCmd(t, "repair", "--dry-run", "--root", root)
	expect(t, code, out, 0, "Would repair 1 of 1")
	if _, err := os.Stat(filepath.Join(root, "data", "nobucket", "ab", "ab00orphan")); err != nil {
		t.Fatal("dry run deleted the file")
	}
	code, out = fsckCmd(t, "repair", "--root", root)
	expect(t, code, out, 0, "Repaired 1 of 1")
	code, out = fsckCmd(t, "check", "--root", root)
	expect(t, code, out, 0, "No problems found")

	// Usage and errors.
	code, out = fsckCmd(t)
	expect(t, code, out, 2, "usage: opens3 fsck")
	code, out = fsckCmd(t, "bogus", "--root", root)
	expect(t, code, out, 2, "unknown command")
	code, out = fsckCmd(t, "check", "--root", t.TempDir())
	expect(t, code, out, 2, "no metadata database")

	// Export of an empty directory: nothing but a manifest.
	var eout bytes.Buffer
	dest := filepath.Join(t.TempDir(), "out")
	if code := runExportIO([]string{"--root", root, "--to", dest}, &eout, &eout); code != 0 {
		t.Fatalf("export: %d %s", code, eout.String())
	}
	if !strings.Contains(eout.String(), "Exported 0 objects") {
		t.Fatalf("export output: %s", eout.String())
	}
	if _, err := os.Stat(filepath.Join(dest, "manifest.jsonl")); err != nil {
		t.Fatal("no manifest")
	}
	eout.Reset()
	if code := runExportIO([]string{"--root", root}, &eout, &eout); code != 2 || !strings.Contains(eout.String(), "--to OUTDIR is required") {
		t.Fatalf("export without --to: %d %s", code, eout.String())
	}
}
