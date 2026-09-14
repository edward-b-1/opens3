package object

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/blob"
	"github.com/edward-b-1/opens3/internal/checksum"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/s3err"
	"github.com/edward-b-1/opens3/internal/sse"
)

// SSERequest describes the encryption a client asked for.
type SSERequest struct {
	Type           string            // "" | AES256 | aws:kms | aws:kms:dsse | SSE-C
	KMSKeyID       string            // aws:kms
	Context        map[string]string // aws:kms encryption context
	CustomerKey    []byte            // SSE-C: 32-byte key
	CustomerKeyMD5 string            // SSE-C: base64 MD5 of key, as sent
}

// ChecksumRequest describes an additional checksum: either the value the
// client sent up front or a function that yields it after the body has
// been read (trailing checksums).
type ChecksumRequest struct {
	Algorithm string
	Value     string // base64, may be empty when Trailer is set
	Trailer   func() (algorithm, value string)
}

// ObjectAttrs are the client-settable attributes of an object.
type ObjectAttrs struct {
	ContentType        string
	ContentEncoding    string
	ContentDisposition string
	ContentLanguage    string
	CacheControl       string
	Expires            string
	WebsiteRedirect    string
	UserMeta           map[string]string
	Tags               []meta.Tag
	StorageClass       string
	ACL                *meta.ACL
	Retention          *meta.Retention
	LegalHold          bool
	LegalHoldSet       bool
}

// PutInput parameters for PutObject.
type PutInput struct {
	Bucket string
	Key    string
	Body   io.Reader
	Size   int64 // -1 if unknown
	// Integrity: expected hex MD5 (from Content-MD5) and hex SHA-256 (from
	// x-amz-content-sha256 when it is a real hash).
	ExpectedMD5    string
	ExpectedSHA256 string
	Checksum       *ChecksumRequest
	SSE            SSERequest
	Attrs          ObjectAttrs
	Conditions     Conditions
	// Event is the notification event name (s3:ObjectCreated:Put, :Post, :Copy).
	Event string
}

// PutResult is what PutObject returns.
type PutResult struct {
	Object *meta.Object
}

// hashing wraps the write path with the digests we need.
type hashing struct {
	md5    hash.Hash
	sha256 hash.Hash
	sum    hash.Hash // additional checksum
	alg    string
	n      int64
}

func newHashing(sha bool, alg string) (*hashing, error) {
	h := &hashing{md5: md5.New()}
	if sha {
		h.sha256 = sha256.New()
	}
	if alg != "" {
		var err error
		if h.sum, err = checksum.New(alg); err != nil {
			return nil, s3err.New(s3err.InvalidArgument).WithMessage("unsupported checksum algorithm %s", alg)
		}
		h.alg = checksum.Normalize(alg)
	}
	return h, nil
}

func (h *hashing) Write(p []byte) (int, error) {
	h.md5.Write(p)
	if h.sha256 != nil {
		h.sha256.Write(p)
	}
	if h.sum != nil {
		h.sum.Write(p)
	}
	h.n += int64(len(p))
	return len(p), nil
}

// sseParams holds the per-object encryption state during a write.
type sseParams struct {
	info *meta.SSE
	dek  []byte
}

// resolveSSE decides the encryption for a new object from the request and
// the bucket default, and generates and wraps a data key.
func (s *Service) resolveSSE(b *meta.Bucket, req SSERequest) (*sseParams, error) {
	typ := req.Type
	keyID := req.KMSKeyID
	if typ == "" && b.Encryption != nil {
		typ = b.Encryption.Algorithm
		keyID = b.Encryption.KMSKeyID
	}
	if typ == "" {
		return nil, nil
	}
	dek := sse.NewDEK()
	info := &meta.SSE{Type: typ, Context: req.Context}
	var wrapped []byte
	var err error
	switch typ {
	case "AES256":
		wrapped, err = s.kms.Wrap("", dek, map[string]string{"bucket": b.Name})
	case "aws:kms", "aws:kms:dsse":
		if keyID == "" {
			keyID = "opens3-default-key"
		}
		if !s.kms.KeyExists(keyID) {
			return nil, s3err.New(s3err.KMSKeyNotFoundException)
		}
		info.KMSKeyID = keyID
		wrapped, err = s.kms.Wrap(keyID, dek, req.Context)
	case "SSE-C":
		if len(req.CustomerKey) != sse.KeySize {
			return nil, s3err.New(s3err.InvalidArgument).WithMessage("The secret key was invalid for the specified algorithm.")
		}
		sum := md5.Sum(req.CustomerKey)
		if base64.StdEncoding.EncodeToString(sum[:]) != req.CustomerKeyMD5 {
			return nil, s3err.New(s3err.InvalidArgument).WithMessage("The calculated MD5 hash of the key did not match the hash that was provided.")
		}
		info.CustomerKeyMD5 = req.CustomerKeyMD5
		wrapped, err = sse.Wrap(req.CustomerKey, dek, []byte("ssec"))
	default:
		return nil, s3err.New(s3err.InvalidEncryptionAlgorithmError)
	}
	if err != nil {
		return nil, err
	}
	info.WrappedKey = wrapped
	return &sseParams{info: info, dek: dek}, nil
}

