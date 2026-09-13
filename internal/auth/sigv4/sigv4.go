// Package sigv4 implements AWS Signature Version 4 verification for S3:
// Authorization-header signing, presigned query-string signing, and the
// aws-chunked streaming payload formats (signed chunks, unsigned payload
// with trailer, and signed chunks with trailer).
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	Algorithm = "AWS4-HMAC-SHA256"
	// Payload hash sentinels.
	UnsignedPayload          = "UNSIGNED-PAYLOAD"
	StreamingSigned          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	StreamingSignedTrailer   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	StreamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	EmptySHA256              = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	timeFormat               = "20060102T150405Z"
	dateFormat               = "20060102"
	MaxPresignExpiry         = 7 * 24 * time.Hour
	MaxClockSkew             = 15 * time.Minute
)

// Errors returned by verification. The API layer maps them to S3 codes.
var (
	ErrMissingAuth        = errors.New("sigv4: no authentication")
	ErrMalformed          = errors.New("sigv4: malformed authorization")
	ErrMalformedQuery     = errors.New("sigv4: malformed query parameters")
	ErrUnknownAccessKey   = errors.New("sigv4: unknown access key")
	ErrSignatureMismatch  = errors.New("sigv4: signature does not match")
	ErrTimeSkew           = errors.New("sigv4: request time too skewed")
	ErrExpired            = errors.New("sigv4: presigned url expired")
	ErrBadExpiry          = errors.New("sigv4: invalid expiry")
	ErrBadService         = errors.New("sigv4: credential scope is not for s3")
	ErrBadRegion          = errors.New("sigv4: credential scope region mismatch")
	ErrMissingContentHash = errors.New("sigv4: missing x-amz-content-sha256")
	ErrMissingDate        = errors.New("sigv4: missing date")
	ErrHostNotSigned      = errors.New("sigv4: host header not signed")
)

// Parsed holds the pieces of a SigV4 authorization (header or query).
type Parsed struct {
	AccessKey     string
	Date          time.Time // X-Amz-Date / Date
	Scope         string    // date/region/service/aws4_request
	ScopeDate     string
	Region        string
	Service       string
	SignedHeaders []string
	Signature     string
	Presigned     bool
	Expires       time.Duration // presigned only
	SessionToken  string
	// ContentSHA256 is the x-amz-content-sha256 value (header auth) or
	// UNSIGNED-PAYLOAD for presigned requests.
	ContentSHA256 string
}

// SecretLookup resolves an access key to its secret key. It returns
// ErrUnknownAccessKey (or wraps it) when the key does not exist.
type SecretLookup func(accessKey string) (secret string, err error)

// ParseRequest extracts SigV4 authentication data from r. It returns
// ErrMissingAuth if the request carries no SigV4 authentication.
func ParseRequest(r *http.Request) (*Parsed, error) {
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") == Algorithm || q.Get("X-Amz-Signature") != "" {
		return parseQuery(q)
	}
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nil, ErrMissingAuth
	}
	if !strings.HasPrefix(auth, Algorithm+" ") && !strings.HasPrefix(auth, Algorithm+",") {
		return nil, ErrMissingAuth
	}
	return parseHeader(r, auth)
}

func parseHeader(r *http.Request, auth string) (*Parsed, error) {
	p := &Parsed{}
	rest := strings.TrimSpace(auth[len(Algorithm):])
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, ErrMalformed
		}
		switch strings.TrimSpace(k) {
		case "Credential":
			if err := p.parseCredential(strings.TrimSpace(v)); err != nil {
				return nil, err
			}
		case "SignedHeaders":
			p.SignedHeaders = strings.Split(strings.TrimSpace(v), ";")
		case "Signature":
			p.Signature = strings.TrimSpace(v)
		default:
			return nil, ErrMalformed
		}
	}
	if p.AccessKey == "" || len(p.SignedHeaders) == 0 || p.Signature == "" {
		return nil, ErrMalformed
	}
	ds := r.Header.Get("X-Amz-Date")
	if ds == "" {
		ds = r.Header.Get("Date")
		if ds == "" {
			return nil, ErrMissingDate
		}
		t, err := http.ParseTime(ds)
		if err != nil {
			return nil, ErrMissingDate
		}
		p.Date = t.UTC()
	} else {
		t, err := time.Parse(timeFormat, ds)
		if err != nil {
			return nil, ErrMalformed
		}
		p.Date = t
	}
	// S3 requires x-amz-content-sha256; other AWS services (IAM, STS) do not
	// send it and sign the SHA-256 of the body instead. The caller fills
	// ContentSHA256 from the body when it is empty, or rejects the request.
	p.ContentSHA256 = r.Header.Get("X-Amz-Content-Sha256")
	p.SessionToken = r.Header.Get("X-Amz-Security-Token")
	return p, nil
}

