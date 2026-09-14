package object

import (
	"context"
	"errors"
	"time"

	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// DeleteInput parameters.
type DeleteInput struct {
	Bucket           string
	Key              string
	VersionID        string
	BypassGovernance bool
	IfMatch          string // conditional delete (ETag or *)
	// IfMatchSize / IfMatchLastModified are accepted for DeleteObjects
	// per-object conditions.
}

// DeleteResult describes what happened.
type DeleteResult struct {
	DeleteMarker bool   // a delete marker was created (or the deleted version was one)
	VersionID    string // version created or deleted (empty for unversioned)
}

// DeleteObject implements the S3 delete semantics for versioned,
// suspended and unversioned buckets.
func (s *Service) DeleteObject(ctx context.Context, actor Actor, in DeleteInput) (*DeleteResult, error) {
	if err := ValidObjectKey(in.Key); err != nil {
		return nil, err
	}
	var res DeleteResult
	var bucket *meta.Bucket
	var removed []meta.Part
	var eventObj *meta.Object
	eventName := "s3:ObjectRemoved:Delete"
	err := s.kv.Update(func(tx kv.Txn) error {
		b, err := getLiveBucket(tx, in.Bucket)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(in.Bucket)
		} else if err != nil {
			return err
		}
		bucket = b
		if in.VersionID != "" {
			// Permanent delete of a specific version.
			o, err := meta.GetVersion(tx, in.Bucket, in.Key, in.VersionID)
			if errors.Is(err, kv.ErrNotFound) {
				if in.VersionID != meta.NullVersionID {
					if _, perr := meta.SeqFromVersionID(in.VersionID); perr != nil {
						return s3err.New(s3err.InvalidArgument).WithMessage("Invalid version id specified")
					}
				}
				return nil // idempotent
			} else if err != nil {
				return err
			}
			if in.IfMatch != "" && !etagMatches(in.IfMatch, o.ETag) {
				return s3err.New(s3err.PreconditionFailed)
			}
			if err := checkLocked(o, in.BypassGovernance); err != nil {
				return err
			}
			if err := meta.DeleteVersion(tx, o); err != nil {
				return err
			}
			removed = o.Parts
			res.VersionID = o.VersionID
			res.DeleteMarker = o.DeleteMarker
			eventObj = o
			return nil
		}
		cur, err := meta.GetLatest(tx, in.Bucket, in.Key)
		exists := err == nil
		if err != nil && !errors.Is(err, kv.ErrNotFound) {
			return err
		}
		if in.IfMatch != "" {
			if !exists || cur.DeleteMarker {
				return s3err.New(s3err.NoSuchKey)
			}
			if !etagMatches(in.IfMatch, cur.ETag) {
				return s3err.New(s3err.PreconditionFailed)
			}
		}
		switch b.Versioning {
		case "Enabled":
			// Insert a delete marker (even if the key does not exist).
			seq := s.seq.Next()
			dm := &meta.Object{Bucket: in.Bucket, Key: in.Key, VersionID: meta.NewVersionID(seq), Seq: seq, DeleteMarker: true,
				ModTime: time.Now().UTC(), Owner: actor.CanonicalID, OwnerDisplay: actor.DisplayName}
			if _, err := meta.PutObject(tx, dm); err != nil {
				return err
			}
			res.DeleteMarker, res.VersionID = true, dm.VersionID
			eventObj = dm
			eventName = "s3:ObjectRemoved:DeleteMarkerCreated"
		case "Suspended":
			// Remove the null version (if any) and insert a null delete marker.
			if nv, err := meta.GetVersion(tx, in.Bucket, in.Key, meta.NullVersionID); err == nil {
				if err := checkLocked(nv, in.BypassGovernance); err != nil {
					return err
				}
				removed = nv.Parts
			}
			seq := s.seq.Next()
			dm := &meta.Object{Bucket: in.Bucket, Key: in.Key, VersionID: meta.NullVersionID, Seq: seq, DeleteMarker: true,
				ModTime: time.Now().UTC(), Owner: actor.CanonicalID, OwnerDisplay: actor.DisplayName}
			if _, err := meta.PutObject(tx, dm); err != nil {
				return err
			}
			res.DeleteMarker, res.VersionID = true, meta.NullVersionID
			eventObj = dm
			eventName = "s3:ObjectRemoved:DeleteMarkerCreated"
		default:
			if !exists {
				return nil
			}
			if err := checkLocked(cur, in.BypassGovernance); err != nil {
				return err
			}
			if err := meta.DeleteVersion(tx, cur); err != nil {
				return err
			}
			removed = cur.Parts
			eventObj = cur
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.deleteBlobs(ctx, in.Bucket, removed)
	if eventObj != nil {
		s.emit(Event{Name: eventName, Bucket: bucket, Object: eventObj, Key: in.Key, VersionID: res.VersionID, Actor: actor})
	}
	return &res, nil
}
