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

	"gitlab.com/Birdsall/opens3/internal/blob"
	"gitlab.com/Birdsall/opens3/internal/kms"
	"gitlab.com/Birdsall/opens3/internal/kv"
	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/s3err"
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
		b, err = meta.GetBucket(tx, name)
		return err
	})
	if errors.Is(err, kv.ErrNotFound) {
		return nil, s3err.New(s3err.NoSuchBucket).WithResource(name)
	}
	return b, err
}

// UpdateBucket applies fn to the bucket record atomically.
func (s *Service) UpdateBucket(ctx context.Context, name string, fn func(b *meta.Bucket) error) (*meta.Bucket, error) {
	var out *meta.Bucket
	err := s.kv.Update(func(tx kv.Txn) error {
		b, err := meta.GetBucket(tx, name)
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
		var err error
		out, err = meta.ListBuckets(tx)
		return err
	})
	return out, err
}

// DeleteBucket removes an empty bucket.
func (s *Service) DeleteBucket(ctx context.Context, name string) error {
	err := s.kv.Update(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, name); errors.Is(err, kv.ErrNotFound) {
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
		return meta.DeleteBucket(tx, name)
	})
	if err != nil {
		return err
	}
	return s.blobs.DeleteBucket(ctx, name)
}

// DeleteBucketForce removes a bucket and everything in it (admin API).
func (s *Service) DeleteBucketForce(ctx context.Context, name string) error {
	err := s.kv.Update(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, name); errors.Is(err, kv.ErrNotFound) {
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
		return meta.DeleteBucket(tx, name)
	})
	if err != nil {
		return err
	}
	return s.blobs.DeleteBucket(ctx, name)
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
