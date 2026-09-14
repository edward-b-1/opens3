package server

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/edward-b-1/opens3/internal/iam"
)

func TestPurgeExpiredCredentials(t *testing.T) {
	s, err := New(Config{Root: t.TempDir(), RootUser: "rootuser", RootPassword: "rootsecret", NoFsync: true,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.IAM.CreateUser("alice", "alicesecret", nil); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-3 * time.Hour)
	dead, _, err := s.IAM.CreateKey("alice", "", "", iam.KindService, nil, &past, "expired")
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	live, _, err := s.IAM.CreateKey("alice", "", "", iam.KindService, nil, &future, "live")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := s.IAM.Resolve("rootuser", "")
	stsAK, _, _, _, err := s.IAM.AssumeRole(root, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// The startup sweep runs concurrently, so the count here may be 0 or 1;
	// what matters is the outcome.
	if n := s.PurgeExpiredCredentials(); n > 1 {
		t.Fatalf("purged %d, want at most 1", n)
	}
	if _, err := s.IAM.GetKey(dead.AccessKey); err == nil {
		t.Fatal("expired key should be gone")
	}
	for _, ak := range []string{live.AccessKey, stsAK, "alice"} {
		if _, err := s.IAM.GetKey(ak); err != nil {
			t.Fatalf("%s should survive: %v", ak, err)
		}
	}
	if n := s.PurgeExpiredCredentials(); n != 0 {
		t.Fatalf("second sweep purged %d", n)
	}
}
