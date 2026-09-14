package s3api

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/lifecycle"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/object"
	"github.com/edward-b-1/opens3/internal/policy"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// --- service ---------------------------------------------------------------

func (s *Server) listBuckets(c *reqCtx) error {
	all, err := s.obj.ListBuckets(c.r.Context())
	if err != nil {
		return err
	}
	q := c.r.URL.Query()
	prefix := q.Get("prefix")
	maxB := atoiDefault(q.Get("max-buckets"), 10000)
	if maxB < 1 || maxB > 10000 {
		return errInvalidArg("max-buckets must be between 1 and 10000")
	}
	token := q.Get("continuation-token")
	res := xmlListAllMyBucketsResult{Xmlns: s3NS, Owner: xmlOwner{ID: c.identity.CanonicalID(), DisplayName: c.identity.Name()}}
	res.Buckets.Bucket = []xmlBucketEntry{}
	for _, b := range all {
		if prefix != "" && !strings.HasPrefix(b.Name, prefix) {
			continue
		}
		if token != "" && b.Name <= token {
			continue
		}
		if !c.identity.IsRoot && b.Owner != c.identity.CanonicalID() {
			// Non-owners see buckets their policies grant s3:ListBucket on.
			req := iam.Request{Identity: c.identity, Action: "s3:ListBucket", Bucket: b.Name, BucketOwner: b.Owner, BucketPolicy: b.Policy,
				BucketACL: b.ACL, Ownership: b.Ownership, Conditions: map[string][]string{}}
			if !s.iam.Authorize(req) {
				continue
			}
		}
		if len(res.Buckets.Bucket) >= maxB {
			res.ContinuationToken = res.Buckets.Bucket[len(res.Buckets.Bucket)-1].Name
			break
		}
		res.Buckets.Bucket = append(res.Buckets.Bucket, xmlBucketEntry{Name: b.Name, CreationDate: iso8601(b.Created), BucketRegion: b.Region})
	}
	res.Prefix = prefix
	return s.writeXML(c, http.StatusOK, res)
}

// --- bucket CRUD -----------------------------------------------------------

func (s *Server) createBucket(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	region := s.cfg.Region
	if len(raw) > 0 {
		var cfg xmlCreateBucketConfiguration
		if err := xml.Unmarshal(raw, &cfg); err != nil {
			return errMalformedXML()
		}
		if cfg.LocationConstraint != "" {
			region = cfg.LocationConstraint
			if s.cfg.EnforceRegion && region != s.cfg.Region {
				return s3err.New(s3err.InvalidLocationConstraint)
			}
		}
	}
	in := object.CreateBucketInput{Name: c.bucket, Region: region}
	if v := c.r.Header.Get("x-amz-bucket-object-lock-enabled"); strings.EqualFold(v, "true") {
		in.ObjectLockEnabled = true
	}
	in.Ownership = c.r.Header.Get("x-amz-object-ownership")
	switch in.Ownership {
	case "", "BucketOwnerEnforced", "BucketOwnerPreferred", "ObjectWriter":
	default:
		return errInvalidArg("Invalid x-amz-object-ownership value")
	}
	if in.Ownership == "" {
		in.Ownership = s.cfg.DefaultOwnership
		if in.Ownership == "" {
			in.Ownership = "BucketOwnerEnforced"
		}
	}
	tmp := &meta.Bucket{Ownership: in.Ownership, Owner: c.actor().CanonicalID, OwnerDisplay: c.actor().DisplayName}
	acl, err := s.parseACLHeaders(c, tmp)
	if err != nil {
		return err
	}
	in.ACL = acl
	if t := c.r.Header.Get("x-amz-bucket-tagging"); t != "" {
		in.Tags = parseTaggingHeader(t)
	}
	b, err := s.obj.CreateBucket(c.r.Context(), c.actor(), in)
	if err != nil {
		// "For legacy compatibility, if you re-create an existing bucket
		// that you already own in the North Virginia Region, Amazon S3
		// returns 200 OK" (CreateBucket API reference, BucketAlreadyOwnedByYou).
		if region == "us-east-1" && errors.Is(err, s3err.New(s3err.BucketAlreadyOwnedByYou)) {
			c.w.Header().Set("Location", "/"+c.bucket)
			c.w.WriteHeader(http.StatusOK)
			return nil
		}
		return err
	}
	loc := "/" + b.Name
	c.w.Header().Set("Location", loc)
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteBucket(c *reqCtx) error {
	if err := s.obj.DeleteBucket(c.r.Context(), c.bucket); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) headBucket(c *reqCtx) error {
	c.w.Header().Set("x-amz-bucket-region", c.bkt.Region)
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) getBucketLocation(c *reqCtx) error {
	loc := c.bkt.Region
	if loc == "us-east-1" {
		loc = ""
	}
	return s.writeXML(c, http.StatusOK, xmlLocationConstraint{Xmlns: s3NS, Location: loc})
}

// --- listing ---------------------------------------------------------------

func (s *Server) listObjectsV1(c *reqCtx) error {
	q := c.r.URL.Query()
	maxKeys := atoiDefault(q.Get("max-keys"), 1000)
	if maxKeys < 0 || q.Get("max-keys") != "" && !isInt(q.Get("max-keys")) {
		return errInvalidArg("Argument max-keys must be an integer between 0 and 2147483647")
	}
	if maxKeys > 1000 {
		maxKeys = 1000
	}
	encode := q.Get("encoding-type") == "url"
	if q.Get("encoding-type") != "" && !encode {
		return errInvalidArg("Invalid Encoding Method specified in Request")
	}
	opt := meta.ListOptions{Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), StartAfter: q.Get("marker"), MaxKeys: maxKeys}
	// ListObjects (v1) returns Prefix verbatim even with encoding-type=url
	// (SDKs decode Delimiter, Marker, NextMarker, Key and CommonPrefixes only).
	res := xmlListBucketResult{Xmlns: s3NS, Name: c.bucket, Prefix: opt.Prefix, MaxKeys: maxKeys, Delimiter: encodeKey(opt.Delimiter, encode)}
	marker := encodeKey(opt.StartAfter, encode)
	res.Marker = &marker
	if encode {
		res.EncodingType = "url"
	}
	res.Contents, res.CommonPrefixes = []xmlObjectEntry{}, []xmlCommonPrefix{}
	if maxKeys == 0 {
		return s.writeXML(c, http.StatusOK, res)
	}
	lr, err := s.obj.ListObjects(c.r.Context(), c.bucket, opt)
	if err != nil {
		return err
	}
	fetchOwner := true
	fillList(&res, lr, encode, fetchOwner)
	if lr.IsTruncated && opt.Delimiter != "" {
		res.NextMarker = encodeKey(lr.NextKey, encode)
	}
	return s.writeXML(c, http.StatusOK, res)
}

