package s3api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/auth/sigv4"
	"gitlab.com/Birdsall/opens3/internal/iam"
	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/s3err"
)

// authenticate verifies the request signature and resolves the identity.
// Anonymous requests are allowed through with a nil identity; the
// authoriser decides whether they may proceed.
func (s *Server) authenticate(c *reqCtx) error {
	r := c.r
	c.body = &bodyReader{r: r.Body, size: r.ContentLength}

	// Browser-based POST uploads authenticate via the policy form field.
	if c.op.name == "PostObject" {
		return nil // handled in postObject
	}
	p, err := sigv4.ParseRequest(r)
	if errors.Is(err, sigv4.ErrMissingAuth) {
		if r.Header.Get("Authorization") != "" || r.URL.Query().Get("AWSAccessKeyId") != "" {
			// SigV2 or unknown scheme.
			if strings.HasPrefix(r.Header.Get("Authorization"), "AWS ") || r.URL.Query().Get("AWSAccessKeyId") != "" {
				return s.authenticateV2(c)
			}
			return s3err.New(s3err.InvalidArgument).WithMessage("Unsupported Authorization Type")
		}
		c.identity = nil
		return nil
	}
	if err != nil {
		return mapSigErr(err)
	}
	opt := sigv4.Options{}
	if s.cfg.EnforceRegion {
		opt.Region = s.cfg.Region
	}
	key, err := sigv4.Verify(r, p, s.iam.LookupSecret, opt)
	if err != nil {
		return mapSigErr(err)
	}
	id, err := s.iam.Resolve(p.AccessKey, p.SessionToken)
	if err != nil {
		return mapSigErr(err)
	}
	c.identity = id
	c.sigVer = "AWS4-HMAC-SHA256"
	c.sigAge = time.Since(p.Date)
	if p.Presigned {
		c.authType = "REST-QUERY-STRING"
	} else {
		c.authType = "REST-HEADER"
	}
	// Payload handling.
	switch p.ContentSHA256 {
	case sigv4.UnsignedPayload, "":
	case sigv4.StreamingSigned, sigv4.StreamingSignedTrailer, sigv4.StreamingUnsignedTrailer:
		if p.Presigned {
			return s3err.New(s3err.InvalidRequest).WithMessage("Streaming payloads are not supported with presigned URLs")
		}
		decoded := r.Header.Get("x-amz-decoded-content-length")
		size := int64(-1)
		if decoded != "" {
			n, err := strconv.ParseInt(decoded, 10, 64)
			if err != nil || n < 0 {
				return s3err.New(s3err.MissingContentLength)
			}
			size = n
		}
		cr, err := sigv4.NewChunkedReader(r.Body, p.ContentSHA256, p.Signature, key, p.Date, p.Scope, r.Header.Get("x-amz-trailer"))
		if err != nil {
			return s3err.New(s3err.InvalidRequest).WithMessage("%s", err.Error())
		}
		c.body = &bodyReader{r: cr, chunked: cr, size: size}
	default:
		if len(p.ContentSHA256) != 64 {
			return s3err.New(s3err.InvalidArgument).WithMessage("x-amz-content-sha256 must be UNSIGNED-PAYLOAD, STREAMING-*, or a hex-encoded SHA256 value.")
		}
		// The handler verifies the hash against the body for object writes
		// (single pass). For other operations verify here by buffering.
		if c.op.level != 2 || (c.op.name != "PutObject" && c.op.name != "UploadPart") {
			if r.ContentLength > 0 && r.ContentLength <= 32<<20 {
				b, err := io.ReadAll(io.LimitReader(r.Body, r.ContentLength))
				if err != nil {
					return s3err.New(s3err.IncompleteBody)
				}
				sum := sha256.Sum256(b)
				if hex.EncodeToString(sum[:]) != strings.ToLower(p.ContentSHA256) {
					return s3err.New(s3err.XAmzContentSHA256Mismatch)
				}
				c.body = &bodyReader{r: strings.NewReader(string(b)), size: int64(len(b))}
			}
		}
		c.body.expectedSHA256 = strings.ToLower(p.ContentSHA256)
	}
	return nil
}

