package s3api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/auth/sigv4"
	"github.com/edward-b-1/opens3/internal/checksum"
	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/policy"
	"github.com/edward-b-1/opens3/internal/s3err"
)

const maxPostFields = 20 << 10

// postObject implements browser-based uploads (POST Object) with a SigV4
// policy document.
func (s *Server) postObject(c *reqCtx) error {
	r := c.r
	ct := r.Header.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || mt != "multipart/form-data" {
		return s3err.New(s3err.RequestIsNotMultiPartContent)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	fields := map[string]string{}
	var filePart *multipart.Part
	var fileName string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return s3err.New(s3err.MalformedPOSTRequest)
		}
		name := p.FormName()
		if strings.EqualFold(name, "file") {
			filePart = p
			fileName = p.FileName()
			break // "file" must be the last field
		}
		v, err := io.ReadAll(io.LimitReader(p, maxPostFields+1))
		if err != nil || len(v) > maxPostFields {
			return s3err.New(s3err.MaxPostPreDataLengthExceededError)
		}
		fields[strings.ToLower(name)] = string(v)
	}
	if filePart == nil {
		return s3err.New(s3err.IncorrectNumberOfFilesInPostRequest).WithMessage("POST requires exactly one file upload per request.")
	}
	key := fields["key"]
	if key == "" {
		return s3err.New(s3err.UserKeyMustBeSpecified).WithMessage("Bucket POST must contain a field named 'key'.  If it is specified, please check the order of the fields.")
	}
	key = strings.ReplaceAll(key, "${filename}", fileName)
	fields["key"] = key
	c.key = key

	// Authentication via policy.
	policyB64 := fields["policy"]
	if policyB64 != "" {
		var err error
		if fields["awsaccesskeyid"] != "" && fields["x-amz-credential"] == "" {
			err = s.verifyPostPolicyV2(c, fields, policyB64)
		} else {
			err = s.verifyPostPolicy(c, fields, policyB64)
		}
		if err != nil {
			return err
		}
	} else {
		c.identity = nil
	}
	// Authorisation.
	req := s.authzRequest(c, "s3:PutObject")
	req.Key = key
	if !s.iam.Authorize(req) {
		return errAccessDenied()
	}
	// Validate the policy conditions against the fields.
	var pol postPolicy
	if policyB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(policyB64)
		if err != nil {
			return s3err.New(s3err.InvalidPolicyDocument)
		}
		if err := json.Unmarshal(raw, &pol); err != nil {
			return s3err.New(s3err.InvalidPolicyDocument)
		}
		// Element names are case-sensitive and both are required.
		var top map[string]json.RawMessage
		if err := json.Unmarshal(raw, &top); err != nil || top["expiration"] == nil || top["conditions"] == nil {
			return s3err.New(s3err.InvalidPolicyDocument).WithMessage("Invalid Policy: Invalid or missing 'expiration' or 'conditions' element")
		}
		if len(pol.Conditions) == 0 {
			return s3err.New(s3err.InvalidPolicyDocument).WithMessage("Invalid Policy: 'conditions' must not be empty")
		}
		exp, err := time.Parse("2006-01-02T15:04:05.000Z", pol.Expiration)
		if err != nil {
			exp, err = time.Parse(time.RFC3339, pol.Expiration)
		}
		if err != nil {
			return s3err.New(s3err.InvalidPolicyDocument).WithMessage("Invalid Policy: Invalid 'expiration' value: '%s'", pol.Expiration)
		}
		if time.Now().After(exp) {
			return s3err.New(s3err.AccessDenied).WithMessage("Invalid according to Policy: Policy expired.")
		}
	}
	// Attributes from fields.
	attrs := object.ObjectAttrs{ContentType: fields["content-type"], ContentEncoding: fields["content-encoding"], ContentDisposition: fields["content-disposition"],
		CacheControl: fields["cache-control"], Expires: fields["expires"], WebsiteRedirect: fields["x-amz-website-redirect-location"], StorageClass: fields["x-amz-storage-class"]}
	for k, v := range fields {
		if strings.HasPrefix(k, "x-amz-meta-") {
			if attrs.UserMeta == nil {
				attrs.UserMeta = map[string]string{}
			}
			attrs.UserMeta[k[len("x-amz-meta-"):]] = v
		}
	}
	if t := fields["tagging"]; t != "" {
		var tg xmlTagging
		if err := xmlUnmarshal([]byte(t), &tg); err != nil {
			return errMalformedXML()
		}
		attrs.Tags = tagsFromXML(&tg)
	}
	if acl := fields["acl"]; acl != "" && c.bkt.Ownership != "BucketOwnerEnforced" {
		a, ok := cannedACL(acl, c.actor().CanonicalID, c.actor().DisplayName, c.bkt.Owner, c.bkt.OwnerDisplay)
		if !ok {
			return errInvalidArg("invalid acl")
		}
		attrs.ACL = a
	}
	var sseReq object.SSERequest
	if v := fields["x-amz-server-side-encryption"]; v != "" {
		sseReq.Type = v
		sseReq.KMSKeyID = fields["x-amz-server-side-encryption-aws-kms-key-id"]
	}
	if alg := fields["x-amz-server-side-encryption-customer-algorithm"]; alg != "" {
		if alg != "AES256" {
			return s3err.New(s3err.InvalidEncryptionAlgorithmError)
		}
		ck, err := base64.StdEncoding.DecodeString(fields["x-amz-server-side-encryption-customer-key"])
		if err != nil || len(ck) != 32 {
			return errInvalidArg("The secret key was invalid for the specified algorithm.")
		}
		sseReq = object.SSERequest{Type: "SSE-C", CustomerKey: ck, CustomerKeyMD5: fields["x-amz-server-side-encryption-customer-key-md5"]}
	}
	var cr *object.ChecksumRequest
	for _, alg := range []string{"CRC32", "CRC32C", "SHA1", "SHA256", "CRC64NVME"} {
		if v := fields[strings.ToLower(checksum.HeaderName(alg))]; v != "" {
			cr = &object.ChecksumRequest{Algorithm: alg, Value: v}
		}
	}
	// Stream the file, enforcing content-length-range and policy conditions.
	var minLen, maxLen int64 = 0, object.MaxObjectSize
	for _, cond := range pol.Conditions {
		if l, ok := cond.([]any); ok {
			if op, _ := l[0].(string); op == "content-length-range" {
				if len(l) != 3 {
					return s3err.New(s3err.InvalidPolicyDocument).WithMessage("Invalid Policy: content-length-range requires a minimum and a maximum")
				}
				minLen = toInt64(l[1])
				maxLen = toInt64(l[2])
			}
		}
	}
	if policyB64 != "" {
		if err := checkPostConditions(pol.Conditions, fields, c.bucket); err != nil {
			return err
		}
	}
	var md5hex string
	if v := fields["content-md5"]; v != "" {
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(b) != 16 {
			return s3err.New(s3err.InvalidDigest)
		}
		md5hex = hex.EncodeToString(b)
	}
	cnt := &countingReader{r: io.LimitReader(filePart, maxLen+1)}
	o, err := s.obj.PutObject(r.Context(), c.actor(), object.PutInput{Bucket: c.bucket, Key: key, Body: cnt, Size: -1, ExpectedMD5: md5hex,
		SSE: sseReq, Attrs: attrs, Checksum: cr, Event: "s3:ObjectCreated:Post"})
	if err != nil {
		return err
	}
	if cnt.n > maxLen || cnt.n < minLen {
		s.obj.DeleteObject(r.Context(), c.actor(), object.DeleteInput{Bucket: c.bucket, Key: key, VersionID: o.VersionID})
		if cnt.n > maxLen {
			return s3err.New(s3err.EntityTooLarge)
		}
		return s3err.New(s3err.EntityTooSmall)
	}
	h := c.w.Header()
	h.Set("ETag", quoteETag(o.ETag))
	if c.bkt.Versioning != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	loc := schemeOf(r) + "://" + r.Host + "/" + c.bucket + "/" + key
	h.Set("Location", loc)
	if redir := fields["success_action_redirect"]; redir != "" {
		u := redir
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "bucket=" + c.bucket + "&key=" + key + "&etag=" + quoteETag(o.ETag)
		h.Set("Location", u)
		c.w.WriteHeader(http.StatusSeeOther)
		return nil
	}
	status := http.StatusNoContent
	switch fields["success_action_status"] {
	case "200":
		status = http.StatusOK
	case "201":
		return s.writeXML(c, http.StatusCreated, xmlPostResponse{Location: loc, Bucket: c.bucket, Key: key, ETag: quoteETag(o.ETag)})
	}
	c.w.WriteHeader(status)
	return nil
}

