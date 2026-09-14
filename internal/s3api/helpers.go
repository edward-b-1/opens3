package s3api

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/checksum"
	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/s3err"
)

const maxXMLBody = 20 << 20

// writeXML writes an XML response body with the given status.
func (s *Server) writeXML(c *reqCtx, status int, v any) error {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(v); err != nil {
		return err
	}
	h := c.w.Header()
	h.Set("Content-Type", "application/xml")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	c.w.WriteHeader(status)
	_, err := c.w.Write(buf.Bytes())
	return err
}

// readXML decodes the request body into v.
func (s *Server) readXML(c *reqCtx, v any) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return s3err.New(s3err.MissingRequestBodyError)
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(v); err != nil {
		return errMalformedXML()
	}
	return nil
}

// readBody reads the whole (decoded) body, bounded.
func (s *Server) readBody(c *reqCtx) ([]byte, error) {
	if c.body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(c.body, maxXMLBody+1))
	if err != nil {
		return nil, s3err.New(s3err.IncompleteBody)
	}
	if len(raw) > maxXMLBody {
		return nil, s3err.New(s3err.MaxMessageLengthExceeded)
	}
	if c.body.expectedSHA256 != "" {
		sum := sha256Hex(raw)
		if sum != c.body.expectedSHA256 {
			return nil, s3err.New(s3err.XAmzContentSHA256Mismatch)
		}
	}
	return raw, nil
}

// requireContentMD5 verifies Content-MD5 against raw when present (and
// required for some configuration PUTs).
func requireContentMD5(c *reqCtx, raw []byte, required bool) error {
	h := c.r.Header.Get("Content-MD5")
	if h == "" {
		if required {
			return s3err.New(s3err.MissingContentMD5)
		}
		return nil
	}
	want, err := base64.StdEncoding.DecodeString(h)
	if err != nil || len(want) != 16 {
		return s3err.New(s3err.InvalidDigest)
	}
	if md5Hex(raw) != hex.EncodeToString(want) {
		return s3err.New(s3err.BadDigest)
	}
	return nil
}

// quoteETag wraps an ETag in quotes.
func quoteETag(e string) string { return `"` + e + `"` }

// setObjectHeaders writes the standard object response headers.
func setObjectHeaders(h http.Header, o *meta.Object, b *meta.Bucket) {
	h.Set("ETag", quoteETag(o.ETag))
	h.Set("Last-Modified", o.ModTime.UTC().Format(http.TimeFormat))
	if o.ContentType != "" {
		h.Set("Content-Type", o.ContentType)
	} else {
		h.Set("Content-Type", "binary/octet-stream")
	}
	setIf(h, "Content-Encoding", o.ContentEncoding)
	setIf(h, "Content-Disposition", o.ContentDisposition)
	setIf(h, "Content-Language", o.ContentLanguage)
	setIf(h, "Cache-Control", o.CacheControl)
	setIf(h, "Expires", o.Expires)
	setIf(h, "x-amz-website-redirect-location", o.WebsiteRedirect)
	for k, v := range o.UserMeta {
		// AWS returns user metadata header names in lower case; bypass
		// Go's canonicalisation ("X-Amz-Meta-Foo") which SDKs surface
		// as a differently-cased metadata key.
		h["x-amz-meta-"+k] = []string{v}
	}
	if b != nil && b.Versioning != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	if o.StorageClass != "" && o.StorageClass != "STANDARD" {
		h.Set("x-amz-storage-class", o.StorageClass)
	}
	if len(o.Tags) > 0 {
		h.Set("x-amz-tagging-count", strconv.Itoa(len(o.Tags)))
	}
	if o.SSE != nil {
		switch o.SSE.Type {
		case "SSE-C":
			h.Set("x-amz-server-side-encryption-customer-algorithm", "AES256")
			h.Set("x-amz-server-side-encryption-customer-key-MD5", o.SSE.CustomerKeyMD5)
		default:
			h.Set("x-amz-server-side-encryption", o.SSE.Type)
			if o.SSE.KMSKeyID != "" {
				h.Set("x-amz-server-side-encryption-aws-kms-key-id", o.SSE.KMSKeyID)
			}
		}
	}
	if o.Retention != nil {
		h.Set("x-amz-object-lock-mode", o.Retention.Mode)
		h.Set("x-amz-object-lock-retain-until-date", o.Retention.RetainUntil.UTC().Format(time.RFC3339))
	}
	if o.LegalHold {
		h.Set("x-amz-object-lock-legal-hold", "ON")
	} else if b != nil && b.ObjectLockEnabled {
		h.Set("x-amz-object-lock-legal-hold", "OFF")
	}
	if o.RestoreExpiry != nil {
		h.Set("x-amz-restore", `ongoing-request="false", expiry-date="`+o.RestoreExpiry.UTC().Format(http.TimeFormat)+`"`)
	}
}

