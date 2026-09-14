package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/meta"
	"gitlab.com/Birdsall/opens3/internal/object"
	"gitlab.com/Birdsall/opens3/internal/policy"
)

type bucketSummary struct {
	Name       string    `json:"name"`
	Created    time.Time `json:"created"`
	Region     string    `json:"region"`
	Owner      string    `json:"owner"`
	OwnerName  string    `json:"ownerName"`
	Versioning string    `json:"versioning"`
	ObjectLock bool      `json:"objectLock"`
	Mine       bool      `json:"mine"`
}

type bucketDetail struct {
	bucketSummary
	Tags              []map[string]string     `json:"tags"`
	Ownership         string                  `json:"ownership"`
	Encryption        *meta.EncryptionRule    `json:"encryption,omitempty"`
	PublicAccessBlock *meta.PublicAccessBlock `json:"publicAccessBlock,omitempty"`
	ObjectLockConfig  *meta.ObjectLockConfig  `json:"objectLockConfig,omitempty"`
	Policy            json.RawMessage         `json:"policy,omitempty"`
	PolicyReadable    bool                    `json:"policyReadable"`
	PolicyPublic      bool                    `json:"policyPublic"`
	Quota             int64                   `json:"quota"`
	HasLifecycle      bool                    `json:"hasLifecycle"`
	HasCORS           bool                    `json:"hasCors"`
	HasNotification   bool                    `json:"hasNotification"`
}

func summarize(b *meta.Bucket, s *session) bucketSummary {
	return bucketSummary{Name: b.Name, Created: b.Created, Region: b.Region, Owner: b.Owner, OwnerName: b.OwnerDisplay,
		Versioning: b.Versioning, ObjectLock: b.ObjectLockEnabled, Mine: b.Owner == s.id.CanonicalID()}
}

// canSee mirrors the S3 ListBuckets filter: owners and root see their
// buckets, others need s3:ListBucket from a policy.
func (h *Handler) canSee(s *session, b *meta.Bucket) bool {
	if s.id.IsRoot || b.Owner == s.id.CanonicalID() {
		return true
	}
	return h.authorize(s, "s3:ListBucket", b.Name, "", b, nil, nil) == nil
}