type postPolicy struct {
	Expiration string `json:"expiration"`
	Conditions []any  `json:"conditions"`
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// verifyPostPolicy checks the SigV4 signature of the policy and resolves
// the identity.
func (s *Server) verifyPostPolicy(c *reqCtx, fields map[string]string, policyB64 string) error {
	cred := fields["x-amz-credential"]
	sig := fields["x-amz-signature"]
	if cred == "" || sig == "" || fields["x-amz-algorithm"] != sigv4.Algorithm {
		return s3err.New(s3err.InvalidArgument).WithMessage("POST requires x-amz-algorithm, x-amz-credential and x-amz-signature fields")
	}
	parts := strings.Split(cred, "/")
	if len(parts) != 5 {
		return s3err.New(s3err.AuthorizationHeaderMalformed)
	}
	secret, err := s.iam.LookupSecret(parts[0])
	if err != nil {
		return mapSigErr(err)
	}
	want := sigv4.SignPostPolicy(policyB64, secret, parts[1], parts[2])
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return s3err.New(s3err.SignatureDoesNotMatch)
	}
	if d, err := time.Parse("20060102T150405Z", fields["x-amz-date"]); err == nil {
		c.sigAge = time.Since(d)
	}
	id, err := s.iam.Resolve(parts[0], fields["x-amz-security-token"])
	if err != nil {
		return mapSigErr(err)
	}
	c.identity = id
	c.authType = "POST"
	c.sigVer = sigv4.Algorithm
	return nil
}