// mapSigErr converts signature errors into S3 error codes.
func mapSigErr(err error) error {
	switch {
	case errors.Is(err, sigv4.ErrUnknownAccessKey), errors.Is(err, iam.ErrNotFound):
		return s3err.New(s3err.InvalidAccessKeyId)
	case errors.Is(err, iam.ErrDisabled):
		return s3err.New(s3err.InvalidAccessKeyId).WithMessage("The access key is disabled.")
	case errors.Is(err, iam.ErrExpired):
		return s3err.New(s3err.ExpiredToken)
	case errors.Is(err, iam.ErrBadToken):
		return s3err.New(s3err.InvalidToken)
	case errors.Is(err, sigv4.ErrSignatureMismatch), errors.Is(err, sigv4.ErrHostNotSigned):
		return s3err.New(s3err.SignatureDoesNotMatch)
	case errors.Is(err, sigv4.ErrTimeSkew):
		return s3err.New(s3err.RequestTimeTooSkewed)
	case errors.Is(err, sigv4.ErrExpired):
		return s3err.New(s3err.AccessDenied).WithMessage("Request has expired")
	case errors.Is(err, sigv4.ErrBadExpiry):
		return s3err.New(s3err.AuthorizationQueryParametersError).WithMessage("X-Amz-Expires must be between 1 and 604800 seconds")
	case errors.Is(err, sigv4.ErrMalformedQuery):
		return s3err.New(s3err.AuthorizationQueryParametersError)
	case errors.Is(err, sigv4.ErrBadRegion):
		return s3err.New(s3err.AuthorizationHeaderMalformed).WithMessage("The authorization header is malformed; the region is wrong.")
	case errors.Is(err, sigv4.ErrBadService):
		return s3err.New(s3err.AuthorizationHeaderMalformed).WithMessage("The authorization header is malformed; incorrect service.")
	case errors.Is(err, sigv4.ErrMissingContentHash):
		return s3err.New(s3err.InvalidRequest).WithMessage("Missing required header for this request: x-amz-content-sha256")
	case errors.Is(err, sigv4.ErrMissingDate):
		return s3err.New(s3err.AccessDenied).WithMessage("AWS authentication requires a valid Date or x-amz-date header")
	}
	return s3err.New(s3err.AuthorizationHeaderMalformed)
}