func parseQuery(q url.Values) (*Parsed, error) {
	p := &Parsed{Presigned: true, ContentSHA256: UnsignedPayload}
	if q.Get("X-Amz-Algorithm") != Algorithm {
		return nil, ErrMalformedQuery
	}
	if err := p.parseCredential(q.Get("X-Amz-Credential")); err != nil {
		return nil, ErrMalformedQuery
	}
	t, err := time.Parse(timeFormat, q.Get("X-Amz-Date"))
	if err != nil {
		return nil, ErrMalformedQuery
	}
	p.Date = t
	exp, err := strconv.ParseInt(q.Get("X-Amz-Expires"), 10, 64)
	if err != nil {
		return nil, ErrBadExpiry
	}
	if exp < 1 || time.Duration(exp)*time.Second > MaxPresignExpiry {
		return nil, ErrBadExpiry
	}
	p.Expires = time.Duration(exp) * time.Second
	p.SignedHeaders = strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	p.Signature = q.Get("X-Amz-Signature")
	p.SessionToken = q.Get("X-Amz-Security-Token")
	if p.Signature == "" || len(p.SignedHeaders) == 0 || p.SignedHeaders[0] == "" {
		return nil, ErrMalformedQuery
	}
	if v := q.Get("X-Amz-Content-Sha256"); v != "" {
		p.ContentSHA256 = v
	}
	return p, nil
}

func (p *Parsed) parseCredential(c string) error {
	parts := strings.Split(c, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" {
		return ErrMalformed
	}
	p.AccessKey = parts[0]
	p.ScopeDate = parts[1]
	p.Region = parts[2]
	p.Service = parts[3]
	p.Scope = strings.Join(parts[1:], "/")
	if _, err := time.Parse(dateFormat, p.ScopeDate); err != nil {
		return ErrMalformed
	}
	return nil
}

// Options controls verification.
type Options struct {
	// Region, if non-empty, must match the credential scope region.
	// Empty accepts any region (S3-compatible servers usually do).
	Region string
	Now    func() time.Time
}

// Verify checks the signature of r against the secret resolved for the
// parsed access key. On success it returns the signing key so the caller
// can validate streaming chunks.
func Verify(r *http.Request, p *Parsed, lookup SecretLookup, opt Options) (signingKey []byte, err error) {
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	if p.Service != "s3" && p.Service != "sts" && p.Service != "iam" {
		return nil, ErrBadService
	}
	if opt.Region != "" && p.Region != opt.Region {
		return nil, ErrBadRegion
	}
	if p.Date.Format(dateFormat) != p.ScopeDate {
		return nil, ErrMalformed
	}
	t := now()
	if p.Presigned {
		if t.Before(p.Date.Add(-MaxClockSkew)) {
			return nil, ErrTimeSkew
		}
		if t.After(p.Date.Add(p.Expires)) {
			return nil, ErrExpired
		}
	} else {
		d := t.Sub(p.Date)
		if d > MaxClockSkew || d < -MaxClockSkew {
			return nil, ErrTimeSkew
		}
	}
	hostSigned := false
	for _, h := range p.SignedHeaders {
		if h == "host" {
			hostSigned = true
		}
	}
	if !hostSigned {
		return nil, ErrHostNotSigned
	}
	secret, err := lookup(p.AccessKey)
	if err != nil {
		return nil, err
	}
	key := SigningKey(secret, p.ScopeDate, p.Region, p.Service)
	cr := CanonicalRequest(r, p)
	sts := StringToSign(p.Date, p.Scope, cr)
	sig := hex.EncodeToString(hmacSHA256(key, []byte(sts)))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(p.Signature)) != 1 {
		return nil, ErrSignatureMismatch
	}
	return key, nil
}

// SigningKey derives the SigV4 signing key.
func SigningKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte("aws4_request"))
}

// StringToSign builds the SigV4 string to sign.
func StringToSign(date time.Time, scope, canonicalRequest string) string {
	return Algorithm + "\n" + date.Format(timeFormat) + "\n" + scope + "\n" + hexSHA256([]byte(canonicalRequest))
}