func (s *Server) listObjectsV2(c *reqCtx) error {
	q := c.r.URL.Query()
	maxKeys := atoiDefault(q.Get("max-keys"), 1000)
	if q.Get("max-keys") != "" && !isInt(q.Get("max-keys")) || maxKeys < 0 {
		return errInvalidArg("Argument max-keys must be an integer between 0 and 2147483647")
	}
	if maxKeys > 1000 {
		maxKeys = 1000
	}
	encode := q.Get("encoding-type") == "url"
	if q.Get("encoding-type") != "" && !encode {
		return errInvalidArg("Invalid Encoding Method specified in Request")
	}
	startAfter := q.Get("start-after")
	token := q.Get("continuation-token")
	if token != "" {
		dec, err := decodeToken(token)
		if err != nil {
			return errInvalidArg("The continuation token provided is incorrect")
		}
		startAfter = dec
	}
	opt := meta.ListOptions{Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), StartAfter: startAfter, MaxKeys: maxKeys}
	res := xmlListBucketResult{Xmlns: s3NS, Name: c.bucket, Prefix: encodeKey(opt.Prefix, encode), MaxKeys: maxKeys, Delimiter: encodeKey(opt.Delimiter, encode),
		StartAfter: encodeKey(q.Get("start-after"), encode)}
	if has(q, "continuation-token") {
		// Echoed whenever the parameter was sent, even when empty.
		res.ContinuationToken = &token
	}
	if encode {
		res.EncodingType = "url"
	}
	res.Contents, res.CommonPrefixes = []xmlObjectEntry{}, []xmlCommonPrefix{}
	kc := 0
	res.KeyCount = &kc
	if maxKeys == 0 {
		return s.writeXML(c, http.StatusOK, res)
	}
	lr, err := s.obj.ListObjects(c.r.Context(), c.bucket, opt)
	if err != nil {
		return err
	}
	fillList(&res, lr, encode, q.Get("fetch-owner") == "true")
	kc = len(res.Contents) + len(res.CommonPrefixes)
	if lr.IsTruncated {
		res.NextContinuationToken = encodeToken(lr.NextKey)
	}
	return s.writeXML(c, http.StatusOK, res)
}

func fillList(res *xmlListBucketResult, lr *meta.ListResult, encode, owner bool) {
	res.IsTruncated = lr.IsTruncated
	for _, e := range lr.Entries {
		if e.Object == nil {
			res.CommonPrefixes = append(res.CommonPrefixes, xmlCommonPrefix{Prefix: encodeKey(e.CommonPrefix, encode)})
			continue
		}
		o := e.Object
		ent := xmlObjectEntry{Key: encodeKey(o.Key, encode), LastModified: iso8601(o.ModTime), ETag: quoteETag(o.ETag), Size: o.Size, StorageClass: o.StorageClass}
		if ent.StorageClass == "" {
			ent.StorageClass = "STANDARD"
		}
		if o.Checksum != nil {
			ent.ChecksumAlgorithm = o.Checksum.Algorithm
			ent.ChecksumType = o.Checksum.Type
		}
		if owner {
			ent.Owner = &xmlOwner{ID: o.Owner, DisplayName: o.OwnerDisplay}
		}
		res.Contents = append(res.Contents, ent)
	}
}

