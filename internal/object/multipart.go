package object

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/checksum"
	"gitlab.com/Birdsall/opens3/internal/kv"
	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/s3err"
)

// CreateUploadInput parameters.
type CreateUploadInput struct {
	Bucket            string
	Key               string
	Attrs             ObjectAttrs
	SSE               SSERequest
	ChecksumAlgorithm string
	ChecksumType      string // FULL_OBJECT | COMPOSITE | ""
}

// CreateUpload starts a multipart upload.
func (s *Service) CreateUpload(ctx context.Context, actor Actor, in CreateUploadInput) (*meta.Upload, error) {
	if err := ValidObjectKey(in.Key); err != nil {
		return nil, err
	}
	b, err := s.GetBucket(ctx, in.Bucket)
	if err != nil {
		return nil, err
	}
	if err := validateAttrs(b, &in.Attrs); err != nil {
		return nil, err
	}
	enc, err := s.resolveSSE(b, in.SSE)
	if err != nil {
		return nil, err
	}
	alg := checksum.Normalize(in.ChecksumAlgorithm)
	if in.ChecksumAlgorithm != "" && alg == "" {
		return nil, s3err.New(s3err.InvalidArgument).WithMessage("Checksum algorithm provided is unsupported.")
	}
	ctype := in.ChecksumType
	if alg != "" {
		if ctype == "" {
			ctype = checksum.Composite
			if alg == checksum.CRC64NVME {
				ctype = checksum.FullObject
			}
		}
		if ctype == checksum.FullObject && !checksum.SupportsFullObjectMultipart(alg) {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("The FULL_OBJECT checksum type cannot be used with the %s checksum algorithm.", alg)
		}
	}
	u := &meta.Upload{Bucket: in.Bucket, Key: in.Key, UploadID: meta.NewID(), Initiated: time.Now().UTC(),
		Owner: actor.CanonicalID, OwnerDisplay: actor.DisplayName, ChecksumAlgorithm: alg, ChecksumType: ctype}
	a := in.Attrs
	u.ContentType, u.ContentEncoding, u.ContentDisposition, u.ContentLanguage = a.ContentType, a.ContentEncoding, a.ContentDisposition, a.ContentLanguage
	u.CacheControl, u.Expires, u.WebsiteRedirect, u.UserMeta, u.Tags, u.StorageClass, u.ACL = a.CacheControl, a.Expires, a.WebsiteRedirect, a.UserMeta, a.Tags, a.StorageClass, a.ACL
	u.Retention, u.LegalHold = a.Retention, a.LegalHold
	if enc != nil {
		u.SSE = enc.info
	}
	err = s.kv.Update(func(tx kv.Txn) error { return meta.PutUpload(tx, u) })
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUpload returns the upload record or NoSuchUpload.
func (s *Service) GetUpload(ctx context.Context, bucket, key, uploadID string) (*meta.Upload, error) {
	var u *meta.Upload
	err := s.kv.View(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, bucket); errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(bucket)
		} else if err != nil {
			return err
		}
		var err error
		u, err = meta.GetUpload(tx, bucket, key, uploadID)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchUpload)
		}
		return err
	})
	return u, err
}

// UploadPartInput parameters.
type UploadPartInput struct {
	Bucket, Key, UploadID string
	PartNumber            int
	Body                  io.Reader
	Size                  int64
	ExpectedMD5           string
	ExpectedSHA256        string
	Checksum              *ChecksumRequest
	SSE                   SSERequest // SSE-C key must be supplied per part
}

// UploadPart stores one part.
func (s *Service) UploadPart(ctx context.Context, in UploadPartInput) (*meta.Part, error) {
	if in.PartNumber < 1 || in.PartNumber > MaxParts {
		return nil, s3err.New(s3err.InvalidArgument).WithMessage("Part number must be an integer between 1 and 10000, inclusive")
	}
	u, err := s.GetUpload(ctx, in.Bucket, in.Key, in.UploadID)
	if err != nil {
		return nil, err
	}
	var enc *sseParams
	if u.SSE != nil {
		dek, err := s.unwrapDEK(in.Bucket, u.SSE, in.SSE)
		if err != nil {
			return nil, err
		}
		enc = &sseParams{info: u.SSE, dek: dek}
	}
	alg := u.ChecksumAlgorithm
	if in.Checksum != nil && in.Checksum.Algorithm != "" {
		if alg != "" && checksum.Normalize(in.Checksum.Algorithm) != alg {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("Checksum Type mismatch occurred, expected checksum Type: %s", alg)
		}
		alg = checksum.Normalize(in.Checksum.Algorithm)
	}
	h, err := newHashing(in.ExpectedSHA256 != "", alg)
	if err != nil {
		return nil, err
	}
	part, err := s.writeBlob(ctx, in.Bucket, in.Body, in.Size, MaxPartSize, h, enc)
	if err != nil {
		return nil, err
	}
	cr := in.Checksum
	if cr == nil && alg != "" {
		cr = &ChecksumRequest{Algorithm: alg}
	}
	cs, err := verifyDigests(h, in.ExpectedMD5, in.ExpectedSHA256, cr)
	if err != nil {
		s.deleteBlobs(ctx, in.Bucket, []meta.Part{part})
		return nil, err
	}
	part.Number = in.PartNumber
	if cs != nil {
		part.Checksum = cs.Value
	}
	var old *meta.Part
	err = s.kv.Update(func(tx kv.Txn) error {
		if _, err := meta.GetUpload(tx, in.Bucket, in.Key, in.UploadID); err != nil {
			return s3err.New(s3err.NoSuchUpload)
		}
		old, err = meta.PutPart(tx, in.Bucket, in.UploadID, part)
		return err
	})
	if err != nil {
		s.deleteBlobs(ctx, in.Bucket, []meta.Part{part})
		return nil, err
	}
	if old != nil {
		s.deleteBlobs(ctx, in.Bucket, []meta.Part{*old})
	}
	return &part, nil
}

