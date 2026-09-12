package meta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"gitlab.com/Birdsall/opens3/internal/kv"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = kv.ErrNotFound

// ErrExists is returned when creating a record that already exists.
var ErrExists = errors.New("meta: already exists")

func encode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("meta: encode: %v", err))
	}
	return b
}

func decode(b []byte, v any) error { return json.Unmarshal(b, v) }

// --- Buckets -------------------------------------------------------------

// GetBucket reads a bucket record.
func GetBucket(tx kv.Txn, name string) (*Bucket, error) {
	b, err := tx.Get(BucketKey(name))
	if err != nil {
		return nil, err
	}
	var bk Bucket
	if err := decode(b, &bk); err != nil {
		return nil, err
	}
	return &bk, nil
}

// CreateBucket writes a new bucket record, failing if it exists.
func CreateBucket(tx kv.Txn, bk *Bucket) error {
	if _, err := tx.Get(BucketKey(bk.Name)); err == nil {
		return ErrExists
	} else if !errors.Is(err, kv.ErrNotFound) {
		return err
	}
	return tx.Put(BucketKey(bk.Name), encode(bk))
}

// PutBucket overwrites a bucket record.
func PutBucket(tx kv.Txn, bk *Bucket) error { return tx.Put(BucketKey(bk.Name), encode(bk)) }

// DeleteBucket removes the bucket record only.
func DeleteBucket(tx kv.Txn, name string) error { return tx.Delete(BucketKey(name)) }

// ListBuckets returns all bucket records in name order.
func ListBuckets(tx kv.Txn) ([]*Bucket, error) {
	var out []*Bucket
	p := BucketPrefix()
	it := tx.Seek(p)
	defer it.Close()
	for ; it.Valid() && bytes.HasPrefix(it.Key(), p); it.Next() {
		var bk Bucket
		if err := decode(it.Value(), &bk); err != nil {
			return nil, err
		}
		out = append(out, &bk)
	}
	return out, nil
}

// BucketHasObjects reports whether any object version or upload exists.
func BucketHasObjects(tx kv.Txn, name string) (bool, error) {
	for _, p := range [][]byte{ObjectBucketPrefix(name), UploadBucketPrefix(name)} {
		it := tx.Seek(p)
		has := it.Valid() && bytes.HasPrefix(it.Key(), p)
		it.Close()
		if has {
			return true, nil
		}
	}
	return false, nil
}

// --- Objects -------------------------------------------------------------

