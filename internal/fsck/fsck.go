// Package fsck checks a data directory against its metadata and repairs
// what can be repaired safely. It works offline: the metadata database
// and the object files are read directly (`opens3 fsck`, with the server
// stopped or on a snapshot).
//
// Check walks every bucket, object version, multipart upload and part
// record and confirms the files they reference exist with the recorded
// size (and, on request, content), then walks the data directory the
// other way and lists files no record references, and verifies the
// index invariants (null-version pointers, upload indexes, buckets
// left in the deleting state).
//
// Repair fixes only what cannot lose data: orphan files, dangling
// pointers and indexes, part records without files, buckets stuck in
// deletion, records whose bucket record is gone (the bucket record is
// recreated). An object whose file is missing is reported and left in
// place, so that a backup can be restored over it.
package fsck

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/sse"
)

// Problem kinds, in report order.
const (
	MissingFile       = "missing object file"           // an object version references a file that does not exist
	SizeMismatch      = "object file size mismatch"     // the file exists with a different size
	ContentMismatch   = "object content mismatch"       // --verify: the bytes do not hash to the recorded ETag
	Unverifiable      = "object not verifiable"         // --verify: encrypted with a customer key, or no master key
	UndecodableRecord = "undecodable record"            // a metadata record that does not parse
	RecordWithoutBkt  = "records without a bucket"      // objects or uploads whose bucket record is gone
	OrphanFile        = "orphan file"                   // a file in data/ no record references
	DanglingNullPtr   = "dangling null-version pointer" // n/ points at a version that does not exist
	MissingNullPtr    = "missing null-version pointer"  // a null version without its n/ pointer
	DanglingUploadIdx = "dangling upload index"         // ui/ entry without its upload record
	MissingUploadIdx  = "missing upload index"          // upload record without its ui/ entry
	OrphanPartRecords = "orphan part records"           // p/ records whose upload does not exist
	PartMissingFile   = "upload part without file"      // a part record whose file is missing
	BucketDeleting    = "bucket left in deletion"       // marked deleting; removal was interrupted
	TempFile          = "leftover temporary file"       // tmp/ content (removed at server start)
)

var kindOrder = []string{MissingFile, SizeMismatch, ContentMismatch, Unverifiable, UndecodableRecord, RecordWithoutBkt,
	OrphanFile, DanglingNullPtr, MissingNullPtr, DanglingUploadIdx, MissingUploadIdx, OrphanPartRecords, PartMissingFile, BucketDeleting, TempFile}

// repairs says what Repair does about each kind ("" = nothing).
var repairs = map[string]string{
	RecordWithoutBkt:  "recreate the bucket record (owner root, private)",
	OrphanFile:        "delete the file",
	DanglingNullPtr:   "delete the pointer",
	MissingNullPtr:    "recreate the pointer",
	DanglingUploadIdx: "delete the index entry",
	MissingUploadIdx:  "recreate the index entry",
	OrphanPartRecords: "delete the records and their files",
	PartMissingFile:   "delete the part record (the client re-uploads the part)",
	BucketDeleting:    "finish the deletion",
	TempFile:          "delete the file",
	MissingFile:       "none: restore the object from a backup, or delete it",
	SizeMismatch:      "none: restore the object from a backup, or delete it",
	ContentMismatch:   "none: restore the object from a backup, or delete it",
	Unverifiable:      "none (informational)",
	UndecodableRecord: "none: inspect the record",
}

// Repairable reports whether Repair acts on a kind.
func Repairable(kind string) bool {
	return !strings.HasPrefix(repairs[kind], "none")
}

// RepairAction describes what Repair does about a kind.
func RepairAction(kind string) string { return repairs[kind] }

// Problem is one finding.
type Problem struct {
	Kind   string
	Bucket string
	Item   string // key (and version), upload id, or file path
	Detail string
}

// Report is the outcome of a check.
type Report struct {
	Root     string
	Buckets  int
	Objects  int // object versions (delete markers included)
	Uploads  int
	Parts    int
	Files    int
	Bytes    int64
	Verified int // objects whose content was hashed
	Problems []Problem
	Started  time.Time
	Duration time.Duration
}

// Count returns the number of problems of a kind.
func (r *Report) Count(kind string) int {
	n := 0
	for _, p := range r.Problems {
		if p.Kind == kind {
			n++
		}
	}
	return n
}