// CompletePart is one entry of the CompleteMultipartUpload request.
type CompletePart struct {
	PartNumber int
	ETag       string
	Checksum   string // base64 value for the upload's algorithm, optional
}

// CompleteInput parameters.
type CompleteInput struct {
	Bucket, Key, UploadID string
	Parts                 []CompletePart
	Conditions            Conditions
	// Checksum is the full-object checksum the client claims (optional).
	Checksum   *ChecksumRequest
	ObjectSize int64 // x-amz-mp-object-size, -1 if absent
}

// CompleteUpload assembles the parts into an object version.
func (s *Service) CompleteUpload(ctx context.Context, actor Actor, in CompleteInput) (*meta.Object, error) {
	if len(in.Parts) == 0 {
		return nil, s3err.New(s3err.MalformedXML).WithMessage("You must specify at least one part")
	}
	u, err := s.GetUpload(ctx, in.Bucket, in.Key, in.UploadID)
	if err != nil {
		return nil, err
	}
	b, err := s.GetBucket(ctx, in.Bucket)
	if err != nil {
		return nil, err
	}
	var stored []meta.Part
	err = s.kv.View(func(tx kv.Txn) error {
		var err error
		stored, err = meta.ListParts(tx, in.Bucket, in.UploadID, 0, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	byNum := map[int]meta.Part{}
	for _, p := range stored {
		byNum[p.Number] = p
	}
	var parts []meta.Part
	var total int64
	md5s := md5.New()
	var partSums [][]byte
	var sizes []int64
	last := 0
	for _, cp := range in.Parts {
		if cp.PartNumber <= last {
			return nil, s3err.New(s3err.InvalidPartOrder)
		}
		last = cp.PartNumber
	}
	for i, cp := range in.Parts {
		p, ok := byNum[cp.PartNumber]
		if !ok {
			return nil, s3err.New(s3err.InvalidPart).WithExtra("PartNumber", itoa(int64(cp.PartNumber)))
		}
		if !etagMatches(cp.ETag, p.ETag) || cp.ETag == "*" {
			return nil, s3err.New(s3err.InvalidPart).WithExtra("PartNumber", itoa(int64(cp.PartNumber)))
		}
		if i < len(in.Parts)-1 && p.Size < MinPartSize {
			return nil, s3err.New(s3err.EntityTooSmall).WithExtra("PartNumber", itoa(int64(cp.PartNumber))).WithExtra("MinSizeAllowed", itoa(MinPartSize))
		}
		if u.ChecksumAlgorithm != "" {
			if cp.Checksum != "" && cp.Checksum != p.Checksum {
				return nil, s3err.New(s3err.InvalidPart).WithMessage("One or more of the specified parts could not be found. The part may not have been uploaded, or the specified checksum may not match the part's checksum.")
			}
			raw, _ := checksum.Decode(u.ChecksumAlgorithm, p.Checksum)
			partSums = append(partSums, raw)
			sizes = append(sizes, p.Size)
		}
		raw, _ := hex.DecodeString(p.ETag)
		md5s.Write(raw)
		total += p.Size
		parts = append(parts, p)
	}
	if in.ObjectSize >= 0 && in.ObjectSize != total {
		return nil, s3err.New(s3err.InvalidRequest).WithMessage("The provided 'x-amz-mp-object-size' header value does not match what was computed.")
	}
	if total > MaxObjectSize {
		return nil, s3err.New(s3err.EntityTooLarge)
	}
	seq := s.seq.Next()
	o := &meta.Object{Bucket: in.Bucket, Key: in.Key, Seq: seq, Size: total, ETag: fmtETag(len(parts), hex.EncodeToString(md5s.Sum(nil))),
		ModTime: time.Now().UTC(), Owner: u.Owner, OwnerDisplay: u.OwnerDisplay, Parts: parts, SSE: u.SSE}
	o.ContentType, o.ContentEncoding, o.ContentDisposition, o.ContentLanguage = u.ContentType, u.ContentEncoding, u.ContentDisposition, u.ContentLanguage
	o.CacheControl, o.Expires, o.WebsiteRedirect, o.UserMeta, o.Tags, o.StorageClass, o.ACL = u.CacheControl, u.Expires, u.WebsiteRedirect, u.UserMeta, u.Tags, u.StorageClass, u.ACL
	o.Retention, o.LegalHold = u.Retention, u.LegalHold
	if u.ChecksumAlgorithm != "" {
		var sum []byte
		if u.ChecksumType == checksum.FullObject {
			sum, err = checksum.CombineCRC(u.ChecksumAlgorithm, partSums, sizes)
		} else {
			sum, err = checksum.CompositeOf(u.ChecksumAlgorithm, partSums)
		}
		if err != nil {
			return nil, err
		}
		val := checksum.Encode(sum)
		if u.ChecksumType == checksum.Composite {
			val = val + "-" + itoa(int64(len(parts)))
		}
		o.Checksum = &meta.Checksum{Algorithm: u.ChecksumAlgorithm, Value: val, Type: u.ChecksumType}
		if in.Checksum != nil && in.Checksum.Value != "" && checksum.Normalize(in.Checksum.Algorithm) == u.ChecksumAlgorithm {
			want := in.Checksum.Value
			if u.ChecksumType == checksum.Composite && !strings.Contains(want, "-") {
				want = want + "-" + itoa(int64(len(parts)))
			}
			if want != val {
				return nil, s3err.New(s3err.BadDigest).WithMessage("The %s you specified did not match the calculated checksum.", u.ChecksumAlgorithm)
			}
		}
	}
	var replaced *meta.Object
	var unused []meta.Part
	err = s.kv.Update(func(tx kv.Txn) error {
		b, err := meta.GetBucket(tx, in.Bucket)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket)
		} else if err != nil {
			return err
		}
		if _, err := meta.GetUpload(tx, in.Bucket, in.Key, in.UploadID); err != nil {
			return s3err.New(s3err.NoSuchUpload)
		}
		if err := checkWriteConditions(tx, in.Bucket, in.Key, in.Conditions); err != nil {
			return err
		}
		o.VersionID = s.versionIDFor(b, seq)
		all, err := meta.DeleteUpload(tx, u)
		if err != nil {
			return err
		}
		used := map[string]bool{}
		for _, p := range parts {
			used[p.Blob] = true
		}
		for _, p := range all {
			if !used[p.Blob] {
				unused = append(unused, p)
			}
		}
		replaced, err = meta.PutObject(tx, o)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.deleteBlobs(ctx, in.Bucket, unused)
	if replaced != nil {
		s.deleteBlobs(ctx, in.Bucket, replaced.Parts)
	}
	s.emit(Event{Name: "s3:ObjectCreated:CompleteMultipartUpload", Bucket: b, Object: o, Key: o.Key, VersionID: o.VersionID, Actor: actor})
	return o, nil
}

// AbortUpload discards an upload and its parts.
func (s *Service) AbortUpload(ctx context.Context, bucket, key, uploadID string) error {
	var parts []meta.Part
	err := s.kv.Update(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, bucket); errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(bucket)
		} else if err != nil {
			return err
		}
		u, err := meta.GetUpload(tx, bucket, key, uploadID)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchUpload)
		} else if err != nil {
			return err
		}
		parts, err = meta.DeleteUpload(tx, u)
		return err
	})
	if err != nil {
		return err
	}
	s.deleteBlobs(ctx, bucket, parts)
	return nil
}

