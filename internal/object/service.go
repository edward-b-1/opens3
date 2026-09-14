// Package object implements the object-store business logic on top of the
// metadata and blob stores: buckets, objects, versioning, multipart
// uploads, conditional writes, checksums, server-side encryption, object
// lock, tagging and ACLs. The S3 API layer translates HTTP into calls
// here; nothing in this package knows about HTTP.
package object

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/edward-b-1/opens3/internal/blob"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// Limits (AWS values).
const (
	MaxObjectSize   = 5 * 1024 * 1024 * 1024 * 1024 // 5 TiB
	MaxPartSize     = 5 * 1024 * 1024 * 1024
	MinPartSize     = 5 * 1024 * 1024
	MaxParts        = 10000
	MaxKeyLength    = 1024
	MaxMetadataSize = 2048
	MaxTags         = 10
)

// Actor identifies who performs an operation (for ownership).
type Actor struct {
	CanonicalID string
	DisplayName string
}

// Event is emitted after successful mutations for the notification system.
type Event struct {
	Name      string // s3:ObjectCreated:Put etc.
	Bucket    *meta.Bucket
	Object    *meta.Object
	Key       string
	VersionID string
	Time      time.Time
	Actor     Actor
}

// Service is the object-store service.
type Service struct {
	kv     kv.Store
	blobs  blob.Store
	kms    kms.KMS
	seq    meta.Sequencer
	log    *slog.Logger
	region string
	// Notify, if set, receives events after each successful mutation.
	Notify func(Event)
	// testBeforeCommit, when set by a test, runs between an operation's
	// authorisation-time bucket read and its write transaction.
	testBeforeCommit func()
}

// New creates a service.
func New(db kv.Store, blobs blob.Store, k kms.KMS, region string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	if region == "" {
		region = "us-east-1"
	}
	return &Service{kv: db, blobs: blobs, kms: k, log: log, region: region}
}

// Region returns the configured region.
func (s *Service) Region() string { return s.region }

// KV exposes the metadata store (for the admin API and workers).
func (s *Service) KV() kv.Store { return s.kv }

// Blobs exposes the blob store.
func (s *Service) Blobs() blob.Store { return s.blobs }

func (s *Service) emit(e Event) {
	if s.Notify != nil {
		e.Time = time.Now().UTC()
		s.Notify(e)
	}
}

// --- validation ----------------------------------------------------------

var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var ipRe = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

// ValidBucketName applies the AWS naming rules for general purpose buckets.
func ValidBucketName(name string) bool {
	if !bucketNameRe.MatchString(name) || ipRe.MatchString(name) {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") {
		return false
	}
	if strings.HasPrefix(name, "xn--") || strings.HasPrefix(name, "sthree-") || strings.HasSuffix(name, "-s3alias") || strings.HasSuffix(name, "--ol-s3") {
		return false
	}
	return true
}

// ValidObjectKey checks key length and characters.
func ValidObjectKey(key string) error {
	if key == "" {
		return s3err.New(s3err.InvalidArgument).WithMessage("Object key cannot be empty")
	}
	if len(key) > MaxKeyLength {
		return s3err.New(s3err.KeyTooLongError)
	}
	if strings.IndexByte(key, 0) >= 0 {
		return s3err.New(s3err.InvalidObjectName)
	}
	if !utf8.ValidString(key) {
		return s3err.New(s3err.InvalidURI)
	}
	return nil
}

// --- buckets -------------------------------------------------------------

// CreateBucketInput parameters.
type CreateBucketInput struct {
	Name              string
	Region            string
	ObjectLockEnabled bool
	ACL               *meta.ACL
	Ownership         string
	Tags              []meta.Tag
}

