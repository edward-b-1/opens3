package s3api

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/edward-b-1/opens3/internal/checksum"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// --- PutObject ---------------------------------------------------------------

func (s *Server) putObject(c *reqCtx) error {
	r := c.r
	if r.Header.Get("x-amz-copy-source") != "" {
		return s.copyObject(c)
	}
	attrs, err := s.parseObjectAttrs(c, false)
	if err != nil {
		return err
	}
	if attrs.ACL != nil && c.bkt.PublicAccessBlock != nil && c.bkt.PublicAccessBlock.BlockPublicAcls && isPublicACL(attrs.ACL) {
		return s3err.New(s3err.AccessDenied).WithMessage("Public ACLs are blocked by the BlockPublicAcls setting")
	}
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
	}
	if sseReq.Type == "SSE-C" && r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") && s.cfg.RequireTLSForSSEC {
		return s3err.New(s3err.InvalidRequest).WithMessage("Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection.")
	}
	cr, err := parseChecksumHeaders(c)
	if err != nil {
		return err
	}
	md5hex, err := contentMD5Hex(r)
	if err != nil {
		return err
	}
	size := c.body.size
	if size < 0 && c.body.chunked == nil && r.ContentLength < 0 && len(r.TransferEncoding) == 0 {
		return s3err.New(s3err.MissingContentLength)
	}
	if err := requireContentLength(r); err != nil {
		return err
	}
	in := object.PutInput{Bucket: c.bucket, Key: c.key, Body: c.body, Size: size, ExpectedMD5: md5hex, ExpectedSHA256: c.body.expectedSHA256,
		Checksum: cr, SSE: sseReq, Attrs: attrs, Conditions: readConditions(r, "")}
	in.Conditions.IfModifiedSince, in.Conditions.IfUnmodifiedSince = time.Time{}, time.Time{}
	o, err := s.obj.PutObject(r.Context(), c.actor(), in)
	if err != nil {
		return err
	}
	h := c.w.Header()
	h.Set("ETag", quoteETag(o.ETag))
	if c.bkt.Versioning != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	setSSEResponseHeaders(h, o)
	setChecksumHeaders(h, o.Checksum)
	c.w.WriteHeader(http.StatusOK)
	return nil
}

// requireContentLength rejects object writes that carry neither a
// Content-Length nor a Transfer-Encoding (Go reads such bodies as empty;
// S3 answers 411 MissingContentLength).
func requireContentLength(r *http.Request) error {
	if _, ok := r.Header["Content-Length"]; !ok && len(r.TransferEncoding) == 0 && r.ContentLength <= 0 {
		return s3err.New(s3err.MissingContentLength)
	}
	return nil
}