// ListParts lists parts of an upload after marker, at most max.
func (s *Service) ListParts(ctx context.Context, bucket, key, uploadID string, marker, max int) (*meta.Upload, []meta.Part, bool, error) {
	u, err := s.GetUpload(ctx, bucket, key, uploadID)
	if err != nil {
		return nil, nil, false, err
	}
	if max <= 0 || max > 1000 {
		max = 1000
	}
	var parts []meta.Part
	err = s.kv.View(func(tx kv.Txn) error {
		var err error
		parts, err = meta.ListParts(tx, bucket, uploadID, marker, max+1)
		return err
	})
	if err != nil {
		return nil, nil, false, err
	}
	truncated := len(parts) > max
	if truncated {
		parts = parts[:max]
	}
	return u, parts, truncated, nil
}

// ListUploads lists in-progress uploads.
func (s *Service) ListUploads(ctx context.Context, bucket string, opt meta.ListOptions) (*meta.UploadListResult, error) {
	var res *meta.UploadListResult
	err := s.kv.View(func(tx kv.Txn) error {
		if _, err := meta.GetBucket(tx, bucket); errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket).WithResource(bucket)
		} else if err != nil {
			return err
		}
		var err error
		res, err = meta.ListUploads(tx, bucket, opt)
		return err
	})
	return res, err
}