// Kinds lists the problem kinds present, in report order.
func (r *Report) Kinds() []string {
	seen := map[string]bool{}
	for _, p := range r.Problems {
		seen[p.Kind] = true
	}
	var out []string
	for _, k := range kindOrder {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// Repairable counts the problems Repair would act on.
func (r *Report) Repairable() int {
	n := 0
	for _, p := range r.Problems {
		if Repairable(p.Kind) {
			n++
		}
	}
	return n
}

// Options control a check.
type Options struct {
	// Bucket restricts the check to one bucket ("" = all).
	Bucket string
	// Verify reads every object file and compares its content with the
	// recorded ETag; needs Objects (for decryption) or only plaintext
	// objects are verified.
	Verify bool
	// Objects, when set, reads objects through the service (decrypting
	// SSE-S3 and SSE-KMS) for Verify.
	Objects *object.Service
	// Progress, if set, receives a line per bucket.
	Progress func(string)
}

// Check runs a check over the data directory at root using db.
func Check(ctx context.Context, db kv.Store, root string, opts Options) (*Report, error) {
	rep := &Report{Root: root, Started: time.Now()}
	c := &checker{db: db, root: root, opts: opts, rep: rep, referenced: map[string]map[string]bool{}}
	if err := c.run(ctx); err != nil {
		return rep, err
	}
	rep.Duration = time.Since(rep.Started)
	return rep, nil
}

type checker struct {
	db   kv.Store
	root string
	opts Options
	rep  *Report
	// referenced[bucket][blob] is every file some record points at.
	referenced map[string]map[string]bool
	// per-bucket counters for the progress line
	bucketObjects, bucketUploads, bucketFiles int
}

func decodeJSON(b []byte, v any) error { return json.Unmarshal(b, v) }

func (c *checker) problem(kind, bucket, item, detail string) {
	c.rep.Problems = append(c.rep.Problems, Problem{Kind: kind, Bucket: bucket, Item: item, Detail: detail})
}

func (c *checker) ref(bucket, blob string) {
	m := c.referenced[bucket]
	if m == nil {
		m = map[string]bool{}
		c.referenced[bucket] = m
	}
	m[blob] = true
}

func (c *checker) filePath(bucket, id string) string {
	if len(id) < 2 {
		id = "00" + id
	}
	return filepath.Join(c.root, "data", bucket, id[:2], id)
}

func (c *checker) run(ctx context.Context) error {
	buckets := map[string]*meta.Bucket{}
	err := c.db.View(func(tx kv.Txn) error {
		all, err := meta.ListBuckets(tx)
		if err != nil {
			return err
		}
		for _, b := range all {
			if c.opts.Bucket == "" || b.Name == c.opts.Bucket {
				buckets[b.Name] = b
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Bucket names that have records but no bucket record.
	for _, name := range c.bucketsWithRecords() {
		if _, ok := buckets[name]; !ok && (c.opts.Bucket == "" || name == c.opts.Bucket) {
			c.problem(RecordWithoutBkt, name, name, "object or upload records exist but the bucket record is missing")
			buckets[name] = nil
		}
	}
	names := make([]string, 0, len(buckets))
	for n := range buckets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		b := buckets[name]
		if b != nil {
			c.rep.Buckets++
			if b.Deleting {
				c.problem(BucketDeleting, name, name, "the record is marked deleting; the data directory may remain")
			}
		}
		if err := c.checkObjects(ctx, name); err != nil {
			return err
		}
		if err := c.checkUploads(name); err != nil {
			return err
		}
		if err := c.checkFiles(name); err != nil {
			return err
		}
		if c.opts.Progress != nil {
			c.opts.Progress(fmt.Sprintf("%s: %d versions, %d uploads, %d files", name, c.bucketObjects, c.bucketUploads, c.bucketFiles))
		}
		c.bucketObjects, c.bucketUploads, c.bucketFiles = 0, 0, 0
	}
	if c.opts.Bucket == "" {
		c.checkStrayDirs(names)
		c.checkTemp()
	}
	return nil
}

// bucketsWithRecords scans the object and upload namespaces for bucket
// names, so records without a bucket record are found.
func (c *checker) bucketsWithRecords() []string {
	seen := map[string]bool{}
	c.db.View(func(tx kv.Txn) error {
		for _, ns := range []string{meta.ObjectNamespace, meta.UploadNamespace} {
			it := tx.Seek([]byte(ns))
			for it.Valid() && bytes.HasPrefix(it.Key(), []byte(ns)) {
				rest := string(it.Key()[len(ns):])
				i := strings.IndexByte(rest, '/')
				if i < 0 {
					it.Next()
					continue
				}
				name := rest[:i]
				seen[name] = true
				// Skip to the next bucket's records.
				next := kv.PrefixSuccessor([]byte(ns + name + "/"))
				if next == nil {
					break
				}
				it.Seek(next)
			}
			it.Close()
		}
		return nil
	})
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (c *checker) checkObjects(ctx context.Context, bucket string) error {
	var toVerify []*meta.Object
	nullPointers := map[string][]byte{} // key -> version key
	err := c.db.View(func(tx kv.Txn) error {
		// Null-version pointers.
		p := meta.NullBucketPrefix(bucket)
		it := tx.Seek(p)
		for ; it.Valid() && bytes.HasPrefix(it.Key(), p); it.Next() {
			nullPointers[string(it.Key()[len(p):])] = append([]byte(nil), it.Value()...)
		}
		it.Close()
		// Object versions.
		op := meta.ObjectBucketPrefix(bucket)
		it = tx.Seek(op)
		defer it.Close()
		for ; it.Valid() && bytes.HasPrefix(it.Key(), op); it.Next() {
			c.rep.Objects++
			c.bucketObjects++
			var o meta.Object
			if err := decodeJSON(it.Value(), &o); err != nil {
				c.problem(UndecodableRecord, bucket, string(it.Key()), err.Error())
				continue
			}
			_, key, seq, ok := meta.SplitObjectKey(it.Key())
			if !ok {
				c.problem(UndecodableRecord, bucket, string(it.Key()), "malformed object record key")
				continue
			}
			if o.VersionID == meta.NullVersionID {
				vk, has := nullPointers[key]
				if !has || string(vk) != verKeyOf(seq) {
					c.problem(MissingNullPtr, bucket, key, "the null version has no pointer (or the pointer names another version)")
				}
				delete(nullPointers, key)
			}
			if o.DeleteMarker {
				continue
			}
			damaged := false
			for _, part := range o.Parts {
				c.ref(bucket, part.Blob)
				c.rep.Bytes += part.Size
				st, err := os.Stat(c.filePath(bucket, part.Blob))
				switch {
				case errors.Is(err, fs.ErrNotExist):
					damaged = true
					c.problem(MissingFile, bucket, item(&o), "part "+itoa(part.Number)+" file "+part.Blob+" is missing")
				case err != nil:
					damaged = true
					c.problem(MissingFile, bucket, item(&o), err.Error())
				case st.Size() != storedSize(&o, part):
					damaged = true
					c.problem(SizeMismatch, bucket, item(&o), fmt.Sprintf("part %d file is %d bytes, record says %d", part.Number, st.Size(), storedSize(&o, part)))
				}
			}
			if c.opts.Verify && len(o.Parts) > 0 && !damaged {
				oc := o
				toVerify = append(toVerify, &oc)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key := range nullPointers {
		c.problem(DanglingNullPtr, bucket, key, "the pointer names a version that does not exist")
	}
	for _, o := range toVerify {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.verifyObject(ctx, o)
	}
	return nil
}

// storedSize is the on-disk size of a part: the plaintext size for plain
// objects, plaintext plus the per-chunk authentication tags when
// encrypted (see internal/sse).
func storedSize(o *meta.Object, p meta.Part) int64 {
	if o.SSE == nil {
		return p.Size
	}
	return sse.EncryptedSize(p.Size)
}

// verifyObject hashes each part and compares with the part's ETag.
// Plaintext parts are read straight from their files; encrypted ones go
// through the object service, which decrypts.
func (c *checker) verifyObject(ctx context.Context, o *meta.Object) {
	encrypted := o.SSE != nil
	if encrypted && o.SSE.Type == "SSE-C" {
		c.problem(Unverifiable, o.Bucket, item(o), "encrypted with a customer-provided key the server does not hold")
		return
	}
	if encrypted && c.opts.Objects == nil {
		c.problem(Unverifiable, o.Bucket, item(o), "encrypted, and no master key is available to decrypt")
		return
	}
	for i, part := range o.Parts {
		var rc io.ReadCloser
		if encrypted {
			res, err := c.opts.Objects.GetObject(ctx, object.GetInput{Bucket: o.Bucket, Key: o.Key, VersionID: o.VersionID, PartNumber: i + 1})
			if err != nil {
				if strings.Contains(err.Error(), "corrupt") {
					c.problem(ContentMismatch, o.Bucket, item(o), fmt.Sprintf("part %d does not decrypt: %v", part.Number, err))
				} else {
					c.problem(Unverifiable, o.Bucket, item(o), fmt.Sprintf("part %d cannot be read through the store: %v", part.Number, err))
				}
				return
			}
			rc = res.Body
		} else {
			f, err := os.Open(c.filePath(o.Bucket, part.Blob))
			if err != nil {
				return // reported as missing already
			}
			rc = f
		}
		h := md5.New()
		_, err := io.Copy(h, rc)
		rc.Close()
		if err != nil {
			c.problem(ContentMismatch, o.Bucket, item(o), fmt.Sprintf("part %d cannot be read: %v", part.Number, err))
			return
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != part.ETag {
			c.problem(ContentMismatch, o.Bucket, item(o), fmt.Sprintf("part %d hashes to %s, record says %s", part.Number, got, part.ETag))
			return
		}
	}
	c.rep.Verified++
}

func (c *checker) checkUploads(bucket string) error {
	return c.db.View(func(tx kv.Txn) error {
		index := map[string]string{} // uploadID -> key from ui/
		ip := []byte("ui/" + bucket + "/")
		it := tx.Seek(ip)
		for ; it.Valid() && bytes.HasPrefix(it.Key(), ip); it.Next() {
			index[string(it.Key()[len(ip):])] = string(it.Value())
		}
		it.Close()
		uploads := map[string]bool{}
		up := meta.UploadBucketPrefix(bucket)
		it = tx.Seek(up)
		for ; it.Valid() && bytes.HasPrefix(it.Key(), up); it.Next() {
			c.rep.Uploads++
			c.bucketUploads++
			var u meta.Upload
			if err := decodeJSON(it.Value(), &u); err != nil {
				c.problem(UndecodableRecord, bucket, string(it.Key()), err.Error())
				continue
			}
			uploads[u.UploadID] = true
			if k, ok := index[u.UploadID]; !ok || k != u.Key {
				c.problem(MissingUploadIdx, bucket, u.UploadID, "upload "+u.Key+" has no index entry")
			}
			delete(index, u.UploadID)
		}
		it.Close()
		for id, key := range index {
			c.problem(DanglingUploadIdx, bucket, id, "index names key "+key+" but the upload record is gone")
		}
		// Parts.
		pp := []byte("p/" + bucket + "/")
		it = tx.Seek(pp)
		defer it.Close()
		orphans := map[string]int{}
		for ; it.Valid() && bytes.HasPrefix(it.Key(), pp); it.Next() {
			c.rep.Parts++
			rest := string(it.Key()[len(pp):])
			i := strings.IndexByte(rest, '/')
			if i < 0 {
				c.problem(UndecodableRecord, bucket, string(it.Key()), "malformed part record key")
				continue
			}
			uploadID := rest[:i]
			var p meta.Part
			if err := decodeJSON(it.Value(), &p); err != nil {
				c.problem(UndecodableRecord, bucket, string(it.Key()), err.Error())
				continue
			}
			c.ref(bucket, p.Blob)
			if !uploads[uploadID] {
				orphans[uploadID]++
				continue
			}
			if _, err := os.Stat(c.filePath(bucket, p.Blob)); errors.Is(err, fs.ErrNotExist) {
				c.problem(PartMissingFile, bucket, uploadID+"/"+itoa(p.Number), "file "+p.Blob+" is missing")
			}
		}
		for id, n := range orphans {
			c.problem(OrphanPartRecords, bucket, id, fmt.Sprintf("%d part records for an upload that does not exist", n))
		}
		return nil
	})
}

func (c *checker) checkFiles(bucket string) error {
	dir := filepath.Join(c.root, "data", bucket)
	refs := c.referenced[bucket]
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		c.rep.Files++
		c.bucketFiles++
		if !refs[d.Name()] {
			rel, _ := filepath.Rel(c.root, path)
			c.problem(OrphanFile, bucket, rel, "no object or upload part references it")
		}
		return nil
	})
}

// checkStrayDirs reports data/<name> directories with no bucket at all.
func (c *checker) checkStrayDirs(buckets []string) {
	known := map[string]bool{}
	for _, b := range buckets {
		known[b] = true
	}
	entries, err := os.ReadDir(filepath.Join(c.root, "data"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || known[e.Name()] {
			continue
		}
		filepath.WalkDir(filepath.Join(c.root, "data", e.Name()), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			c.rep.Files++
			rel, _ := filepath.Rel(c.root, path)
			c.problem(OrphanFile, e.Name(), rel, "the bucket does not exist")
			return nil
		})
	}
}

func (c *checker) checkTemp() {
	entries, _ := os.ReadDir(filepath.Join(c.root, "tmp"))
	for _, e := range entries {
		c.problem(TempFile, "", filepath.Join("tmp", e.Name()), "an interrupted write; the server removes these at start")
	}
}

func item(o *meta.Object) string {
	if o.VersionID == meta.NullVersionID {
		return o.Key
	}
	return o.Key + " (version " + o.VersionID + ")"
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

// verKeyOf mirrors meta's version-key encoding.
func verKeyOf(seq uint64) string { return fmt.Sprintf("%016x", ^seq) }

// RootUser is the owner recorded on bucket records recreated by Repair.
var RootUser = iam.CanonicalID("root")