// authorize loads the bucket (and object when needed) and evaluates
// policies and ACLs for the operation.
func (s *Server) authorize(c *reqCtx) error {
	op := c.op
	if op.action == "" {
		return nil
	}
	ctx := c.r.Context()
	if op.name == "PostObject" {
		// Authentication and authorisation happen in the handler once the
		// signed policy has been read from the form.
		b, err := s.obj.GetBucket(ctx, c.bucket)
		if err != nil {
			return err
		}
		c.bkt = b
		return nil
	}
	if op.level >= 1 && op.name != "CreateBucket" {
		b, err := s.obj.GetBucket(ctx, c.bucket)
		if err != nil {
			// Do not reveal bucket existence to anonymous callers... AWS
			// returns 404 for a missing bucket regardless, so do the same.
			return err
		}
		c.bkt = b
		if want := c.r.Header.Get("x-amz-expected-bucket-owner"); want != "" && want != b.Owner && want != s.iam.AccountID() {
			return errAccessDenied()
		}
	}
	if op.level == 2 && op.needsObject && c.bkt != nil {
		vid := c.r.URL.Query().Get("versionId")
		o, err := s.obj.StatObject(ctx, c.bucket, c.key, vid)
		if err == nil {
			c.objMeta = o
		}
		// Missing objects are reported by the handler after authorisation
		// (AWS returns 403 for unauthorised callers, 404 for authorised).
	}
	req := iam.Request{Identity: c.identity, Action: op.action, Bucket: c.bucket, Conditions: s.conditionContext(c)}
	if op.level == 2 {
		req.Key = c.key
	}
	if c.bkt != nil {
		req.BucketOwner = c.bkt.Owner
		req.BucketPolicy = c.bkt.Policy
		req.BucketACL = c.bkt.ACL
		req.PublicAccessBlock = c.bkt.PublicAccessBlock
		req.Ownership = c.bkt.Ownership
	}
	if c.objMeta != nil {
		req.ObjectOwner = c.objMeta.Owner
		req.ObjectACL = c.objMeta.ACL
		for _, t := range c.objMeta.Tags {
			req.Conditions["s3:existingobjecttag/"+strings.ToLower(t.Key)] = []string{t.Value}
		}
	}
	if op.name == "ListBuckets" {
		// Every authenticated identity may call ListBuckets; results are
		// filtered per bucket.
		if c.identity == nil {
			return errAccessDenied()
		}
		return nil
	}
	if op.name == "CreateBucket" {
		if c.identity == nil {
			return errAccessDenied()
		}
		if c.identity.IsRoot {
			return nil
		}
		if !s.iam.Authorize(req) {
			return errAccessDenied()
		}
		return nil
	}
	if !s.iam.Authorize(req) {
		// A caller allowed to list the bucket learns that the key does not
		// exist (404) instead of 403, as on AWS; the key is evaluated as
		// the s3:prefix condition of the ListBucket permission.
		if op.level == 2 && op.needsObject && c.objMeta == nil && (op.name == "GetObject" || op.name == "HeadObject") {
			lreq := req
			lreq.Action, lreq.Key = "s3:ListBucket", ""
			lreq.Conditions = map[string][]string{}
			for k, v := range req.Conditions {
				lreq.Conditions[k] = v
			}
			lreq.Conditions["s3:prefix"] = []string{c.key}
			if s.iam.Authorize(lreq) {
				return s3err.New(s3err.NoSuchKey)
			}
		}
		return errAccessDenied()
	}
	return nil
}

