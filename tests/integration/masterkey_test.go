package integration

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/masterkey"
	"github.com/edward-b-1/opens3/internal/server"
)

// TestMasterKeyRotation writes every kind of master-wrapped record through
// the API, rotates the master key with the server stopped (what `opens3
// master rotate` then `retire` do), and checks that a server started on
// the pruned ring can read all of it.
func TestMasterKeyRotation(t *testing.T) {
	root := t.TempDir()
	start := func() *env {
		srv, err := server.New(server.Config{Root: root, RootUser: rootUser, RootPassword: rootPass, Region: "us-east-1", NoFsync: true,
			Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))})
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv.Handler())
		e := &env{t: t, srv: srv, ts: ts, ctx: context.Background()}
		e.s3 = e.client(rootUser, rootPass, "")
		return e
	}
	stop := func(e *env) { e.ts.Close(); e.srv.Close() }

	e := start()
	e.mkBucket("rot")
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("rot"), Key: aws.String("s3"), Body: strings.NewReader("sse-s3 body"), ServerSideEncryption: types.ServerSideEncryptionAes256}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("rot"), Key: aws.String("kms"), Body: strings.NewReader("sse-kms body"), ServerSideEncryption: types.ServerSideEncryptionAwsKms}); err != nil {
		t.Fatal(err)
	}
	e.put("rot", "plain", "plain body")
	mp, err := e.s3.CreateMultipartUpload(e.ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("rot"), Key: aws.String("big"), ServerSideEncryption: types.ServerSideEncryptionAes256})
	if err != nil {
		t.Fatal(err)
	}
	ic := e.iam(rootUser, rootPass)
	if _, err := ic.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String("bob")}); err != nil {
		t.Fatal(err)
	}
	ck, err := ic.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{UserName: aws.String("bob")})
	if err != nil {
		t.Fatal(err)
	}
	bobAK, bobSK := *ck.AccessKey.AccessKeyId, *ck.AccessKey.SecretAccessKey
	stop(e)

	// Rotate offline: add a key, re-wrap, prune the old key.
	keyFile := filepath.Join(root, "meta", "master.keys")
	ring, err := kms.LoadMasterFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	oldFP := ring.Fingerprint()
	if _, err := ring.AddKey(); err != nil {
		t.Fatal(err)
	}
	db, err := kv.OpenBolt(filepath.Join(root, "meta", "opens3.db"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := masterkey.Run(db, ring, masterkey.Options{})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	// check value + default key + bob's key + one SSE-S3 object + one upload.
	if rep.Rewrapped() != 5 || rep.Stale() != 0 || rep.Unreadable() != 0 {
		t.Fatalf("rewrap report: %+v", rep)
	}
	if _, err := ring.Prune(); err != nil {
		t.Fatal(err)
	}
	if pruned, _ := kms.LoadMasterFile(keyFile); pruned.Keys() != 1 || pruned.Fingerprint() == oldFP {
		t.Fatal("old key still in the ring")
	}

	// Everything is readable on the new key alone.
	e = start()
	defer stop(e)
	for key, want := range map[string]string{"s3": "sse-s3 body", "kms": "sse-kms body", "plain": "plain body"} {
		if body, _ := e.get("rot", key); body != want {
			t.Fatalf("object %s after rotation: %q", key, body)
		}
	}
	up, err := e.s3.UploadPart(e.ctx, &s3.UploadPartInput{Bucket: aws.String("rot"), Key: aws.String("big"), UploadId: mp.UploadId, PartNumber: aws.Int32(1), Body: strings.NewReader("part one")})
	if err != nil {
		t.Fatalf("upload part after rotation: %v", err)
	}
	if _, err := e.s3.CompleteMultipartUpload(e.ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String("rot"), Key: aws.String("big"), UploadId: mp.UploadId,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{PartNumber: aws.Int32(1), ETag: up.ETag}}}}); err != nil {
		t.Fatalf("complete after rotation: %v", err)
	}
	if body, g := e.get("rot", "big"); body != "part one" || g.ServerSideEncryption != types.ServerSideEncryptionAes256 {
		t.Fatalf("multipart object after rotation: %q %v", body, g.ServerSideEncryption)
	}
	if _, err := e.client(bobAK, bobSK, "").ListBuckets(e.ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatalf("bob's access key after rotation: %v", err)
	}
}