// CreateBucket creates a bucket owned by actor.
func (s *Service) CreateBucket(ctx context.Context, actor Actor, in CreateBucketInput) (*meta.Bucket, error) {
	if !ValidBucketName(in.Name) {
		return nil, s3err.New(s3err.InvalidBucketName)
	}
	if in.Region == "" {
		in.Region = s.region
	}
	if in.Ownership == "" {
		in.Ownership = "BucketOwnerEnforced"
	}
	b := &meta.Bucket{Name: in.Name, Owner: actor.CanonicalID, OwnerDisplay: actor.DisplayName, Created: time.Now().UTC(),
		Region: in.Region, ObjectLockEnabled: in.ObjectLockEnabled, ACL: in.ACL, Ownership: in.Ownership, Tags: in.Tags}
	if in.ObjectLockEnabled {
		b.Versioning = "Enabled"
	}
	if b.ACL == nil {
		b.ACL = &meta.ACL{Owner: actor.CanonicalID, OwnerDisplay: actor.DisplayName,
			Grants: []meta.Grant{{Grantee: actor.CanonicalID, GranteeType: "CanonicalUser", DisplayName: actor.DisplayName, Permission: "FULL_CONTROL"}}}
	}
	err := s.kv.Update(func(tx kv.Txn) error {
		if err := meta.CreateBucket(tx, b); errors.Is(err, meta.ErrExists) {
			old, _ := meta.GetBucket(tx, in.Name)
			if old != nil && old.Deleting {
				// The previous bucket of this name is still being removed.
				return s3err.New(s3err.OperationAborted)
			}
			if old != nil && old.Owner == actor.CanonicalID {
				return s3err.New(s3err.BucketAlreadyOwnedByYou)
			}
			return s3err.New(s3err.BucketAlreadyExists)
		} else if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// GetBucket returns the bucket record or NoSuchBucket.
func (s *Service) GetBucket(ctx context.Context, name string) (*meta.Bucket, error) {
	var b *meta.Bucket
	err := s.kv.View(func(tx kv.Txn) error {
		var err error
		b, err = getLiveBucket(tx, name)
		return err
	})
	if errors.Is(err, kv.ErrNotFound) {
		return nil, s3err.New(s3err.NoSuchBucket).WithResource(name)
	}
	return b, err
}

// getLiveBucket is meta.GetBucket that treats a bucket being deleted as
// absent.
func getLiveBucket(tx kv.Txn, name string) (*meta.Bucket, error) {
	b, err := meta.GetBucket(tx, name)
	if err != nil {
		return nil, err
	}
	if b.Deleting {
		return nil, kv.ErrNotFound
	}
	return b, nil
}

// liveBucket is getLiveBucket that also requires the bucket to be the
// incarnation the request was authorised against (see expect.go).
func liveBucket(ctx context.Context, tx kv.Txn, name string) (*meta.Bucket, error) {
	b, err := getLiveBucket(tx, name)
	if err != nil {
		return nil, err
	}
	if !bucketExpected(ctx, b) {
		return nil, kv.ErrNotFound
	}
	return b, nil
}

// errChanged is returned when the object a request was authorised against
// was replaced before the operation ran.
func errChanged() error {
	return s3err.New(s3err.NoSuchKey).WithMessage("The object changed while the request was being processed. Retry the request.")
}

// sameBucket reports whether cur is the same incarnation of the bucket
// that was read (and authorised) earlier: a bucket deleted and recreated
// under the same name has a different creation time, and writes
// authorised against the old one must not land in the new one.
func sameBucket(cur, seen *meta.Bucket) bool {
	return cur != nil && seen != nil && !cur.Deleting && cur.Created.Equal(seen.Created)
}

// UpdateBucket applies fn to the bucket record atomically.
func (s *Service) UpdateBucket(ctx context.Context, name string, fn func(b *meta.Bucket) error) (*meta.Bucket, error) {
	var out *meta.Bucket
	err := s.kv.Update(func(tx kv.Txn) error {
		b, err := liveBucket(ctx, tx, name)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(name)
		} else if err != nil {
			return err
		}
		if err := fn(b); err != nil {
			return err
		}
		out = b
		return meta.PutBucket(tx, b)
	})
	return out, err
}

// ListBuckets returns all buckets (the API layer filters by owner/policy).
func (s *Service) ListBuckets(ctx context.Context) ([]*meta.Bucket, error) {
	var out []*meta.Bucket
	err := s.kv.View(func(tx kv.Txn) error {
		all, err := meta.ListBuckets(tx)
		if err != nil {
			return err
		}
		for _, b := range all {
			if !b.Deleting {
				out = append(out, b)
			}
		}
		return nil
	})
	return out, err
}

// DeleteBucket removes an empty bucket. The record is first marked as
// deleting (which hides the bucket and blocks recreation of the name),
// then the data directory is removed, then the record: a bucket created
// under the same name can never lose files to the removal of the old one.
func (s *Service) DeleteBucket(ctx context.Context, name string) error {
	err := s.kv.Update(func(tx kv.Txn) error {
		b, err := getLiveBucket(tx, name)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(name)
		} else if err != nil {
			return err
		}
		has, err := meta.BucketHasObjects(tx, name)
		if err != nil {
			return err
		}
		if has {
			return s3err.New(s3err.BucketNotEmpty)
		}
		// In-progress multipart uploads are not objects: AWS deletes the
		// bucket regardless, so abort them here (their blobs go with the
		// bucket directory below).
		var dead [][]byte
		for _, p := range [][]byte{meta.UploadBucketPrefix(name), []byte("ui/" + name + "/"), []byte("p/" + name + "/")} {
			it := tx.Seek(p)
			for ; it.Valid() && kv.HasPrefix(it.Key(), p); it.Next() {
				dead = append(dead, append([]byte(nil), it.Key()...))
			}
			it.Close()
		}
		for _, k := range dead {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		b.Deleting = true
		return meta.PutBucket(tx, b)
	})
	if err != nil {
		return err
	}
	return s.finishDelete(ctx, name)
}

// finishDelete removes a deleting bucket's data directory and then its
// record.
func (s *Service) finishDelete(ctx context.Context, name string) error {
	if err := s.blobs.DeleteBucket(ctx, name); err != nil {
		return err
	}
	return s.kv.Update(func(tx kv.Txn) error {
		b, err := meta.GetBucket(tx, name)
		if errors.Is(err, kv.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if !b.Deleting {
			return nil // recreated already (should not happen: creation is blocked)
		}
		return meta.DeleteBucket(tx, name)
	})
}

// FinishDeletes completes bucket removals interrupted by a crash; the
// server calls it at start.
func (s *Service) FinishDeletes(ctx context.Context) (int, error) {
	var names []string
	err := s.kv.View(func(tx kv.Txn) error {
		all, err := meta.ListBuckets(tx)
		for _, b := range all {
			if b.Deleting {
				names = append(names, b.Name)
			}
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	for _, n := range names {
		if err := s.finishDelete(ctx, n); err != nil {
			return 0, err
		}
	}
	return len(names), nil
}

// DeleteBucketForce removes a bucket and everything in it (admin API).
func (s *Service) DeleteBucketForce(ctx context.Context, name string) error {
	err := s.kv.Update(func(tx kv.Txn) error {
		b, err := getLiveBucket(tx, name)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(name)
		} else if err != nil {
			return err
		}
		var dead [][]byte
		for _, p := range [][]byte{meta.ObjectBucketPrefix(name), meta.NullBucketPrefix(name), meta.UploadBucketPrefix(name),
			[]byte("ui/" + name + "/"), []byte("p/" + name + "/")} {
			it := tx.Seek(p)
			for ; it.Valid() && kv.HasPrefix(it.Key(), p); it.Next() {
				dead = append(dead, append([]byte(nil), it.Key()...))
			}
			it.Close()
		}
		for _, k := range dead {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		b.Deleting = true
		return meta.PutBucket(tx, b)
	})
	if err != nil {
		return err
	}
	return s.finishDelete(ctx, name)
}

// --- helpers -------------------------------------------------------------

func (s *Service) deleteBlobs(ctx context.Context, bucket string, parts []meta.Part) {
	for _, p := range parts {
		if p.Blob == "" {
			continue
		}
		if err := s.blobs.Delete(ctx, bucket, p.Blob); err != nil {
			s.log.Warn("delete blob", "bucket", bucket, "blob", p.Blob, "err", err)
		}
	}
}

// versionIDFor returns the version ID for a new write in bucket b.
func (s *Service) versionIDFor(b *meta.Bucket, seq uint64) string {
	if b.Versioning == "Enabled" {
		return meta.NewVersionID(seq)
	}
	return meta.NullVersionID
}

// checkLocked returns an error if the object version is protected by
// retention or legal hold.
func checkLocked(o *meta.Object, bypassGovernance bool) error {
	if o == nil {
		return nil
	}
	if o.LegalHold {
		return s3err.New(s3err.AccessDenied).WithMessage("Object is under legal hold and cannot be deleted")
	}
	if o.Retention != nil && time.Now().Before(o.Retention.RetainUntil) {
		if o.Retention.Mode == "COMPLIANCE" || !bypassGovernance {
			return s3err.New(s3err.AccessDenied).WithMessage("Object is WORM protected and cannot be deleted")
		}
	}
	return nil
}

// Conditions are the conditional-request headers.
type Conditions struct {
	IfMatch           string // ETag or "*"
	IfNoneMatch       string // ETag or "*"
	IfModifiedSince   time.Time
	IfUnmodifiedSince time.Time
}

func etagMatches(list, etag string) bool {
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		e = strings.TrimPrefix(e, "W/")
		e = strings.Trim(e, `"`)
		if e == "*" || e == etag {
			return true
		}
	}
	return false
}

// checkReadConditions applies RFC 7232 precondition semantics as S3 does.
func checkReadConditions(o *meta.Object, c Conditions) error {
	if c.IfMatch != "" && !etagMatches(c.IfMatch, o.ETag) {
		return s3err.New(s3err.PreconditionFailed)
	}
	if !c.IfUnmodifiedSince.IsZero() && o.ModTime.Truncate(time.Second).After(c.IfUnmodifiedSince) && c.IfMatch == "" {
		return s3err.New(s3err.PreconditionFailed)
	}
	if c.IfNoneMatch != "" && etagMatches(c.IfNoneMatch, o.ETag) {
		return s3err.New(s3err.NotModified)
	}
	if !c.IfModifiedSince.IsZero() && c.IfNoneMatch == "" && !o.ModTime.Truncate(time.Second).After(c.IfModifiedSince) {
		return s3err.New(s3err.NotModified)
	}
	return nil
}

// checkWriteConditions applies the conditional-write rules (If-None-Match:
// *, If-Match: etag) against the current latest version inside a
// transaction.
func checkWriteConditions(tx kv.Txn, bucket, key string, c Conditions) error {
	if c.IfMatch == "" && c.IfNoneMatch == "" {
		return nil
	}
	cur, err := meta.GetLatest(tx, bucket, key)
	exists := err == nil && !cur.DeleteMarker
	if c.IfNoneMatch != "" && exists && etagMatches(c.IfNoneMatch, cur.ETag) {
		return s3err.New(s3err.PreconditionFailed)
	}
	if c.IfMatch != "" {
		if !exists {
			return s3err.New(s3err.NoSuchKey)
		}
		if !etagMatches(c.IfMatch, cur.ETag) {
			return s3err.New(s3err.PreconditionFailed)
		}
	}
	return nil
}

// NotFoundError maps kv not-found to NoSuchKey.
func keyErr(err error, bucket, key string) error {
	if errors.Is(err, kv.ErrNotFound) {
		return s3err.New(s3err.NoSuchKey).WithResource("/" + bucket + "/" + key)
	}
	return err
}

func fmtETag(parts int, md5hex string) string {
	if parts > 0 {
		return fmt.Sprintf("%s-%d", md5hex, parts)
	}
	return md5hex
}