// conditionContext builds the IAM condition keys for the request.
func (s *Server) conditionContext(c *reqCtx) map[string][]string {
	r := c.r
	q := r.URL.Query()
	m := map[string][]string{}
	ip := clientIP(r)
	if ip != "" {
		m["aws:sourceip"] = []string{ip}
	}
	secure := "false"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		secure = "true"
	}
	m["aws:securetransport"] = []string{secure}
	if r.TLS != nil {
		m["s3:tlsversion"] = []string{tlsVersion(r.TLS.Version)}
	}
	now := time.Now().UTC()
	m["aws:currenttime"] = []string{now.Format(time.RFC3339)}
	m["aws:epochtime"] = []string{strconv.FormatInt(now.Unix(), 10)}
	if v := r.Header.Get("Referer"); v != "" {
		m["aws:referer"] = []string{v}
	}
	if v := r.Header.Get("User-Agent"); v != "" {
		m["aws:useragent"] = []string{v}
	}
	if c.authType != "" {
		m["s3:authtype"] = []string{c.authType}
		m["s3:signatureversion"] = []string{c.sigVer}
		m["s3:signatureage"] = []string{strconv.FormatInt(c.sigAge.Milliseconds(), 10)}
	}
	if c.identity != nil {
		m["aws:principalarn"] = []string{c.identity.ARN()}
		m["aws:userid"] = []string{c.identity.CanonicalID()}
		m["aws:username"] = []string{c.identity.Name()}
		m["aws:principaltype"] = []string{c.identity.PrincipalType()}
	}
	// Request-specific keys.
	for qk, ck := range map[string]string{"prefix": "s3:prefix", "delimiter": "s3:delimiter", "max-keys": "s3:max-keys", "versionId": "s3:versionid"} {
		if v := q.Get(qk); v != "" {
			m[ck] = []string{v}
		}
	}
	for hk, ck := range map[string]string{
		"x-amz-acl": "s3:x-amz-acl", "x-amz-storage-class": "s3:x-amz-storage-class",
		"x-amz-server-side-encryption": "s3:x-amz-server-side-encryption", "x-amz-server-side-encryption-aws-kms-key-id": "s3:x-amz-server-side-encryption-aws-kms-key-id",
		"x-amz-server-side-encryption-customer-algorithm": "s3:x-amz-server-side-encryption-customer-algorithm",
		"x-amz-copy-source": "s3:x-amz-copy-source", "x-amz-metadata-directive": "s3:x-amz-metadata-directive",
		"x-amz-grant-read": "s3:x-amz-grant-read", "x-amz-grant-write": "s3:x-amz-grant-write", "x-amz-grant-full-control": "s3:x-amz-grant-full-control",
		"x-amz-grant-read-acp": "s3:x-amz-grant-read-acp", "x-amz-grant-write-acp": "s3:x-amz-grant-write-acp",
		"x-amz-object-lock-mode": "s3:object-lock-mode", "x-amz-object-lock-legal-hold": "s3:object-lock-legal-hold",
		"x-amz-object-lock-retain-until-date": "s3:object-lock-retain-until-date", "x-amz-website-redirect-location": "s3:x-amz-website-redirect-location",
		"if-match": "s3:if-match", "if-none-match": "s3:if-none-match",
	} {
		if v := r.Header.Get(hk); v != "" {
			m[ck] = []string{v}
		}
	}
	if v := r.Header.Get("x-amz-object-lock-retain-until-date"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			days := int(time.Until(t).Hours() / 24)
			m["s3:object-lock-remaining-retention-days"] = []string{strconv.Itoa(days)}
		}
	}
	if v := r.Header.Get("x-amz-tagging"); v != "" {
		for _, t := range parseTaggingHeader(v) {
			m["s3:requestobjecttag/"+strings.ToLower(t.Key)] = []string{t.Value}
		}
	}
	if c.bkt != nil {
		for _, t := range c.bkt.Tags {
			m["aws:resourcetag/"+strings.ToLower(t.Key)] = []string{t.Value}
		}
	}
	return m
}

func tlsVersion(v uint16) string {
	switch v {
	case 0x0301:
		return "1.0"
	case 0x0302:
		return "1.1"
	case 0x0303:
		return "1.2"
	case 0x0304:
		return "1.3"
	}
	return ""
}

func clientIP(r *http.Request) string {
	// Trust X-Forwarded-For only if configured (not yet); use RemoteAddr.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authenticateV2 verifies AWS Signature Version 2 (legacy clients).
func (s *Server) authenticateV2(c *reqCtx) error {
	r := c.r
	var accessKey, signature string
	q := r.URL.Query()
	presigned := false
	if ak := q.Get("AWSAccessKeyId"); ak != "" {
		accessKey, signature, presigned = ak, q.Get("Signature"), true
		exp, err := strconv.ParseInt(q.Get("Expires"), 10, 64)
		if err != nil {
			return s3err.New(s3err.AuthorizationQueryParametersError)
		}
		if time.Now().Unix() > exp {
			return s3err.New(s3err.AccessDenied).WithMessage("Request has expired")
		}
	} else {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "AWS ")
		var ok bool
		accessKey, signature, ok = strings.Cut(auth, ":")
		if !ok {
			return s3err.New(s3err.AuthorizationHeaderMalformed)
		}
		// x-amz-date takes precedence over Date whenever it is present.
		ds, ok := r.Header["X-Amz-Date"]
		if !ok {
			ds = r.Header["Date"]
		}
		var t time.Time
		if len(ds) > 0 {
			t = parseHTTPTime(ds[0])
		}
		if t.IsZero() || t.Unix() < 0 {
			return s3err.New(s3err.AccessDenied).WithMessage("AWS authentication requires a valid Date or x-amz-date header")
		}
		if d := time.Since(t); d > 15*time.Minute || d < -15*time.Minute {
			return s3err.New(s3err.RequestTimeTooSkewed)
		}
		c.sigAge = time.Since(t)
	}
	secret, err := s.iam.LookupSecret(accessKey)
	if err != nil {
		return mapSigErr(err)
	}
	if !verifyV2(r, c.bucket, c.key, secret, signature, presigned) {
		return s3err.New(s3err.SignatureDoesNotMatch)
	}
	id, err := s.iam.Resolve(accessKey, r.Header.Get("x-amz-security-token"))
	if err != nil {
		return mapSigErr(err)
	}
	c.identity = id
	c.sigVer = "AWS"
	c.authType = "REST-HEADER"
	if presigned {
		c.authType = "REST-QUERY-STRING"
	}
	return nil
}

