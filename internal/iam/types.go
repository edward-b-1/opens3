// Package iam manages identities (root, users, groups, service accounts,
// STS sessions), their credentials and attached policies, and makes
// authorisation decisions combining identity policies, bucket policies and
// ACLs the way S3 does.
package iam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Key kinds.
const (
	KindUser    = "user"    // long-lived key of an IAM user
	KindService = "service" // service account (child of a user, optional session policy)
	KindSTS     = "sts"     // temporary credentials with session token
)

// User is an identity that can own access keys and belong to groups.
type User struct {
	Name     string    `json:"n"`
	Enabled  bool      `json:"e"`
	Policies []string  `json:"p,omitempty"` // attached named policies
	Groups   []string  `json:"g,omitempty"`
	Created  time.Time `json:"c"`
	// Console password (optional): PBKDF2-SHA256 hash with a per-user salt.
	// Never usable against the S3 API.
	PasswordHash []byte    `json:"ph,omitempty"`
	PasswordSalt []byte    `json:"ps,omitempty"`
	PasswordSet  time.Time `json:"pt,omitempty"`
}

// HasPassword reports whether a console password is set.
func (u *User) HasPassword() bool { return u != nil && len(u.PasswordHash) > 0 }

// Key is an access key record.
type Key struct {
	AccessKey     string          `json:"ak"`
	SecretWrapped []byte          `json:"sw"` // secret key encrypted with the IAM master key
	User          string          `json:"u"`  // owning user name
	Kind          string          `json:"k"`
	Enabled       bool            `json:"e"`
	SessionPolicy json.RawMessage `json:"sp,omitempty"` // service accounts / STS: intersected with user's policies
	SessionToken  string          `json:"st,omitempty"` // STS only
	Expires       *time.Time      `json:"x,omitempty"`  // STS only
	Description   string          `json:"d,omitempty"`
	Created       time.Time       `json:"c"`
}

// Group is a named set of users with attached policies.
type Group struct {
	Name     string    `json:"n"`
	Enabled  bool      `json:"e"`
	Members  []string  `json:"m,omitempty"`
	Policies []string  `json:"p,omitempty"`
	Created  time.Time `json:"c"`
}

// Policy is a named policy document.
type Policy struct {
	Name     string          `json:"n"`
	Document json.RawMessage `json:"d"`
	Created  time.Time       `json:"c"`
	Updated  time.Time       `json:"u"`
	BuiltIn  bool            `json:"b,omitempty"`
}

// Identity is a resolved, authenticated caller.
type Identity struct {
	User   *User
	Key    *Key
	IsRoot bool
	// Effective identity policies (documents) and session policy.
	Policies      []json.RawMessage
	SessionPolicy json.RawMessage
	AccountID     string
}

// Name returns the user name ("root" for the root account).
func (id *Identity) Name() string {
	if id == nil {
		return ""
	}
	if id.IsRoot {
		return "root"
	}
	return id.User.Name
}

// ARN returns the IAM ARN of the caller.
func (id *Identity) ARN() string {
	if id == nil {
		return ""
	}
	if id.IsRoot {
		return "arn:aws:iam::" + id.AccountID + ":root"
	}
	return "arn:aws:iam::" + id.AccountID + ":user/" + id.User.Name
}

// CanonicalID returns the 64-hex canonical user ID used in ACLs and
// ListBuckets owner elements.
func (id *Identity) CanonicalID() string {
	if id == nil {
		return ""
	}
	return CanonicalID(id.Name())
}

// CanonicalID derives the canonical ID for a user name.
func CanonicalID(name string) string {
	h := sha256.Sum256([]byte("opens3-canonical:" + name))
	return hex.EncodeToString(h[:])
}

// PrincipalType is aws:PrincipalType.
func (id *Identity) PrincipalType() string {
	switch {
	case id == nil:
		return "Anonymous"
	case id.IsRoot:
		return "Account"
	case id.Key != nil && id.Key.Kind == KindSTS:
		return "AssumedRole"
	default:
		return "User"
	}
}