// unwrapDEK recovers the data key of an existing object.
func (s *Service) unwrapDEK(bucket string, o *meta.SSE, req SSERequest) ([]byte, error) {
	if o == nil {
		return nil, nil
	}
	switch o.Type {
	case "AES256":
		return s.kms.Unwrap("", o.WrappedKey, map[string]string{"bucket": bucket})
	case "aws:kms", "aws:kms:dsse":
		return s.kms.Unwrap(o.KMSKeyID, o.WrappedKey, o.Context)
	case "SSE-C":
		if len(req.CustomerKey) != sse.KeySize || req.CustomerKeyMD5 != o.CustomerKeyMD5 {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object.")
		}
		dek, err := sse.Unwrap(req.CustomerKey, o.WrappedKey, []byte("ssec"))
		if err != nil {
			return nil, s3err.New(s3err.AccessDenied).WithMessage("The provided customer key does not match the key used to encrypt the object.")
		}
		return dek, nil
	}
	return nil, s3err.New(s3err.InternalError)
}

// writeBlob streams body into a new blob, hashing and (optionally)
// encrypting. It returns the committed part reference and the hashes.
func (s *Service) writeBlob(ctx context.Context, bucket string, body io.Reader, size int64, maxSize int64, h *hashing, enc *sseParams) (meta.Part, error) {
	w, err := s.blobs.Create(ctx, bucket)
	if err != nil {
		return meta.Part{}, err
	}
	var dst io.Writer = w
	var encw *sse.Encrypter
	var nonce []byte
	if enc != nil {
		nonce = sse.NewNonce()
		encw, err = sse.NewEncrypter(w, enc.dek, nonce)
		if err != nil {
			w.Abort()
			return meta.Part{}, err
		}
		dst = encw
	}
	limit := maxSize + 1
	if size >= 0 {
		if size > maxSize {
			w.Abort()
			return meta.Part{}, s3err.New(s3err.EntityTooLarge)
		}
		limit = size
	}
	n, err := io.Copy(io.MultiWriter(h, dst), io.LimitReader(body, limit))
	if err != nil {
		w.Abort()
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return meta.Part{}, s3err.New(s3err.IncompleteBody)
		}
		return meta.Part{}, err
	}
	if size >= 0 && n != size {
		w.Abort()
		return meta.Part{}, s3err.New(s3err.IncompleteBody)
	}
	if size < 0 && n > maxSize {
		w.Abort()
		return meta.Part{}, s3err.New(s3err.EntityTooLarge)
	}
	if encw != nil {
		if err := encw.Close(); err != nil {
			w.Abort()
			return meta.Part{}, err
		}
	}
	id, _, err := w.Commit()
	if err != nil {
		return meta.Part{}, err
	}
	return meta.Part{Blob: id, Size: n, ETag: hex.EncodeToString(h.md5.Sum(nil)), Nonce: nonce}, nil
}