func setChecksumHeaders(h http.Header, cs *meta.Checksum) {
	if cs == nil {
		return
	}
	h.Set(checksum.HeaderName(cs.Algorithm), cs.Value)
	h.Set("x-amz-checksum-type", cs.Type)
}

func setIf(h http.Header, k, v string) {
	if v != "" {
		h.Set(k, v)
	}
}

// parseObjectAttrs extracts the settable attributes from request headers.
func (s *Server) parseObjectAttrs(c *reqCtx, forCopy bool) (object.ObjectAttrs, error) {
	r := c.r
	a := object.ObjectAttrs{
		ContentType: r.Header.Get("Content-Type"), ContentEncoding: r.Header.Get("Content-Encoding"),
		ContentDisposition: r.Header.Get("Content-Disposition"), ContentLanguage: r.Header.Get("Content-Language"),
		CacheControl: r.Header.Get("Cache-Control"), Expires: r.Header.Get("Expires"),
		WebsiteRedirect: r.Header.Get("x-amz-website-redirect-location"), StorageClass: r.Header.Get("x-amz-storage-class"),
	}
	if strings.Contains(a.ContentEncoding, "aws-chunked") {
		// Strip aws-chunked from the stored encoding (AWS keeps the rest).
		var parts []string
		for _, p := range strings.Split(a.ContentEncoding, ",") {
			if p = strings.TrimSpace(p); p != "" && p != "aws-chunked" {
				parts = append(parts, p)
			}
		}
		a.ContentEncoding = strings.Join(parts, ", ")
	}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-meta-") {
			if a.UserMeta == nil {
				a.UserMeta = map[string]string{}
			}
			a.UserMeta[lk[len("x-amz-meta-"):]] = strings.Join(v, ",")
		}
	}
	if t := r.Header.Get("x-amz-tagging"); t != "" {
		a.Tags = parseTaggingHeader(t)
	}
	acl, err := s.parseACLHeaders(c, c.bkt)
	if err != nil {
		return a, err
	}
	a.ACL = acl
	if mode := r.Header.Get("x-amz-object-lock-mode"); mode != "" {
		until, err := time.Parse(time.RFC3339, r.Header.Get("x-amz-object-lock-retain-until-date"))
		if err != nil {
			return a, s3err.New(s3err.InvalidArgument).WithMessage("The retain until date must be provided in ISO 8601 format")
		}
		a.Retention = &meta.Retention{Mode: strings.ToUpper(mode), RetainUntil: until}
	} else if r.Header.Get("x-amz-object-lock-retain-until-date") != "" {
		return a, s3err.New(s3err.InvalidArgument).WithMessage("x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied")
	}
	if lh := r.Header.Get("x-amz-object-lock-legal-hold"); lh != "" {
		a.LegalHoldSet = true
		a.LegalHold = strings.EqualFold(lh, "ON")
	}
	return a, nil
}

