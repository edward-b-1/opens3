// Package meta defines the metadata records stored in the KV store and the
// key layout used for them. See docs/PLAN.md and docs/FORMAT.md.
package meta

import (
	"encoding/json"
	"time"
)

// FormatVersion is bumped whenever the encoding or key layout changes.
const FormatVersion = 1

// Tag is a key/value tag.
type Tag struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

// Grant is one ACL grant.
type Grant struct {
	// Grantee is a canonical user ID, or one of the group URIs
	// (http://acs.amazonaws.com/groups/global/AllUsers,
	// .../global/AuthenticatedUsers, .../s3/LogDelivery).
	Grantee     string `json:"g"`
	GranteeType string `json:"t"` // CanonicalUser | Group
	DisplayName string `json:"d,omitempty"`
	Permission  string `json:"p"` // FULL_CONTROL | READ | WRITE | READ_ACP | WRITE_ACP
}

// ACL is an access control list.
type ACL struct {
	Owner        string  `json:"o"`
	OwnerDisplay string  `json:"od,omitempty"`
	Grants       []Grant `json:"g"`
}

// ObjectLockConfig is the bucket default retention.
type ObjectLockConfig struct {
	Mode  string `json:"m"` // GOVERNANCE | COMPLIANCE
	Days  int    `json:"d,omitempty"`
	Years int    `json:"y,omitempty"`
}

// EncryptionRule is the bucket default encryption.
type EncryptionRule struct {
	Algorithm string `json:"a"` // AES256 | aws:kms | aws:kms:dsse
	KMSKeyID  string `json:"k,omitempty"`
	BucketKey bool   `json:"b,omitempty"`
}

// PublicAccessBlock mirrors the S3 configuration of the same name.
type PublicAccessBlock struct {
	BlockPublicAcls       bool `json:"bpa"`
	IgnorePublicAcls      bool `json:"ipa"`
	BlockPublicPolicy     bool `json:"bpp"`
	RestrictPublicBuckets bool `json:"rpb"`
}

// Quota is a bucket hard quota in bytes (0 = unlimited).
type Quota struct {
	Bytes int64 `json:"b"`
}

// Bucket is the bucket record. Sub-resource configurations that the
// server only stores and returns are kept as the raw XML the client sent,
// which guarantees round-trip fidelity; the ones the server evaluates are
// typed.
type Bucket struct {
	Name         string    `json:"n"`
	Owner        string    `json:"o"`
	OwnerDisplay string    `json:"od,omitempty"`
	Created      time.Time `json:"c"`
	// Deleting marks a bucket whose record is kept while its data
	// directory is being removed: it is invisible to every operation, and
	// the name cannot be recreated until the removal has finished.
	Deleting bool   `json:"del,omitempty"`
	Region   string `json:"r,omitempty"`

	Versioning        string             `json:"v,omitempty"` // "" | Enabled | Suspended
	MFADelete         bool               `json:"mfa,omitempty"`
	ObjectLockEnabled bool               `json:"ole,omitempty"`
	ObjectLock        *ObjectLockConfig  `json:"ol,omitempty"`
	Tags              []Tag              `json:"t,omitempty"`
	Policy            json.RawMessage    `json:"p,omitempty"`
	PolicyText        string             `json:"pt,omitempty"` // policy exactly as submitted (GetBucketPolicy returns it verbatim)
	ACL               *ACL               `json:"acl,omitempty"`
	Encryption        *EncryptionRule    `json:"enc,omitempty"`
	PublicAccessBlock *PublicAccessBlock `json:"pab,omitempty"`
	Ownership         string             `json:"own,omitempty"` // BucketOwnerEnforced | BucketOwnerPreferred | ObjectWriter
	Quota             *Quota             `json:"q,omitempty"`
	// SelfLock records that the owner confirmed (x-amz-confirm-remove-self-
	// bucket-access) a policy that may deny it access to the policy itself.
	SelfLock bool `json:"sl,omitempty"`

	// Raw XML configurations.
	LifecycleXML          []byte            `json:"lc,omitempty"`
	CORSXML               []byte            `json:"cors,omitempty"`
	NotificationXML       []byte            `json:"nt,omitempty"`
	WebsiteXML            []byte            `json:"ws,omitempty"`
	LoggingXML            []byte            `json:"lg,omitempty"`
	ReplicationXML        []byte            `json:"rp,omitempty"`
	AccelerateXML         []byte            `json:"ac,omitempty"`
	RequestPaymentXML     []byte            `json:"rq,omitempty"`
	MetricsXML            map[string][]byte `json:"mx,omitempty"`
	AnalyticsXML          map[string][]byte `json:"ax,omitempty"`
	InventoryXML          map[string][]byte `json:"ix,omitempty"`
	IntelligentTieringXML map[string][]byte `json:"itx,omitempty"`
}

