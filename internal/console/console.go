// Package console is the embedded web console: a small JSON API over the
// internal services plus a static single-page application, both served
// under /console/. It never goes through the S3 HTTP API; it talks to the
// IAM store, the object service and the KMS directly and authorises every
// action with the same policy engine the S3 API uses.
//
// Security model (see docs/CONSOLE.md): a login exchanges an access key
// and secret for temporary STS credentials (iam.Store.AssumeRole) which
// are kept in an HttpOnly, SameSite=Strict cookie. Every request resolves
// the session again, so disabling a user or deleting the session key
// takes effect immediately. Mutating requests must carry the custom
// header X-OpenS3-Console: 1 and must not be cross-origin.
package console

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/edward-b-1/OpenS3/internal/iam"
	"github.com/edward-b-1/OpenS3/internal/kms"
	"github.com/edward-b-1/OpenS3/internal/meta"
	"github.com/edward-b-1/OpenS3/internal/object"
	"github.com/edward-b-1/OpenS3/internal/s3err"
)

//go:embed static
var staticFS embed.FS

const (
	// Prefix is the URL prefix the console is mounted at.
	Prefix = "/console/"
	// CookieName holds the session credentials.
	CookieName = "opens3_console"
	// CSRFHeader must be present on every mutating request.
	CSRFHeader = "X-OpenS3-Console"
	// SessionDuration is the lifetime of a console login.
	SessionDuration = 12 * time.Hour
	maxJSONBody     = 1 << 20
)

// Deps are the services the console uses.
type Deps struct {
	IAM     *iam.Store
	Obj     *object.Service
	KMS     *kms.Local
	Log     *slog.Logger
	Region  string
	Version string
}

// Handler serves the console.
type Handler struct {
	d       Deps
	mux     *http.ServeMux
	static  fs.FS
	started time.Time
	etagMu  sync.Mutex
	etags   map[string]string
}

// New builds the console handler.
func New(d Deps) *Handler {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Version == "" {
		d.Version = "dev"
	}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	h := &Handler{d: d, mux: http.NewServeMux(), static: sub, started: time.Now()}
	h.routes()
	return h
}

// session is the authenticated caller of one request.
type session struct {
	id        *iam.Identity
	accessKey string
}

func (s *session) actor() object.Actor {
	return object.Actor{CanonicalID: s.id.CanonicalID(), DisplayName: s.id.Name()}
}

type handlerFunc func(w http.ResponseWriter, r *http.Request, s *session) error