func (h *Handler) listBuckets(w http.ResponseWriter, r *http.Request, s *session) error {
	all, err := h.d.Obj.ListBuckets(r.Context())
	if err != nil {
		return err
	}
	out := []bucketSummary{}
	for _, b := range all {
		if h.canSee(s, b) {
			out = append(out, summarize(b, s))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": out})
	return nil
}

func (h *Handler) createBucket(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		Name       string `json:"name"`
		ObjectLock bool   `json:"objectLock"`
		Versioning bool   `json:"versioning"`
		Tags       []struct{ Key, Value string }
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if !object.ValidBucketName(in.Name) {
		return badRequest("invalid bucket name: 3-63 lower-case letters, digits, dots and hyphens")
	}
	if !s.id.IsRoot {
		if err := h.authorize(s, "s3:CreateBucket", in.Name, "", nil, nil, nil); err != nil {
			return err
		}
	}
	tags := tagsIn(in.Tags)
	if err := object.ValidateTags(tags); err != nil {
		return err
	}
	b, err := h.d.Obj.CreateBucket(r.Context(), s.actor(), object.CreateBucketInput{Name: in.Name, ObjectLockEnabled: in.ObjectLock, Tags: tags})
	if err != nil {
		return err
	}
	if in.Versioning && !in.ObjectLock {
		if b, err = h.d.Obj.UpdateBucket(r.Context(), in.Name, func(b *meta.Bucket) error { b.Versioning = "Enabled"; return nil }); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusCreated, summarize(b, s))
	return nil
}

// loadBucket fetches the bucket and authorises action on it.
func (h *Handler) loadBucket(r *http.Request, s *session, action string) (*meta.Bucket, error) {
	name := r.PathValue("bucket")
	b, err := h.d.Obj.GetBucket(r.Context(), name)
	if err != nil {
		return nil, err
	}
	if err := h.authorize(s, action, name, "", b, nil, nil); err != nil {
		return nil, err
	}
	return b, nil
}

func (h *Handler) getBucket(w http.ResponseWriter, r *http.Request, s *session) error {
	b, err := h.loadBucket(r, s, "s3:ListBucket")
	if err != nil {
		return err
	}
	d := bucketDetail{bucketSummary: summarize(b, s), Tags: tagsOut(b.Tags), Ownership: b.Ownership, Encryption: b.Encryption,
		PublicAccessBlock: b.PublicAccessBlock, ObjectLockConfig: b.ObjectLock, HasLifecycle: len(b.LifecycleXML) > 0,
		HasCORS: len(b.CORSXML) > 0, HasNotification: len(b.NotificationXML) > 0}
	if b.Quota != nil {
		d.Quota = b.Quota.Bytes
	}
	if h.authorize(s, "s3:GetBucketPolicy", b.Name, "", b, nil, nil) == nil {
		d.PolicyReadable = true
		d.Policy = b.Policy
		if doc, err := policy.Parse(b.Policy); err == nil && len(b.Policy) > 0 {
			d.PolicyPublic = doc.IsPublic()
		}
	}
	writeJSON(w, http.StatusOK, d)
	return nil
}

func (h *Handler) deleteBucket(w http.ResponseWriter, r *http.Request, s *session) error {
	b, err := h.loadBucket(r, s, "s3:DeleteBucket")
	if err != nil {
		return err
	}
	if err := h.d.Obj.DeleteBucket(r.Context(), b.Name); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (h *Handler) putVersioning(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		Status string `json:"status"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Status != "Enabled" && in.Status != "Suspended" {
		return badRequest("status must be Enabled or Suspended")
	}
	b, err := h.loadBucket(r, s, "s3:PutBucketVersioning")
	if err != nil {
		return err
	}
	b, err = h.d.Obj.UpdateBucket(r.Context(), b.Name, func(b *meta.Bucket) error {
		if b.ObjectLockEnabled && in.Status == "Suspended" {
			return badRequest("versioning cannot be suspended while Object Lock is enabled")
		}
		b.Versioning = in.Status
		return nil
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, summarize(b, s))
	return nil
}

// putBucketEncryption sets or clears the bucket's default encryption
// (what the AWS console calls "default encryption" under bucket properties
// and MinIO's console exposed under bucket settings).
func (h *Handler) putBucketEncryption(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		Algorithm string `json:"algorithm"` // "" (none) | AES256 | aws:kms
		KMSKeyID  string `json:"kmsKeyId"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var rule *meta.EncryptionRule
	switch in.Algorithm {
	case "":
	case "AES256":
		if in.KMSKeyID != "" {
			return badRequest("a KMS key applies only to aws:kms")
		}
		rule = &meta.EncryptionRule{Algorithm: "AES256"}
	case "aws:kms":
		if in.KMSKeyID != "" && !h.d.KMS.KeyExists(in.KMSKeyID) {
			return badRequest("encryption key " + in.KMSKeyID + " does not exist")
		}
		rule = &meta.EncryptionRule{Algorithm: "aws:kms", KMSKeyID: in.KMSKeyID}
	default:
		return badRequest("algorithm must be empty, AES256 or aws:kms")
	}
	b, err := h.loadBucket(r, s, "s3:PutEncryptionConfiguration")
	if err != nil {
		return err
	}
	b, err = h.d.Obj.UpdateBucket(r.Context(), b.Name, func(b *meta.Bucket) error { b.Encryption = rule; return nil })
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket": summarize(b, s), "encryption": b.Encryption})
	return nil
}

func (h *Handler) putBucketTags(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		Tags []struct{ Key, Value string } `json:"tags"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	tags := tagsIn(in.Tags)
	if err := object.ValidateTags(tags); err != nil {
		return err
	}
	b, err := h.loadBucket(r, s, "s3:PutBucketTagging")
	if err != nil {
		return err
	}
	if _, err := h.d.Obj.UpdateBucket(r.Context(), b.Name, func(b *meta.Bucket) error { b.Tags = tags; return nil }); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tagsOut(tags)})
	return nil
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request, s *session) error {
	b, err := h.loadBucket(r, s, "s3:GetBucketPolicy")
	if err != nil {
		return err
	}
	var pol any
	if len(b.Policy) > 0 {
		pol = b.Policy
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": pol})
	return nil
}

func (h *Handler) putPolicy(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		Policy json.RawMessage `json:"policy"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	b, err := h.loadBucket(r, s, "s3:PutBucketPolicy")
	if err != nil {
		return err
	}
	doc, err := policy.Parse(in.Policy)
	if err != nil {
		return apiErr(http.StatusBadRequest, "MalformedPolicy", strings.TrimPrefix(err.Error(), "policy: malformed policy document: "))
	}
	if err := policy.ValidateBucketPolicy(doc, b.Name); err != nil {
		return apiErr(http.StatusBadRequest, "MalformedPolicy", strings.TrimPrefix(err.Error(), "policy: malformed policy document: "))
	}
	if pab := b.PublicAccessBlock; pab != nil && pab.BlockPublicPolicy && doc.IsPublic() {
		return apiErr(http.StatusForbidden, "AccessDenied", "the policy is public and BlockPublicPolicy is enabled on this bucket")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, in.Policy); err != nil {
		return badRequest(err.Error())
	}
	raw := json.RawMessage(compact.Bytes())
	if _, err := h.d.Obj.UpdateBucket(r.Context(), b.Name, func(b *meta.Bucket) error { b.Policy = raw; return nil }); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": raw, "public": doc.IsPublic()})
	return nil
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request, s *session) error {
	b, err := h.loadBucket(r, s, "s3:DeleteBucketPolicy")
	if err != nil {
		return err
	}
	if _, err := h.d.Obj.UpdateBucket(r.Context(), b.Name, func(b *meta.Bucket) error { b.Policy = nil; return nil }); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}
