package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/auth/sigv4"
	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// maxBody bounds admin request bodies (policy documents are small).
const maxBody = 4 << 20

// Options configures the handler.
type Options struct {
	IAM           *iam.Store
	KMS           *kms.Local
	Obj           *object.Service
	Region        string
	EnforceRegion bool
	Log           *slog.Logger
}

// Handler serves the admin API. Mount it at Prefix + "/".
type Handler struct {
	opt     Options
	started time.Time
	mux     *http.ServeMux
}

// New builds the handler.
func New(opt Options) *Handler {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	h := &Handler{opt: opt, started: time.Now().UTC(), mux: http.NewServeMux()}
	h.routes()
	return h
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// req is one authenticated admin request.
type req struct {
	w    http.ResponseWriter
	r    *http.Request
	id   *iam.Identity
	body []byte
}

func (c *req) path(name string) string { return c.r.PathValue(name) }

// decode parses the JSON body into v (an empty body is an empty object).
func (c *req) decode(v any) error {
	if len(c.body) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(c.body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &Error{Status: http.StatusBadRequest, Code: "InvalidRequest", Message: "malformed JSON body: " + err.Error()}
	}
	return nil
}

type handlerFunc func(c *req) (any, error)

// routes registers every endpoint. The operation name is the AWS-style
// IAM action required to call it.
func (h *Handler) routes() {
	p := Prefix
	reg := func(pattern, op string, fn handlerFunc) {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+p+path, h.op(op, fn))
	}
	reg("GET /info", "opens3:ServerInfo", h.info)
	reg("GET /health", "opens3:Health", h.health)

	reg("GET /users", "iam:ListUsers", h.listUsers)
	reg("GET /users/{name}", "iam:GetUser", h.getUser)
	reg("PUT /users/{name}", "iam:CreateUser", h.putUser)
	reg("DELETE /users/{name}", "iam:DeleteUser", h.deleteUser)
	reg("POST /users/{name}/enable", "iam:UpdateUser", h.setUserStatus(true))
	reg("POST /users/{name}/disable", "iam:UpdateUser", h.setUserStatus(false))
	reg("PUT /users/{name}/policies", "iam:AttachUserPolicy", h.setUserPolicies)
	reg("PUT /users/{name}/password", "iam:UpdateLoginProfile", h.setUserPassword)
	reg("DELETE /users/{name}/password", "iam:UpdateLoginProfile", h.clearUserPassword)

	reg("GET /keys", "iam:ListAccessKeys", h.listKeys)
	reg("POST /keys", "", h.createKey) // authorised in the handler, on the target user
	reg("GET /keys/{ak}", "iam:ListAccessKeys", h.getKey)
	reg("DELETE /keys/{ak}", "iam:DeleteAccessKey", h.deleteKey)
	reg("POST /keys/{ak}/enable", "iam:UpdateAccessKey", h.setKeyStatus(true))
	reg("POST /keys/{ak}/disable", "iam:UpdateAccessKey", h.setKeyStatus(false))
	reg("POST /keys/{ak}/rotate", "iam:UpdateAccessKey", h.rotateKey)

	reg("GET /groups", "iam:ListGroups", h.listGroups)
	reg("GET /groups/{name}", "iam:GetGroup", h.getGroup)
	reg("PUT /groups/{name}", "iam:CreateGroup", h.putGroup)
	reg("DELETE /groups/{name}", "iam:DeleteGroup", h.deleteGroup)
	reg("POST /groups/{name}/members", "iam:AddUserToGroup", h.groupMembers)

	reg("GET /policies", "iam:ListPolicies", h.listPolicies)
	reg("GET /policies/{name}", "iam:GetPolicy", h.getPolicy)
	reg("PUT /policies/{name}", "iam:CreatePolicy", h.putPolicy)
	reg("DELETE /policies/{name}", "iam:DeletePolicy", h.deletePolicy)

	reg("GET /buckets", "s3:ListAllMyBuckets", h.listBuckets)
	reg("DELETE /buckets/{name}", "s3:DeleteBucket", h.deleteBucket)

	reg("GET /kms/keys", "kms:ListKeys", h.listKMSKeys)
	reg("POST /kms/keys", "kms:CreateKey", h.createKMSKey)
	reg("DELETE /kms/keys/{id}", "kms:ScheduleKeyDeletion", h.deleteKMSKey)

	// Unknown paths still require authentication so the API cannot be
	// probed anonymously.
	h.mux.HandleFunc(p+"/", func(w http.ResponseWriter, r *http.Request) {
		if _, err := h.authenticate(w, r); err != nil {
			writeErr(w, err)
			return
		}
		writeErr(w, &Error{Status: http.StatusNotFound, Code: "NotFound", Message: "no such admin endpoint"})
	})
}

