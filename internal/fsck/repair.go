package fsck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
)

// Action is one repair performed (or, in a dry run, proposed).
type Action struct {
	Kind   string
	Bucket string
	Item   string
	What   string
}

// RepairResult lists what Repair did.
type RepairResult struct {
	Actions []Action
	DryRun  bool
}

// Repair acts on the repairable problems of a report. With dryRun it only
// lists what it would do. It re-reads each record before touching it, so
// a report from a copy of the data directory is still safe to apply.
func Repair(ctx context.Context, db kv.Store, root string, rep *Report, dryRun bool) (*RepairResult, error) {
	res := &RepairResult{DryRun: dryRun}
	r := &repairer{db: db, root: root, dry: dryRun, res: res}
	for _, p := range rep.Problems {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		var err error
		switch p.Kind {
		case OrphanFile, TempFile:
			err = r.removeFile(p)
		case DanglingNullPtr:
			err = r.update(p, "delete the pointer", func(tx kv.Txn) error {
				return tx.Delete(meta.NullKey(p.Bucket, p.Item))
			})
		case MissingNullPtr:
			err = r.update(p, "recreate the pointer", func(tx kv.Txn) error {
				// The null version is the only record of the key with the
				// "null" version id; point at it.
				var target []byte
				pre := meta.ObjectPrefix(p.Bucket, p.Item)
				it := tx.Seek(pre)
				defer it.Close()
				for ; it.Valid() && bytes.HasPrefix(it.Key(), pre); it.Next() {
					var o meta.Object
					if decodeJSON(it.Value(), &o) == nil && o.VersionID == meta.NullVersionID {
						target = append([]byte(nil), it.Key()[len(pre):]...)
						break
					}
				}
				if target == nil {
					return errors.New("no null version found")
				}
				return tx.Put(meta.NullKey(p.Bucket, p.Item), target)
			})
		case DanglingUploadIdx:
			err = r.update(p, "delete the index entry", func(tx kv.Txn) error {
				return tx.Delete(meta.UploadIDKey(p.Bucket, p.Item))
			})
		case MissingUploadIdx:
			err = r.update(p, "recreate the index entry", func(tx kv.Txn) error {
				var key string
				meta.ScanBucketUploads(tx, p.Bucket, func(u *meta.Upload) bool {
					if u.UploadID == p.Item {
						key = u.Key
						return false
					}
					return true
				})
				if key == "" {
					return errors.New("upload record not found")
				}
				return tx.Put(meta.UploadIDKey(p.Bucket, p.Item), []byte(key))
			})
		case OrphanPartRecords:
			err = r.removeParts(p)
		case PartMissingFile:
			err = r.update(p, "delete the part record", func(tx kv.Txn) error {
				uploadID, num, ok := strings.Cut(p.Item, "/")
				if !ok {
					return errors.New("malformed item")
				}
				var n int
				fmt.Sscanf(num, "%d", &n)
				return tx.Delete(meta.PartKey(p.Bucket, uploadID, n))
			})
		case BucketDeleting:
			err = r.finishDelete(p)
		case RecordWithoutBkt:
			err = r.update(p, "recreate the bucket record", func(tx kv.Txn) error {
				if _, err := meta.GetBucket(tx, p.Bucket); err == nil {
					return nil
				}
				b := &meta.Bucket{Name: p.Bucket, Owner: RootUser, OwnerDisplay: "root", Created: time.Now().UTC(), Ownership: "BucketOwnerEnforced",
					ACL: &meta.ACL{Owner: RootUser, OwnerDisplay: "root", Grants: []meta.Grant{{Grantee: RootUser, GranteeType: "CanonicalUser", DisplayName: "root", Permission: "FULL_CONTROL"}}}}
				return meta.PutBucket(tx, b)
			})
		default:
			continue
		}
		if err != nil {
			return res, fmt.Errorf("%s %s/%s: %w", p.Kind, p.Bucket, p.Item, err)
		}
	}
	return res, nil
}

type repairer struct {
	db   kv.Store
	root string
	dry  bool
	res  *RepairResult
}

func (r *repairer) did(p Problem, what string) {
	r.res.Actions = append(r.res.Actions, Action{Kind: p.Kind, Bucket: p.Bucket, Item: p.Item, What: what})
}

func (r *repairer) update(p Problem, what string, fn func(tx kv.Txn) error) error {
	r.did(p, what)
	if r.dry {
		return nil
	}
	return r.db.Update(fn)
}

func (r *repairer) removeFile(p Problem) error {
	r.did(p, "delete "+p.Item)
	if r.dry {
		return nil
	}
	path := filepath.Join(r.root, filepath.FromSlash(p.Item))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Dir(path)) // the shard directory, if now empty
	return nil
}

func (r *repairer) removeParts(p Problem) error {
	var blobs []string
	err := r.update(p, "delete the part records and their files", func(tx kv.Txn) error {
		pre := meta.PartPrefix(p.Bucket, p.Item)
		var keys [][]byte
		it := tx.Seek(pre)
		for ; it.Valid() && bytes.HasPrefix(it.Key(), pre); it.Next() {
			var part meta.Part
			if decodeJSON(it.Value(), &part) == nil {
				blobs = append(blobs, part.Blob)
			}
			keys = append(keys, append([]byte(nil), it.Key()...))
		}
		it.Close()
		for _, k := range keys {
			if err := tx.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || r.dry {
		return err
	}
	for _, b := range blobs {
		if len(b) < 2 {
			b = "00" + b
		}
		path := filepath.Join(r.root, "data", p.Bucket, b[:2], b)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// finishDelete completes an interrupted bucket deletion. A bucket is only
// ever marked deleting once it is empty, so one that still holds object
// records is not something the server produced: it is restored rather
// than emptied, since restoring loses nothing.
func (r *repairer) finishDelete(p Problem) error {
	var hasObjects bool
	r.db.View(func(tx kv.Txn) error {
		var err error
		hasObjects, err = meta.BucketHasObjects(tx, p.Bucket)
		return err
	})
	if hasObjects {
		return r.update(p, "the bucket still holds objects: deletion cancelled, bucket restored", func(tx kv.Txn) error {
			b, err := meta.GetBucket(tx, p.Bucket)
			if err != nil {
				return nil
			}
			b.Deleting = false
			return meta.PutBucket(tx, b)
		})
	}
	r.did(p, "remove the data directory and the record")
	if r.dry {
		return nil
	}
	if err := os.RemoveAll(filepath.Join(r.root, "data", p.Bucket)); err != nil {
		return err
	}
	return r.db.Update(func(tx kv.Txn) error {
		b, err := meta.GetBucket(tx, p.Bucket)
		if err != nil || !b.Deleting {
			return nil
		}
		return meta.DeleteBucket(tx, p.Bucket)
	})
}
