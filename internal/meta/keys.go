package meta

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NullVersionID is the version ID of objects written while versioning is
// off or suspended.
const NullVersionID = "null"

// Key namespaces. Bucket names never contain '/', so "<ns>/<bucket>/" is an
// unambiguous prefix. Object keys may contain any byte except NUL, which is
// used as the key/version separator.
const (
	nsBucket   = "b/"
	nsObject   = "o/"
	nsNull     = "n/"
	nsUpload   = "u/"
	nsUploadID = "ui/"
	nsPart     = "p/"
	sep        = "\x00"
)

// ObjectNamespace and UploadNamespace are the prefixes of all object and
// all multipart-upload records, for tools that scan every record.
const (
	ObjectNamespace = nsObject
	UploadNamespace = nsUpload
)

// ErrBadVersionID is returned when a version ID cannot be parsed.
var ErrBadVersionID = errors.New("meta: malformed version id")

// BucketKey returns the KV key of a bucket record.
func BucketKey(name string) []byte { return []byte(nsBucket + name) }

// BucketPrefix is the prefix of all bucket records.
func BucketPrefix() []byte { return []byte(nsBucket) }

// ObjectBucketPrefix is the prefix of all object records in a bucket.
func ObjectBucketPrefix(bucket string) []byte { return []byte(nsObject + bucket + "/") }

// ObjectPrefix is the prefix of all versions of one key (keys sharing a
// textual prefix are excluded because of the NUL separator).
func ObjectPrefix(bucket, key string) []byte {
	return []byte(nsObject + bucket + "/" + key + sep)
}

// ObjectScanPrefix is the prefix used to scan keys starting with prefix.
func ObjectScanPrefix(bucket, prefix string) []byte {
	return []byte(nsObject + bucket + "/" + prefix)
}

// verKey encodes a sequence so that newer versions sort first.
func verKey(seq uint64) string { return fmt.Sprintf("%016x", ^seq) }

// ObjectKey returns the KV key of one object version.
func ObjectKey(bucket, key string, seq uint64) []byte {
	return []byte(nsObject + bucket + "/" + key + sep + verKey(seq))
}

// SplitObjectKey extracts bucket, key and seq from an object record key.
func SplitObjectKey(k []byte) (bucket, key string, seq uint64, ok bool) {
	s := string(k)
	if !strings.HasPrefix(s, nsObject) {
		return "", "", 0, false
	}
	s = s[len(nsObject):]
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return "", "", 0, false
	}
	bucket = s[:i]
	rest := s[i+1:]
	j := strings.LastIndex(rest, sep)
	if j < 0 || len(rest)-j-1 != 16 {
		return "", "", 0, false
	}
	v, err := strconv.ParseUint(rest[j+1:], 16, 64)
	if err != nil {
		return "", "", 0, false
	}
	return bucket, rest[:j], ^v, true
}

// NullKey returns the KV key of the null-version pointer of key.
func NullKey(bucket, key string) []byte { return []byte(nsNull + bucket + "/" + key) }

// NullBucketPrefix is the prefix of all null pointers in a bucket.
func NullBucketPrefix(bucket string) []byte { return []byte(nsNull + bucket + "/") }

// UploadKey returns the KV key of an upload record.
func UploadKey(bucket, key, uploadID string) []byte {
	return []byte(nsUpload + bucket + "/" + key + sep + uploadID)
}

// UploadBucketPrefix is the prefix of all uploads in a bucket.
func UploadBucketPrefix(bucket string) []byte { return []byte(nsUpload + bucket + "/") }

// UploadScanPrefix is the prefix for uploads whose key starts with prefix.
func UploadScanPrefix(bucket, prefix string) []byte { return []byte(nsUpload + bucket + "/" + prefix) }

// SplitUploadKey extracts key and uploadID from an upload record key.
func SplitUploadKey(k []byte, bucket string) (key, uploadID string, ok bool) {
	p := nsUpload + bucket + "/"
	s := string(k)
	if !strings.HasPrefix(s, p) {
		return "", "", false
	}
	rest := s[len(p):]
	j := strings.LastIndex(rest, sep)
	if j < 0 {
		return "", "", false
	}
	return rest[:j], rest[j+1:], true
}

// UploadIDKey returns the KV key of the uploadID → key reverse index.
func UploadIDKey(bucket, uploadID string) []byte { return []byte(nsUploadID + bucket + "/" + uploadID) }

// PartKey returns the KV key of one uploaded part.
func PartKey(bucket, uploadID string, n int) []byte {
	return []byte(fmt.Sprintf("%s%s/%s/%05d", nsPart, bucket, uploadID, n))
}

// PartPrefix is the prefix of all parts of an upload.
func PartPrefix(bucket, uploadID string) []byte {
	return []byte(nsPart + bucket + "/" + uploadID + "/")
}

// NewVersionID creates a version ID that embeds seq (so the record key can
// be derived from it) followed by random bytes.
func NewVersionID(seq uint64) string {
	var r [8]byte
	_, _ = rand.Read(r[:])
	return fmt.Sprintf("%016x%s", seq, hex.EncodeToString(r[:]))
}

// SeqFromVersionID extracts the sequence from a version ID.
func SeqFromVersionID(vid string) (uint64, error) {
	if len(vid) != 32 {
		return 0, ErrBadVersionID
	}
	v, err := strconv.ParseUint(vid[:16], 16, 64)
	if err != nil {
		return 0, ErrBadVersionID
	}
	return v, nil
}

// NewID returns a random 32-hex-char identifier (blob IDs, upload IDs).
func NewID() string {
	var r [16]byte
	_, _ = rand.Read(r[:])
	return hex.EncodeToString(r[:])
}

// Sequencer hands out monotonically increasing sequence numbers that are
// also nanosecond timestamps, so ordering survives restarts.
type Sequencer struct {
	mu   sync.Mutex
	last uint64
}

// Next returns the next sequence number.
func (s *Sequencer) Next() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := uint64(time.Now().UnixNano())
	if now <= s.last {
		now = s.last + 1
	}
	s.last = now
	return now
}