// resourceFor derives the IAM resource ARN an admin route acts on from
// its path: the user, group, policy or key target. Routes without a
// target (lists, info) evaluate against the account.
func (h *Handler) resourceFor(r *http.Request) string {
	acct := h.opt.IAM.AccountID()
	p := r.URL.Path
	switch {
	case strings.Contains(p, "/users/"):
		if n := r.PathValue("name"); n != "" {
			return iam.UserARN(acct, n)
		}
	case strings.Contains(p, "/groups/"):
		if n := r.PathValue("name"); n != "" {
			return iam.GroupARN(acct, n)
		}
	case strings.Contains(p, "/policies/"):
		if n := r.PathValue("name"); n != "" {
			return iam.PolicyARN(acct, n, h.opt.IAM.IsBuiltInPolicy(n))
		}
	case strings.Contains(p, "/keys/"):
		if ak := r.PathValue("ak"); ak != "" {
			if k, err := h.opt.IAM.GetKey(ak); err == nil {
				return iam.UserARN(acct, k.User)
			}
		}
	case strings.Contains(p, "/kms/keys/"):
		if id := r.PathValue("id"); id != "" {
			return iam.KMSKeyARN(acct, id)
		}
	}
	return ""
}

// op wraps an endpoint with authentication, authorisation and JSON
// encoding. An empty action means the handler authorises itself (it
// needs the request body to know the target).
func (h *Handler) op(name string, fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := h.authenticate(w, r)
		if err != nil {
			writeErr(w, err)
			return
		}
		if name != "" && !h.opt.IAM.Authorize(iam.Request{Identity: c.id, Action: name, Resource: h.resourceFor(r)}) {
			h.opt.Log.Warn("admin: denied", "op", name, "user", c.id.Name())
			writeErr(w, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: "not authorised for " + name})
			return
		}
		out, err := fn(c)
		if err != nil {
			e := mapErr(err)
			if e.Status >= 500 {
				h.opt.Log.Error("admin: internal error", "op", name, "err", err)
			}
			writeErr(w, e)
			return
		}
		h.opt.Log.Debug("admin", "op", name, "user", c.id.Name())
		status := http.StatusOK
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			if _, ok := out.(*Credentials); ok {
				status = http.StatusCreated
			}
		}
		if out == nil {
			out = Status{Status: "ok"}
		}
		writeJSON(w, status, out)
	}
}

// authenticate verifies the SigV4 signature (header or presigned query),
// checks the payload hash against the body and resolves the identity.
// Anonymous requests are rejected: every admin operation needs a caller.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (*req, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, &Error{Status: http.StatusBadRequest, Code: "IncompleteBody", Message: "reading request body: " + err.Error()}
	}
	if len(body) > maxBody {
		return nil, &Error{Status: http.StatusRequestEntityTooLarge, Code: "EntityTooLarge", Message: "request body too large"}
	}
	p, err := sigv4.ParseRequest(r)
	if errors.Is(err, sigv4.ErrMissingAuth) {
		return nil, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: "admin API requires AWS Signature Version 4 authentication"}
	}
	if err != nil {
		return nil, sigErr(err)
	}
	opt := sigv4.Options{}
	if h.opt.EnforceRegion {
		opt.Region = h.opt.Region
	}
	if _, err := sigv4.Verify(r, p, h.opt.IAM.LookupSecret, opt); err != nil {
		return nil, sigErr(err)
	}
	// Payload integrity: a non-empty body must be covered by the signature.
	switch p.ContentSHA256 {
	case sigv4.UnsignedPayload, "":
		if len(body) > 0 {
			return nil, &Error{Status: http.StatusBadRequest, Code: "InvalidRequest", Message: "admin requests with a body must sign the payload (x-amz-content-sha256)"}
		}
	default:
		if len(p.ContentSHA256) != 64 {
			return nil, &Error{Status: http.StatusBadRequest, Code: "InvalidArgument", Message: "x-amz-content-sha256 must be a hex-encoded SHA256 value"}
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != strings.ToLower(p.ContentSHA256) {
			return nil, &Error{Status: http.StatusBadRequest, Code: "XAmzContentSHA256Mismatch", Message: "the provided x-amz-content-sha256 does not match the request body"}
		}
	}
	id, err := h.opt.IAM.Resolve(p.AccessKey, p.SessionToken)
	if err != nil {
		return nil, sigErr(err)
	}
	return &req{w: w, r: r, id: id, body: body}, nil
}

