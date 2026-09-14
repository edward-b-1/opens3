package iam

import (
	"encoding/json"

	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/policy"
)

// Request is an authorisation request.
type Request struct {
	Identity *Identity // nil for anonymous
	Action   string    // s3:GetObject etc.
	Bucket   string
	Key      string
	// Bucket state needed for the decision.
	BucketOwner       string // canonical ID
	BucketPolicy      json.RawMessage
	BucketACL         *meta.ACL
	PublicAccessBlock *meta.PublicAccessBlock
	Ownership         string
	// Object state (for object-level operations on existing objects).
	ObjectOwner string
	ObjectACL   *meta.ACL
	// SelfLockConfirmed: the bucket owner confirmed a policy that may lock
	// it out of policy operations, so the root escape hatch is off.
	SelfLockConfirmed bool
	// Condition context keys (lower-case) → values.
	Conditions map[string][]string
}

// Authorize decides whether the request is allowed.
//
// Order: explicit deny anywhere wins; then bucket-policy allow; then
// identity-policy allow (same account); then ACL grants; else deny. Root
// is allowed everything unless a bucket policy explicitly denies it, and
// can always modify the bucket policy so it can never lock itself out.
// Grant is the outcome of an authorisation decision.
type Grant int

const (
	// Refused: an explicit deny, or a session policy that does not allow.
	Refused Grant = iota
	// NoGrant: nothing denied it, but nothing allowed it either.
	NoGrant
	// ByPolicy: allowed by a bucket or identity policy (or root).
	ByPolicy
	// ByACL: allowed only by a bucket or object ACL grant (or ownership).
	ByACL
)

// Allowed reports whether the grant permits the request.
func (g Grant) Allowed() bool { return g == ByPolicy || g == ByACL }

// Authorize decides a request: deny wins, then bucket policy, identity
// and session policies, then ACLs.
func (s *Store) Authorize(r Request) bool { return s.Decide(r).Allowed() }

// Decide is Authorize with the source of the grant, for callers that
// must treat a policy grant and an ACL grant differently.
func (s *Store) Decide(r Request) Grant {
	id := r.Identity
	vars := map[string]string{}
	args := policy.Args{Action: r.Action, Resource: policy.ResourceARN(r.Bucket, r.Key), Conditions: r.Conditions, Vars: vars}
	if id == nil {
		args.Anonymous = true
		vars["aws:principaltype"] = "Anonymous"
	} else {
		args.PrincipalARN = id.ARN()
		args.CanonicalID = id.CanonicalID()
		vars["aws:username"] = id.Name()
		vars["aws:userid"] = id.CanonicalID()
		vars["aws:principaltype"] = id.PrincipalType()
	}
	// Make variables available as condition keys too.
	if r.Conditions == nil {
		r.Conditions = map[string][]string{}
		args.Conditions = r.Conditions
	}
	for k, v := range vars {
		if _, ok := r.Conditions[k]; !ok {
			r.Conditions[k] = []string{v}
		}
	}

	// Bucket policy.
	bp := policy.NoMatch
	if len(r.BucketPolicy) > 0 {
		if doc, err := s.ParsedPolicy(r.BucketPolicy); err == nil {
			// RestrictPublicBuckets: public statements only apply to
			// principals of this account, so skip the policy for anonymous.
			skip := id == nil && r.PublicAccessBlock != nil && r.PublicAccessBlock.RestrictPublicBuckets && doc.IsPublic()
			if !skip {
				bp = doc.Evaluate(args)
			}
		}
	}
	if bp == policy.Denied {
		// Root escape hatch for bucket policy management.
		if id != nil && id.IsRoot && !r.SelfLockConfirmed && (r.Action == "s3:PutBucketPolicy" || r.Action == "s3:DeleteBucketPolicy" || r.Action == "s3:GetBucketPolicy") {
			return ByPolicy
		}
		return Refused
	}
	if id != nil && id.IsRoot {
		return ByPolicy
	}

	// Identity policies and session policy.
	ip := policy.NoMatch
	if id != nil {
		for _, raw := range id.Policies {
			doc, err := s.ParsedPolicy(raw)
			if err != nil {
				continue
			}
			switch doc.Evaluate(args) {
			case policy.Denied:
				return Refused
			case policy.Allowed:
				ip = policy.Allowed
			}
		}
		// Session policies are filters: each one (the credential's own and
		// every inherited one) must allow, or nothing grants.
		for _, raw := range append([]json.RawMessage{id.SessionPolicy}, id.ParentPolicies...) {
			if len(raw) == 0 {
				continue
			}
			doc, err := s.ParsedPolicy(raw)
			if err != nil {
				return Refused
			}
			if doc.Evaluate(args) != policy.Allowed {
				return Refused
			}
		}
		// Administrative actions are only granted by identity policies.
		if IsAdministrative(r.Action) {
			if ip == policy.Allowed {
				return ByPolicy
			}
			return NoGrant
		}
	}
	if bp == policy.Allowed || ip == policy.Allowed {
		return ByPolicy
	}
	if aclAllows(r) {
		return ByACL
	}
	return NoGrant
}

