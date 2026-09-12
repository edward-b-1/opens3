package s3api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gitlab.com/Birdsall/opens3/internal/s3err"
)

// sts handles the STS AssumeRole family posted to the root path with
// Action=... form parameters (as MinIO does).
func (s *Server) sts(c *reqCtx) error {
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
	if action != "AssumeRole" {
		return s.stsError(c, "InvalidAction", "The action "+action+" is not valid for this web service.")
	}
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
	out.Xmlns = "https://sts.amazonaws.com/doc/2011-06-15/"
	out.Result.Credentials.AccessKeyId, out.Result.Credentials.SecretAccessKey, out.Result.Credentials.SessionToken = ak, sk, tok
	out.Result.Credentials.Expiration = exp.UTC().Format(time.RFC3339)
	out.Result.AssumedRoleUser.Arn = "arn:aws:sts::" + s.iam.AccountID() + ":assumed-role/" + c.identity.Name() + "/" + form.Get("RoleSessionName")
	out.Result.AssumedRoleUser.AssumedRoleId = c.identity.CanonicalID()[:20] + ":" + form.Get("RoleSessionName")
	out.ResponseMetadata.RequestId = c.id
	return s.writeXML(c, http.StatusOK, out)
}

func (s *Server) stsError(c *reqCtx, code, msg string) error {
	var e xmlSTSError
	e.Xmlns = "https://sts.amazonaws.com/doc/2011-06-15/"
	e.Error.Type, e.Error.Code, e.Error.Message = "Sender", code, msg
	e.RequestId = c.id
	status := http.StatusBadRequest
	if code == "AccessDenied" {
		status = http.StatusForbidden
	}
	return s.writeXML(c, status, e)
}

var _ = s3err.AccessDenied
