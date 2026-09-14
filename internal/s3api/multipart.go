package s3api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/edward-b-1/opens3/internal/checksum"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/s3err"
)

func (s *Server) createMultipartUpload(c *reqCtx) error {
	r := c.r
	attrs, err := s.parseObjectAttrs(c, false)
	if err != nil {
		return err
	}
	if err := s.authorizeAttributes(c, c.r.Header.Get); err != nil {
		return err
	}
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
	}
	in := object.CreateUploadInput{Bucket: c.bucket, Key: c.key, Attrs: attrs, SSE: sseReq,
		ChecksumAlgorithm: r.Header.Get("x-amz-checksum-algorithm"), ChecksumType: r.Header.Get("x-amz-checksum-type")}
	u, err := s.obj.CreateUpload(r.Context(), c.actor(), in)
	if err != nil {
		return err
	}
	h := c.w.Header()
	if u.SSE != nil {
		setSSEResponseHeaders(h, &meta.Object{SSE: u.SSE})
	}
	if u.ChecksumAlgorithm != "" {
		h.Set("x-amz-checksum-algorithm", u.ChecksumAlgorithm)
		h.Set("x-amz-checksum-type", u.ChecksumType)
	}
	return s.writeXML(c, http.StatusOK, xmlInitiateMultipartUploadResult{Xmlns: s3NS, Bucket: c.bucket, Key: c.key, UploadId: u.UploadID})
}

func (s *Server) uploadPart(c *reqCtx) error {
	r := c.r
	q := r.URL.Query()
	pn, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil {
		return errInvalidArg("Part number must be an integer between 1 and 10000, inclusive")
	}
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
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
	p, err := s.obj.UploadPart(r.Context(), object.UploadPartInput{Bucket: c.bucket, Key: c.key, UploadID: q.Get("uploadId"), PartNumber: pn,
		Body: c.body, Size: size, ExpectedMD5: md5hex, ExpectedSHA256: c.body.expectedSHA256, Checksum: cr, SSE: sseReq})
	if err != nil {
		return err
	}
	h := c.w.Header()
	h.Set("ETag", quoteETag(p.ETag))
	if p.Checksum != "" {
		u, _ := s.obj.GetUpload(r.Context(), c.bucket, c.key, q.Get("uploadId"))
		alg := ""
		if u != nil {
			alg = u.ChecksumAlgorithm
		}
		if alg == "" && cr != nil {
			alg = checksum.Normalize(cr.Algorithm)
		}
		if alg != "" {
			h.Set(checksum.HeaderName(alg), p.Checksum)
		}
	}
	if sseReq.Type == "SSE-C" {
		h.Set("x-amz-server-side-encryption-customer-algorithm", "AES256")
		h.Set("x-amz-server-side-encryption-customer-key-MD5", sseReq.CustomerKeyMD5)
	} else if u, err := s.obj.GetUpload(r.Context(), c.bucket, c.key, q.Get("uploadId")); err == nil && u.SSE != nil {
		setSSEResponseHeaders(h, &meta.Object{SSE: u.SSE})
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) uploadPartCopy(c *reqCtx) error {
	r := c.r
	q := r.URL.Query()
	pn, err := strconv.Atoi(q.Get("partNumber"))
	if err != nil {
		return errInvalidArg("Part number must be an integer between 1 and 10000, inclusive")
	}
	sb, sk, sv, err := parseCopySource(r.Header.Get("x-amz-copy-source"))
	if err != nil {
		return err
	}
	srcBucket, err := s.obj.GetBucket(r.Context(), sb)
	if err != nil {
		return err
	}
	srcAction := "s3:GetObject"
	if sv != "" {
		srcAction = "s3:GetObjectVersion"
	}
	srcReq := iamRequest{Identity: c.identity, Action: srcAction, Bucket: sb, Key: sk, Conditions: s.conditionContextFor(c, srcBucket),
		BucketOwner: srcBucket.Owner, BucketPolicy: srcBucket.Policy, BucketACL: srcBucket.ACL, PublicAccessBlock: srcBucket.PublicAccessBlock, Ownership: srcBucket.Ownership}
	so, soErr := s.obj.StatObject(r.Context(), sb, sk, sv)
	if soErr == nil {
		applyObjectContext(&srcReq, so)
	} else {
		so = nil
	}
	c.r = c.r.WithContext(object.WithExpectedSource(c.r.Context(), srcBucket, so))
	r = c.r
	if !s.iam.Authorize(srcReq) {
		return errAccessDenied()
	}
	var rng *object.Range
	if v := r.Header.Get("x-amz-copy-source-range"); v != "" {
		rng, err = parseRange(v)
		if err != nil || rng == nil || rng.Start < 0 || rng.End < 0 {
			return s3err.New(s3err.InvalidArgument).WithMessage("The x-amz-copy-source-range value must be of the form bytes=first-last where first and last are the zero-based offsets of the first and last byte to copy")
		}
	}
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
	}
	srcSSE, err := parseSSEHeaders(r, true)
	if err != nil {
		return err
	}
	p, src, err := s.obj.UploadPartCopy(r.Context(), object.UploadPartCopyInput{SrcBucket: sb, SrcKey: sk, SrcVersionID: sv, SrcRange: rng,
		SrcConditions: readConditions(r, "x-amz-copy-source-"), SrcSSE: srcSSE, Bucket: c.bucket, Key: c.key, UploadID: q.Get("uploadId"), PartNumber: pn, SSE: sseReq})
	if err != nil {
		return err
	}
	h := c.w.Header()
	if srcBucket.Versioning != "" {
		h.Set("x-amz-copy-source-version-id", src.VersionID)
	}
	if sseReq.Type == "SSE-C" {
		h.Set("x-amz-server-side-encryption-customer-algorithm", "AES256")
		h.Set("x-amz-server-side-encryption-customer-key-MD5", sseReq.CustomerKeyMD5)
	} else if u, err := s.obj.GetUpload(r.Context(), c.bucket, c.key, q.Get("uploadId")); err == nil && u.SSE != nil {
		setSSEResponseHeaders(h, &meta.Object{SSE: u.SSE})
	}
	return s.writeXML(c, http.StatusOK, xmlCopyPartResult{Xmlns: s3NS, ETag: quoteETag(p.ETag), LastModified: iso8601(src.ModTime)})
}

