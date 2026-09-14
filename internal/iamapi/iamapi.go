// Package iamapi implements the AWS IAM Query API (the protocol behind
// `aws iam ...`, boto3's iam client and Terraform's IAM resources) over
// the OpenS3 identity store, so standard AWS tooling can manage users,
// access keys, groups, policies and console passwords ("login profiles").
package iamapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/iam"
)

// Namespace is the IAM API XML namespace and version.
const (
	Namespace = "https://iam.amazonaws.com/doc/2010-05-08/"
	Version   = "2010-05-08"
)

// Handler serves IAM actions.
type Handler struct {
	IAM *iam.Store
	Log *slog.Logger
}

// Request carries one parsed Query-API call.
type Request struct {
	W         http.ResponseWriter
	R         *http.Request
	Form      url.Values
	Identity  *iam.Identity // nil = anonymous
	RequestID string
}

// Error is an IAM API error.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errNoSuchEntity(what string) *Error {
	return &Error{http.StatusNotFound, "NoSuchEntity", what}
}
func errExists(msg string) *Error       { return &Error{http.StatusConflict, "EntityAlreadyExists", msg} }
func errConflict(msg string) *Error     { return &Error{http.StatusConflict, "DeleteConflict", msg} }
func errValidation(msg string) *Error   { return &Error{http.StatusBadRequest, "ValidationError", msg} }
func errInvalidInput(msg string) *Error { return &Error{http.StatusBadRequest, "InvalidInput", msg} }
func errMalformedPolicy(msg string) *Error {
	return &Error{http.StatusBadRequest, "MalformedPolicyDocument", msg}
}
func errAccessDenied(action string) *Error {
	return &Error{http.StatusForbidden, "AccessDenied", "User is not authorized to perform: " + action}
}
func errAccessDeniedMsg(msg string) *Error { return &Error{http.StatusForbidden, "AccessDenied", msg} }

// Actions lists the supported IAM actions.
var actions = map[string]func(*Handler, *Request) (any, error){
	"CreateUser": (*Handler).createUser, "GetUser": (*Handler).getUser, "ListUsers": (*Handler).listUsers, "DeleteUser": (*Handler).deleteUser,
	"UpdateUser":      (*Handler).updateUser,
	"CreateAccessKey": (*Handler).createAccessKey, "ListAccessKeys": (*Handler).listAccessKeys, "UpdateAccessKey": (*Handler).updateAccessKey,
	"DeleteAccessKey": (*Handler).deleteAccessKey,
	"CreateGroup":     (*Handler).createGroup, "GetGroup": (*Handler).getGroup, "ListGroups": (*Handler).listGroups, "DeleteGroup": (*Handler).deleteGroup,
	"AddUserToGroup": (*Handler).addUserToGroup, "RemoveUserFromGroup": (*Handler).removeUserFromGroup, "ListGroupsForUser": (*Handler).listGroupsForUser,
	"CreatePolicy": (*Handler).createPolicy, "GetPolicy": (*Handler).getPolicy, "GetPolicyVersion": (*Handler).getPolicyVersion,
	"ListPolicies": (*Handler).listPolicies, "DeletePolicy": (*Handler).deletePolicy,
	"AttachUserPolicy": (*Handler).attachUserPolicy, "DetachUserPolicy": (*Handler).detachUserPolicy, "ListAttachedUserPolicies": (*Handler).listAttachedUserPolicies,
	"AttachGroupPolicy": (*Handler).attachGroupPolicy, "DetachGroupPolicy": (*Handler).detachGroupPolicy, "ListAttachedGroupPolicies": (*Handler).listAttachedGroupPolicies,
	"CreateLoginProfile": (*Handler).createLoginProfile, "UpdateLoginProfile": (*Handler).updateLoginProfile, "DeleteLoginProfile": (*Handler).deleteLoginProfile,
	"GetLoginProfile": (*Handler).getLoginProfile, "ChangePassword": (*Handler).changePassword,
	"GetAccountSummary": (*Handler).getAccountSummary,
	// Roles and identity providers do not exist here; list them as empty so
	// clean-up loops in AWS tooling (and the s3-tests teardown) pass.
	"ListRoles": (*Handler).listRoles, "ListOpenIDConnectProviders": (*Handler).listOIDCProviders, "ListSAMLProviders": (*Handler).listSAMLProviders,
}

// Supported reports whether action is an IAM action this package serves.
func Supported(action string) bool { _, ok := actions[action]; return ok }