// aclAllows evaluates bucket and object ACL grants.
func aclAllows(r Request) bool {
	id := r.Identity
	// The bucket owner has full control of the bucket itself (AWS: the
	// owning account), independent of ACL grants.
	isOwner := id != nil && r.BucketOwner != "" && id.CanonicalID() == r.BucketOwner
	if r.Ownership == "BucketOwnerEnforced" {
		// ACLs disabled: only the bucket owner has access; nothing else.
		return isOwner
	}
	ignorePublic := r.PublicAccessBlock != nil && r.PublicAccessBlock.IgnorePublicAcls
	has := func(acl *meta.ACL, owner string, perms ...string) bool {
		if acl == nil && owner == "" {
			return false
		}
		if id != nil && owner != "" && id.CanonicalID() == owner {
			return true
		}
		if acl == nil {
			return false
		}
		for _, g := range acl.Grants {
			match := false
			switch g.GranteeType {
			case "Group":
				switch g.Grantee {
				case "http://acs.amazonaws.com/groups/global/AllUsers":
					match = !ignorePublic
				case "http://acs.amazonaws.com/groups/global/AuthenticatedUsers":
					match = id != nil && !ignorePublic
				}
			default:
				match = id != nil && g.Grantee == id.CanonicalID()
			}
			if !match {
				continue
			}
			if g.Permission == "FULL_CONTROL" {
				return true
			}
			for _, p := range perms {
				if g.Permission == p {
					return true
				}
			}
		}
		return false
	}
	switch r.Action {
	case "s3:ListBucket", "s3:ListBucketVersions", "s3:ListBucketMultipartUploads", "s3:GetBucketLocation":
		return has(r.BucketACL, r.BucketOwner, "READ")
	case "s3:PutObject", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:AbortMultipartUpload":
		return has(r.BucketACL, r.BucketOwner, "WRITE")
	case "s3:GetBucketAcl":
		return has(r.BucketACL, r.BucketOwner, "READ_ACP")
	case "s3:PutBucketAcl":
		return has(r.BucketACL, r.BucketOwner, "WRITE_ACP")
	case "s3:GetObject", "s3:GetObjectVersion", "s3:GetObjectTagging", "s3:GetObjectVersionTagging", "s3:GetObjectAttributes", "s3:ListMultipartUploadParts", "s3:GetObjectRetention", "s3:GetObjectLegalHold":
		return has(r.ObjectACL, r.ObjectOwner, "READ")
	case "s3:GetObjectAcl", "s3:GetObjectVersionAcl":
		return has(r.ObjectACL, r.ObjectOwner, "READ_ACP")
	case "s3:PutObjectAcl", "s3:PutObjectVersionAcl":
		return has(r.ObjectACL, r.ObjectOwner, "WRITE_ACP")
	}
	// Everything else (bucket configuration) requires the bucket owner.
	return isOwner
}