// CanonicalRequest builds the canonical request for r.
func CanonicalRequest(r *http.Request, p *Parsed) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(EncodePath(r.URL.Path))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(r.URL.Query(), p.Presigned))
	b.WriteByte('\n')
	for _, h := range p.SignedHeaders {
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(headerValue(r, h))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(p.SignedHeaders, ";"))
	b.WriteByte('\n')
	b.WriteString(p.ContentSHA256)
	return b.String()
}

func headerValue(r *http.Request, name string) string {
	var vals []string
	switch name {
	case "host":
		vals = []string{r.Host}
	case "content-length":
		if r.ContentLength >= 0 && r.Header.Get("Content-Length") == "" {
			vals = []string{strconv.FormatInt(r.ContentLength, 10)}
		} else {
			vals = r.Header.Values("Content-Length")
		}
	default:
		vals = r.Header.Values(http.CanonicalHeaderKey(name))
	}
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(out, ",")
}

func canonicalQuery(q url.Values, presigned bool) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if presigned && k == "X-Amz-Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, encodeQueryComponent(k)+"="+encodeQueryComponent(v))
		}
	}
	return strings.Join(parts, "&")
}

// EncodePath URI-encodes a path once, leaving '/' and unreserved
// characters as-is, the way S3 canonicalises URIs.
func EncodePath(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '/' || isUnreserved(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

func encodeQueryComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~'
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Sign signs an outgoing request (used by tests and the replication /
// tiering clients). It sets X-Amz-Date, X-Amz-Content-Sha256 (if unset)
// and Authorization.
func Sign(r *http.Request, accessKey, secret, region string, now time.Time, payloadHash string) {
	now = now.UTC()
	r.Header.Set("X-Amz-Date", now.Format(timeFormat))
	if payloadHash == "" {
		payloadHash = UnsignedPayload
	}
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	for k := range r.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") && lk != "x-amz-content-sha256" && lk != "x-amz-date" {
			signed = append(signed, lk)
		}
		if lk == "content-type" || lk == "content-md5" || lk == "range" {
			signed = append(signed, lk)
		}
	}
	sort.Strings(signed)
	p := &Parsed{
		AccessKey: accessKey, Date: now, ScopeDate: now.Format(dateFormat), Region: region, Service: "s3",
		Scope: now.Format(dateFormat) + "/" + region + "/s3/aws4_request", SignedHeaders: signed, ContentSHA256: payloadHash,
	}
	cr := CanonicalRequest(r, p)
	key := SigningKey(secret, p.ScopeDate, region, "s3")
	sig := hex.EncodeToString(hmacSHA256(key, []byte(StringToSign(now, p.Scope, cr))))
	r.Header.Set("Authorization", Algorithm+" Credential="+accessKey+"/"+p.Scope+", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)
}

// Presign adds query-string authentication to r's URL.
func Presign(r *http.Request, accessKey, secret, region string, now time.Time, expires time.Duration) {
	now = now.UTC()
	q := r.URL.Query()
	scope := now.Format(dateFormat) + "/" + region + "/s3/aws4_request"
	q.Set("X-Amz-Algorithm", Algorithm)
	q.Set("X-Amz-Credential", accessKey+"/"+scope)
	q.Set("X-Amz-Date", now.Format(timeFormat))
	q.Set("X-Amz-Expires", strconv.Itoa(int(expires.Seconds())))
	q.Set("X-Amz-SignedHeaders", "host")
	r.URL.RawQuery = q.Encode()
	p := &Parsed{AccessKey: accessKey, Date: now, ScopeDate: now.Format(dateFormat), Region: region, Service: "s3",
		Scope: scope, SignedHeaders: []string{"host"}, ContentSHA256: UnsignedPayload, Presigned: true}
	cr := CanonicalRequest(r, p)
	key := SigningKey(secret, p.ScopeDate, region, "s3")
	sig := hex.EncodeToString(hmacSHA256(key, []byte(StringToSign(now, scope, cr))))
	q.Set("X-Amz-Signature", sig)
	r.URL.RawQuery = q.Encode()
}

// SignPostPolicy returns the signature of a base64 POST policy.
func SignPostPolicy(policyB64, secret, date, region string) string {
	key := SigningKey(secret, date, region, "s3")
	return hex.EncodeToString(hmacSHA256(key, []byte(policyB64)))
}