// parseSSEHeaders reads server-side encryption request headers. copySource
// selects the x-amz-copy-source-server-side-encryption-customer-* set.
func parseSSEHeaders(r *http.Request, copySource bool) (object.SSERequest, error) {
	prefix := "x-amz-server-side-encryption"
	if copySource {
		prefix = "x-amz-copy-source-server-side-encryption"
	}
	var req object.SSERequest
	if alg := r.Header.Get(prefix + "-customer-algorithm"); alg != "" {
		if alg != "AES256" {
			return req, s3err.New(s3err.InvalidEncryptionAlgorithmError)
		}
		if !copySource && r.Header.Get("x-amz-server-side-encryption") != "" {
			return req, s3err.New(s3err.InvalidArgument).WithMessage("Server-side encryption with customer-provided keys cannot be combined with x-amz-server-side-encryption")
		}
		key, err := base64.StdEncoding.DecodeString(r.Header.Get(prefix + "-customer-key"))
		if err != nil || len(key) != 32 {
			return req, s3err.New(s3err.InvalidArgument).WithMessage("The secret key was invalid for the specified algorithm.")
		}
		req.Type = "SSE-C"
		req.CustomerKey = key
		req.CustomerKeyMD5 = r.Header.Get(prefix + "-customer-key-MD5")
		if req.CustomerKeyMD5 == "" {
			req.CustomerKeyMD5 = r.Header.Get(prefix + "-customer-key-md5")
		}
		return req, nil
	}
	if r.Header.Get(prefix+"-customer-key") != "" || r.Header.Get(prefix+"-customer-key-MD5") != "" {
		return req, s3err.New(s3err.InvalidArgument).WithMessage("Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm.")
	}
	if copySource {
		return req, nil
	}
	switch v := r.Header.Get("x-amz-server-side-encryption"); v {
	case "":
		if r.Header.Get("x-amz-server-side-encryption-aws-kms-key-id") != "" {
			return req, s3err.New(s3err.InvalidArgument).WithMessage("x-amz-server-side-encryption-aws-kms-key-id requires x-amz-server-side-encryption: aws:kms")
		}
	case "AES256", "aws:kms", "aws:kms:dsse":
		req.Type = v
		req.KMSKeyID = r.Header.Get("x-amz-server-side-encryption-aws-kms-key-id")
		if v == "AES256" && req.KMSKeyID != "" {
			return req, s3err.New(s3err.InvalidArgument).WithMessage("x-amz-server-side-encryption-aws-kms-key-id is only valid with x-amz-server-side-encryption: aws:kms")
		}
		if ctx := r.Header.Get("x-amz-server-side-encryption-context"); ctx != "" {
			raw, err := base64.StdEncoding.DecodeString(ctx)
			if err != nil {
				return req, s3err.New(s3err.InvalidArgument).WithMessage("The encryption context is not valid base64")
			}
			req.Context = map[string]string{}
			if err := jsonUnmarshal(raw, &req.Context); err != nil {
				return req, s3err.New(s3err.InvalidArgument).WithMessage("The encryption context is not valid JSON")
			}
		}
	default:
		return req, s3err.New(s3err.InvalidArgument).WithMessage("The encryption method specified is not supported").WithExtra("ArgumentName", "x-amz-server-side-encryption").WithExtra("ArgumentValue", v)
	}
	return req, nil
}

// parseChecksumHeaders builds the ChecksumRequest from x-amz-checksum-* /
// x-amz-sdk-checksum-algorithm headers and trailers.
func parseChecksumHeaders(c *reqCtx) (*object.ChecksumRequest, error) {
	r := c.r
	var cr *object.ChecksumRequest
	for _, alg := range []string{"CRC32", "CRC32C", "SHA1", "SHA256", "CRC64NVME"} {
		if v := r.Header.Get(checksum.HeaderName(alg)); v != "" {
			if cr != nil {
				return nil, s3err.New(s3err.InvalidRequest).WithMessage("Expecting a single x-amz-checksum- header. Multiple checksum Types are not allowed.")
			}
			cr = &object.ChecksumRequest{Algorithm: alg, Value: v}
		}
	}
	if alg := r.Header.Get("x-amz-sdk-checksum-algorithm"); alg != "" {
		n := checksum.Normalize(alg)
		if n == "" {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("The value specified in the x-amz-sdk-checksum-algorithm header is invalid.")
		}
		if cr != nil && cr.Algorithm != n {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("Value for x-amz-sdk-checksum-algorithm header is invalid.")
		}
		if cr == nil {
			cr = &object.ChecksumRequest{Algorithm: n}
		}
	}
	if tr := r.Header.Get("x-amz-trailer"); tr != "" {
		th := strings.ToLower(strings.TrimSpace(tr))
		if !strings.HasPrefix(th, "x-amz-checksum-") {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("The value specified in the x-amz-trailer header is not supported")
		}
		alg := checksum.Normalize(strings.TrimPrefix(th, "x-amz-checksum-"))
		if alg == "" {
			return nil, s3err.New(s3err.InvalidRequest).WithMessage("The value specified in the x-amz-trailer header is not supported")
		}
		if cr == nil {
			cr = &object.ChecksumRequest{Algorithm: alg}
		}
		body := c.body
		cr.Trailer = func() (string, string) {
			t := body.Trailer()
			if t == nil {
				return "", ""
			}
			return alg, t.Get(th)
		}
	}
	return cr, nil
}

// parseRange parses an HTTP Range header (single range only, like S3).
func parseRange(h string) (*object.Range, error) {
	if h == "" {
		return nil, nil
	}
	if !strings.HasPrefix(h, "bytes=") {
		return nil, nil // S3 ignores unknown units
	}
	spec := strings.TrimPrefix(h, "bytes=")
	if strings.Contains(spec, ",") {
		return nil, nil // multiple ranges: S3 ignores the header
	}
	a, b, ok := strings.Cut(spec, "-")
	if !ok {
		return nil, s3err.New(s3err.InvalidRange)
	}
	if a == "" {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return nil, nil
		}
		return &object.Range{Start: -n, End: -1}, nil
	}
	start, err := strconv.ParseInt(a, 10, 64)
	if err != nil || start < 0 {
		return nil, nil
	}
	end := int64(-1)
	if b != "" {
		end, err = strconv.ParseInt(b, 10, 64)
		if err != nil || end < start {
			return nil, nil
		}
	}
	return &object.Range{Start: start, End: end}, nil
}

func parseHTTPTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func readConditions(r *http.Request, prefix string) object.Conditions {
	return object.Conditions{
		IfMatch:           r.Header.Get(prefix + "If-Match"),
		IfNoneMatch:       r.Header.Get(prefix + "If-None-Match"),
		IfModifiedSince:   parseHTTPTime(r.Header.Get(prefix + "If-Modified-Since")),
		IfUnmodifiedSince: parseHTTPTime(r.Header.Get(prefix + "If-Unmodified-Since")),
	}
}

// parseCopySource parses x-amz-copy-source into bucket, key, versionId.
func parseCopySource(v string) (bucket, key, versionID string, err error) {
	v = strings.TrimPrefix(v, "/")
	if i := strings.Index(v, "?versionId="); i >= 0 {
		versionID = v[i+len("?versionId="):]
		v = v[:i]
	}
	u, uerr := url.PathUnescape(v)
	if uerr == nil {
		v = u
	}
	bucket, key, ok := strings.Cut(v, "/")
	if !ok || bucket == "" || key == "" {
		return "", "", "", s3err.New(s3err.InvalidArgument).WithMessage("Copy Source must mention the source bucket and key: sourcebucket/sourcekey")
	}
	return bucket, key, versionID, nil
}

// contentMD5Hex converts a Content-MD5 header to hex, validating it.
func contentMD5Hex(r *http.Request) (string, error) {
	v := r.Header.Get("Content-MD5")
	if v == "" {
		if _, present := r.Header["Content-Md5"]; present {
			return "", s3err.New(s3err.InvalidDigest)
		}
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(b) != 16 {
		return "", s3err.New(s3err.InvalidDigest)
	}
	return hex.EncodeToString(b), nil
}

// encodeKey applies encoding-type=url to a key for listing responses.
// S3 percent-encodes every byte except unreserved characters and '/'
// (so "foo+1/bar baz" becomes "foo%2B1/bar%20baz"), which is what SDKs
// decode; url.QueryEscape would encode '/' and turn spaces into '+'.
func encodeKey(k string, encode bool) string {
	if !encode {
		return k
	}
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// authorizeAttributes enforces the permissions AWS requires for the
// attributes an upload can set in one request besides the bytes: an ACL
// needs s3:PutObjectAcl, tags s3:PutObjectTagging, a retention
// s3:PutObjectRetention and a legal hold s3:PutObjectLegalHold. get looks
// up a header (or, for browser form uploads, the equivalent form field).
func (s *Server) authorizeAttributes(c *reqCtx, get func(string) string) error {
	if c.identity != nil && c.identity.IsRoot {
		return nil
	}
	hasGrant := false
	for _, h := range []string{"x-amz-grant-read", "x-amz-grant-write", "x-amz-grant-read-acp", "x-amz-grant-write-acp", "x-amz-grant-full-control"} {
		if get(h) != "" {
			hasGrant = true
		}
	}
	checks := []struct {
		present bool
		action  string
		byACL   bool // an ACL grant of the upload covers it (AWS behaviour, per the conformance suite)
	}{
		{get("x-amz-acl") != "" || hasGrant, "s3:PutObjectAcl", true},
		{get("x-amz-tagging") != "", "s3:PutObjectTagging", true},
		{get("x-amz-object-lock-mode") != "" || get("x-amz-object-lock-retain-until-date") != "", "s3:PutObjectRetention", false},
		{get("x-amz-object-lock-legal-hold") != "", "s3:PutObjectLegalHold", false},
	}
	for _, ck := range checks {
		if !ck.present {
			continue
		}
		switch g := s.iam.Decide(s.authzRequest(c, ck.action)); {
		case g == iam.ByPolicy:
		case g == iam.ByACL && ck.byACL:
		case g == iam.NoGrant && c.grant == iam.ByACL && ck.byACL:
			// The upload itself was granted by an ACL (bucket WRITE, or
			// ownership). Under the ACL model that grant covers the ACL and
			// tags the uploader sets on its own new object; Object Lock
			// settings are policy-era features and always need a policy.
		default:
			return s3err.New(s3err.AccessDenied).WithMessage("Access Denied: the request sets an attribute that requires %s", ck.action)
		}
	}
	return nil
}
