package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/auth/sigv4"
)

// Client calls the admin API with SigV4-signed requests. It is used by
// the `opens3 admin` CLI and by tests.
type Client struct {
	Endpoint  string // e.g. http://localhost:9000
	AccessKey string
	SecretKey string
	Region    string       // defaults to us-east-1
	HTTP      *http.Client // defaults to a client with a 60s timeout
}

// NewClient returns a client for endpoint with the given credentials.
func NewClient(endpoint, accessKey, secretKey string) *Client {
	return &Client{Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey}
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body []byte
	if in != nil {
		if raw, ok := in.(json.RawMessage); ok {
			body = raw
		} else {
			var err error
			if body, err = json.Marshal(in); err != nil {
				return err
			}
		}
	}
	u := strings.TrimRight(c.Endpoint, "/") + Prefix + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	sum := sha256.Sum256(body)
	region := c.Region
	if region == "" {
		region = "us-east-1"
	}
	sigv4.Sign(r, c.AccessKey, c.SecretKey, region, time.Now(), hex.EncodeToString(sum[:]))
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		e := &Error{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Code == "" {
			e.Code = http.StatusText(resp.StatusCode)
			e.Message = strings.TrimSpace(string(data))
			if e.Message == "" {
				e.Message = fmt.Sprintf("%s %s failed", method, path)
			}
		}
		return e
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func esc(s string) string { return url.PathEscape(s) }

// Info returns server information.
func (c *Client) Info(ctx context.Context) (*ServerInfo, error) {
	var out ServerInfo
	return &out, c.do(ctx, http.MethodGet, "/info", nil, nil, &out)
}

// Health checks readiness.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/health", nil, nil, nil)
}

// --- users ---------------------------------------------------------------

// ListUsers lists users.
func (c *Client) ListUsers(ctx context.Context) ([]*UserInfo, error) {
	var out []*UserInfo
	return out, c.do(ctx, http.MethodGet, "/users", nil, nil, &out)
}

// GetUser returns one user.
func (c *Client) GetUser(ctx context.Context, name string) (*UserInfo, error) {
	var out UserInfo
	return &out, c.do(ctx, http.MethodGet, "/users/"+esc(name), nil, nil, &out)
}

// PutUser creates or updates a user.
func (c *Client) PutUser(ctx context.Context, name string, in PutUserRequest) (*UserInfo, error) {
	var out UserInfo
	return &out, c.do(ctx, http.MethodPut, "/users/"+esc(name), nil, in, &out)
}

// SetUserPassword sets a user's console password.
func (c *Client) SetUserPassword(ctx context.Context, name, password string) error {
	return c.do(ctx, http.MethodPut, "/users/"+esc(name)+"/password", nil, PasswordRequest{Password: password}, nil)
}

// ClearUserPassword removes a user's console password.
func (c *Client) ClearUserPassword(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/users/"+esc(name)+"/password", nil, nil, nil)
}

// DeleteUser removes a user and its keys.
func (c *Client) DeleteUser(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/users/"+esc(name), nil, nil, nil)
}

// SetUserStatus enables or disables a user.
func (c *Client) SetUserStatus(ctx context.Context, name string, enabled bool) (*UserInfo, error) {
	var out UserInfo
	return &out, c.do(ctx, http.MethodPost, "/users/"+esc(name)+"/"+statusPath(enabled), nil, nil, &out)
}

// SetUserPolicies replaces the policies attached to a user.
func (c *Client) SetUserPolicies(ctx context.Context, name string, policies []string) (*UserInfo, error) {
	var out UserInfo
	return &out, c.do(ctx, http.MethodPut, "/users/"+esc(name)+"/policies", nil, PoliciesRequest{Policies: policies}, &out)
}

func statusPath(enabled bool) string {
	if enabled {
		return "enable"
	}
	return "disable"
}

// --- keys ----------------------------------------------------------------

// ListKeys lists access keys, optionally of one user.
func (c *Client) ListKeys(ctx context.Context, user string) ([]KeyInfo, error) {
	q := url.Values{}
	if user != "" {
		q.Set("user", user)
	}
	var out []KeyInfo
	return out, c.do(ctx, http.MethodGet, "/keys", q, nil, &out)
}

// GetKey returns one access key record.
func (c *Client) GetKey(ctx context.Context, accessKey string) (*KeyInfo, error) {
	var out KeyInfo
	return &out, c.do(ctx, http.MethodGet, "/keys/"+esc(accessKey), nil, nil, &out)
}

// CreateKey creates an access key; the secret is only returned here.
func (c *Client) CreateKey(ctx context.Context, in CreateKeyRequest) (*Credentials, error) {
	var out Credentials
	return &out, c.do(ctx, http.MethodPost, "/keys", nil, in, &out)
}

// DeleteKey removes an access key.
func (c *Client) DeleteKey(ctx context.Context, accessKey string) error {
	return c.do(ctx, http.MethodDelete, "/keys/"+esc(accessKey), nil, nil, nil)
}

// SetKeyStatus enables or disables an access key.
func (c *Client) SetKeyStatus(ctx context.Context, accessKey string, enabled bool) (*KeyInfo, error) {
	var out KeyInfo
	return &out, c.do(ctx, http.MethodPost, "/keys/"+esc(accessKey)+"/"+statusPath(enabled), nil, nil, &out)
}

// RotateKey replaces the secret of an access key (generated if empty).
func (c *Client) RotateKey(ctx context.Context, accessKey, secret string) (*Credentials, error) {
	var out Credentials
	return &out, c.do(ctx, http.MethodPost, "/keys/"+esc(accessKey)+"/rotate", nil, RotateKeyRequest{SecretKey: secret}, &out)
}

// --- groups --------------------------------------------------------------

// ListGroups lists groups.
func (c *Client) ListGroups(ctx context.Context) ([]*GroupInfo, error) {
	var out []*GroupInfo
	return out, c.do(ctx, http.MethodGet, "/groups", nil, nil, &out)
}

// GetGroup returns one group.
func (c *Client) GetGroup(ctx context.Context, name string) (*GroupInfo, error) {
	var out GroupInfo
	return &out, c.do(ctx, http.MethodGet, "/groups/"+esc(name), nil, nil, &out)
}

// PutGroup creates or updates a group.
func (c *Client) PutGroup(ctx context.Context, name string, in PutGroupRequest) (*GroupInfo, error) {
	var out GroupInfo
	return &out, c.do(ctx, http.MethodPut, "/groups/"+esc(name), nil, in, &out)
}

// DeleteGroup removes a group.
func (c *Client) DeleteGroup(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/groups/"+esc(name), nil, nil, nil)
}

// UpdateGroupMembers adds and removes members.
func (c *Client) UpdateGroupMembers(ctx context.Context, name string, add, remove []string) (*GroupInfo, error) {
	var out GroupInfo
	return &out, c.do(ctx, http.MethodPost, "/groups/"+esc(name)+"/members", nil, GroupMembersRequest{Add: add, Remove: remove}, &out)
}

// --- policies ------------------------------------------------------------

// ListPolicies lists named policies (without documents).
func (c *Client) ListPolicies(ctx context.Context) ([]*PolicyInfo, error) {
	var out []*PolicyInfo
	return out, c.do(ctx, http.MethodGet, "/policies", nil, nil, &out)
}

// GetPolicy returns a policy with its document.
func (c *Client) GetPolicy(ctx context.Context, name string) (*PolicyInfo, error) {
	var out PolicyInfo
	return &out, c.do(ctx, http.MethodGet, "/policies/"+esc(name), nil, nil, &out)
}

// PutPolicy creates or replaces a policy from a raw JSON document.
func (c *Client) PutPolicy(ctx context.Context, name string, doc json.RawMessage) (*PolicyInfo, error) {
	var out PolicyInfo
	return &out, c.do(ctx, http.MethodPut, "/policies/"+esc(name), nil, doc, &out)
}

// DeletePolicy removes a policy, detaching it from users and groups.
func (c *Client) DeletePolicy(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/policies/"+esc(name), nil, nil, nil)
}

// --- buckets -------------------------------------------------------------

// ListBuckets lists buckets; usage adds object count and bytes.
func (c *Client) ListBuckets(ctx context.Context, usage bool) ([]BucketInfo, error) {
	q := url.Values{}
	if usage {
		q.Set("usage", "true")
	}
	var out []BucketInfo
	return out, c.do(ctx, http.MethodGet, "/buckets", q, nil, &out)
}

// DeleteBucket removes a bucket; force also deletes its contents.
func (c *Client) DeleteBucket(ctx context.Context, name string, force bool) error {
	q := url.Values{}
	if force {
		q.Set("force", "true")
	}
	return c.do(ctx, http.MethodDelete, "/buckets/"+esc(name), q, nil, nil)
}

// --- kms -----------------------------------------------------------------

// ListKMSKeys lists named KMS keys.
func (c *Client) ListKMSKeys(ctx context.Context) ([]KMSKeyInfo, error) {
	var out []KMSKeyInfo
	return out, c.do(ctx, http.MethodGet, "/kms/keys", nil, nil, &out)
}

// CreateKMSKey creates a named KMS key.
func (c *Client) CreateKMSKey(ctx context.Context, id string) (*KMSKeyInfo, error) {
	var out KMSKeyInfo
	return &out, c.do(ctx, http.MethodPost, "/kms/keys", nil, CreateKMSKeyRequest{ID: id}, &out)
}

// DeleteKMSKey deletes a named KMS key. Objects encrypted with it become
// unreadable.
func (c *Client) DeleteKMSKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/kms/keys/"+esc(id), nil, nil, nil)
}