// Part references one blob holding a contiguous byte range of an object.
type Part struct {
	Number   int    `json:"n"`
	Blob     string `json:"b"`
	Size     int64  `json:"s"`
	ETag     string `json:"e"`           // hex MD5 of the plaintext part, no quotes
	Checksum string `json:"c,omitempty"` // base64 checksum of the part in the object's algorithm
	// Nonce is the SSE nonce base for this part's blob (nil if unencrypted).
	Nonce []byte `json:"nn,omitempty"`
}

// Checksum is the object-level additional checksum.
type Checksum struct {
	Algorithm string `json:"a"` // CRC32 | CRC32C | SHA1 | SHA256 | CRC64NVME
	Value     string `json:"v"` // base64
	Type      string `json:"t"` // FULL_OBJECT | COMPOSITE
}

// SSE records how an object's blobs are encrypted.
type SSE struct {
	Type           string            `json:"t"`             // AES256 | aws:kms | aws:kms:dsse | SSE-C
	KMSKeyID       string            `json:"k,omitempty"`   // for aws:kms
	WrappedKey     []byte            `json:"w"`             // data key wrapped by master/KMS/customer key
	CustomerKeyMD5 string            `json:"cm,omitempty"`  // for SSE-C
	Context        map[string]string `json:"ctx,omitempty"` // KMS encryption context
}

// Retention is an object lock retention setting.
type Retention struct {
	Mode        string    `json:"m"`
	RetainUntil time.Time `json:"u"`
}

// Object is one version of an object (or a delete marker).
type Object struct {
	Bucket       string    `json:"b"`
	Key          string    `json:"k"`
	VersionID    string    `json:"v"` // "null" for the null version
	Seq          uint64    `json:"q"`
	DeleteMarker bool      `json:"dm,omitempty"`
	Size         int64     `json:"s"`
	ETag         string    `json:"e"` // no quotes
	ModTime      time.Time `json:"m"`
	Owner        string    `json:"o"`
	OwnerDisplay string    `json:"od,omitempty"`

	ContentType        string            `json:"ct,omitempty"`
	ContentEncoding    string            `json:"ce,omitempty"`
	ContentDisposition string            `json:"cd,omitempty"`
	ContentLanguage    string            `json:"cl,omitempty"`
	CacheControl       string            `json:"cc,omitempty"`
	Expires            string            `json:"ex,omitempty"`
	WebsiteRedirect    string            `json:"wr,omitempty"`
	UserMeta           map[string]string `json:"um,omitempty"` // lower-case keys without x-amz-meta-
	Tags               []Tag             `json:"t,omitempty"`
	StorageClass       string            `json:"sc,omitempty"`
	ACL                *ACL              `json:"acl,omitempty"`

	Parts     []Part     `json:"p,omitempty"`
	Checksum  *Checksum  `json:"cs,omitempty"`
	SSE       *SSE       `json:"sse,omitempty"`
	Retention *Retention `json:"rt,omitempty"`
	LegalHold bool       `json:"lh,omitempty"`
	// Restore holds RestoreObject state for archived-class objects.
	RestoreExpiry *time.Time `json:"rx,omitempty"`
}

// IsNull reports whether this is the null version.
func (o *Object) IsNull() bool { return o.VersionID == NullVersionID }

// Upload is an in-progress multipart upload.
type Upload struct {
	Bucket       string    `json:"b"`
	Key          string    `json:"k"`
	UploadID     string    `json:"u"`
	Initiated    time.Time `json:"i"`
	Owner        string    `json:"o"`
	OwnerDisplay string    `json:"od,omitempty"`

	ContentType        string            `json:"ct,omitempty"`
	ContentEncoding    string            `json:"ce,omitempty"`
	ContentDisposition string            `json:"cd,omitempty"`
	ContentLanguage    string            `json:"cl,omitempty"`
	CacheControl       string            `json:"cc,omitempty"`
	Expires            string            `json:"ex,omitempty"`
	WebsiteRedirect    string            `json:"wr,omitempty"`
	UserMeta           map[string]string `json:"um,omitempty"`
	Tags               []Tag             `json:"t,omitempty"`
	StorageClass       string            `json:"sc,omitempty"`
	ACL                *ACL              `json:"acl,omitempty"`
	SSE                *SSE              `json:"sse,omitempty"`
	ChecksumAlgorithm  string            `json:"ca,omitempty"`
	ChecksumType       string            `json:"cty,omitempty"`
	Retention          *Retention        `json:"rt,omitempty"`
	LegalHold          bool              `json:"lh,omitempty"`
}