func (s *Server) completeMultipartUpload(c *reqCtx) error {
	r := c.r
	var in xmlCompleteMultipartUpload
	if err := s.readXML(c, &in); err != nil {
		if errors.Is(err, s3err.New(s3err.MissingRequestBodyError)) {
			return errMalformedXML()
		}
		return err
	}
	parts := make([]object.CompletePart, 0, len(in.Parts))
	for _, p := range in.Parts {
		cp := object.CompletePart{PartNumber: p.PartNumber, ETag: strings.Trim(p.ETag, `"`)}
		for _, v := range []string{p.ChecksumCRC32, p.ChecksumCRC32C, p.ChecksumSHA1, p.ChecksumSHA256, p.ChecksumCRC64NVME} {
			if v != "" {
				cp.Checksum = v
			}
		}
		parts = append(parts, cp)
	}
	cr, err := parseChecksumHeaders(c)
	if err != nil {
		return err
	}
	if cr != nil {
		cr.Trailer = nil
	}
	objSize := int64(-1)
	if v := r.Header.Get("x-amz-mp-object-size"); v != "" {
		if objSize, err = strconv.ParseInt(v, 10, 64); err != nil || objSize < 0 {
			return errInvalidArg("x-amz-mp-object-size must be a non-negative integer")
		}
	}
	cond := readConditions(r, "")
	sseReq, err := parseSSEHeaders(r, false)
	if err != nil {
		return err
	}
	o, err := s.obj.CompleteUpload(r.Context(), c.actor(), object.CompleteInput{Bucket: c.bucket, Key: c.key, UploadID: r.URL.Query().Get("uploadId"),
		Parts: parts, Conditions: object.Conditions{IfMatch: cond.IfMatch, IfNoneMatch: cond.IfNoneMatch}, Checksum: cr, ObjectSize: objSize, SSE: sseReq})
	if err != nil {
		return err
	}
	h := c.w.Header()
	if c.bkt.Versioning != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	setSSEResponseHeaders(h, o)
	loc := s.schemeOf(r) + "://" + r.Host + "/" + c.bucket + "/" + c.key
	out := xmlCompleteMultipartUploadResult{Xmlns: s3NS, Location: loc, Bucket: c.bucket, Key: c.key, ETag: quoteETag(o.ETag)}
	if o.Checksum != nil {
		setChecksumField(&out.ChecksumCRC32, &out.ChecksumCRC32C, &out.ChecksumSHA1, &out.ChecksumSHA256, &out.ChecksumCRC64NVME, o.Checksum)
		out.ChecksumType = o.Checksum.Type
	}
	return s.writeXML(c, http.StatusOK, out)
}

func (s *Server) schemeOf(r *http.Request) string {
	if s.cfg.TrustedProxies.Secure(r) {
		return "https"
	}
	return "http"
}

