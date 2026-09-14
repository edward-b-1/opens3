package s3api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/edward-b-1/OpenS3/internal/iamapi"
)

const stsNS = "https://sts.amazonaws.com/doc/2011-06-15/"

// awsQuery serves the AWS Query-protocol APIs that share the S3 endpoint:
// STS (AssumeRole, GetCallerIdentity) and IAM (users, keys, groups,
// policies, login profiles). Requests are SigV4-signed POSTs to "/" with
// form-encoded Action=... parameters.
func (s *Server) awsQuery(c *reqCtx) error {
	r := c.r
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	form, perr := url.ParseQuery(string(raw))
	if perr != nil {
		return s.stsError(c, "InvalidParameterValue", "malformed form")
	}
	for k, v := range r.URL.Query() {
		if _, ok := form[k]; !ok {
			form[k] = v
		}
	}
	action := form.Get("Action")
	switch action {
	case "AssumeRole":
		return s.assumeRole(c, form)
	case "GetCallerIdentity":
		return s.getCallerIdentity(c)
	}
	if iamapi.Supported(action) {
		s.iamAPI.Serve(&iamapi.Request{W: c.w, R: r, Form: form, Identity: c.identity, RequestID: c.id})
		return nil
	}
	return s.stsError(c, "InvalidAction", "The action "+action+" is not valid for this web service.")
}

func (s *Server) assumeRole(c *reqCtx, form url.Values) error {
	if c.identity == nil {
		return s.stsError(c, "AccessDenied", "Access Denied")
	}
	dur := time.Hour
	if v := form.Get("DurationSeconds"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 900 || n > 604800 {
			return s.stsError(c, "ValidationError", "DurationSeconds must be between 900 and 604800")
		}
		dur = time.Duration(n) * time.Second
	}
	var pol json.RawMessage
	if p := form.Get("Policy"); p != "" {
		pol = json.RawMessage(p)
	}
	ak, sk, tok, exp, err := s.iam.AssumeRole(c.identity, pol, dur)
	if err != nil {
		return s.stsError(c, "MalformedPolicyDocument", err.Error())
	}
	var out xmlAssumeRoleResponse
	out.Xmlns = stsNS
	out.Result.Credentials.AccessKeyId, out.Result.Credentials.SecretAccessKey, out.Result.Credentials.SessionToken = ak, sk, tok
	out.Result.Credentials.Expiration = exp.UTC().Format(time.RFC3339)
	out.Result.AssumedRoleUser.Arn = "arn:aws:sts::" + s.iam.AccountID() + ":assumed-role/" + c.identity.Name() + "/" + form.Get("RoleSessionName")
	out.Result.AssumedRoleUser.AssumedRoleId = c.identity.CanonicalID()[:20] + ":" + form.Get("RoleSessionName")
	out.ResponseMetadata.RequestId = c.id
	return s.writeXML(c, http.StatusOK, out)
}

// getCallerIdentity is the first thing most people run: `aws sts get-caller-identity`.
func (s *Server) getCallerIdentity(c *reqCtx) error {
	if c.identity == nil {
		return s.stsError(c, "AccessDenied", "Access Denied")
	}
	out := xmlGetCallerIdentityResponse{Xmlns: stsNS}
	out.Result.Arn = c.identity.ARN()
	out.Result.UserId = c.identity.CanonicalID()[:21]
	out.Result.Account = s.iam.AccountID()
	out.ResponseMetadata.RequestId = c.id
	return s.writeXML(c, http.StatusOK, out)
}

func (s *Server) stsError(c *reqCtx, code, msg string) error {
	var e xmlSTSError
	e.Xmlns = stsNS
	e.Error.Type, e.Error.Code, e.Error.Message = "Sender", code, msg
	e.RequestId = c.id
	status := http.StatusBadRequest
	switch code {
	case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken", "RequestTimeTooSkewed":
		status = http.StatusForbidden
	case "InternalError":
		status = http.StatusInternalServerError
	}
	return s.writeXML(c, status, e)
}
