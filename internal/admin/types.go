// Package admin implements the OpenS3 administration REST API
// (mounted at /opens3/admin/v1/), a Go client for it, and the JSON
// types shared by both. The concepts (users, service accounts / keys,
// groups, named policies, KMS keys) mirror MinIO's admin API so that
// migrated tooling maps one-to-one.
package admin

import (
	"encoding/json"
	"fmt"
	"time"
)

// Prefix is the URL path prefix of the admin API.
const Prefix = "/opens3/admin/v1"

// Version is reported by the info endpoint. The main package sets it.
var Version = "dev"

// Error is the JSON error body returned by the API. The client returns it
// as a Go error with the HTTP status filled in.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message)
	}
	return e.Code + ": " + e.Message
}

// Status is the body of the health endpoint and of simple mutations.
type Status struct {
	Status string `json:"status"`
}

// DiskStats is the blob backend usage.
type DiskStats struct {
	TotalBytes int64 `json:"total_bytes"`
	FreeBytes  int64 `json:"free_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
}

// ServerInfo is returned by GET info.
type ServerInfo struct {
	Version       string    `json:"version"`
	Region        string    `json:"region"`
	StartTime     time.Time `json:"start_time"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Buckets       int       `json:"buckets"`
	Objects       int64     `json:"objects"`
	TotalBytes    int64     `json:"total_bytes"`
	Disk          DiskStats `json:"disk"`
}

// UserInfo describes an IAM user (never includes secrets).
type UserInfo struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Policies []string  `json:"policies"`
	Groups   []string  `json:"groups"`
	Created  time.Time `json:"created"`
}

// PutUserRequest creates or updates a user. On creation an access key
// equal to the user name is created when SecretKey is set (MinIO
// convention); on update a non-empty SecretKey rotates (or creates) that
// key. A nil Policies leaves the attached policies unchanged.
type PutUserRequest struct {
	SecretKey string   `json:"secret_key,omitempty"`
	Policies  []string `json:"policies"`
}

// PoliciesRequest sets the policies attached to a user.
type PoliciesRequest struct {
	Policies []string `json:"policies"`
}

// KeyInfo describes an access key (never includes the secret).
type KeyInfo struct {
	AccessKey     string          `json:"access_key"`
	User          string          `json:"user"`
	Kind          string          `json:"kind"` // user | service | sts
	Enabled       bool            `json:"enabled"`
	SessionPolicy json.RawMessage `json:"session_policy,omitempty"`
	Expires       *time.Time      `json:"expires,omitempty"`
	Description   string          `json:"description,omitempty"`
	Created       time.Time       `json:"created"`
}

// CreateKeyRequest creates an access key for a user. Empty AccessKey and
// SecretKey are generated. Kind defaults to "user"; "service" keys may
// carry a session policy that restricts the owner's permissions.
type CreateKeyRequest struct {
	User          string          `json:"user"`
	AccessKey     string          `json:"access_key,omitempty"`
	SecretKey     string          `json:"secret_key,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	SessionPolicy json.RawMessage `json:"session_policy,omitempty"`
	Expires       *time.Time      `json:"expires,omitempty"`
	Description   string          `json:"description,omitempty"`
}

// RotateKeyRequest rotates a key's secret; an empty SecretKey is generated.
type RotateKeyRequest struct {
	SecretKey string `json:"secret_key,omitempty"`
}

// Credentials is returned exactly once, when a key is created or rotated.
type Credentials struct {
	KeyInfo
	SecretKey string `json:"secret_key"`
}

// GroupInfo describes a group.
type GroupInfo struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Members  []string  `json:"members"`
	Policies []string  `json:"policies"`
	Created  time.Time `json:"created"`
}

// PutGroupRequest creates or updates a group. On update a nil slice
// leaves that field unchanged.
type PutGroupRequest struct {
	Members  []string `json:"members"`
	Policies []string `json:"policies"`
}

// GroupMembersRequest adds and removes members.
type GroupMembersRequest struct {
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// PolicyInfo describes a named policy. Document is only set by GET
// policies/{name}.
type PolicyInfo struct {
	Name     string          `json:"name"`
	BuiltIn  bool            `json:"builtin"`
	Created  time.Time       `json:"created"`
	Updated  time.Time       `json:"updated"`
	Document json.RawMessage `json:"document,omitempty"`
}

// BucketInfo describes a bucket. Objects and Bytes are only computed when
// usage is requested.
type BucketInfo struct {
	Name       string    `json:"name"`
	Created    time.Time `json:"created"`
	Owner      string    `json:"owner"`
	Versioning string    `json:"versioning,omitempty"`
	ObjectLock bool      `json:"object_lock"`
	Objects    *int64    `json:"objects,omitempty"`
	Bytes      *int64    `json:"bytes,omitempty"`
}

// KMSKeyInfo describes a named KMS key.
type KMSKeyInfo struct {
	ID       string    `json:"id"`
	Created  time.Time `json:"created"`
	Disabled bool      `json:"disabled,omitempty"`
}

// CreateKMSKeyRequest creates a named KMS key.
type CreateKMSKeyRequest struct {
	ID string `json:"id"`
}