func (s *Server) abortMultipartUpload(c *reqCtx) error {
	if err := s.obj.AbortUpload(c.r.Context(), c.bucket, c.key, c.r.URL.Query().Get("uploadId")); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listParts(c *reqCtx) error {
	q := c.r.URL.Query()
	maxParts := atoiDefault(q.Get("max-parts"), 1000)
	if maxParts < 0 {
		return errInvalidArg("max-parts must be non-negative")
	}
	marker := atoiDefault(q.Get("part-number-marker"), 0)
	u, parts, truncated, err := s.obj.ListParts(c.r.Context(), c.bucket, c.key, q.Get("uploadId"), marker, maxParts)
	if err != nil {
		return err
	}
	out := xmlListPartsResult{Xmlns: s3NS, Bucket: c.bucket, Key: c.key, UploadId: u.UploadID, PartNumberMarker: marker, MaxParts: maxParts, IsTruncated: truncated,
		Initiator: xmlOwner{ID: u.Owner, DisplayName: u.OwnerDisplay}, Owner: xmlOwner{ID: u.Owner, DisplayName: u.OwnerDisplay}, StorageClass: u.StorageClass,
		ChecksumAlgorithm: u.ChecksumAlgorithm, ChecksumType: u.ChecksumType}
	if out.StorageClass == "" {
		out.StorageClass = "STANDARD"
	}
	out.Parts = []xmlPart{}
	for _, p := range parts {
		xp := xmlPart{PartNumber: p.Number, ETag: quoteETag(p.ETag), Size: p.Size, LastModified: iso8601(u.Initiated)}
		if u.ChecksumAlgorithm != "" && p.Checksum != "" {
			setChecksumField(&xp.ChecksumCRC32, &xp.ChecksumCRC32C, &xp.ChecksumSHA1, &xp.ChecksumSHA256, &xp.ChecksumCRC64NVME, &meta.Checksum{Algorithm: u.ChecksumAlgorithm, Value: p.Checksum})
		}
		out.Parts = append(out.Parts, xp)
	}
	if truncated && len(parts) > 0 {
		out.NextPartNumberMarker = parts[len(parts)-1].Number
	}
	return s.writeXML(c, http.StatusOK, out)
}

func (s *Server) listMultipartUploads(c *reqCtx) error {
	q := c.r.URL.Query()
	maxU := atoiDefault(q.Get("max-uploads"), 1000)
	if maxU < 0 || maxU > 1000 {
		maxU = 1000
	}
	encode := q.Get("encoding-type") == "url"
	opt := meta.ListOptions{Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), KeyMarker: q.Get("key-marker"), VersionMarker: q.Get("upload-id-marker"), MaxKeys: maxU}
	res, err := s.obj.ListUploads(c.r.Context(), c.bucket, opt)
	if err != nil {
		return err
	}
	out := xmlListMultipartUploadsResult{Xmlns: s3NS, Bucket: c.bucket, KeyMarker: encodeKey(opt.KeyMarker, encode), UploadIdMarker: opt.VersionMarker,
		Prefix: encodeKey(opt.Prefix, encode), Delimiter: encodeKey(opt.Delimiter, encode), MaxUploads: maxU, IsTruncated: res.IsTruncated}
	if encode {
		out.EncodingType = "url"
	}
	out.Uploads, out.CommonPrefixes = []xmlUpload{}, []xmlCommonPrefix{}
	for _, u := range res.Uploads {
		xu := xmlUpload{Key: encodeKey(u.Key, encode), UploadId: u.UploadID, Initiator: xmlOwner{ID: u.Owner, DisplayName: u.OwnerDisplay}, Owner: xmlOwner{ID: u.Owner, DisplayName: u.OwnerDisplay},
			StorageClass: u.StorageClass, Initiated: iso8601(u.Initiated), ChecksumAlgorithm: u.ChecksumAlgorithm, ChecksumType: u.ChecksumType}
		if xu.StorageClass == "" {
			xu.StorageClass = "STANDARD"
		}
		out.Uploads = append(out.Uploads, xu)
	}
	for _, p := range res.Prefixes {
		out.CommonPrefixes = append(out.CommonPrefixes, xmlCommonPrefix{Prefix: encodeKey(p, encode)})
	}
	if res.IsTruncated {
		out.NextKeyMarker = encodeKey(res.NextKey, encode)
		out.NextUploadIdMarker = res.NextUploadID
	}
	return s.writeXML(c, http.StatusOK, out)
}