func setSSEResponseHeaders(h http.Header, o *meta.Object) {
	if o.SSE == nil {
		return
	}
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

// --- GetObject / HeadObject -------------------------------------------------

func (s *Server) getObject(c *reqCtx) error { return s.serveObject(c, false) }

func (s *Server) headObject(c *reqCtx) error { return s.serveObject(c, true) }

func (s *Server) serveObject(c *reqCtx, head bool) error {
	r := c.r
	q := r.URL.Query()
	rng, err := parseRange(r.Header.Get("Range"))
	if err != nil {
		return err
	}
	partNumber := 0
	if pn := q.Get("partNumber"); pn != "" {
		partNumber, err = strconv.Atoi(pn)
		if err != nil || partNumber < 1 || partNumber > 10000 {
			return errInvalidArg("Part number must be an integer between 1 and 10000, inclusive")
		}
		if rng != nil {
			return errInvalidArg("Cannot specify both Range header and partNumber query parameter")
		}
	}
	if r.Header.Get("x-amz-server-side-encryption") != "" || r.Header.Get("x-amz-server-side-encryption-aws-kms-key-id") != "" {
		return errInvalidArg("Server-side encryption headers are only valid on object writes")
	}
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
	}
	in := object.GetInput{Bucket: c.bucket, Key: c.key, VersionID: q.Get("versionId"), Range: rng, PartNumber: partNumber, Conditions: readConditions(r, ""), SSE: sseReq}
	if in.VersionID != "" && in.VersionID != meta.NullVersionID {
		if _, err := meta.SeqFromVersionID(in.VersionID); err != nil {
			return s3err.New(s3err.InvalidArgument).WithMessage("Invalid version id specified").WithExtra("ArgumentName", "versionId").WithExtra("ArgumentValue", in.VersionID)
		}
	}
	res, err := s.obj.GetObject(r.Context(), in)
	if err == nil {
		if aerr := s.reauthorizeServed(c, res.Object); aerr != nil {
			res.Body.Close()
			return aerr
		}
	}
	if err != nil {
		var e *s3err.Error
		if errors.As(err, &e) && e.Code == s3err.NoSuchKey && e.Headers["x-amz-delete-marker"] == "" {
			c.w.Header().Set("x-amz-delete-marker", "false")
		}
		if errors.As(err, &e) && e.Code == s3err.NotModified {
			// Include ETag/Last-Modified on 304 like AWS.
			if o, serr := s.obj.StatObject(r.Context(), c.bucket, c.key, in.VersionID); serr == nil {
				c.w.Header().Set("ETag", quoteETag(o.ETag))
				c.w.Header().Set("Last-Modified", o.ModTime.UTC().Format(http.TimeFormat))
			}
		}
		return err
	}
	defer res.Body.Close()
	o := res.Object
	h := c.w.Header()
	setObjectHeaders(h, o, c.bkt)
	if q.Get("x-amz-checksum-mode") != "" || strings.EqualFold(r.Header.Get("x-amz-checksum-mode"), "ENABLED") {
		if partNumber > 0 && o.Checksum != nil && partNumber <= len(o.Parts) && o.Parts[partNumber-1].Checksum != "" {
			h.Set(checksum.HeaderName(o.Checksum.Algorithm), o.Parts[partNumber-1].Checksum)
			h.Set("x-amz-checksum-type", o.Checksum.Type)
		} else if res.Range == nil {
			// A whole-object checksum is not returned for a byte range: SDKs
			// would validate it against the partial body.
			setChecksumHeaders(h, o.Checksum)
		}
	}
	// response-* overrides (only for authenticated requests on AWS).
	if c.identity != nil {
		for qk, hk := range map[string]string{"response-content-type": "Content-Type", "response-content-language": "Content-Language",
			"response-expires": "Expires", "response-cache-control": "Cache-Control", "response-content-disposition": "Content-Disposition", "response-content-encoding": "Content-Encoding"} {
			if v := q.Get(qk); v != "" {
				h.Set(hk, v)
			}
		}
	}
	if len(o.Parts) > 1 || partNumber > 0 {
		h.Set("x-amz-mp-parts-count", strconv.Itoa(len(o.Parts)))
	}
	status := http.StatusOK
	length := o.Size
	if res.Range != nil {
		length = res.Range.End - res.Range.Start + 1
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", res.Range.Start, res.Range.End, o.Size))
		status = http.StatusPartialContent
	}
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if len(o.Tags) > 0 {
		h.Set("x-amz-tagging-count", strconv.Itoa(len(o.Tags)))
	}
	c.w.WriteHeader(status)
	if head {
		return nil
	}
	_, err = io.Copy(c.w, res.Body)
	if err != nil {
		s.log.Debug("get object body copy", "err", err)
	}
	return nil
}

// --- DeleteObject / DeleteObjects -------------------------------------------