// sigErr maps signature/credential failures to a 403 with the S3 code
// name, so callers see the same vocabulary as on the S3 API.
func sigErr(err error) *Error {
	code, msg := "AccessDenied", "authentication failed"
	switch {
	case errors.Is(err, sigv4.ErrUnknownAccessKey), errors.Is(err, iam.ErrNotFound):
		code, msg = "InvalidAccessKeyId", "the access key does not exist"
	case errors.Is(err, iam.ErrDisabled):
		code, msg = "InvalidAccessKeyId", "the access key is disabled"
	case errors.Is(err, iam.ErrExpired):
		code, msg = "ExpiredToken", "the credentials have expired"
	case errors.Is(err, iam.ErrBadToken):
		code, msg = "InvalidToken", "the session token is invalid"
	case errors.Is(err, sigv4.ErrSignatureMismatch), errors.Is(err, sigv4.ErrHostNotSigned):
		code, msg = "SignatureDoesNotMatch", "the request signature does not match"
	case errors.Is(err, sigv4.ErrTimeSkew):
		code, msg = "RequestTimeTooSkewed", "the request time is too skewed"
	case errors.Is(err, sigv4.ErrExpired):
		code, msg = "AccessDenied", "the presigned request has expired"
	case errors.Is(err, sigv4.ErrBadRegion):
		code, msg = "AuthorizationHeaderMalformed", "the credential scope region is wrong"
	case errors.Is(err, sigv4.ErrBadService):
		code, msg = "AuthorizationHeaderMalformed", "the credential scope service is wrong"
	case errors.Is(err, sigv4.ErrMissingContentHash):
		code, msg = "InvalidRequest", "missing x-amz-content-sha256 header"
	case errors.Is(err, sigv4.ErrMissingDate):
		code, msg = "AccessDenied", "missing x-amz-date header"
	case errors.Is(err, sigv4.ErrMalformed), errors.Is(err, sigv4.ErrMalformedQuery), errors.Is(err, sigv4.ErrBadExpiry):
		code, msg = "AuthorizationHeaderMalformed", "the authorization header or query is malformed"
	}
	return &Error{Status: http.StatusForbidden, Code: code, Message: msg}
}

// mapErr converts store errors into API errors.
func mapErr(err error) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	var se *s3err.Error
	if errors.As(err, &se) {
		return &Error{Status: se.Status, Code: string(se.Code), Message: se.Message}
	}
	switch {
	case errors.Is(err, iam.ErrNotFound), errors.Is(err, kms.ErrKeyNotFound), errors.Is(err, kv.ErrNotFound):
		return &Error{Status: http.StatusNotFound, Code: "NotFound", Message: err.Error()}
	case errors.Is(err, iam.ErrExists), errors.Is(err, kms.ErrKeyExists):
		return &Error{Status: http.StatusConflict, Code: "AlreadyExists", Message: err.Error()}
	case errors.Is(err, iam.ErrInvalid), errors.Is(err, kms.ErrInvalidKey), errors.Is(err, iam.ErrBuiltin), errors.Is(err, iam.ErrRoot):
		return &Error{Status: http.StatusBadRequest, Code: "InvalidArgument", Message: err.Error()}
	}
	return &Error{Status: http.StatusInternalServerError, Code: "InternalError", Message: "internal error"}
}