// Serve dispatches one call and writes the response.
func (h *Handler) Serve(req *Request) {
	action := req.Form.Get("Action")
	fn, ok := actions[action]
	if !ok {
		h.writeError(req, &Error{http.StatusBadRequest, "InvalidAction", "Could not find operation " + action + " for version " + Version})
		return
	}
	if req.Identity == nil {
		h.writeError(req, errAccessDenied("iam:"+action))
		return
	}
	out, err := fn(h, req)
	if err != nil {
		h.writeError(req, toError(err))
		return
	}
	h.writeResult(req, action, out)
}

// authorize checks iam:<Action> against the caller's identity policies.
func (h *Handler) authorize(req *Request, action string) error {
	if !h.IAM.Authorize(iam.Request{Identity: req.Identity, Action: action, Conditions: map[string][]string{}}) {
		return errAccessDenied(action)
	}
	return nil
}

func toError(err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	switch {
	case iam.IsNotFound(err):
		return errNoSuchEntity(err.Error())
	case iam.IsExists(err):
		return errExists(err.Error())
	case iam.IsInvalid(err):
		return errValidation(strings.TrimPrefix(err.Error(), "iam: invalid argument: "))
	case iam.IsBuiltin(err), iam.IsRoot(err):
		return errInvalidInput(err.Error())
	}
	return &Error{http.StatusInternalServerError, "ServiceFailure", "internal error"}
}

// --- XML ---------------------------------------------------------------------

type responseMetadata struct {
	RequestID string `xml:"RequestId"`
}