// verifyDigests checks the MD5, SHA-256 and additional checksum after the
// body has been consumed. It returns the checksum record, if any.
func verifyDigests(h *hashing, expectedMD5, expectedSHA string, cr *ChecksumRequest) (*meta.Checksum, error) {
	if expectedMD5 != "" && hex.EncodeToString(h.md5.Sum(nil)) != expectedMD5 {
		return nil, s3err.New(s3err.BadDigest)
	}
	if expectedSHA != "" && h.sha256 != nil && hex.EncodeToString(h.sha256.Sum(nil)) != strings.ToLower(expectedSHA) {
		return nil, s3err.New(s3err.XAmzContentSHA256Mismatch)
	}
	if cr == nil || h.sum == nil {
		return nil, nil
	}
	want := cr.Value
	if cr.Trailer != nil {
		alg, v := cr.Trailer()
		if alg != "" && checksum.Normalize(alg) != h.alg {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("Trailer checksum algorithm does not match")
		}
		want = v
	}
	got := checksum.Encode(h.sum.Sum(nil))
	if want == "" {
		// Client asked us to compute only.
		return &meta.Checksum{Algorithm: h.alg, Value: got, Type: checksum.FullObject}, nil
	}
	if _, err := checksum.Decode(h.alg, want); err != nil {
		return nil, s3err.New(s3err.BadDigest).WithMessage("Value for x-amz-checksum-%s header is invalid.", strings.ToLower(h.alg))
	}
	if got != want {
		return nil, s3err.New(s3err.BadDigest).WithMessage("The %s you specified did not match the calculated checksum.", h.alg)
	}
	return &meta.Checksum{Algorithm: h.alg, Value: got, Type: checksum.FullObject}, nil
}

// grantsFullControl reports whether acl grants FULL_CONTROL to the user id.
func grantsFullControl(acl *meta.ACL, id string) bool {
	if acl == nil {
		return false
	}
	for _, g := range acl.Grants {
		if g.GranteeType == "CanonicalUser" && g.Grantee == id && g.Permission == "FULL_CONTROL" {
			return true
		}
	}
	return false
}

func applyAttrs(o *meta.Object, a ObjectAttrs) {
	o.ContentType = a.ContentType
	o.ContentEncoding = a.ContentEncoding
	o.ContentDisposition = a.ContentDisposition
	o.ContentLanguage = a.ContentLanguage
	o.CacheControl = a.CacheControl
	o.Expires = a.Expires
	o.WebsiteRedirect = a.WebsiteRedirect
	o.UserMeta = a.UserMeta
	o.Tags = a.Tags
	o.StorageClass = a.StorageClass
	o.ACL = a.ACL
	o.Retention = a.Retention
	o.LegalHold = a.LegalHold
}

// validateAttrs enforces metadata and tag limits and object-lock rules.
func validateAttrs(b *meta.Bucket, a *ObjectAttrs) error {
	total := 0
	for k, v := range a.UserMeta {
		total += len(k) + len(v)
	}
	if total > MaxMetadataSize {
		return s3err.New(s3err.MetadataTooLarge)
	}
	if len(a.Tags) > MaxTags {
		return s3err.New(s3err.BadRequest).WithMessage("Object tags cannot be greater than 10")
	}
	if err := ValidateTags(a.Tags); err != nil {
		return err
	}
	if a.StorageClass == "" {
		a.StorageClass = "STANDARD"
	}
	if !ValidStorageClass(a.StorageClass) {
		return s3err.New(s3err.InvalidStorageClass)
	}
	if (a.Retention != nil || a.LegalHoldSet) && !b.ObjectLockEnabled {
		return s3err.New(s3err.InvalidRequest).WithMessage("Bucket is missing Object Lock Configuration")
	}
	if a.Retention != nil {
		if a.Retention.Mode != "GOVERNANCE" && a.Retention.Mode != "COMPLIANCE" {
			return s3err.New(s3err.MalformedXML)
		}
		if !a.Retention.RetainUntil.After(time.Now()) {
			return s3err.New(s3err.InvalidRetentionDate)
		}
	}
	if a.Retention == nil && b.ObjectLockEnabled && b.ObjectLock != nil {
		until := time.Now().UTC()
		if b.ObjectLock.Days > 0 {
			until = until.AddDate(0, 0, b.ObjectLock.Days)
		} else {
			until = until.AddDate(b.ObjectLock.Years, 0, 0)
		}
		a.Retention = &meta.Retention{Mode: b.ObjectLock.Mode, RetainUntil: until}
	}
	return nil
}

var storageClasses = map[string]bool{"STANDARD": true, "REDUCED_REDUNDANCY": true, "STANDARD_IA": true, "ONEZONE_IA": true,
	"INTELLIGENT_TIERING": true, "GLACIER": true, "DEEP_ARCHIVE": true, "GLACIER_IR": true, "OUTPOSTS": true, "EXPRESS_ONEZONE": true}