// GetLatest returns the newest version record of key (may be a delete
// marker), or ErrNotFound.
func GetLatest(tx kv.Txn, bucket, key string) (*Object, error) {
	p := ObjectPrefix(bucket, key)
	it := tx.Seek(p)
	defer it.Close()
	if !it.Valid() || !bytes.HasPrefix(it.Key(), p) {
		return nil, ErrNotFound
	}
	var o Object
	if err := decode(it.Value(), &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// GetVersion returns a specific version. versionID may be NullVersionID.
func GetVersion(tx kv.Txn, bucket, key, versionID string) (*Object, error) {
	var seq uint64
	if versionID == NullVersionID {
		vk, err := tx.Get(NullKey(bucket, key))
		if err != nil {
			return nil, err
		}
		b, err := tx.Get(append(ObjectPrefix(bucket, key), vk...))
		if err != nil {
			return nil, err
		}
		var o Object
		if err := decode(b, &o); err != nil {
			return nil, err
		}
		return &o, nil
	}
	seq, err := SeqFromVersionID(versionID)
	if err != nil {
		return nil, ErrNotFound
	}
	b, err := tx.Get(ObjectKey(bucket, key, seq))
	if err != nil {
		return nil, err
	}
	var o Object
	if err := decode(b, &o); err != nil {
		return nil, err
	}
	if o.VersionID != versionID {
		return nil, ErrNotFound
	}
	return &o, nil
}

// PutObject writes a version record. For the null version it also updates
// the null pointer and returns the previous null version (which the caller
// must delete along with its blobs), if any.
func PutObject(tx kv.Txn, o *Object) (prevNull *Object, err error) {
	if o.VersionID == NullVersionID {
		nk := NullKey(o.Bucket, o.Key)
		if vk, err := tx.Get(nk); err == nil {
			pk := append(ObjectPrefix(o.Bucket, o.Key), vk...)
			if b, err := tx.Get(pk); err == nil {
				var prev Object
				if err := decode(b, &prev); err == nil {
					prevNull = &prev
				}
				if err := tx.Delete(pk); err != nil {
					return nil, err
				}
			}
		} else if !errors.Is(err, kv.ErrNotFound) {
			return nil, err
		}
		if err := tx.Put(nk, []byte(verKey(o.Seq))); err != nil {
			return nil, err
		}
	}
	return prevNull, tx.Put(ObjectKey(o.Bucket, o.Key, o.Seq), encode(o))
}

// DeleteVersion removes one version record (and the null pointer if it was
// the null version).
func DeleteVersion(tx kv.Txn, o *Object) error {
	if o.VersionID == NullVersionID {
		if err := tx.Delete(NullKey(o.Bucket, o.Key)); err != nil && !errors.Is(err, kv.ErrNotFound) {
			return err
		}
	}
	return tx.Delete(ObjectKey(o.Bucket, o.Key, o.Seq))
}

// ListOptions controls object listing.
type ListOptions struct {
	Prefix     string
	Delimiter  string
	StartAfter string // exclusive; for ListObjectsV2 start-after / continuation
	// For version listing: resume after (key, versionID). VersionMarker ""
	// with KeyMarker set means "start after all versions of KeyMarker".
	KeyMarker     string
	VersionMarker string
	MaxKeys       int
}

// ListEntry is one listing result: an object version or a common prefix.
type ListEntry struct {
	Object       *Object
	CommonPrefix string
	IsLatest     bool
}

// ListResult is a page of listing results.
type ListResult struct {
	Entries     []ListEntry
	IsTruncated bool
	// NextKey / NextVersion identify the last returned entry for markers.
	NextKey     string
	NextVersion string
}

// ListObjects lists the latest version of each key (skipping keys whose
// latest is a delete marker), collapsing on Delimiter. Complexity is
// proportional to the number of results.
func ListObjects(tx kv.Txn, bucket string, opt ListOptions) (*ListResult, error) {
	return list(tx, bucket, opt, false)
}

// ListVersions lists every version and delete marker.
func ListVersions(tx kv.Txn, bucket string, opt ListOptions) (*ListResult, error) {
	return list(tx, bucket, opt, true)
}

func list(tx kv.Txn, bucket string, opt ListOptions, versions bool) (*ListResult, error) {
	if opt.MaxKeys <= 0 {
		opt.MaxKeys = 1000
	}
	res := &ListResult{}
	bucketPrefix := ObjectBucketPrefix(bucket)
	scan := ObjectScanPrefix(bucket, opt.Prefix)

	start := scan
	if !versions && opt.StartAfter != "" && opt.StartAfter >= opt.Prefix {
		start = afterKey(bucket, opt.StartAfter, opt.Delimiter)
		if start == nil {
			return res, nil
		}
	}
	if versions && opt.KeyMarker != "" && opt.KeyMarker >= opt.Prefix {
		if opt.VersionMarker == "" {
			start = afterKey(bucket, opt.KeyMarker, opt.Delimiter)
		} else if seq, err := SeqFromVersionID(opt.VersionMarker); err == nil {
			s := ObjectKey(bucket, opt.KeyMarker, seq)
			start = append(s, 0) // strictly after the marker record
		} else if opt.VersionMarker == NullVersionID {
			if vk, err := tx.Get(NullKey(bucket, opt.KeyMarker)); err == nil {
				start = append(append(ObjectPrefix(bucket, opt.KeyMarker), vk...), 0)
			} else {
				start = kv.PrefixSuccessor(ObjectPrefix(bucket, opt.KeyMarker))
			}
		}
		if start == nil {
			return res, nil
		}
	}

	it := tx.Seek(start)
	defer it.Close()
	var lastKey string
	for it.Valid() && bytes.HasPrefix(it.Key(), scan) {
		_, key, _, ok := SplitObjectKey(it.Key())
		if !ok {
			it.Next()
			continue
		}
		// Delimiter collapsing.
		if opt.Delimiter != "" {
			rest := key[len(opt.Prefix):]
			if i := indexOf(rest, opt.Delimiter); i >= 0 {
				cp := opt.Prefix + rest[:i+len(opt.Delimiter)]
				next := kv.PrefixSuccessor(ObjectScanPrefix(bucket, cp))
				// Like AWS, a common prefix whose keys are all delete
				// markers is not listed (versions listing shows markers, so
				// it is listed there).
				if versions || hasLiveKey(tx, bucket, cp) {
					if len(res.Entries) >= opt.MaxKeys {
						res.IsTruncated = true
						return res, nil
					}
					res.Entries = append(res.Entries, ListEntry{CommonPrefix: cp})
					res.NextKey, res.NextVersion = cp, ""
				}
				if next == nil || !bytes.HasPrefix(next, bucketPrefix) {
					return res, nil
				}
				it.Seek(next)
				continue
			}
		}
		var o Object
		if err := decode(it.Value(), &o); err != nil {
			return nil, err
		}
		isLatest := key != lastKey
		lastKey = key
		if versions {
			if len(res.Entries) >= opt.MaxKeys {
				res.IsTruncated = true
				return res, nil
			}
			res.Entries = append(res.Entries, ListEntry{Object: &o, IsLatest: isLatest})
			res.NextKey, res.NextVersion = o.Key, o.VersionID
			it.Next()
			continue
		}
		// Latest-only: this is the first (newest) record for key.
		if !o.DeleteMarker {
			if len(res.Entries) >= opt.MaxKeys {
				res.IsTruncated = true
				return res, nil
			}
			res.Entries = append(res.Entries, ListEntry{Object: &o, IsLatest: true})
			res.NextKey = o.Key
		}
		// Skip the remaining versions of this key.
		next := kv.PrefixSuccessor(ObjectPrefix(bucket, key))
		if next == nil {
			break
		}
		it.Seek(next)
	}
	return res, nil
}

// hasLiveKey reports whether any key under prefix has a latest version that
// is not a delete marker.
func hasLiveKey(tx kv.Txn, bucket, prefix string) bool {
	scan := ObjectScanPrefix(bucket, prefix)
	it := tx.Seek(scan)
	defer it.Close()
	for it.Valid() && bytes.HasPrefix(it.Key(), scan) {
		_, key, _, ok := SplitObjectKey(it.Key())
		if !ok {
			it.Next()
			continue
		}
		var o Object
		if decode(it.Value(), &o) == nil && !o.DeleteMarker {
			return true
		}
		next := kv.PrefixSuccessor(ObjectPrefix(bucket, key))
		if next == nil {
			return false
		}
		it.Seek(next)
	}
	return false
}

func indexOf(s, sub string) int { return bytes.Index([]byte(s), []byte(sub)) }

// afterKey returns the first KV key strictly after every record of marker.
// If marker is a common prefix (ends with the delimiter) the whole subtree
// is skipped; otherwise only the versions of that exact key are skipped, so
// that a marker "a" still yields "ab".
func afterKey(bucket, marker, delimiter string) []byte {
	if delimiter != "" && len(marker) >= len(delimiter) && marker[len(marker)-len(delimiter):] == delimiter {
		return kv.PrefixSuccessor(ObjectScanPrefix(bucket, marker))
	}
	return kv.PrefixSuccessor(ObjectPrefix(bucket, marker))
}

// --- Multipart uploads ---------------------------------------------------

// PutUpload creates an upload record and its reverse index.
func PutUpload(tx kv.Txn, u *Upload) error {
	if err := tx.Put(UploadKey(u.Bucket, u.Key, u.UploadID), encode(u)); err != nil {
		return err
	}
	return tx.Put(UploadIDKey(u.Bucket, u.UploadID), []byte(u.Key))
}

// GetUpload finds an upload by ID. If key is non-empty it must match.
func GetUpload(tx kv.Txn, bucket, key, uploadID string) (*Upload, error) {
	k, err := tx.Get(UploadIDKey(bucket, uploadID))
	if err != nil {
		return nil, err
	}
	if key != "" && string(k) != key {
		return nil, ErrNotFound
	}
	b, err := tx.Get(UploadKey(bucket, string(k), uploadID))
	if err != nil {
		return nil, err
	}
	var u Upload
	if err := decode(b, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteUpload removes the upload record, its index and all part records.
// It returns the parts so the caller can delete their blobs.
func DeleteUpload(tx kv.Txn, u *Upload) ([]Part, error) {
	parts, err := ListParts(tx, u.Bucket, u.UploadID, 0, 0)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		if err := tx.Delete(PartKey(u.Bucket, u.UploadID, p.Number)); err != nil {
			return nil, err
		}
	}
	if err := tx.Delete(UploadIDKey(u.Bucket, u.UploadID)); err != nil {
		return nil, err
	}
	return parts, tx.Delete(UploadKey(u.Bucket, u.Key, u.UploadID))
}

// UploadListResult is a page of uploads.
type UploadListResult struct {
	Uploads      []*Upload
	Prefixes     []string
	IsTruncated  bool
	NextKey      string
	NextUploadID string
}

// ListUploads lists in-progress uploads in (key, uploadID) order.
func ListUploads(tx kv.Txn, bucket string, opt ListOptions) (*UploadListResult, error) {
	if opt.MaxKeys <= 0 {
		opt.MaxKeys = 1000
	}
	res := &UploadListResult{}
	scan := UploadScanPrefix(bucket, opt.Prefix)
	start := scan
	if opt.KeyMarker != "" {
		if opt.VersionMarker != "" {
			start = append(UploadKey(bucket, opt.KeyMarker, opt.VersionMarker), 0)
		} else {
			start = kv.PrefixSuccessor([]byte(nsUpload + bucket + "/" + opt.KeyMarker + sep))
		}
	}
	it := tx.Seek(start)
	defer it.Close()
	for it.Valid() && bytes.HasPrefix(it.Key(), scan) {
		key, uid, ok := SplitUploadKey(it.Key(), bucket)
		if !ok {
			it.Next()
			continue
		}
		if opt.Delimiter != "" {
			rest := key[len(opt.Prefix):]
			if i := indexOf(rest, opt.Delimiter); i >= 0 {
				cp := opt.Prefix + rest[:i+len(opt.Delimiter)]
				if len(res.Uploads)+len(res.Prefixes) >= opt.MaxKeys {
					res.IsTruncated = true
					return res, nil
				}
				res.Prefixes = append(res.Prefixes, cp)
				res.NextKey, res.NextUploadID = cp, ""
				next := kv.PrefixSuccessor(UploadScanPrefix(bucket, cp))
				if next == nil {
					return res, nil
				}
				it.Seek(next)
				continue
			}
		}
		if len(res.Uploads)+len(res.Prefixes) >= opt.MaxKeys {
			res.IsTruncated = true
			return res, nil
		}
		var u Upload
		if err := decode(it.Value(), &u); err != nil {
			return nil, err
		}
		res.Uploads = append(res.Uploads, &u)
		res.NextKey, res.NextUploadID = key, uid
		it.Next()
	}
	return res, nil
}

// PutPart writes (or replaces) a part record, returning the replaced part
// if any so its blob can be deleted.
func PutPart(tx kv.Txn, bucket, uploadID string, p Part) (*Part, error) {
	k := PartKey(bucket, uploadID, p.Number)
	var old *Part
	if b, err := tx.Get(k); err == nil {
		var prev Part
		if decode(b, &prev) == nil {
			old = &prev
		}
	}
	return old, tx.Put(k, encode(&p))
}

// ListParts returns parts with number > afterPart, at most max (0 = all).
func ListParts(tx kv.Txn, bucket, uploadID string, afterPart, max int) ([]Part, error) {
	p := PartPrefix(bucket, uploadID)
	start := p
	if afterPart > 0 {
		start = append(PartKey(bucket, uploadID, afterPart), 0)
	}
	it := tx.Seek(start)
	defer it.Close()
	var out []Part
	for ; it.Valid() && bytes.HasPrefix(it.Key(), p); it.Next() {
		if max > 0 && len(out) >= max {
			break
		}
		var pt Part
		if err := decode(it.Value(), &pt); err != nil {
			return nil, err
		}
		out = append(out, pt)
	}
	return out, nil
}

// ScanBucketObjects calls fn for every version record in a bucket in key
// order. Returning false stops the scan.
func ScanBucketObjects(tx kv.Txn, bucket string, fn func(o *Object) bool) error {
	p := ObjectBucketPrefix(bucket)
	it := tx.Seek(p)
	defer it.Close()
	for ; it.Valid() && bytes.HasPrefix(it.Key(), p); it.Next() {
		var o Object
		if err := decode(it.Value(), &o); err != nil {
			return err
		}
		if !fn(&o) {
			return nil
		}
	}
	return nil
}

// ScanBucketUploads calls fn for every upload in a bucket.
func ScanBucketUploads(tx kv.Txn, bucket string, fn func(u *Upload) bool) error {
	p := UploadBucketPrefix(bucket)
	it := tx.Seek(p)
	defer it.Close()
	for ; it.Valid() && bytes.HasPrefix(it.Key(), p); it.Next() {
		var u Upload
		if err := decode(it.Value(), &u); err != nil {
			return err
		}
		if !fn(&u) {
			return nil
		}
	}
	return nil
}