func (s *Server) listObjectVersions(c *reqCtx) error {
	q := c.r.URL.Query()
	maxKeys := atoiDefault(q.Get("max-keys"), 1000)
	if q.Get("max-keys") != "" && !isInt(q.Get("max-keys")) || maxKeys < 0 {
		return errInvalidArg("Argument max-keys must be an integer between 0 and 2147483647")
	}
	if maxKeys > 1000 {
		maxKeys = 1000
	}
	encode := q.Get("encoding-type") == "url"
	opt := meta.ListOptions{Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), KeyMarker: q.Get("key-marker"), VersionMarker: q.Get("version-id-marker"), MaxKeys: maxKeys}
	if opt.VersionMarker != "" && opt.KeyMarker == "" {
		return errInvalidArg("A version-id marker cannot be specified without a key marker.")
	}
	res := xmlListVersionsResult{Xmlns: s3NS, Name: c.bucket, Prefix: encodeKey(opt.Prefix, encode), KeyMarker: encodeKey(opt.KeyMarker, encode),
		VersionIdMarker: opt.VersionMarker, MaxKeys: maxKeys, Delimiter: encodeKey(opt.Delimiter, encode)}
	if encode {
		res.EncodingType = "url"
	}
	res.Entries = []any{}
	if maxKeys == 0 {
		return s.writeXML(c, http.StatusOK, res)
	}
	lr, err := s.obj.ListVersions(c.r.Context(), c.bucket, opt)
	if err != nil {
		return err
	}
	res.IsTruncated = lr.IsTruncated
	for _, e := range lr.Entries {
		if e.Object == nil {
			res.Entries = append(res.Entries, xmlCommonPrefixEntry{Prefix: encodeKey(e.CommonPrefix, encode)})
			continue
		}
		o := e.Object
		own := xmlOwner{ID: o.Owner, DisplayName: o.OwnerDisplay}
		if o.DeleteMarker {
			res.Entries = append(res.Entries, xmlDeleteMarkerEntry{Key: encodeKey(o.Key, encode), VersionId: o.VersionID, IsLatest: e.IsLatest, LastModified: iso8601(o.ModTime), Owner: own})
			continue
		}
		v := xmlVersionEntry{Key: encodeKey(o.Key, encode), VersionId: o.VersionID, IsLatest: e.IsLatest, LastModified: iso8601(o.ModTime), ETag: quoteETag(o.ETag), Size: o.Size, Owner: own, StorageClass: o.StorageClass}
		if v.StorageClass == "" {
			v.StorageClass = "STANDARD"
		}
		if o.Checksum != nil {
			v.ChecksumAlgorithm, v.ChecksumType = o.Checksum.Algorithm, o.Checksum.Type
		}
		res.Entries = append(res.Entries, v)
	}
	if lr.IsTruncated {
		res.NextKeyMarker = encodeKey(lr.NextKey, encode)
		res.NextVersionIdMarker = lr.NextVersion
	}
	return s.writeXML(c, http.StatusOK, res)
}