func invalid(format string, args ...any) error {
	return &Error{Status: http.StatusBadRequest, Code: "InvalidArgument", Message: fmt.Sprintf(format, args...)}
}

// randomSecret generates a 40-character secret key.
func randomSecret() string {
	b := make([]byte, 30)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	e := mapErr(err)
	writeJSON(w, e.Status, e)
}

// --- info / health -------------------------------------------------------

func (h *Handler) info(c *req) (any, error) {
	ctx := c.r.Context()
	info := &ServerInfo{Version: Version, Region: h.opt.Region, StartTime: h.started, UptimeSeconds: int64(time.Since(h.started).Seconds())}
	err := h.opt.Obj.KV().View(func(tx kv.Txn) error {
		bs, err := meta.ListBuckets(tx)
		if err != nil {
			return err
		}
		info.Buckets = len(bs)
		for _, b := range bs {
			n, sz, err := bucketUsage(tx, b.Name)
			if err != nil {
				return err
			}
			info.Objects += n
			info.TotalBytes += sz
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if st, err := h.opt.Obj.Blobs().Stats(ctx); err == nil {
		info.Disk = DiskStats{TotalBytes: st.TotalBytes, FreeBytes: st.FreeBytes, UsedBytes: st.UsedBytes}
	} else {
		h.opt.Log.Warn("admin: blob stats", "err", err)
	}
	return info, nil
}

// bucketUsage counts object versions (delete markers excluded) and bytes.
func bucketUsage(tx kv.Txn, bucket string) (n, bytes int64, err error) {
	err = meta.ScanBucketObjects(tx, bucket, func(o *meta.Object) bool {
		if !o.DeleteMarker {
			n++
			bytes += o.Size
		}
		return true
	})
	return
}

func (h *Handler) health(c *req) (any, error) {
	if _, err := h.opt.Obj.Blobs().Stats(c.r.Context()); err != nil {
		return nil, &Error{Status: http.StatusServiceUnavailable, Code: "ServiceUnavailable", Message: err.Error()}
	}
	return Status{Status: "ok"}, nil
}

// --- users ---------------------------------------------------------------

func userInfo(u *iam.User) *UserInfo {
	return &UserInfo{Name: u.Name, Enabled: u.Enabled, Policies: nonNil(u.Policies), Groups: nonNil(u.Groups), Created: u.Created, HasPassword: u.HasPassword()}
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (h *Handler) listUsers(c *req) (any, error) {
	us, err := h.opt.IAM.ListUsers()
	if err != nil {
		return nil, err
	}
	out := make([]*UserInfo, 0, len(us))
	for _, u := range us {
		out = append(out, userInfo(u))
	}
	return out, nil
}

func (h *Handler) getUser(c *req) (any, error) {
	u, err := h.opt.IAM.GetUser(c.path("name"))
	if err != nil {
		return nil, err
	}
	return userInfo(u), nil
}

func checkNotRoot(name string) error {
	if name == "root" {
		return iam.ErrRoot
	}
	return nil
}

func (h *Handler) putUser(c *req) (any, error) {
	name := c.path("name")
	if err := checkNotRoot(name); err != nil {
		return nil, err
	}
	if err := iam.CheckCredentialIssuer(c.id); err != nil {
		return nil, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: err.Error()}
	}
	var in PutUserRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	st := h.opt.IAM
	var creds *Credentials
	if _, err := st.GetUser(name); errors.Is(err, iam.ErrNotFound) {
		if err := st.CreateUser(name, in.SecretKey, nonNil(in.Policies)); err != nil {
			return nil, err
		}
		if in.GenerateKey {
			k, secret, err := st.CreateKey(name, "", "", iam.KindUser, nil, nil, "")
			if err != nil {
				return nil, err
			}
			creds = &Credentials{KeyInfo: keyInfo(k), SecretKey: secret}
		}
	} else if err != nil {
		return nil, err
	} else {
		if in.Policies != nil {
			if err := st.UpdateUser(name, func(u *iam.User) error { u.Policies = in.Policies; return nil }); err != nil {
				return nil, err
			}
		}
		if in.SecretKey != "" {
			if _, err := st.GetKey(name); errors.Is(err, iam.ErrNotFound) {
				if _, _, err := st.CreateKey(name, name, in.SecretKey, iam.KindUser, nil, nil, ""); err != nil {
					return nil, err
				}
			} else if err != nil {
				return nil, err
			} else if err := st.SetSecret(name, in.SecretKey); err != nil {
				return nil, err
			}
		}
	}
	if in.Password != "" {
		if err := st.SetPassword(name, in.Password); err != nil {
			return nil, err
		}
	}
	u, err := st.GetUser(name)
	if err != nil {
		return nil, err
	}
	out := userInfo(u)
	out.Credentials = creds
	return out, nil
}

func (h *Handler) setUserPassword(c *req) (any, error) {
	name := c.path("name")
	if err := checkNotRoot(name); err != nil {
		return nil, err
	}
	if err := iam.CheckCredentialIssuer(c.id); err != nil {
		return nil, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: err.Error()}
	}
	var in PasswordRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	if err := h.opt.IAM.SetPassword(name, in.Password); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (h *Handler) clearUserPassword(c *req) (any, error) {
	name := c.path("name")
	if err := checkNotRoot(name); err != nil {
		return nil, err
	}
	if err := h.opt.IAM.ClearPassword(name); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (h *Handler) deleteUser(c *req) (any, error) {
	name := c.path("name")
	if err := checkNotRoot(name); err != nil {
		return nil, err
	}
	return nil, h.opt.IAM.DeleteUser(name)
}

func (h *Handler) setUserStatus(enabled bool) handlerFunc {
	return func(c *req) (any, error) {
		name := c.path("name")
		if err := checkNotRoot(name); err != nil {
			return nil, err
		}
		err := h.opt.IAM.UpdateUser(name, func(u *iam.User) error { u.Enabled = enabled; return nil })
		if err != nil {
			return nil, err
		}
		u, err := h.opt.IAM.GetUser(name)
		if err != nil {
			return nil, err
		}
		return userInfo(u), nil
	}
}

func (h *Handler) setUserPolicies(c *req) (any, error) {
	name := c.path("name")
	if err := checkNotRoot(name); err != nil {
		return nil, err
	}
	var in PoliciesRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	err := h.opt.IAM.UpdateUser(name, func(u *iam.User) error { u.Policies = nonNil(in.Policies); return nil })
	if err != nil {
		return nil, err
	}
	u, err := h.opt.IAM.GetUser(name)
	if err != nil {
		return nil, err
	}
	return userInfo(u), nil
}

// --- keys ----------------------------------------------------------------

func keyInfo(k *iam.Key) KeyInfo {
	return KeyInfo{AccessKey: k.AccessKey, User: k.User, Kind: k.Kind, Enabled: k.Enabled, SessionPolicy: k.SessionPolicy,
		Expires: k.Expires, Description: k.Description, Created: k.Created}
}

func (h *Handler) listKeys(c *req) (any, error) {
	ks, err := h.opt.IAM.ListKeys(c.r.URL.Query().Get("user"))
	if err != nil {
		return nil, err
	}
	out := make([]KeyInfo, 0, len(ks))
	for _, k := range ks {
		out = append(out, keyInfo(k))
	}
	return out, nil
}

func (h *Handler) getKey(c *req) (any, error) {
	k, err := h.opt.IAM.GetKey(c.path("ak"))
	if err != nil {
		return nil, err
	}
	return keyInfo(k), nil
}

func (h *Handler) createKey(c *req) (any, error) {
	if err := iam.CheckCredentialIssuer(c.id); err != nil {
		return nil, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: err.Error()}
	}
	var in CreateKeyRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	if in.User == "" {
		return nil, invalid("user is required")
	}
	if !h.opt.IAM.Authorize(iam.Request{Identity: c.id, Action: "iam:CreateAccessKey", Resource: iam.UserARN(h.opt.IAM.AccountID(), in.User)}) {
		return nil, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: "not authorised for iam:CreateAccessKey on user " + in.User}
	}
	if in.Kind == "" {
		in.Kind = iam.KindUser
	}
	if in.Kind != iam.KindUser && in.Kind != iam.KindService {
		return nil, invalid("kind must be %q or %q", iam.KindUser, iam.KindService)
	}
	if in.Expires != nil && in.Expires.Before(time.Now()) {
		return nil, invalid("expires is in the past")
	}
	k, secret, err := h.opt.IAM.CreateKey(in.User, in.AccessKey, in.SecretKey, in.Kind, in.SessionPolicy, in.Expires, in.Description)
	if err != nil {
		return nil, err
	}
	return &Credentials{KeyInfo: keyInfo(k), SecretKey: secret}, nil
}

func (h *Handler) deleteKey(c *req) (any, error) {
	return nil, h.opt.IAM.DeleteKey(c.path("ak"))
}

func (h *Handler) setKeyStatus(enabled bool) handlerFunc {
	return func(c *req) (any, error) {
		ak := c.path("ak")
		if err := h.opt.IAM.UpdateKey(ak, func(k *iam.Key) error { k.Enabled = enabled; return nil }); err != nil {
			return nil, err
		}
		k, err := h.opt.IAM.GetKey(ak)
		if err != nil {
			return nil, err
		}
		return keyInfo(k), nil
	}
}

func (h *Handler) rotateKey(c *req) (any, error) {
	if err := iam.CheckCredentialIssuer(c.id); err != nil {
		return nil, &Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: err.Error()}
	}
	ak := c.path("ak")
	var in RotateKeyRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	secret := in.SecretKey
	if secret == "" {
		secret = randomSecret()
	}
	if err := h.opt.IAM.SetSecret(ak, secret); err != nil {
		return nil, err
	}
	k, err := h.opt.IAM.GetKey(ak)
	if err != nil {
		return nil, err
	}
	return &Credentials{KeyInfo: keyInfo(k), SecretKey: secret}, nil
}

// --- groups --------------------------------------------------------------

func groupInfo(g *iam.Group) *GroupInfo {
	return &GroupInfo{Name: g.Name, Enabled: g.Enabled, Members: nonNil(g.Members), Policies: nonNil(g.Policies), Created: g.Created}
}

func (h *Handler) listGroups(c *req) (any, error) {
	gs, err := h.opt.IAM.ListGroups()
	if err != nil {
		return nil, err
	}
	out := make([]*GroupInfo, 0, len(gs))
	for _, g := range gs {
		out = append(out, groupInfo(g))
	}
	return out, nil
}

func (h *Handler) getGroup(c *req) (any, error) {
	g, err := h.opt.IAM.GetGroup(c.path("name"))
	if err != nil {
		return nil, err
	}
	return groupInfo(g), nil
}

func (h *Handler) putGroup(c *req) (any, error) {
	name := c.path("name")
	var in PutGroupRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	st := h.opt.IAM
	if _, err := st.GetGroup(name); errors.Is(err, iam.ErrNotFound) {
		if err := st.CreateGroup(name, nonNil(in.Members), nonNil(in.Policies)); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if err := st.UpdateGroup(name, func(g *iam.Group) error {
		if in.Members != nil {
			g.Members = in.Members
		}
		if in.Policies != nil {
			g.Policies = in.Policies
		}
		return nil
	}); err != nil {
		return nil, err
	}
	g, err := st.GetGroup(name)
	if err != nil {
		return nil, err
	}
	return groupInfo(g), nil
}

func (h *Handler) deleteGroup(c *req) (any, error) {
	return nil, h.opt.IAM.DeleteGroup(c.path("name"))
}

func (h *Handler) groupMembers(c *req) (any, error) {
	name := c.path("name")
	var in GroupMembersRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	err := h.opt.IAM.UpdateGroup(name, func(g *iam.Group) error {
		set := map[string]bool{}
		for _, m := range g.Members {
			set[m] = true
		}
		for _, m := range in.Add {
			set[m] = true
		}
		for _, m := range in.Remove {
			delete(set, m)
		}
		g.Members = g.Members[:0]
		for m := range set {
			g.Members = append(g.Members, m)
		}
		sort.Strings(g.Members)
		return nil
	})
	if err != nil {
		return nil, err
	}
	g, err := h.opt.IAM.GetGroup(name)
	if err != nil {
		return nil, err
	}
	return groupInfo(g), nil
}

// --- policies ------------------------------------------------------------

func policyInfo(p *iam.Policy, withDoc bool) *PolicyInfo {
	pi := &PolicyInfo{Name: p.Name, BuiltIn: p.BuiltIn, Created: p.Created, Updated: p.Updated}
	if withDoc {
		pi.Document = p.Document
	}
	return pi
}

func (h *Handler) listPolicies(c *req) (any, error) {
	ps, err := h.opt.IAM.ListPolicies()
	if err != nil {
		return nil, err
	}
	out := make([]*PolicyInfo, 0, len(ps))
	for _, p := range ps {
		out = append(out, policyInfo(p, false))
	}
	return out, nil
}

func (h *Handler) getPolicy(c *req) (any, error) {
	p, err := h.opt.IAM.GetPolicy(c.path("name"))
	if err != nil {
		return nil, err
	}
	return policyInfo(p, true), nil
}

func (h *Handler) putPolicy(c *req) (any, error) {
	name := c.path("name")
	if !json.Valid(c.body) {
		return nil, invalid("policy document is not valid JSON")
	}
	if err := h.opt.IAM.PutPolicy(name, json.RawMessage(c.body)); err != nil {
		return nil, err
	}
	p, err := h.opt.IAM.GetPolicy(name)
	if err != nil {
		return nil, err
	}
	return policyInfo(p, true), nil
}

func (h *Handler) deletePolicy(c *req) (any, error) {
	return nil, h.opt.IAM.DeletePolicy(c.path("name"))
}

// --- buckets -------------------------------------------------------------

func (h *Handler) listBuckets(c *req) (any, error) {
	usage := isTrue(c.r.URL.Query().Get("usage"))
	var out []BucketInfo
	err := h.opt.Obj.KV().View(func(tx kv.Txn) error {
		bs, err := meta.ListBuckets(tx)
		if err != nil {
			return err
		}
		out = make([]BucketInfo, 0, len(bs))
		for _, b := range bs {
			bi := BucketInfo{Name: b.Name, Created: b.Created, Owner: b.OwnerDisplay, Versioning: b.Versioning, ObjectLock: b.ObjectLockEnabled}
			if bi.Owner == "" {
				bi.Owner = b.Owner
			}
			if usage {
				n, sz, err := bucketUsage(tx, b.Name)
				if err != nil {
					return err
				}
				bi.Objects, bi.Bytes = &n, &sz
			}
			out = append(out, bi)
		}
		return nil
	})
	return out, err
}

func (h *Handler) deleteBucket(c *req) (any, error) {
	name := c.path("name")
	ctx := c.r.Context()
	if isTrue(c.r.URL.Query().Get("force")) {
		return nil, h.opt.Obj.DeleteBucketForce(ctx, name)
	}
	return nil, h.opt.Obj.DeleteBucket(ctx, name)
}

func isTrue(s string) bool {
	s = strings.ToLower(s)
	return s == "true" || s == "1" || s == "yes"
}

// --- kms -----------------------------------------------------------------

func (h *Handler) listKMSKeys(c *req) (any, error) {
	ks, err := h.opt.KMS.ListKeys()
	if err != nil {
		return nil, err
	}
	out := make([]KMSKeyInfo, 0, len(ks))
	for _, k := range ks {
		out = append(out, KMSKeyInfo{ID: k.ID, Created: k.Created, Disabled: k.Disabled})
	}
	return out, nil
}

func (h *Handler) createKMSKey(c *req) (any, error) {
	var in CreateKMSKeyRequest
	if err := c.decode(&in); err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, invalid("id is required")
	}
	if err := h.opt.KMS.CreateKey(in.ID); err != nil {
		return nil, err
	}
	ks, err := h.opt.KMS.ListKeys()
	if err != nil {
		return nil, err
	}
	for _, k := range ks {
		if k.ID == in.ID {
			return KMSKeyInfo{ID: k.ID, Created: k.Created, Disabled: k.Disabled}, nil
		}
	}
	return KMSKeyInfo{ID: in.ID}, nil
}

func (h *Handler) deleteKMSKey(c *req) (any, error) {
	return nil, h.opt.KMS.DeleteKey(c.path("id"))
}