func (s *Server) deleteObject(c *reqCtx) error {
	r := c.r
	vid := r.URL.Query().Get("versionId")
	in := object.DeleteInput{Bucket: c.bucket, Key: c.key, VersionID: vid, BypassGovernance: strings.EqualFold(r.Header.Get("x-amz-bypass-governance-retention"), "true"),
		IfMatch: r.Header.Get("If-Match")}
	if in.BypassGovernance && !s.mayBypass(c) {
		in.BypassGovernance = false
	}
	if r.Header.Get("x-amz-mfa") != "" {
		return errNotImplemented("MFA Delete is not supported")
	}
	res, err := s.obj.DeleteObject(r.Context(), c.actor(), in)
	if err != nil {
		return err
	}
	h := c.w.Header()
	h.Set("x-amz-delete-marker", strconv.FormatBool(res.DeleteMarker))
	if res.VersionID != "" {
		h.Set("x-amz-version-id", res.VersionID)
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// mayBypass reports whether the caller has s3:BypassGovernanceRetention.
func (s *Server) mayBypass(c *reqCtx) bool {
	if c.identity != nil && c.identity.IsRoot {
		return true
	}
	req := s.authzRequest(c, "s3:BypassGovernanceRetention")
	return s.iam.Authorize(req)
}

func (s *Server) deleteObjects(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	if err := requireContentMD5(c, raw, false); err != nil {
		return err
	}
	var del xmlDelete
	if err := xml.Unmarshal(raw, &del); err != nil {
		return errMalformedXML()
	}
	if len(del.Objects) == 0 {
		return errMalformedXML()
	}
	if len(del.Objects) > 1000 {
		return s3err.New(s3err.MalformedXML).WithMessage("The XML you provided was not well-formed or did not validate against our published schema. Too many keys.")
	}
	bypass := strings.EqualFold(c.r.Header.Get("x-amz-bypass-governance-retention"), "true") && s.mayBypass(c)
	res := xmlDeleteResult{Xmlns: s3NS}
	for _, o := range del.Objects {
		if o.Key == "" {
			res.Errors = append(res.Errors, xmlDeleteError{Key: o.Key, Code: "InvalidArgument", Message: "Key cannot be empty"})
			continue
		}
		// Per-key authorisation for non-root callers.
		if c.identity == nil || !c.identity.IsRoot {
			action := "s3:DeleteObject"
			if o.VersionId != "" {
				action = "s3:DeleteObjectVersion"
			}
			req := s.authzRequest(c, action)
			req.Key = o.Key
			if !s.iam.Authorize(req) {
				res.Errors = append(res.Errors, xmlDeleteError{Key: o.Key, VersionId: o.VersionId, Code: "AccessDenied", Message: "Access Denied"})
				continue
			}
		}
		dr, err := s.obj.DeleteObject(c.r.Context(), c.actor(), object.DeleteInput{Bucket: c.bucket, Key: o.Key, VersionID: o.VersionId, BypassGovernance: bypass, IfMatch: o.ETag})
		if err != nil {
			e := s3err.From(err)
			res.Errors = append(res.Errors, xmlDeleteError{Key: o.Key, VersionId: o.VersionId, Code: string(e.Code), Message: e.Message})
			continue
		}
		if del.Quiet {
			continue
		}
		d := xmlDeletedObject{Key: o.Key, VersionId: o.VersionId}
		if dr.DeleteMarker {
			d.DeleteMarker = true
			d.DeleteMarkerVersionId = dr.VersionID
		}
		res.Deleted = append(res.Deleted, d)
	}
	return s.writeXML(c, http.StatusOK, res)
}

// authzRequest builds an authorisation request for an alternate action.
func (s *Server) authzRequest(c *reqCtx, action string) iamRequest {
	req := iamRequest{Identity: c.identity, Action: action, Bucket: c.bucket, Key: c.key, Conditions: s.conditionContext(c)}
	if c.bkt != nil {
		req.BucketOwner, req.BucketPolicy, req.BucketACL, req.PublicAccessBlock, req.Ownership = c.bkt.Owner, c.bkt.Policy, c.bkt.ACL, c.bkt.PublicAccessBlock, c.bkt.Ownership
	}
	return req
}

// --- CopyObject --------------------------------------------------------------

func (s *Server) copyObject(c *reqCtx) error {
	r := c.r
	sb, sk, sv, err := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if err != nil {
		return err
	}
	// Source authorisation.
	srcBucket, err := s.obj.GetBucket(r.Context(), sb)
	if err != nil {
		return err
	}
	srcAction := "s3:GetObject"
	if sv != "" {
		srcAction = "s3:GetObjectVersion"
	}
	srcReq := iamRequest{Identity: c.identity, Action: srcAction, Bucket: sb, Key: sk, Conditions: s.conditionContext(c),
		BucketOwner: srcBucket.Owner, BucketPolicy: srcBucket.Policy, BucketACL: srcBucket.ACL, PublicAccessBlock: srcBucket.PublicAccessBlock, Ownership: srcBucket.Ownership}
	if so, err := s.obj.StatObject(r.Context(), sb, sk, sv); err == nil {
		applyObjectContext(&srcReq, so)
	}
	if !s.iam.Authorize(srcReq) {
		return errAccessDenied()
	}
	attrs, err := s.parseObjectAttrs(c, true)
	if err != nil {
		return err
	}
	dir := strings.ToUpper(r.Header.Get("x-amz-metadata-directive"))
	if dir != "" && dir != "COPY" && dir != "REPLACE" {
		return errInvalidArg("Unknown metadata directive.")
	}
	tdir := strings.ToUpper(r.Header.Get("x-amz-tagging-directive"))
	if tdir != "" && tdir != "COPY" && tdir != "REPLACE" {
		return errInvalidArg("Unknown tagging directive.")
	}
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
	}
	srcSSE, err := parseSSEHeaders(r, true)
	if err != nil {
		return err
	}
	var cr *object.ChecksumRequest
	if alg := r.Header.Get("x-amz-checksum-algorithm"); alg != "" {
		if checksum.Normalize(alg) == "" {
			return errInvalidArg("Checksum algorithm provided is unsupported.")
		}
		cr = &object.ChecksumRequest{Algorithm: alg}
	}
	in := object.CopyInput{SrcBucket: sb, SrcKey: sk, SrcVersionID: sv, SrcConditions: readConditions(r, "x-amz-copy-source-"), SrcSSE: srcSSE,
		DstBucket: c.bucket, DstKey: c.key, DstConditions: readConditions(r, ""), MetadataDirective: dir, TaggingDirective: tdir, Attrs: attrs, SSE: sseReq, Checksum: cr}
	in.DstConditions.IfModifiedSince, in.DstConditions.IfUnmodifiedSince = time.Time{}, time.Time{}
	dst, src, err := s.obj.CopyObject(r.Context(), c.actor(), in)
	if err != nil {
		return err
	}
	h := c.w.Header()
	if sv != "" || srcBucket.Versioning != "" {
		h.Set("x-amz-copy-source-version-id", src.VersionID)
	}
	if c.bkt.Versioning != "" {
		h.Set("x-amz-version-id", dst.VersionID)
	}
	setSSEResponseHeaders(h, dst)
	out := xmlCopyObjectResult{Xmlns: s3NS, ETag: quoteETag(dst.ETag), LastModified: iso8601(dst.ModTime)}
	if dst.Checksum != nil {
		setChecksumField(&out.ChecksumCRC32, &out.ChecksumCRC32C, &out.ChecksumSHA1, &out.ChecksumSHA256, &out.ChecksumCRC64NVME, dst.Checksum)
		out.ChecksumType = dst.Checksum.Type
	}
	return s.writeXML(c, http.StatusOK, out)
}

func setChecksumField(crc32, crc32c, sha1, sha256, crc64 *string, cs *meta.Checksum) {
	switch cs.Algorithm {
	case checksum.CRC32:
		*crc32 = cs.Value
	case checksum.CRC32C:
		*crc32c = cs.Value
	case checksum.SHA1:
		*sha1 = cs.Value
	case checksum.SHA256:
		*sha256 = cs.Value
	case checksum.CRC64NVME:
		*crc64 = cs.Value
	}
}

// --- tagging -----------------------------------------------------------------

func (s *Server) objectVersionID(c *reqCtx) string { return c.r.URL.Query().Get("versionId") }

func (s *Server) getObjectTagging(c *reqCtx) error {
	o, err := s.obj.StatObject(c.r.Context(), c.bucket, c.key, s.objectVersionID(c))
	if err == nil {
		if aerr := s.reauthorizeServed(c, o); aerr != nil {
			return aerr
		}
	}
	if err != nil {
		return err
	}
	if o.DeleteMarker {
		return s3err.New(s3err.MethodNotAllowed).WithHeader("x-amz-delete-marker", "true")
	}
	if c.bkt.Versioning != "" {
		c.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	return s.writeXML(c, http.StatusOK, tagsToXML(o.Tags))
}

func (s *Server) putObjectTagging(c *reqCtx) error {
	var t xmlTagging
	if err := s.readXML(c, &t); err != nil {
		return err
	}
	o, err := s.obj.PutObjectTagging(c.r.Context(), c.actor(), c.bucket, c.key, s.objectVersionID(c), tagsFromXML(&t))
	if err != nil {
		return err
	}
	if c.bkt.Versioning != "" {
		c.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteObjectTagging(c *reqCtx) error {
	o, err := s.obj.DeleteObjectTagging(c.r.Context(), c.actor(), c.bucket, c.key, s.objectVersionID(c))
	if err != nil {
		return err
	}
	if c.bkt.Versioning != "" {
		c.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- ACL ---------------------------------------------------------------------

func (s *Server) getObjectACL(c *reqCtx) error {
	o, err := s.obj.StatObject(c.r.Context(), c.bucket, c.key, s.objectVersionID(c))
	if err == nil {
		if aerr := s.reauthorizeServed(c, o); aerr != nil {
			return aerr
		}
	}
	if err != nil {
		return err
	}
	if o.DeleteMarker {
		return s3err.New(s3err.MethodNotAllowed).WithHeader("x-amz-delete-marker", "true")
	}
	acl := o.ACL
	if acl == nil {
		owner, disp := o.Owner, o.OwnerDisplay
		if c.bkt.Ownership == "BucketOwnerEnforced" {
			owner, disp = c.bkt.Owner, c.bkt.OwnerDisplay
		}
		acl = &meta.ACL{Owner: owner, OwnerDisplay: disp, Grants: []meta.Grant{{Grantee: owner, GranteeType: "CanonicalUser", DisplayName: disp, Permission: "FULL_CONTROL"}}}
	}
	if c.bkt.Versioning != "" {
		c.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	return s.writeXML(c, http.StatusOK, aclToXML(acl))
}

func (s *Server) putObjectACL(c *reqCtx) error {
	if c.bkt.Ownership == "BucketOwnerEnforced" {
		if c.r.Header.Get("x-amz-acl") == "bucket-owner-full-control" {
			c.w.WriteHeader(http.StatusOK)
			return nil
		}
		return s3err.New(s3err.AccessControlListNotSupported)
	}
	acl, err := s.parseACLHeaders(c, c.bkt)
	if err != nil {
		return err
	}
	o, err := s.obj.StatObject(c.r.Context(), c.bucket, c.key, s.objectVersionID(c))
	if err == nil {
		if aerr := s.reauthorizeServed(c, o); aerr != nil {
			return aerr
		}
	}
	if err != nil {
		return err
	}
	if acl == nil {
		var in xmlAccessControlPolicyIn
		if err := s.readXML(c, &in); err != nil {
			return err
		}
		if acl, err = s.aclFromXML(&in, o.Owner); err != nil {
			return err
		}
	}
	acl.Owner, acl.OwnerDisplay = o.Owner, o.OwnerDisplay
	if pab := c.bkt.PublicAccessBlock; pab != nil && pab.BlockPublicAcls && isPublicACL(acl) {
		return s3err.New(s3err.AccessDenied).WithMessage("Public ACLs are blocked by the BlockPublicAcls setting")
	}
	if _, err := s.obj.PutObjectACL(c.r.Context(), c.actor(), c.bucket, c.key, s.objectVersionID(c), acl); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

// --- object lock -------------------------------------------------------------

func (s *Server) getObjectRetention(c *reqCtx) error {
	if !c.bkt.ObjectLockEnabled {
		return s3err.New(s3err.InvalidRequest).WithMessage("Bucket is missing Object Lock Configuration")
	}
	o, err := s.obj.StatObject(c.r.Context(), c.bucket, c.key, s.objectVersionID(c))
	if err == nil {
		if aerr := s.reauthorizeServed(c, o); aerr != nil {
			return aerr
		}
	}
	if err != nil {
		return err
	}
	if o.DeleteMarker {
		return s3err.New(s3err.MethodNotAllowed).WithHeader("x-amz-delete-marker", "true")
	}
	if o.Retention == nil {
		return s3err.New(s3err.NoSuchObjectLockConfiguration)
	}
	return s.writeXML(c, http.StatusOK, xmlRetention{Xmlns: s3NS, Mode: o.Retention.Mode, RetainUntilDate: iso8601(o.Retention.RetainUntil)})
}

func (s *Server) putObjectRetention(c *reqCtx) error {
	var in xmlRetention
	if err := s.readXML(c, &in); err != nil {
		return err
	}
	var ret *meta.Retention
	if in.Mode != "" || in.RetainUntilDate != "" {
		until, err := time.Parse(time.RFC3339, in.RetainUntilDate)
		if err != nil {
			return errMalformedXML()
		}
		if !until.After(time.Now()) {
			return s3err.New(s3err.InvalidRetentionDate)
		}
		ret = &meta.Retention{Mode: in.Mode, RetainUntil: until}
	}
	bypass := strings.EqualFold(c.r.Header.Get("x-amz-bypass-governance-retention"), "true") && s.mayBypass(c)
	if _, err := s.obj.PutObjectRetention(c.r.Context(), c.actor(), c.bucket, c.key, s.objectVersionID(c), ret, bypass); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) getObjectLegalHold(c *reqCtx) error {
	if !c.bkt.ObjectLockEnabled {
		return s3err.New(s3err.InvalidRequest).WithMessage("Bucket is missing Object Lock Configuration")
	}
	o, err := s.obj.StatObject(c.r.Context(), c.bucket, c.key, s.objectVersionID(c))
	if err == nil {
		if aerr := s.reauthorizeServed(c, o); aerr != nil {
			return aerr
		}
	}
	if err != nil {
		return err
	}
	if o.DeleteMarker {
		return s3err.New(s3err.MethodNotAllowed).WithHeader("x-amz-delete-marker", "true")
	}
	status := "OFF"
	if o.LegalHold {
		status = "ON"
	}
	return s.writeXML(c, http.StatusOK, xmlLegalHold{Xmlns: s3NS, Status: status})
}

func (s *Server) putObjectLegalHold(c *reqCtx) error {
	var in xmlLegalHold
	if err := s.readXML(c, &in); err != nil {
		return err
	}
	if in.Status != "ON" && in.Status != "OFF" {
		return errMalformedXML()
	}
	if _, err := s.obj.PutObjectLegalHold(c.r.Context(), c.actor(), c.bucket, c.key, s.objectVersionID(c), in.Status == "ON"); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

// --- GetObjectAttributes ------------------------------------------------------

func (s *Server) getObjectAttributes(c *reqCtx) error {
	r := c.r
	want := map[string]bool{}
	for _, a := range strings.Split(strings.Join(r.Header.Values("x-amz-object-attributes"), ","), ",") {
		if a = strings.TrimSpace(a); a != "" {
			want[a] = true
		}
	}
	if len(want) == 0 {
		return errInvalidArg("x-amz-object-attributes header is required")
	}
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
	}
	o, err := s.obj.StatObject(r.Context(), c.bucket, c.key, s.objectVersionID(c))
	if err == nil {
		if aerr := s.reauthorizeServed(c, o); aerr != nil {
			return aerr
		}
	}
	if err != nil {
		return err
	}
	if o.DeleteMarker {
		return s3err.New(s3err.NoSuchKey).WithHeader("x-amz-delete-marker", "true")
	}
	if o.SSE != nil && o.SSE.Type == "SSE-C" && sseReq.CustomerKey == nil {
		return s3err.New(s3err.InvalidRequest).WithMessage("The object was stored using a form of Server Side Encryption. The correct parameters must be provided to retrieve the object.")
	}
	out := xmlGetObjectAttributesResponse{Xmlns: s3NS}
	if want["ETag"] {
		out.ETag = o.ETag
	}
	if want["Checksum"] && o.Checksum != nil {
		out.Checksum = new(struct {
			ChecksumCRC32     string `xml:"ChecksumCRC32,omitempty"`
			ChecksumCRC32C    string `xml:"ChecksumCRC32C,omitempty"`
			ChecksumSHA1      string `xml:"ChecksumSHA1,omitempty"`
			ChecksumSHA256    string `xml:"ChecksumSHA256,omitempty"`
			ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME,omitempty"`
			ChecksumType      string `xml:"ChecksumType,omitempty"`
		})
		setChecksumField(&out.Checksum.ChecksumCRC32, &out.Checksum.ChecksumCRC32C, &out.Checksum.ChecksumSHA1, &out.Checksum.ChecksumSHA256, &out.Checksum.ChecksumCRC64NVME, o.Checksum)
		out.Checksum.ChecksumType = o.Checksum.Type
	}
	if want["ObjectParts"] && len(o.Parts) > 0 && strings.Contains(o.ETag, "-") {
		maxParts := atoiDefault(r.Header.Get("x-amz-max-parts"), 1000)
		marker := atoiDefault(r.Header.Get("x-amz-part-number-marker"), 0)
		op := new(struct {
			IsTruncated          bool `xml:"IsTruncated"`
			MaxParts             int  `xml:"MaxParts"`
			NextPartNumberMarker int  `xml:"NextPartNumberMarker,omitempty"`
			PartNumberMarker     int  `xml:"PartNumberMarker"`
			Parts                []struct {
				ChecksumCRC32     string `xml:"ChecksumCRC32,omitempty"`
				ChecksumCRC32C    string `xml:"ChecksumCRC32C,omitempty"`
				ChecksumSHA1      string `xml:"ChecksumSHA1,omitempty"`
				ChecksumSHA256    string `xml:"ChecksumSHA256,omitempty"`
				ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME,omitempty"`
				PartNumber        int    `xml:"PartNumber"`
				Size              int64  `xml:"Size"`
			} `xml:"Part"`
			TotalPartsCount int `xml:"PartsCount"`
		})
		op.MaxParts, op.PartNumberMarker, op.TotalPartsCount = maxParts, marker, len(o.Parts)
		for _, p := range o.Parts {
			if p.Number <= marker {
				continue
			}
			if len(op.Parts) >= maxParts {
				op.IsTruncated = true
				op.NextPartNumberMarker = op.Parts[len(op.Parts)-1].PartNumber
				break
			}
			var e struct {
				ChecksumCRC32     string `xml:"ChecksumCRC32,omitempty"`
				ChecksumCRC32C    string `xml:"ChecksumCRC32C,omitempty"`
				ChecksumSHA1      string `xml:"ChecksumSHA1,omitempty"`
				ChecksumSHA256    string `xml:"ChecksumSHA256,omitempty"`
				ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME,omitempty"`
				PartNumber        int    `xml:"PartNumber"`
				Size              int64  `xml:"Size"`
			}
			e.PartNumber, e.Size = p.Number, p.Size
			if o.Checksum != nil && p.Checksum != "" {
				setChecksumField(&e.ChecksumCRC32, &e.ChecksumCRC32C, &e.ChecksumSHA1, &e.ChecksumSHA256, &e.ChecksumCRC64NVME, &meta.Checksum{Algorithm: o.Checksum.Algorithm, Value: p.Checksum})
			}
			op.Parts = append(op.Parts, e)
		}
		out.ObjectParts = op
	}
	if want["StorageClass"] {
		out.StorageClass = o.StorageClass
		if out.StorageClass == "" {
			out.StorageClass = "STANDARD"
		}
	}
	if want["ObjectSize"] {
		sz := o.Size
		out.ObjectSize = &sz
	}
	h := c.w.Header()
	h.Set("Last-Modified", o.ModTime.UTC().Format(http.TimeFormat))
	if c.bkt.Versioning != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	return s.writeXML(c, http.StatusOK, out)
}

// --- RestoreObject -----------------------------------------------------------

func (s *Server) restoreObject(c *reqCtx) error {
	var in xmlRestoreRequest
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	if len(raw) > 0 {
		if err := xml.Unmarshal(raw, &in); err != nil {
			return errMalformedXML()
		}
	}
	if in.Type == "SELECT" {
		return errNotImplemented("Restore with Type=SELECT is not supported")
	}
	o, err := s.obj.StatObject(c.r.Context(), c.bucket, c.key, s.objectVersionID(c))
	if err == nil {
		if aerr := s.reauthorizeServed(c, o); aerr != nil {
			return aerr
		}
	}
	if err != nil {
		return err
	}
	if o.DeleteMarker {
		return s3err.New(s3err.MethodNotAllowed).WithHeader("x-amz-delete-marker", "true")
	}
	if o.StorageClass != "GLACIER" && o.StorageClass != "DEEP_ARCHIVE" && o.StorageClass != "GLACIER_IR" {
		return s3err.New(s3err.InvalidObjectState).WithMessage("Restore is not allowed for the object's current storage class")
	}
	days := in.Days
	if days <= 0 {
		days = 1
	}
	exp := time.Now().UTC().AddDate(0, 0, days)
	status := http.StatusAccepted
	if o.RestoreExpiry != nil && o.RestoreExpiry.After(time.Now()) {
		status = http.StatusOK
	}
	if _, err := s.obj.UpdateObjectMeta(c.r.Context(), c.bucket, c.key, o.VersionID, func(o *meta.Object) error { o.RestoreExpiry = &exp; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(status)
	return nil
}