func isInt(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// Continuation tokens are opaque base64 of the last key.
func encodeToken(k string) string { return base64URL([]byte(k)) }
func decodeToken(t string) (string, error) {
	b, err := base64URLDecode(t)
	return string(b), err
}

// --- versioning ------------------------------------------------------------

func (s *Server) getBucketVersioning(c *reqCtx) error {
	v := xmlVersioningConfiguration{Xmlns: s3NS, Status: c.bkt.Versioning}
	if c.bkt.Versioning != "" {
		if c.bkt.MFADelete {
			v.MfaDelete = "Enabled"
		} else {
			v.MfaDelete = "Disabled"
		}
	}
	return s.writeXML(c, http.StatusOK, v)
}

func (s *Server) putBucketVersioning(c *reqCtx) error {
	var v xmlVersioningConfiguration
	if err := s.readXML(c, &v); err != nil {
		return err
	}
	if v.Status != "Enabled" && v.Status != "Suspended" {
		return s3err.New(s3err.IllegalVersioningConfigurationException)
	}
	if v.MfaDelete == "Enabled" {
		return errNotImplemented("MFA Delete is not supported")
	}
	_, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error {
		if b.ObjectLockEnabled && v.Status == "Suspended" {
			return s3err.New(s3err.InvalidBucketState).WithMessage("An Object Lock configuration is present on this bucket, so the versioning state cannot be changed.")
		}
		b.Versioning = v.Status
		return nil
	})
	if err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

// --- tagging ---------------------------------------------------------------

func (s *Server) getBucketTagging(c *reqCtx) error {
	if len(c.bkt.Tags) == 0 {
		return s3err.New(s3err.NoSuchTagSet)
	}
	return s.writeXML(c, http.StatusOK, tagsToXML(c.bkt.Tags))
}

func (s *Server) putBucketTagging(c *reqCtx) error {
	var t xmlTagging
	if err := s.readXML(c, &t); err != nil {
		return err
	}
	tags := tagsFromXML(&t)
	if len(tags) > 50 {
		return s3err.New(s3err.BadRequest).WithMessage("Bucket tag count cannot be greater than 50")
	}
	if err := object.ValidateTags(tags); err != nil {
		return err
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.Tags = tags; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) deleteBucketTagging(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.Tags = nil; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- policy ----------------------------------------------------------------

func (s *Server) getBucketPolicy(c *reqCtx) error {
	if len(c.bkt.Policy) == 0 {
		return s3err.New(s3err.NoSuchBucketPolicy)
	}
	body := []byte(c.bkt.PolicyText)
	if len(body) == 0 {
		body = c.bkt.Policy
	}
	c.w.Header().Set("Content-Type", "application/json")
	c.w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	c.w.WriteHeader(http.StatusOK)
	c.w.Write(body)
	return nil
}

func (s *Server) putBucketPolicy(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return s3err.New(s3err.MissingRequestBodyError)
	}
	if len(raw) > 20*1024 {
		return s3err.New(s3err.MalformedPolicy).WithMessage("Policy exceeds the maximum allowed document size.")
	}
	doc, err := policy.Parse(raw)
	if err != nil {
		return s3err.New(s3err.MalformedPolicy).WithMessage("%s", strings.TrimPrefix(err.Error(), "policy: malformed policy document: "))
	}
	for _, st := range doc.Statements {
		if st.Effect == policy.Allow && st.NotPrincipal != nil {
			return s3err.New(s3err.MalformedPolicy).WithMessage("NotPrincipal cannot be used with an Allow effect")
		}
	}
	if err := policy.ValidateBucketPolicy(doc, c.bucket); err != nil {
		return s3err.New(s3err.MalformedPolicy).WithMessage("%s", strings.TrimPrefix(err.Error(), "policy: malformed policy document: "))
	}
	if pab := c.bkt.PublicAccessBlock; pab != nil && pab.BlockPublicPolicy && doc.IsPublic() {
		return s3err.New(s3err.AccessDenied).WithMessage("Bucket policy contains public statements and BlockPublicPolicy is enabled")
	}
	var compact json.RawMessage = raw
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error {
		b.Policy, b.PolicyText = compact, string(raw)
		b.SelfLock = strings.EqualFold(c.r.Header.Get("x-amz-confirm-remove-self-bucket-access"), "true")
		return nil
	}); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) deleteBucketPolicy(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.Policy, b.PolicyText = nil, ""; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) getBucketPolicyStatus(c *reqCtx) error {
	// The status reflects both the bucket policy and the bucket ACL.
	pub := false
	if len(c.bkt.Policy) > 0 {
		doc, err := policy.Parse(c.bkt.Policy)
		pub = err == nil && doc.IsPublic()
		if pab := c.bkt.PublicAccessBlock; pab != nil && pab.RestrictPublicBuckets {
			pub = false
		}
	}
	if c.bkt.Ownership != "BucketOwnerEnforced" && isPublicACL(c.bkt.ACL) {
		if pab := c.bkt.PublicAccessBlock; pab == nil || !pab.IgnorePublicAcls {
			pub = true
		}
	}
	return s.writeXML(c, http.StatusOK, xmlPolicyStatus{Xmlns: s3NS, IsPublic: pub})
}

// --- ACL -------------------------------------------------------------------

func (s *Server) getBucketACL(c *reqCtx) error {
	acl := c.bkt.ACL
	if acl == nil {
		acl = &meta.ACL{Owner: c.bkt.Owner, OwnerDisplay: c.bkt.OwnerDisplay, Grants: []meta.Grant{{Grantee: c.bkt.Owner, GranteeType: "CanonicalUser", DisplayName: c.bkt.OwnerDisplay, Permission: "FULL_CONTROL"}}}
	}
	return s.writeXML(c, http.StatusOK, aclToXML(acl))
}

func (s *Server) putBucketACL(c *reqCtx) error {
	if c.bkt.Ownership == "BucketOwnerEnforced" {
		canned := c.r.Header.Get("x-amz-acl")
		if canned == "bucket-owner-full-control" || canned == "" && c.r.ContentLength <= 0 {
			c.w.WriteHeader(http.StatusOK)
			return nil
		}
		if canned == "" {
			// Body form: accept only an owner-only ACL.
			var in xmlAccessControlPolicyIn
			if err := s.readXML(c, &in); err != nil {
				return err
			}
			acl, err := s.aclFromXML(&in, c.bkt.Owner)
			if err != nil {
				return err
			}
			for _, g := range acl.Grants {
				if g.Grantee != c.bkt.Owner {
					return s3err.New(s3err.AccessControlListNotSupported)
				}
			}
			c.w.WriteHeader(http.StatusOK)
			return nil
		}
		return s3err.New(s3err.AccessControlListNotSupported)
	}
	acl, err := s.parseACLHeaders(c, c.bkt)
	if err != nil {
		return err
	}
	if acl == nil {
		var in xmlAccessControlPolicyIn
		if err := s.readXML(c, &in); err != nil {
			return err
		}
		if acl, err = s.aclFromXML(&in, c.bkt.Owner); err != nil {
			return err
		}
	}
	acl.Owner, acl.OwnerDisplay = c.bkt.Owner, c.bkt.OwnerDisplay
	if pab := c.bkt.PublicAccessBlock; pab != nil && pab.BlockPublicAcls && isPublicACL(acl) {
		return s3err.New(s3err.AccessDenied).WithMessage("Public ACLs are blocked by the BlockPublicAcls setting")
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.ACL = acl; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

// --- CORS ------------------------------------------------------------------

func (s *Server) getBucketCORS(c *reqCtx) error {
	if len(c.bkt.CORSXML) == 0 {
		return s3err.New(s3err.NoSuchCORSConfiguration)
	}
	return s.writeRawXML(c, c.bkt.CORSXML)
}

func (s *Server) putBucketCORS(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var cfg xmlCORSConfiguration
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return errMalformedXML()
	}
	if err := validateCORS(&cfg); err != nil {
		return err
	}
	if err := requireContentMD5(c, raw, false); err != nil {
		return err
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.CORSXML = raw; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteBucketCORS(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.CORSXML = nil; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// writeRawXML writes stored XML verbatim.
func (s *Server) writeRawXML(c *reqCtx, raw []byte) error {
	body := raw
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "<?xml") {
		body = append([]byte(xml.Header), raw...)
	}
	c.w.Header().Set("Content-Type", "application/xml")
	c.w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	c.w.WriteHeader(http.StatusOK)
	_, err := c.w.Write(body)
	return err
}

// --- lifecycle -------------------------------------------------------------

func (s *Server) getBucketLifecycle(c *reqCtx) error {
	if len(c.bkt.LifecycleXML) == 0 {
		return s3err.New(s3err.NoSuchLifecycleConfiguration)
	}
	return s.writeRawXML(c, c.bkt.LifecycleXML)
}

func (s *Server) putBucketLifecycle(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	cfg, err := lifecycle.Parse(raw)
	if err != nil {
		return err
	}
	if err := lifecycle.Validate(cfg); err != nil {
		return err
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.LifecycleXML = raw; return nil }); err != nil {
		return err
	}
	c.w.Header().Set("x-amz-transition-default-minimum-object-size", "all_storage_classes_128K")
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteBucketLifecycle(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.LifecycleXML = nil; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- encryption ------------------------------------------------------------

func (s *Server) getBucketEncryption(c *reqCtx) error {
	if c.bkt.Encryption == nil {
		return s3err.New(s3err.ServerSideEncryptionConfigurationNotFoundError)
	}
	var out xmlServerSideEncryptionConfiguration
	out.Xmlns = s3NS
	out.Rules = make([]struct {
		ApplyServerSideEncryptionByDefault *struct {
			SSEAlgorithm   string `xml:"SSEAlgorithm"`
			KMSMasterKeyID string `xml:"KMSMasterKeyID,omitempty"`
		} `xml:"ApplyServerSideEncryptionByDefault"`
		BucketKeyEnabled *bool `xml:"BucketKeyEnabled"`
	}, 1)
	out.Rules[0].ApplyServerSideEncryptionByDefault = &struct {
		SSEAlgorithm   string `xml:"SSEAlgorithm"`
		KMSMasterKeyID string `xml:"KMSMasterKeyID,omitempty"`
	}{SSEAlgorithm: c.bkt.Encryption.Algorithm, KMSMasterKeyID: c.bkt.Encryption.KMSKeyID}
	bk := c.bkt.Encryption.BucketKey
	out.Rules[0].BucketKeyEnabled = &bk
	return s.writeXML(c, http.StatusOK, out)
}

func (s *Server) putBucketEncryption(c *reqCtx) error {
	var in xmlServerSideEncryptionConfiguration
	if err := s.readXML(c, &in); err != nil {
		return err
	}
	if len(in.Rules) != 1 || in.Rules[0].ApplyServerSideEncryptionByDefault == nil {
		return errMalformedXML()
	}
	d := in.Rules[0].ApplyServerSideEncryptionByDefault
	rule := &meta.EncryptionRule{Algorithm: d.SSEAlgorithm, KMSKeyID: d.KMSMasterKeyID}
	switch d.SSEAlgorithm {
	case "AES256":
		if d.KMSMasterKeyID != "" {
			return errMalformedXML()
		}
	case "aws:kms", "aws:kms:dsse":
		if d.KMSMasterKeyID != "" && !s.kms.KeyExists(d.KMSMasterKeyID) {
			return s3err.New(s3err.KMSKeyNotFoundException)
		}
	default:
		return errMalformedXML()
	}
	if in.Rules[0].BucketKeyEnabled != nil {
		rule.BucketKey = *in.Rules[0].BucketKeyEnabled
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.Encryption = rule; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteBucketEncryption(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.Encryption = nil; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- object lock -----------------------------------------------------------

func (s *Server) getObjectLockConfig(c *reqCtx) error {
	if !c.bkt.ObjectLockEnabled {
		return s3err.New(s3err.ObjectLockConfigurationNotFoundError)
	}
	out := xmlObjectLockConfiguration{Xmlns: s3NS, ObjectLockEnabled: "Enabled"}
	if ol := c.bkt.ObjectLock; ol != nil {
		out.Rule = &struct {
			DefaultRetention struct {
				Mode  string `xml:"Mode"`
				Days  int    `xml:"Days,omitempty"`
				Years int    `xml:"Years,omitempty"`
			} `xml:"DefaultRetention"`
		}{}
		out.Rule.DefaultRetention.Mode, out.Rule.DefaultRetention.Days, out.Rule.DefaultRetention.Years = ol.Mode, ol.Days, ol.Years
	}
	return s.writeXML(c, http.StatusOK, out)
}

func (s *Server) putObjectLockConfig(c *reqCtx) error {
	var in xmlObjectLockConfiguration
	if err := s.readXML(c, &in); err != nil {
		return err
	}
	if in.ObjectLockEnabled != "Enabled" {
		return errMalformedXML()
	}
	var cfg *meta.ObjectLockConfig
	if in.Rule != nil {
		d := in.Rule.DefaultRetention
		if d.Mode != "GOVERNANCE" && d.Mode != "COMPLIANCE" {
			return errMalformedXML()
		}
		if d.Days < 0 || d.Years < 0 || (d.Days == 0 && d.Years == 0) || d.Years > 100 || d.Days > 36500 {
			return s3err.New(s3err.InvalidRetentionPeriod)
		}
		if d.Days > 0 && d.Years > 0 {
			return errMalformedXML()
		}
		cfg = &meta.ObjectLockConfig{Mode: d.Mode, Days: d.Days, Years: d.Years}
	}
	_, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error {
		if !b.ObjectLockEnabled {
			// AWS allows enabling lock on existing buckets when versioning is on.
			if b.Versioning != "Enabled" {
				return s3err.New(s3err.InvalidBucketState).WithMessage("Versioning must be 'Enabled' on the bucket to apply a Object Lock configuration")
			}
			b.ObjectLockEnabled = true
		}
		b.ObjectLock = cfg
		return nil
	})
	if err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

// --- notification ----------------------------------------------------------

func (s *Server) getBucketNotification(c *reqCtx) error {
	if len(c.bkt.NotificationXML) == 0 {
		return s.writeXML(c, http.StatusOK, xmlNotificationConfiguration{Xmlns: s3NS})
	}
	return s.writeRawXML(c, c.bkt.NotificationXML)
}

func (s *Server) putBucketNotification(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var cfg xmlNotificationConfiguration
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return errMalformedXML()
	}
	for _, list := range [][]xmlNotificationTarget{cfg.Topics, cfg.Queues, cfg.Lambdas} {
		for _, t := range list {
			if len(t.Events) == 0 {
				return errMalformedXML()
			}
			for _, e := range t.Events {
				if !validEventName(e) {
					return s3err.New(s3err.InvalidArgument).WithMessage("The event '%s' is not supported for notifications.", e)
				}
			}
			arn := t.Topic + t.Queue + t.Lambda
			if s.ValidateTarget != nil {
				if err := s.ValidateTarget(arn); err != nil {
					return s3err.New(s3err.InvalidArgument).WithMessage("Unable to validate the following destination configurations: %s", arn)
				}
			}
		}
	}
	if len(cfg.Topics)+len(cfg.Queues)+len(cfg.Lambdas) == 0 && cfg.EventBridge == nil {
		raw = nil
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.NotificationXML = raw; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

var eventNames = []string{"s3:ObjectCreated:*", "s3:ObjectCreated:Put", "s3:ObjectCreated:Post", "s3:ObjectCreated:Copy", "s3:ObjectCreated:CompleteMultipartUpload",
	"s3:ObjectRemoved:*", "s3:ObjectRemoved:Delete", "s3:ObjectRemoved:DeleteMarkerCreated", "s3:ObjectRestore:*", "s3:ObjectRestore:Post", "s3:ObjectRestore:Completed", "s3:ObjectRestore:Delete",
	"s3:ReducedRedundancyLostObject", "s3:Replication:*", "s3:Replication:OperationFailedReplication", "s3:Replication:OperationMissedThreshold", "s3:Replication:OperationReplicatedAfterThreshold", "s3:Replication:OperationNotTracked",
	"s3:LifecycleExpiration:*", "s3:LifecycleExpiration:Delete", "s3:LifecycleExpiration:DeleteMarkerCreated", "s3:LifecycleTransition", "s3:IntelligentTiering",
	"s3:ObjectTagging:*", "s3:ObjectTagging:Put", "s3:ObjectTagging:Delete", "s3:ObjectAcl:Put", "s3:ObjectRetention:Put", "s3:ObjectAnnotation:*", "s3:ObjectAnnotation:Put", "s3:ObjectAnnotation:Delete"}

func validEventName(e string) bool {
	for _, n := range eventNames {
		if n == e {
			return true
		}
	}
	return false
}

// --- website, logging, replication, PAB, ownership, accelerate, payment ------

func (s *Server) getBucketWebsite(c *reqCtx) error {
	if len(c.bkt.WebsiteXML) == 0 {
		return s3err.New(s3err.NoSuchWebsiteConfiguration)
	}
	return s.writeRawXML(c, c.bkt.WebsiteXML)
}

func (s *Server) putBucketWebsite(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var cfg xmlWebsiteConfiguration
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return errMalformedXML()
	}
	if cfg.RedirectAllRequestsTo == nil && (cfg.IndexDocument == nil || cfg.IndexDocument.Suffix == "") {
		return s3err.New(s3err.InvalidArgument).WithMessage("A value for IndexDocument Suffix must be provided if RedirectAllRequestsTo is empty")
	}
	if cfg.RedirectAllRequestsTo != nil && (cfg.IndexDocument != nil || cfg.ErrorDocument != nil || cfg.RoutingRules != nil) {
		return s3err.New(s3err.InvalidArgument).WithMessage("RedirectAllRequestsTo cannot be provided in conjunction with other Routing Rules.")
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.WebsiteXML = raw; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteBucketWebsite(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.WebsiteXML = nil; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) getBucketLogging(c *reqCtx) error {
	if len(c.bkt.LoggingXML) == 0 {
		return s.writeRawXML(c, []byte(`<BucketLoggingStatus xmlns="`+s3NS+`"></BucketLoggingStatus>`))
	}
	return s.writeRawXML(c, c.bkt.LoggingXML)
}

func (s *Server) putBucketLogging(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var cfg struct {
		XMLName        xml.Name `xml:"BucketLoggingStatus"`
		LoggingEnabled *struct {
			TargetBucket string `xml:"TargetBucket"`
			TargetPrefix string `xml:"TargetPrefix"`
		} `xml:"LoggingEnabled"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return errMalformedXML()
	}
	if cfg.LoggingEnabled != nil {
		if _, err := s.obj.GetBucket(c.r.Context(), cfg.LoggingEnabled.TargetBucket); err != nil {
			return s3err.New(s3err.InvalidTargetBucketForLogging)
		}
	} else {
		raw = nil
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.LoggingXML = raw; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) getBucketReplication(c *reqCtx) error {
	if len(c.bkt.ReplicationXML) == 0 {
		return s3err.New(s3err.ReplicationConfigurationNotFoundError)
	}
	return s.writeRawXML(c, c.bkt.ReplicationXML)
}

func (s *Server) putBucketReplication(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var cfg struct {
		XMLName xml.Name `xml:"ReplicationConfiguration"`
		Role    string   `xml:"Role"`
		Rules   []struct {
			Status      string `xml:"Status"`
			Destination struct {
				Bucket string `xml:"Bucket"`
			} `xml:"Destination"`
		} `xml:"Rule"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil || len(cfg.Rules) == 0 {
		return errMalformedXML()
	}
	if c.bkt.Versioning != "Enabled" {
		return s3err.New(s3err.InvalidRequest).WithMessage("Versioning must be 'Enabled' on the bucket to apply a replication configuration")
	}
	for _, r := range cfg.Rules {
		if r.Status != "Enabled" && r.Status != "Disabled" || r.Destination.Bucket == "" {
			return errMalformedXML()
		}
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.ReplicationXML = raw; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteBucketReplication(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.ReplicationXML = nil; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) getPublicAccessBlock(c *reqCtx) error {
	p := c.bkt.PublicAccessBlock
	if p == nil {
		return s3err.New(s3err.NoSuchPublicAccessBlockConfiguration)
	}
	return s.writeXML(c, http.StatusOK, xmlPublicAccessBlock{Xmlns: s3NS, BlockPublicAcls: p.BlockPublicAcls, IgnorePublicAcls: p.IgnorePublicAcls, BlockPublicPolicy: p.BlockPublicPolicy, RestrictPublicBuckets: p.RestrictPublicBuckets})
}

func (s *Server) putPublicAccessBlock(c *reqCtx) error {
	var in xmlPublicAccessBlock
	if err := s.readXML(c, &in); err != nil {
		return err
	}
	p := &meta.PublicAccessBlock{BlockPublicAcls: in.BlockPublicAcls, IgnorePublicAcls: in.IgnorePublicAcls, BlockPublicPolicy: in.BlockPublicPolicy, RestrictPublicBuckets: in.RestrictPublicBuckets}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.PublicAccessBlock = p; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deletePublicAccessBlock(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.PublicAccessBlock = nil; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) getOwnershipControls(c *reqCtx) error {
	if c.bkt.Ownership == "" {
		return s3err.New(s3err.OwnershipControlsNotFoundError)
	}
	var out xmlOwnershipControls
	out.Xmlns = s3NS
	out.Rules = []struct {
		ObjectOwnership string `xml:"ObjectOwnership"`
	}{{ObjectOwnership: c.bkt.Ownership}}
	return s.writeXML(c, http.StatusOK, out)
}

func (s *Server) putOwnershipControls(c *reqCtx) error {
	var in xmlOwnershipControls
	if err := s.readXML(c, &in); err != nil {
		return err
	}
	if len(in.Rules) != 1 {
		return errMalformedXML()
	}
	v := in.Rules[0].ObjectOwnership
	switch v {
	case "BucketOwnerEnforced", "BucketOwnerPreferred", "ObjectWriter":
	default:
		return errMalformedXML()
	}
	if v == "BucketOwnerEnforced" && c.bkt.ACL != nil {
		for _, g := range c.bkt.ACL.Grants {
			if g.GranteeType != "CanonicalUser" || g.Grantee != c.bkt.Owner {
				return s3err.New(s3err.InvalidBucketAclWithObjectOwnership)
			}
		}
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.Ownership = v; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteOwnershipControls(c *reqCtx) error {
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.Ownership = ""; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) getBucketAccelerate(c *reqCtx) error {
	if len(c.bkt.AccelerateXML) == 0 {
		return s.writeRawXML(c, []byte(`<AccelerateConfiguration xmlns="`+s3NS+`"/>`))
	}
	return s.writeRawXML(c, c.bkt.AccelerateXML)
}

func (s *Server) putBucketAccelerate(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var cfg struct {
		XMLName xml.Name `xml:"AccelerateConfiguration"`
		Status  string   `xml:"Status"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil || (cfg.Status != "Enabled" && cfg.Status != "Suspended") {
		return errMalformedXML()
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.AccelerateXML = raw; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) getBucketRequestPayment(c *reqCtx) error {
	if len(c.bkt.RequestPaymentXML) == 0 {
		return s.writeRawXML(c, []byte(`<RequestPaymentConfiguration xmlns="`+s3NS+`"><Payer>BucketOwner</Payer></RequestPaymentConfiguration>`))
	}
	return s.writeRawXML(c, c.bkt.RequestPaymentXML)
}

func (s *Server) putBucketRequestPayment(c *reqCtx) error {
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var cfg struct {
		XMLName xml.Name `xml:"RequestPaymentConfiguration"`
		Payer   string   `xml:"Payer"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil || (cfg.Payer != "Requester" && cfg.Payer != "BucketOwner") {
		return errMalformedXML()
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error { b.RequestPaymentXML = raw; return nil }); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

// --- named configurations (metrics, analytics, inventory, tiering) ---------

func namedConfigMap(b *meta.Bucket, kind string) *map[string][]byte {
	switch kind {
	case "metrics":
		return &b.MetricsXML
	case "analytics":
		return &b.AnalyticsXML
	case "inventory":
		return &b.InventoryXML
	default:
		return &b.IntelligentTieringXML
	}
}

func namedConfigKind(c *reqCtx) string {
	q := c.r.URL.Query()
	for _, k := range []string{"metrics", "analytics", "inventory", "intelligent-tiering"} {
		if has(q, k) {
			return k
		}
	}
	return ""
}

func (s *Server) getNamedConfig(c *reqCtx) error {
	kind := namedConfigKind(c)
	id := c.r.URL.Query().Get("id")
	m := *namedConfigMap(c.bkt, kind)
	if id == "" {
		// List form.
		root := map[string]string{"metrics": "ListMetricsConfigurationsResult", "analytics": "ListBucketAnalyticsConfigurationsResult",
			"inventory": "ListInventoryConfigurationsResult", "intelligent-tiering": "ListBucketIntelligentTieringConfigurationsResult"}[kind]
		var sb strings.Builder
		sb.WriteString(xml.Header + "<" + root + ` xmlns="` + s3NS + `">`)
		for _, k := range sortedKeys(m) {
			sb.Write(stripXMLHeader(m[k]))
		}
		sb.WriteString("<IsTruncated>false</IsTruncated></" + root + ">")
		return s.writeRawXML(c, []byte(sb.String()))
	}
	raw, ok := m[id]
	if !ok {
		return s3err.New(s3err.NoSuchConfiguration)
	}
	return s.writeRawXML(c, raw)
}

func (s *Server) putNamedConfig(c *reqCtx) error {
	kind := namedConfigKind(c)
	id := c.r.URL.Query().Get("id")
	if id == "" {
		return errInvalidArg("id is required")
	}
	raw, err := s.readBody(c)
	if err != nil {
		return err
	}
	var probe struct {
		XMLName xml.Name
		ID      string `xml:"Id"`
	}
	if err := xml.Unmarshal(raw, &probe); err != nil {
		return errMalformedXML()
	}
	if probe.ID != "" && probe.ID != id {
		return errInvalidArg("Configuration Id in the body does not match the id in the request")
	}
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error {
		m := namedConfigMap(b, kind)
		if *m == nil {
			*m = map[string][]byte{}
		}
		if len(*m) >= 1000 {
			return s3err.New(s3err.TooManyConfigurations)
		}
		(*m)[id] = raw
		return nil
	}); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusOK)
	return nil
}

func (s *Server) deleteNamedConfig(c *reqCtx) error {
	kind := namedConfigKind(c)
	id := c.r.URL.Query().Get("id")
	if _, err := s.obj.UpdateBucket(c.r.Context(), c.bucket, func(b *meta.Bucket) error {
		m := namedConfigMap(b, kind)
		if _, ok := (*m)[id]; !ok {
			return s3err.New(s3err.NoSuchConfiguration)
		}
		delete(*m, id)
		return nil
	}); err != nil {
		return err
	}
	c.w.WriteHeader(http.StatusNoContent)
	return nil
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func stripXMLHeader(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "<?xml") {
		if i := strings.Index(s, "?>"); i >= 0 {
			s = strings.TrimSpace(s[i+2:])
		}
	}
	return []byte(s)
}