// --- CORS ----------------------------------------------------------------

func (s *Server) handlePreflight(c *reqCtx) {
	origin := c.r.Header.Get("Origin")
	method := c.r.Header.Get("Access-Control-Request-Method")
	if origin == "" || method == "" || c.bucket == "" {
		s.writeError(c, s3err.New(s3err.BadRequest).WithMessage("Insufficient information. Origin request header needed."))
		return
	}
	b, err := s.obj.GetBucket(c.r.Context(), c.bucket)
	if err != nil {
		s.writeError(c, err)
		return
	}
	cfg, err := parseCORS(b.CORSXML)
	if err != nil || cfg == nil {
		s.writeError(c, s3err.New(s3err.AccessDenied).WithMessage("CORSResponse: CORS is not enabled for this bucket."))
		return
	}
	reqHeaders := strings.Split(c.r.Header.Get("Access-Control-Request-Headers"), ",")
	rule := cfg.match(origin, method, reqHeaders)
	if rule == nil {
		s.writeError(c, s3err.New(s3err.AccessDenied).WithMessage("CORSResponse: This CORS request is not allowed. This is usually because the evalution of Origin, request method / Access-Control-Request-Method or Access-Control-Request-Headers are not whitelisted by the resource's CORS spec."))
		return
	}
	h := c.w.Header()
	rule.apply(h, origin)
	h.Set("Access-Control-Allow-Methods", strings.Join(rule.AllowedMethods, ", "))
	if len(rule.AllowedHeaders) > 0 {
		h.Set("Access-Control-Allow-Headers", strings.Join(reqHeaders, ", "))
	}
	if rule.MaxAgeSeconds > 0 {
		h.Set("Access-Control-Max-Age", strconv.Itoa(rule.MaxAgeSeconds))
	}
	c.w.WriteHeader(http.StatusOK)
}

// applyCORS adds CORS headers to a normal response when the bucket's
// configuration allows the origin.
func (s *Server) applyCORS(c *reqCtx) {
	origin := c.r.Header.Get("Origin")
	if origin == "" || c.bkt == nil || len(c.bkt.CORSXML) == 0 {
		return
	}
	cfg, err := parseCORS(c.bkt.CORSXML)
	if err != nil {
		return
	}
	method := c.r.Header.Get("Access-Control-Request-Method")
	if method == "" {
		method = c.r.Method
	}
	if rule := cfg.match(origin, method, nil); rule != nil {
		rule.apply(c.w.Header(), origin)
	}
}

// --- helpers used by handlers ----------------------------------------------

func parseTaggingHeader(v string) []meta.Tag {
	var out []meta.Tag
	for _, kv := range strings.Split(v, "&") {
		if kv == "" {
			continue
		}
		k, val, _ := strings.Cut(kv, "=")
		k, _ = unescapeQuery(k)
		val, _ = unescapeQuery(val)
		out = append(out, meta.Tag{Key: k, Value: val})
	}
	return out
}
