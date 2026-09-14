package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edward-b-1/opens3/internal/server"
)

const (
	testMaterialA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-material-a"
	testMaterialB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-material-b"
)

// initRoot starts and stops a server on root so that the data directory
// carries a key-check value, the default encryption key and the built-in
// policies, the way a real installation does.
func initRoot(t *testing.T, root, masterKey string) {
	t.Helper()
	srv, err := server.New(server.Config{Root: root, RootUser: "root", RootPassword: "rootsecret", MasterKey: masterKey, NoFsync: true,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))})
	if err != nil {
		t.Fatalf("server on %s: %v", root, err)
	}
	srv.Close()
}

func master(t *testing.T, args ...string) (code int, out string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code = runMasterIO(args, &stdout, &stderr)
	return code, stdout.String() + stderr.String()
}

func expect(t *testing.T, code int, out string, wantCode int, wantText ...string) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("exit %d, want %d:\n%s", code, wantCode, out)
	}
	for _, w := range wantText {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestMasterFileMode(t *testing.T) {
	t.Setenv("OPENS3_MASTER_KEY", "")
	t.Setenv("OPENS3_MASTER_KEY_OLD", "")
	t.Setenv("OPENS3_MASTER_KEY_NEW", "")
	root := t.TempDir()
	initRoot(t, root, "")

	code, out := master(t, "status", "--root", root)
	expect(t, code, out, 0, "k1", "(current)", "key-check value", "encryption keys")
	if strings.Contains(out, "retire") {
		t.Fatalf("single-key ring must not suggest retire:\n%s", out)
	}
	code, out = master(t, "retire", "--root", root)
	expect(t, code, out, 0, "Nothing to retire")

	code, out = master(t, "--root", root, "rotate")
	expect(t, code, out, 0, "Added key k2", "Back up", "retire")
	code, out = master(t, "status", "--root", root)
	expect(t, code, out, 0, "k2", "k1", "Every record is under the current key")
	code, out = master(t, "rewrap", "--dry-run", "--root", root)
	expect(t, code, out, 0, "Dry run: 0 records")
	code, out = master(t, "retire", "--root", root)
	expect(t, code, out, 0, "Removed k1")
	code, out = master(t, "status", "--root", root)
	expect(t, code, out, 0, "k2")
	if strings.Contains(out, "k1") {
		t.Fatalf("k1 still listed:\n%s", out)
	}
	// The server starts on the rotated directory.
	initRoot(t, root, "")

	// Usage and errors.
	code, out = master(t)
	expect(t, code, out, 2, "usage: opens3 master")
	code, out = master(t, "bogus")
	expect(t, code, out, 2, "unknown command")
	code, out = master(t, "status", "--root", t.TempDir())
	expect(t, code, out, 1, "no master key file")
	t.Setenv("OPENS3_MASTER_KEY_NEW", testMaterialB)
	code, out = master(t, "rotate", "--root", root)
	expect(t, code, out, 1, "OPENS3_MASTER_KEY_NEW is only used")
	t.Setenv("OPENS3_MASTER_KEY_NEW", "")

	// A running server holds the database lock.
	srv, err := server.New(server.Config{Root: root, RootUser: "root", RootPassword: "rootsecret", NoFsync: true,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	code, out = master(t, "status", "--root", root)
	expect(t, code, out, 1, "locked: stop the server first")
}

func TestMasterEnvironmentMode(t *testing.T) {
	t.Setenv("OPENS3_MASTER_KEY", "")
	t.Setenv("OPENS3_MASTER_KEY_OLD", "")
	t.Setenv("OPENS3_MASTER_KEY_NEW", "")
	root := t.TempDir()
	initRoot(t, root, "")
	keyFile := filepath.Join(root, "meta", "master.keys")

	// Move from the key file to an environment key: status shows the
	// records under the file key, rewrap moves them, retire deletes the file.
	t.Setenv("OPENS3_MASTER_KEY", testMaterialA)
	code, out := master(t, "status", "--root", root)
	expect(t, code, out, 0, "OPENS3_MASTER_KEY", "k1", "run `opens3 master rewrap`")
	code, out = master(t, "retire", "--root", root)
	expect(t, code, out, 1, "still wrapped under older keys")
	code, out = master(t, "rewrap", "--root", root)
	expect(t, code, out, 0, "Re-wrapped")
	code, out = master(t, "retire", "--root", root)
	expect(t, code, out, 0, "Removed "+keyFile)
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Fatal("key file still present")
	}
	initRoot(t, root, testMaterialA)
	code, out = master(t, "retire", "--root", root)
	expect(t, code, out, 0, "Nothing to retire")

	// Rotate between environment keys.
	code, out = master(t, "rotate", "--root", root)
	expect(t, code, out, 1, "OPENS3_MASTER_KEY_NEW")
	t.Setenv("OPENS3_MASTER_KEY_NEW", testMaterialB)
	code, out = master(t, "rotate", "--root", root)
	expect(t, code, out, 0, "Set OPENS3_MASTER_KEY to the value of OPENS3_MASTER_KEY_NEW")
	t.Setenv("OPENS3_MASTER_KEY_NEW", "")
	t.Setenv("OPENS3_MASTER_KEY", testMaterialB)
	initRoot(t, root, testMaterialB)
	code, out = master(t, "status", "--root", root)
	expect(t, code, out, 0, "OPENS3_MASTER_KEY")
	if strings.Contains(out, "older keys: run") {
		t.Fatalf("records left under the old key:\n%s", out)
	}
	// The wrong key is refused without touching anything.
	t.Setenv("OPENS3_MASTER_KEY", testMaterialA)
	code, out = master(t, "status", "--root", root)
	expect(t, code, out, 1, "does not match")

	// Move back from the environment to a key file.
	t.Setenv("OPENS3_MASTER_KEY", "")
	code, out = master(t, "rotate", "--root", root)
	expect(t, code, out, 1, "set OPENS3_MASTER_KEY_OLD")
	t.Setenv("OPENS3_MASTER_KEY_OLD", testMaterialB)
	code, out = master(t, "rotate", "--root", root)
	expect(t, code, out, 0, "Created "+keyFile, "Start the server without OPENS3_MASTER_KEY")
	code, out = master(t, "retire", "--root", root)
	expect(t, code, out, 0, "Nothing to retire", "OPENS3_MASTER_KEY_OLD is no longer needed")
	t.Setenv("OPENS3_MASTER_KEY_OLD", "")
	initRoot(t, root, "")
	code, out = master(t, "status", "--root", root)
	expect(t, code, out, 0, "k1", "(current)")
}
