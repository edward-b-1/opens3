package object

import (
	"context"
	"errors"
	"time"

	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// updateVersion applies fn to a specific (or latest) version record
// atomically. Delete markers are rejected with MethodNotAllowed.
func (s *Service) updateVersion(ctx context.Context, bucket, key, versionID string, fn func(o *meta.Object) error) (*meta.Object, error) {
	var out *meta.Object
	err := s.kv.Update(func(tx kv.Txn) error {
		if _, err := liveBucket(ctx, tx, bucket); errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(bucket)
		} else if err != nil {
			return err
		}
		var o *meta.Object
		var err error
		if versionID == "" {
			o, err = meta.GetLatest(tx, bucket, key)
		} else {
			o, err = meta.GetVersion(tx, bucket, key, versionID)
			if errors.Is(err, kv.ErrNotFound) {
				return s3err.New(s3err.NoSuchVersion)
			}
		}
		if err != nil {
			return keyErr(err, bucket, key)
		}
		if !objectExpected(ctx, o) {
			return errChanged()
		}
		if o.DeleteMarker {
			return s3err.New(s3err.MethodNotAllowed).WithHeader("x-amz-delete-marker", "true").WithHeader("x-amz-version-id", o.VersionID)
		}
		if err := fn(o); err != nil {
			return err
		}
		out = o
		// Re-put without touching the null pointer (same seq/version).
		return tx.Put(meta.ObjectKey(bucket, key, o.Seq), encodeObject(o))
	})
	return out, err
}

// PutObjectTagging replaces the tag set of a version.
func (s *Service) PutObjectTagging(ctx context.Context, actor Actor, bucket, key, versionID string, tags []meta.Tag) (*meta.Object, error) {
	if len(tags) > MaxTags {
		return nil, s3err.New(s3err.InvalidTag).WithMessage("Object tags cannot be greater than 10")
	}
	if err := ValidateTags(tags); err != nil {
		return nil, err
	}
	o, err := s.updateVersion(ctx, bucket, key, versionID, func(o *meta.Object) error { o.Tags = tags; return nil })
	if err != nil {
		return nil, err
	}
	b, _ := s.GetBucket(ctx, bucket)
	s.emit(Event{Name: "s3:ObjectTagging:Put", Bucket: b, Object: o, Key: key, VersionID: o.VersionID, Actor: actor})
	return o, nil
}

// DeleteObjectTagging clears the tag set.
func (s *Service) DeleteObjectTagging(ctx context.Context, actor Actor, bucket, key, versionID string) (*meta.Object, error) {
	o, err := s.updateVersion(ctx, bucket, key, versionID, func(o *meta.Object) error { o.Tags = nil; return nil })
	if err != nil {
		return nil, err
	}
	b, _ := s.GetBucket(ctx, bucket)
	s.emit(Event{Name: "s3:ObjectTagging:Delete", Bucket: b, Object: o, Key: key, VersionID: o.VersionID, Actor: actor})
	return o, nil
}

// PutObjectACL replaces the ACL of a version.
func (s *Service) PutObjectACL(ctx context.Context, actor Actor, bucket, key, versionID string, acl *meta.ACL) (*meta.Object, error) {
	o, err := s.updateVersion(ctx, bucket, key, versionID, func(o *meta.Object) error { o.ACL = acl; return nil })
	if err != nil {
		return nil, err
	}
	b, _ := s.GetBucket(ctx, bucket)
	s.emit(Event{Name: "s3:ObjectAcl:Put", Bucket: b, Object: o, Key: key, VersionID: o.VersionID, Actor: actor})
	return o, nil
}

// PutObjectRetention sets retention, enforcing the rules for shortening
// or changing mode (COMPLIANCE cannot be reduced; GOVERNANCE needs bypass).
func (s *Service) PutObjectRetention(ctx context.Context, actor Actor, bucket, key, versionID string, r *meta.Retention, bypassGovernance bool) (*meta.Object, error) {
	b, err := s.GetBucket(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !b.ObjectLockEnabled {
		return nil, s3err.New(s3err.InvalidRequest).WithMessage("Bucket is missing Object Lock Configuration")
	}
	if r != nil && r.Mode != "GOVERNANCE" && r.Mode != "COMPLIANCE" {
		return nil, s3err.New(s3err.MalformedXML)
	}
	o, err := s.updateVersion(ctx, bucket, key, versionID, func(o *meta.Object) error {
		cur := o.Retention
		if cur != nil && time.Now().Before(cur.RetainUntil) {
			shortening := r == nil || r.RetainUntil.Before(cur.RetainUntil) || r.Mode != cur.Mode
			if shortening {
				if cur.Mode == "COMPLIANCE" || !bypassGovernance {
					return s3err.New(s3err.AccessDenied)
				}
			}
		}
		o.Retention = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.emit(Event{Name: "s3:ObjectRetention:Put", Bucket: b, Object: o, Key: key, VersionID: o.VersionID, Actor: actor})
	return o, nil
}

// PutObjectLegalHold sets or clears the legal hold.
func (s *Service) PutObjectLegalHold(ctx context.Context, actor Actor, bucket, key, versionID string, on bool) (*meta.Object, error) {
	b, err := s.GetBucket(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !b.ObjectLockEnabled {
		return nil, s3err.New(s3err.InvalidRequest).WithMessage("Bucket is missing Object Lock Configuration")
	}
	return s.updateVersion(ctx, bucket, key, versionID, func(o *meta.Object) error { o.LegalHold = on; return nil })
}

// UpdateObjectMeta applies fn to a version (used by lifecycle transitions
// and restore).
func (s *Service) UpdateObjectMeta(ctx context.Context, bucket, key, versionID string, fn func(o *meta.Object) error) (*meta.Object, error) {
	return s.updateVersion(ctx, bucket, key, versionID, fn)
}
