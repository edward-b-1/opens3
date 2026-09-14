package fsck

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
)

// ExportOptions control an export.
type ExportOptions struct {
	Bucket      string // "" = every bucket
	Prefix      string
	AllVersions bool // every version, not only the current one
	Overwrite   bool // replace files already in the output directory
	Progress    func(string)
}

// ExportEntry is one manifest line.
type ExportEntry struct {
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	VersionID    string            `json:"versionId,omitempty"`
	Current      bool              `json:"current"`
	Path         string            `json:"path,omitempty"` // relative to the output directory
	Size         int64             `json:"size"`
	ETag         string            `json:"etag"`
	Checksum     *ExportChecksum   `json:"checksum,omitempty"`
	ContentType  string            `json:"contentType,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Tags         []meta.Tag        `json:"tags,omitempty"`
	LastModified time.Time         `json:"lastModified"`
	Encryption   string            `json:"encryption,omitempty"`
	Exported     bool              `json:"exported"`
	Reason       string            `json:"reason,omitempty"` // why not exported
}

// ExportChecksum is an object's additional checksum in the manifest.
type ExportChecksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"` // base64
	Type      string `json:"type"`  // FULL_OBJECT | COMPOSITE
}

// ExportResult summarises an export.
type ExportResult struct {
	Buckets, Exported, Skipped int
	Bytes                      int64
	Manifest                   string
}

// Export writes objects to plain files under out: <bucket>/<key> for the
// current version (or, with AllVersions, <key>@<versionId> for older
// versions), decrypting SSE-S3 and SSE-KMS through the object service.
// Delete markers and SSE-C objects (the server holds no key) are recorded
// in the manifest but not written. Keys that cannot be used as file paths
// (".." segments, absolute) go under <bucket>/_unsafe/<hex of key>.
func Export(ctx context.Context, svc *object.Service, out string, opts ExportOptions) (*ExportResult, error) {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}
	res := &ExportResult{Manifest: filepath.Join(out, "manifest.jsonl")}
	mf, err := os.OpenFile(res.Manifest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer mf.Close()
	enc := json.NewEncoder(mf)
	buckets, err := svc.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range buckets {
		if opts.Bucket != "" && b.Name != opts.Bucket {
			continue
		}
		res.Buckets++
		n := 0
		err := walkVersions(ctx, svc, b.Name, opts, func(v *meta.Object, current bool) error {
			n++
			e, err := exportOne(ctx, svc, out, v, current, opts)
			if err != nil {
				return err
			}
			if e.Exported {
				res.Exported++
				res.Bytes += e.Size
			} else {
				res.Skipped++
			}
			return enc.Encode(e)
		})
		if err != nil {
			return res, fmt.Errorf("bucket %s: %w", b.Name, err)
		}
		if opts.Progress != nil {
			opts.Progress(fmt.Sprintf("%s: %d objects", b.Name, n))
		}
	}
	if opts.Bucket != "" && res.Buckets == 0 {
		return res, fmt.Errorf("bucket %q does not exist", opts.Bucket)
	}
	return res, nil
}

// walkVersions calls fn for the current version of each key (and, with
// AllVersions, every version), pages of 1000.
func walkVersions(ctx context.Context, svc *object.Service, bucket string, opts ExportOptions, fn func(*meta.Object, bool) error) error {
	if !opts.AllVersions {
		after := ""
		for {
			lr, err := svc.ListObjects(ctx, bucket, meta.ListOptions{Prefix: opts.Prefix, StartAfter: after, MaxKeys: 1000})
			if err != nil {
				return err
			}
			for _, e := range lr.Entries {
				if e.Object == nil {
					continue
				}
				if err := fn(e.Object, true); err != nil {
					return err
				}
			}
			if !lr.IsTruncated || lr.NextKey == "" {
				return nil
			}
			after = lr.NextKey
		}
	}
	keyMarker, verMarker := "", ""
	for {
		lr, err := svc.ListVersions(ctx, bucket, meta.ListOptions{Prefix: opts.Prefix, KeyMarker: keyMarker, VersionMarker: verMarker, MaxKeys: 1000})
		if err != nil {
			return err
		}
		for _, e := range lr.Entries {
			if e.Object == nil {
				continue
			}
			if err := fn(e.Object, e.IsLatest); err != nil {
				return err
			}
		}
		if !lr.IsTruncated || lr.NextKey == "" {
			return nil
		}
		keyMarker, verMarker = lr.NextKey, lr.NextVersion
	}
}

func exportOne(ctx context.Context, svc *object.Service, out string, v *meta.Object, current bool, opts ExportOptions) (*ExportEntry, error) {
	e := &ExportEntry{Bucket: v.Bucket, Key: v.Key, Current: current, Size: v.Size, ETag: v.ETag,
		ContentType: v.ContentType, Metadata: v.UserMeta, Tags: v.Tags, LastModified: v.ModTime}
	if v.Checksum != nil {
		e.Checksum = &ExportChecksum{Algorithm: v.Checksum.Algorithm, Value: v.Checksum.Value, Type: v.Checksum.Type}
	}
	if v.VersionID != meta.NullVersionID {
		e.VersionID = v.VersionID
	}
	if v.SSE != nil {
		e.Encryption = v.SSE.Type
	}
	switch {
	case v.DeleteMarker:
		e.Reason = "delete marker"
		return e, nil
	case v.SSE != nil && v.SSE.Type == "SSE-C":
		e.Reason = "encrypted with a customer-provided key the server does not hold"
		return e, nil
	case strings.HasSuffix(v.Key, "/") && v.Size == 0:
		e.Reason = "directory marker"
		rel, _ := safePath(v.Bucket, v.Key, "", current)
		if err := os.MkdirAll(filepath.Join(out, rel), 0o700); err != nil {
			return nil, err
		}
		e.Path = rel
		return e, nil
	}
	rel, unsafe := safePath(v.Bucket, v.Key, v.VersionID, current)
	if unsafe {
		e.Reason = "key is not a safe file path; written under _unsafe/"
	}
	dst := filepath.Join(out, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil, err
	}
	if !opts.Overwrite {
		if _, err := os.Stat(dst); err == nil {
			e.Path, e.Exported = rel, true
			e.Reason = "already present; not overwritten"
			return e, nil
		}
	}
	res, err := svc.GetObject(ctx, object.GetInput{Bucket: v.Bucket, Key: v.Key, VersionID: v.VersionID})
	if err != nil {
		e.Reason = "cannot read: " + err.Error()
		return e, nil
	}
	defer res.Body.Close()
	tmp := dst + ".exporting"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, res.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		e.Reason = "cannot read: " + err.Error()
		return e, nil
	}
	if n != v.Size {
		os.Remove(tmp)
		e.Reason = fmt.Sprintf("read %d bytes, record says %d", n, v.Size)
		return e, nil
	}
	if err := os.Rename(tmp, dst); err != nil {
		return nil, err
	}
	os.Chtimes(dst, v.ModTime, v.ModTime)
	e.Path, e.Exported = rel, true
	return e, nil
}

// safePath maps a key to a relative output path. Older versions get a
// "@<versionId>" suffix so they sit beside the current file.
func safePath(bucket, key, versionID string, current bool) (rel string, unsafe bool) {
	name := key
	if !current && versionID != "" {
		name += "@" + versionID
	}
	clean := true
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		clean = false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." || seg == "." || (seg == "" && !strings.HasSuffix(name, "/")) {
			clean = false
		}
	}
	if !clean {
		return filepath.Join(bucket, "_unsafe", hex.EncodeToString([]byte(name))), true
	}
	return filepath.Join(bucket, filepath.FromSlash(name)), false
}