// verifyPostPolicyV2 checks the legacy (Signature Version 2) POST form:
// AWSAccessKeyId plus signature = base64(HMAC-SHA1(secret, policy)).
func (s *Server) verifyPostPolicyV2(c *reqCtx, fields map[string]string, policyB64 string) error {
	ak, sig := fields["awsaccesskeyid"], fields["signature"]
	if ak == "" || sig == "" {
		return s3err.New(s3err.InvalidArgument).WithMessage("POST requires AWSAccessKeyId and signature fields")
	}
	secret, err := s.iam.LookupSecret(ak)
	if err != nil {
		return mapSigErr(err)
	}
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(policyB64))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return s3err.New(s3err.SignatureDoesNotMatch)
	}
	id, err := s.iam.Resolve(ak, fields["x-amz-security-token"])
	if err != nil {
		return mapSigErr(err)
	}
	c.identity = id
	c.authType = "POST"
	c.sigVer = "AWS"
	return nil
}

// checkPostConditions validates the submitted fields against the policy.
func checkPostConditions(conds []any, fields map[string]string, bucket string) error {
	fail := func(msg string) error {
		return s3err.New(s3err.AccessDenied).WithMessage("Invalid according to Policy: %s", msg)
	}
	checked := map[string]bool{"policy": true, "x-amz-signature": true, "file": true}
	for _, cond := range conds {
		switch v := cond.(type) {
		case map[string]any:
			if len(v) == 0 {
				return s3err.New(s3err.InvalidPolicyDocument).WithMessage("Invalid Policy: empty condition")
			}
			for k, val := range v {
				k = strings.ToLower(k)
				checked[k] = true
				got := fields[k]
				if k == "bucket" {
					got = bucket
				}
				if got != toString(val) {
					return fail("Policy Condition failed: [\"eq\", \"$" + k + "\", \"" + toString(val) + "\"]")
				}
			}
		case []any:
			if len(v) != 3 {
				return s3err.New(s3err.InvalidPolicyDocument).WithMessage("Invalid Policy: malformed condition")
			}
			op, _ := v[0].(string)
			field := strings.ToLower(strings.TrimPrefix(toString(v[1]), "$"))
			checked[field] = true
			got := fields[field]
			if field == "bucket" {
				got = bucket
			}
			switch strings.ToLower(op) {
			case "eq":
				if got != toString(v[2]) {
					return fail("Policy Condition failed: [\"eq\", \"$" + field + "\", \"" + toString(v[2]) + "\"]")
				}
			case "starts-with":
				if !strings.HasPrefix(got, toString(v[2])) {
					return fail("Policy Condition failed: [\"starts-with\", \"$" + field + "\", \"" + toString(v[2]) + "\"]")
				}
			case "content-length-range":
			default:
				return fail("unknown condition " + op)
			}
		}
	}
	// Every submitted field (except a few) must be covered by a condition.
	for k := range fields {
		if checked[k] {
			continue
		}
		switch k {
		case "x-amz-algorithm", "x-amz-credential", "x-amz-date", "x-amz-security-token", "policy", "x-amz-signature", "awsaccesskeyid", "signature":
			continue
		}
		if strings.HasPrefix(k, "x-amz-checksum-") {
			continue
		}
		if strings.HasPrefix(k, "x-ignore-") {
			continue
		}
		return fail("Extra input fields: " + k)
	}
	return nil
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

var _ = iam.CanonicalID
var _ = meta.NullVersionID
var _ = policy.Match
var _ = bytes.Equal