func (h *Handler) writeResult(req *Request, action string, result any) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<` + action + `Response xmlns="` + Namespace + `">`)
	if result != nil {
		enc := xml.NewEncoder(&buf)
		// Results are anonymous structs; name the element after the action.
		if err := enc.EncodeElement(result, xml.StartElement{Name: xml.Name{Local: action + "Result"}}); err != nil {
			h.Log.Error("iam api encode", "action", action, "err", err)
			h.writeError(req, &Error{http.StatusInternalServerError, "ServiceFailure", "internal error"})
			return
		}
		if err := enc.Flush(); err != nil {
			return
		}
	}
	buf.WriteString(`<ResponseMetadata><RequestId>` + req.RequestID + `</RequestId></ResponseMetadata></` + action + `Response>`)
	req.W.Header().Set("Content-Type", "text/xml")
	req.W.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	req.W.WriteHeader(http.StatusOK)
	req.W.Write(buf.Bytes())
}

func (h *Handler) writeError(req *Request, e *Error) {
	type errBody struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Xmlns   string   `xml:"xmlns,attr"`
		Error   struct {
			Type    string `xml:"Type"`
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
		RequestID string `xml:"RequestId"`
	}
	var b errBody
	b.Xmlns = Namespace
	b.Error.Type, b.Error.Code, b.Error.Message = "Sender", e.Code, e.Message
	if e.Status >= 500 {
		b.Error.Type = "Receiver"
	}
	b.RequestID = req.RequestID
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	xml.NewEncoder(&buf).Encode(b)
	req.W.Header().Set("Content-Type", "text/xml")
	req.W.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	req.W.WriteHeader(e.Status)
	req.W.Write(buf.Bytes())
}

// --- shapes ------------------------------------------------------------------

type xmlUser struct {
	Path       string `xml:"Path"`
	UserName   string `xml:"UserName"`
	UserID     string `xml:"UserId"`
	Arn        string `xml:"Arn"`
	CreateDate string `xml:"CreateDate"`
}

type xmlGroup struct {
	Path       string `xml:"Path"`
	GroupName  string `xml:"GroupName"`
	GroupID    string `xml:"GroupId"`
	Arn        string `xml:"Arn"`
	CreateDate string `xml:"CreateDate"`
}

type xmlAccessKey struct {
	UserName        string `xml:"UserName"`
	AccessKeyID     string `xml:"AccessKeyId"`
	Status          string `xml:"Status"`
	SecretAccessKey string `xml:"SecretAccessKey,omitempty"`
	CreateDate      string `xml:"CreateDate"`
}

type xmlPolicy struct {
	PolicyName                    string `xml:"PolicyName"`
	PolicyID                      string `xml:"PolicyId"`
	Arn                           string `xml:"Arn"`
	Path                          string `xml:"Path"`
	DefaultVersionID              string `xml:"DefaultVersionId"`
	AttachmentCount               int    `xml:"AttachmentCount"`
	PermissionsBoundaryUsageCount int    `xml:"PermissionsBoundaryUsageCount"`
	IsAttachable                  bool   `xml:"IsAttachable"`
	CreateDate                    string `xml:"CreateDate"`
	UpdateDate                    string `xml:"UpdateDate"`
}

type xmlAttachedPolicy struct {
	PolicyName string `xml:"PolicyName"`
	PolicyArn  string `xml:"PolicyArn"`
}

type xmlLoginProfile struct {
	UserName              string `xml:"UserName"`
	CreateDate            string `xml:"CreateDate"`
	PasswordResetRequired bool   `xml:"PasswordResetRequired"`
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// entityID derives a stable 21-character identifier for a name.
func entityID(kind, name string) string {
	sum := sha256.Sum256([]byte(kind + ":" + name))
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])[:21]
}

func (h *Handler) userARN(name string) string {
	return "arn:aws:iam::" + h.IAM.AccountID() + ":user/" + name
}
func (h *Handler) groupARN(name string) string {
	return "arn:aws:iam::" + h.IAM.AccountID() + ":group/" + name
}
func (h *Handler) policyARN(p *iam.Policy) string {
	if p.BuiltIn {
		return "arn:aws:iam::aws:policy/" + p.Name
	}
	return "arn:aws:iam::" + h.IAM.AccountID() + ":policy/" + p.Name
}

// policyNameFromARN accepts our ARNs (account or aws-managed) and, for
// convenience, a bare policy name.
func (h *Handler) policyNameFromARN(arn string) (string, error) {
	if arn == "" {
		return "", errValidation("PolicyArn is required")
	}
	if !strings.HasPrefix(arn, "arn:") {
		return arn, nil
	}
	for _, p := range []string{"arn:aws:iam::" + h.IAM.AccountID() + ":policy/", "arn:aws:iam::aws:policy/"} {
		if strings.HasPrefix(arn, p) {
			return strings.TrimPrefix(arn, p), nil
		}
	}
	return "", errNoSuchEntity("Policy " + arn + " does not exist or is not attachable.")
}

// --- pagination ----------------------------------------------------------------

type page struct {
	IsTruncated bool
	Marker      string
}

// paginate applies Marker/MaxItems to a sorted list of names and returns
// the slice to render plus the page marker.
func paginate(req *Request, names []string) ([]string, page) {
	sort.Strings(names)
	marker := req.Form.Get("Marker")
	if marker != "" {
		i := sort.SearchStrings(names, marker)
		if i < len(names) && names[i] == marker {
			i++
		}
		names = names[i:]
	}
	max := 100
	if v := req.Form.Get("MaxItems"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 1000 {
			max = n
		}
	}
	if len(names) > max {
		return names[:max], page{IsTruncated: true, Marker: names[max-1]}
	}
	return names, page{}
}

// urlEncode percent-encodes everything but RFC 3986 unreserved characters
// (how AWS returns policy documents).
func urlEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func requireParam(req *Request, name string) (string, error) {
	v := strings.TrimSpace(req.Form.Get(name))
	if v == "" {
		return "", errValidation("1 validation error detected: Value at '" + lowerFirst(name) + "' failed to satisfy constraint: Member must not be null")
	}
	return v, nil
}

// pathOf normalises an IAM path parameter ("/" when empty).
func pathOf(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// matchesPathPrefix applies the PathPrefix list filter.
func matchesPathPrefix(req *Request, path string) bool {
	pfx := req.Form.Get("PathPrefix")
	return pfx == "" || strings.HasPrefix(pathOf(path), pfx)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func (h *Handler) listRoles(req *Request) (any, error) {
	if err := h.authorize(req, "iam:ListRoles"); err != nil {
		return nil, err
	}
	return struct {
		Roles       []struct{} `xml:"Roles>member"`
		IsTruncated bool       `xml:"IsTruncated"`
	}{[]struct{}{}, false}, nil
}

func (h *Handler) listOIDCProviders(req *Request) (any, error) {
	if err := h.authorize(req, "iam:ListOpenIDConnectProviders"); err != nil {
		return nil, err
	}
	return struct {
		List []struct{} `xml:"OpenIDConnectProviderList>member"`
	}{[]struct{}{}}, nil
}

func (h *Handler) listSAMLProviders(req *Request) (any, error) {
	if err := h.authorize(req, "iam:ListSAMLProviders"); err != nil {
		return nil, err
	}
	return struct {
		List []struct{} `xml:"SAMLProviderList>member"`
	}{[]struct{}{}}, nil
}