func (h *Handler) routes() {
	api := func(pattern string, f handlerFunc) {
		h.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s, err := h.authenticate(r)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			if err := f(w, r, s); err != nil {
				h.fail(w, r, err)
			}
		})
	}
	h.mux.HandleFunc("POST /console/api/login", h.login)
	api("POST /console/api/logout", h.logout)
	api("GET /console/api/me", h.me)
	api("GET /console/api/info", h.info)

	api("GET /console/api/buckets", h.listBuckets)
	api("POST /console/api/buckets", h.createBucket)
	api("GET /console/api/buckets/{bucket}", h.getBucket)
	api("DELETE /console/api/buckets/{bucket}", h.deleteBucket)
	api("PUT /console/api/buckets/{bucket}/versioning", h.putVersioning)
	api("PUT /console/api/buckets/{bucket}/tags", h.putBucketTags)
	api("PUT /console/api/buckets/{bucket}/encryption", h.putBucketEncryption)
	api("GET /console/api/buckets/{bucket}/policy", h.getPolicy)
	api("PUT /console/api/buckets/{bucket}/policy", h.putPolicy)
	api("DELETE /console/api/buckets/{bucket}/policy", h.deletePolicy)

	api("GET /console/api/buckets/{bucket}/objects", h.listObjects)
	api("GET /console/api/buckets/{bucket}/object", h.headObject)
	api("GET /console/api/buckets/{bucket}/download", h.download)
	api("PUT /console/api/buckets/{bucket}/upload", h.upload)
	api("POST /console/api/buckets/{bucket}/upload", h.upload)
	api("POST /console/api/buckets/{bucket}/delete", h.deleteObjects)
	api("POST /console/api/buckets/{bucket}/delete-prefix", h.deletePrefix)
	api("GET /console/api/buckets/{bucket}/zip", h.zipPrefix)

	api("GET /console/api/users", h.listUsers)
	api("POST /console/api/users", h.createUser)
	api("GET /console/api/users/{name}", h.getUser)
	api("PATCH /console/api/users/{name}", h.updateUser)
	api("PUT /console/api/users/{name}/password", h.setUserPassword)
	api("DELETE /console/api/users/{name}/password", h.clearUserPassword)
	api("PUT /console/api/me/password", h.changeMyPassword)
	api("DELETE /console/api/users/{name}", h.deleteUser)
	api("GET /console/api/keys", h.listKeys)
	api("POST /console/api/keys", h.createKey)
	api("PATCH /console/api/keys/{ak}", h.updateKey)
	api("DELETE /console/api/keys/{ak}", h.deleteKey)
	api("GET /console/api/groups", h.listGroups)
	api("POST /console/api/groups", h.createGroup)
	api("PATCH /console/api/groups/{name}", h.updateGroup)
	api("DELETE /console/api/groups/{name}", h.deleteGroup)
	api("GET /console/api/policies", h.listPolicies)
	api("GET /console/api/policies/{name}", h.getIAMPolicy)
	api("PUT /console/api/policies/{name}", h.putIAMPolicy)
	api("DELETE /console/api/policies/{name}", h.deleteIAMPolicy)
	api("GET /console/api/kms/keys", h.listKMSKeys)
	api("POST /console/api/kms/keys", h.createKMSKey)
	api("DELETE /console/api/kms/keys/{id}", h.deleteKMSKey)

	h.mux.HandleFunc("/console/api/", func(w http.ResponseWriter, r *http.Request) {
		h.fail(w, r, apiErr(http.StatusNotFound, "NotFound", "no such console API endpoint"))
	})
	h.mux.HandleFunc("/console/", h.serveStatic)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/console" {
		http.Redirect(w, r, Prefix, http.StatusMovedPermanently)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	if strings.HasPrefix(r.URL.Path, "/console/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if isMutating(r.Method) {
			if err := checkCSRF(r); err != nil {
				h.fail(w, r, err)
				return
			}
		}
	}
	h.mux.ServeHTTP(w, r)
}

func isMutating(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// checkCSRF enforces the custom header and rejects cross-origin callers.
// Browsers always send Sec-Fetch-Site; for cross-site requests they also
// send Origin. The custom header cannot be set by a cross-origin form.
func checkCSRF(r *http.Request) error {
	if r.Header.Get(CSRFHeader) != "1" {
		return apiErr(http.StatusForbidden, "MissingCSRFHeader", "the "+CSRFHeader+" header is required on mutating requests")
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return apiErr(http.StatusForbidden, "CrossOrigin", "cross-origin console requests are not allowed")
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return apiErr(http.StatusForbidden, "CrossOrigin", "cross-origin console requests are not allowed")
		}
	}
	return nil
}

// --- static ----------------------------------------------------------------

func (h *Handler) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, Prefix)
	if name == "" || strings.HasSuffix(name, "/") {
		name = "index.html"
	}
	name = path.Clean(name)
	f, err := h.static.Open(name)
	if err != nil {
		// Unknown paths (hash routes never reach here, but deep links might)
		// get the app shell.
		name = "index.html"
		if f, err = h.static.Open(name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if name == "index.html" {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'")
	}
	// Every asset revalidates on each load (a conditional request answered
	// with 304 when unchanged) so an upgraded binary is picked up on the
	// next page load without a hard reload.
	w.Header().Set("Cache-Control", "no-cache")
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if tag := h.etag(name, rs); tag != "" {
		w.Header().Set("ETag", tag)
	}
	http.ServeContent(w, r, name, h.started, rs)
}

// etag returns a content hash for an embedded asset, computed once.
func (h *Handler) etag(name string, rs io.ReadSeeker) string {
	h.etagMu.Lock()
	defer h.etagMu.Unlock()
	if h.etags == nil {
		h.etags = map[string]string{}
	}
	if t, ok := h.etags[name]; ok {
		return t
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, rs); err != nil {
		return ""
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	t := `"` + hex.EncodeToString(sum.Sum(nil)[:16]) + `"`
	h.etags[name] = t
	return t
}

// --- sessions --------------------------------------------------------------

type loginRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
	// Legacy field names: a client sending an access key pair gets a
	// pointed error rather than a silent failure.
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if err := readJSON(r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	if in.User == "" && in.AccessKey != "" {
		h.fail(w, r, apiErr(http.StatusBadRequest, "AccessKeyNotAccepted", accessKeyNotAcceptedMsg))
		return
	}
	bad := apiErr(http.StatusUnauthorized, "InvalidCredentials", "invalid user name or password")
	if in.User == "" || in.Password == "" {
		h.fail(w, r, bad)
		return
	}
	id, err := h.d.IAM.VerifyPassword(in.User, in.Password)
	if err != nil {
		h.d.Log.Info("console login failed", "user", in.User)
		if looksLikeKeyPair(in.User, in.Password) {
			// An API key pair pasted into the form: say why it is refused
			// rather than "invalid user name or password". Decided on the
			// shape of the input alone, never on whether such a key exists.
			bad = apiErr(http.StatusUnauthorized, "AccessKeyNotAccepted", accessKeyNotAcceptedMsg)
		}
		h.fail(w, r, bad)
		return
	}
	ak, _, token, exp, err := h.d.IAM.AssumeRole(id, nil, SessionDuration)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: ak + "." + token, Path: Prefix, Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isTLS(r)})
	h.d.Log.Info("console login", "user", id.Name(), "session", ak)
	s := &session{id: h.sessionIdentity(id, ak), accessKey: ak}
	writeJSON(w, http.StatusOK, h.meInfo(s, exp))
}