// ValidStorageClass reports whether sc is an S3 storage class name.
func ValidStorageClass(sc string) bool { return storageClasses[sc] }

// ValidateTags applies the S3 tag rules.
func ValidateTags(tags []meta.Tag) error {
	seen := map[string]bool{}
	for _, t := range tags {
		if len(t.Key) == 0 || len(t.Key) > 128 || len(t.Value) > 256 {
			return s3err.New(s3err.InvalidTag)
		}
		if seen[t.Key] {
			return s3err.New(s3err.InvalidTag).WithMessage("Cannot provide multiple Tags with the same key")
		}
		seen[t.Key] = true
	}
	return nil
}

// PutObject stores a new object version.
func (s *Service) PutObject(ctx context.Context, actor Actor, in PutInput) (*meta.Object, error) {
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
	alg := ""
	if in.Checksum != nil {
		alg = in.Checksum.Algorithm
	}
	h, err := newHashing(in.ExpectedSHA256 != "", alg)
	if err != nil {
		return nil, err
	}
	part, err := s.writeBlob(ctx, in.Bucket, in.Body, in.Size, MaxObjectSize, h, enc)
	if err != nil {
		return nil, err
	}
	cs, err := verifyDigests(h, in.ExpectedMD5, in.ExpectedSHA256, in.Checksum)
	if err != nil {
		s.deleteBlobs(ctx, in.Bucket, []meta.Part{part})
		return nil, err
	}
	part.Number = 1
	if cs != nil {
		part.Checksum = cs.Value
	}
	seq := s.seq.Next()
	owner, ownerDisplay := actor.CanonicalID, actor.DisplayName
	if b.Ownership == "BucketOwnerEnforced" && b.Owner != "" {
		// ACLs disabled: the bucket owner owns every object.
		owner, ownerDisplay = b.Owner, b.OwnerDisplay
	}
	if b.Ownership == "BucketOwnerPreferred" && b.Owner != owner && grantsFullControl(in.Attrs.ACL, b.Owner) {
		// bucket-owner-full-control under BucketOwnerPreferred transfers
		// ownership to the bucket owner.
		owner, ownerDisplay = b.Owner, b.OwnerDisplay
		in.Attrs.ACL.Owner, in.Attrs.ACL.OwnerDisplay = owner, ownerDisplay
	}
	o := &meta.Object{Bucket: in.Bucket, Key: in.Key, Seq: seq, Size: part.Size, ETag: part.ETag, ModTime: time.Now().UTC(),
		Owner: owner, OwnerDisplay: ownerDisplay, Parts: []meta.Part{part}, Checksum: cs}
	applyAttrs(o, in.Attrs)
	if enc != nil {
		o.SSE = enc.info
	}
	var replaced *meta.Object
	err = s.kv.Update(func(tx kv.Txn) error {
		// Re-read the bucket inside the transaction: versioning may have changed.
		b, err := meta.GetBucket(tx, in.Bucket)
		if errors.Is(err, kv.ErrNotFound) {
			return s3err.New(s3err.NoSuchBucket)
		} else if err != nil {
			return err
		}
		if err := checkWriteConditions(tx, in.Bucket, in.Key, in.Conditions); err != nil {
			return err
		}
		o.VersionID = s.versionIDFor(b, seq)
		if o.VersionID == meta.NullVersionID {
			if cur, err := meta.GetVersion(tx, in.Bucket, in.Key, meta.NullVersionID); err == nil {
				if err := checkLocked(cur, false); err != nil {
					return err
				}
			}
		}
		replaced, err = meta.PutObject(tx, o)
		return err
	})
	if err != nil {
		s.deleteBlobs(ctx, in.Bucket, []meta.Part{part})
		return nil, err
	}
	if replaced != nil {
		s.deleteBlobs(ctx, in.Bucket, replaced.Parts)
	}
	ev := in.Event
	if ev == "" {
		ev = "s3:ObjectCreated:Put"
	}
	s.emit(Event{Name: ev, Bucket: b, Object: o, Key: o.Key, VersionID: o.VersionID, Actor: actor})
	return o, nil
}

// bytesReader is a small helper for tests and copies.
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

var _ = blob.ErrNotFound