const accessKeyNotAcceptedMsg = "the console signs in with a user name and console password; access keys work only with the S3 API"

// looksLikeKeyPair reports whether user and password have the shape of a
// generated access key pair (iam.GenerateAccessKey / GenerateSecretKey): a
// 20-character upper-case alphanumeric ID and a 40-character secret.
func looksLikeKeyPair(user, password string) bool {
	if len(user) != iam.AccessKeyLength || len(password) != iam.SecretKeyLength {
		return false
	}
	for _, c := range user {
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, s *session) error {
	_ = h.d.IAM.DeleteKey(s.accessKey)
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: Prefix, MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isTLS(r)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// authenticate resolves the session cookie.
func (h *Handler) authenticate(r *http.Request) (*session, error) {
	unauth := apiErr(http.StatusUnauthorized, "NotLoggedIn", "log in to use the console")
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, unauth
	}
	ak, token, ok := strings.Cut(c.Value, ".")
	if !ok {
		return nil, unauth
	}
	id, err := h.d.IAM.Resolve(ak, token)
	if err != nil {
		return nil, unauth
	}
	if id.Key == nil || id.Key.Kind != iam.KindSTS {
		return nil, unauth
	}
	return &session{id: h.sessionIdentity(id, ak), accessKey: ak}, nil
}

// sessionIdentity normalises the identity for a console session. Sessions
// issued to the root account act as root (the IAM store resolves them to
// the synthetic "root" user carrying consoleAdmin), so that root keeps
// its bucket-policy escape hatch in the console too. A session policy
// (inherited from a service account) keeps the identity narrowed.
func (h *Handler) sessionIdentity(id *iam.Identity, ak string) *iam.Identity {
	if !id.IsRoot && id.User != nil && id.User.Name == "root" && len(id.SessionPolicy) == 0 {
		cp := *id
		cp.IsRoot = true
		return &cp
	}
	return id
}

func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

type meResponse struct {
	User        string     `json:"user"`
	ARN         string     `json:"arn"`
	CanonicalID string     `json:"canonicalId"`
	IsRoot      bool       `json:"isRoot"`
	Admin       bool       `json:"admin"`
	Policies    []string   `json:"policies"`
	Groups      []string   `json:"groups"`
	Expires     *time.Time `json:"expires,omitempty"`
	Region      string     `json:"region"`
	Version     string     `json:"version"`
}

func (h *Handler) meInfo(s *session, exp time.Time) meResponse {
	m := meResponse{User: s.id.Name(), ARN: s.id.ARN(), CanonicalID: s.id.CanonicalID(), IsRoot: s.id.IsRoot, Region: h.d.Region, Version: h.d.Version,
		Admin: h.allowedAdmin(s, "opens3:ServerInfo") && h.allowedAdmin(s, "iam:ListUsers"), Policies: []string{}, Groups: []string{}}
	if s.id.User != nil {
		m.Policies = append(m.Policies, s.id.User.Policies...)
		m.Groups = append(m.Groups, s.id.User.Groups...)
	}
	if !exp.IsZero() {
		m.Expires = &exp
	} else if s.id.Key != nil && s.id.Key.Expires != nil {
		m.Expires = s.id.Key.Expires
	}
	return m
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request, s *session) error {
	writeJSON(w, http.StatusOK, h.meInfo(s, time.Time{}))
	return nil
}

// --- authorisation ---------------------------------------------------------

// authorize evaluates an S3 action against bucket (and object) state.
func (h *Handler) authorize(s *session, action, bucket, key string, b *meta.Bucket, o *meta.Object, cond map[string][]string) error {
	if cond == nil {
		cond = map[string][]string{}
	}
	req := iam.Request{Identity: s.id, Action: action, Bucket: bucket, Key: key, Conditions: cond}
	if b != nil {
		req.BucketOwner, req.BucketPolicy, req.BucketACL, req.PublicAccessBlock, req.Ownership = b.Owner, b.Policy, b.ACL, b.PublicAccessBlock, b.Ownership
	}
	if o != nil {
		req.ObjectOwner, req.ObjectACL = o.Owner, o.ACL
		for _, t := range o.Tags {
			cond["s3:existingobjecttag/"+strings.ToLower(t.Key)] = []string{t.Value}
		}
	}
	if !h.d.IAM.Authorize(req) {
		return apiErr(http.StatusForbidden, "AccessDenied", "you are not allowed to "+action+describe(bucket, key))
	}
	return nil
}

func describe(bucket, key string) string {
	switch {
	case bucket == "":
		return ""
	case key == "":
		return " on bucket " + bucket
	default:
		return " on " + bucket + "/" + key
	}
}

// admin authorises an administrative (iam/kms/opens3) action; identity policies only.
func (h *Handler) admin(s *session, action string) error {
	if !h.allowedAdmin(s, action) {
		return apiErr(http.StatusForbidden, "AccessDenied", "administrative permission "+action+" is required")
	}
	return nil
}

func (h *Handler) allowedAdmin(s *session, action string) bool {
	return h.d.IAM.Authorize(iam.Request{Identity: s.id, Action: action, Conditions: map[string][]string{}})
}

// --- JSON helpers ----------------------------------------------------------

// apiError is an error with an HTTP status and a code for the UI.
type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func apiErr(status int, code, msg string) *apiError {
	return &apiError{Status: status, Code: code, Message: msg}
}

func badRequest(msg string) *apiError { return apiErr(http.StatusBadRequest, "InvalidRequest", msg) }

// toAPIError maps service errors to HTTP.
func toAPIError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var se *s3err.Error
	if errors.As(err, &se) {
		return &apiError{Status: se.Status, Code: string(se.Code), Message: se.Message}
	}
	switch {
	case errors.Is(err, iam.ErrNotFound), errors.Is(err, kms.ErrKeyNotFound):
		return apiErr(http.StatusNotFound, "NotFound", err.Error())
	case errors.Is(err, iam.ErrExists), errors.Is(err, kms.ErrKeyExists):
		return apiErr(http.StatusConflict, "AlreadyExists", err.Error())
	case errors.Is(err, iam.ErrInvalid), errors.Is(err, kms.ErrInvalidKey):
		return apiErr(http.StatusBadRequest, "InvalidRequest", err.Error())
	case errors.Is(err, iam.ErrBuiltin), errors.Is(err, iam.ErrRoot):
		return apiErr(http.StatusForbidden, "ReadOnly", err.Error())
	case errors.Is(err, iam.ErrDisabled), errors.Is(err, iam.ErrExpired), errors.Is(err, iam.ErrBadToken):
		return apiErr(http.StatusUnauthorized, "NotLoggedIn", err.Error())
	}
	return nil
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	ae := toAPIError(err)
	if ae == nil {
		h.d.Log.Error("console request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		ae = apiErr(http.StatusInternalServerError, "InternalError", "internal error")
	}
	writeJSON(w, ae.Status, map[string]any{"error": ae})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

func tagsOut(tags []meta.Tag) []map[string]string {
	out := make([]map[string]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, map[string]string{"key": t.Key, "value": t.Value})
	}
	return out
}

func tagsIn(in []struct{ Key, Value string }) []meta.Tag {
	out := make([]meta.Tag, 0, len(in))
	for _, t := range in {
		out = append(out, meta.Tag{Key: t.Key, Value: t.Value})
	}
	return out
}

func strs(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
