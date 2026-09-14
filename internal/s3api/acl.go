package s3api

import (
	"strings"

	"github.com/edward-b-1/OpenS3/internal/iam"
	"github.com/edward-b-1/OpenS3/internal/meta"
	"github.com/edward-b-1/OpenS3/internal/s3err"
)

const (
	groupAllUsers           = "http://acs.amazonaws.com/groups/global/AllUsers"
	groupAuthenticatedUsers = "http://acs.amazonaws.com/groups/global/AuthenticatedUsers"
	groupLogDelivery        = "http://acs.amazonaws.com/groups/s3/LogDelivery"
)

// cannedACL expands a canned ACL for an owner (and bucket owner for
// bucket-owner-* variants).
func cannedACL(name, owner, ownerDisplay, bucketOwner, bucketOwnerDisplay string) (*meta.ACL, bool) {
	full := meta.Grant{Grantee: owner, GranteeType: "CanonicalUser", DisplayName: ownerDisplay, Permission: "FULL_CONTROL"}
	acl := &meta.ACL{Owner: owner, OwnerDisplay: ownerDisplay, Grants: []meta.Grant{full}}
	switch name {
	case "private", "":
	case "public-read":
		acl.Grants = append(acl.Grants, meta.Grant{Grantee: groupAllUsers, GranteeType: "Group", Permission: "READ"})
	case "public-read-write":
		acl.Grants = append(acl.Grants, meta.Grant{Grantee: groupAllUsers, GranteeType: "Group", Permission: "READ"},
			meta.Grant{Grantee: groupAllUsers, GranteeType: "Group", Permission: "WRITE"})
	case "authenticated-read":
		acl.Grants = append(acl.Grants, meta.Grant{Grantee: groupAuthenticatedUsers, GranteeType: "Group", Permission: "READ"})
	case "aws-exec-read":
	case "bucket-owner-read":
		if bucketOwner != "" && bucketOwner != owner {
			acl.Grants = append(acl.Grants, meta.Grant{Grantee: bucketOwner, GranteeType: "CanonicalUser", DisplayName: bucketOwnerDisplay, Permission: "READ"})
		}
	case "bucket-owner-full-control":
		if bucketOwner != "" && bucketOwner != owner {
			acl.Grants = append(acl.Grants, meta.Grant{Grantee: bucketOwner, GranteeType: "CanonicalUser", DisplayName: bucketOwnerDisplay, Permission: "FULL_CONTROL"})
		}
	case "log-delivery-write":
		acl.Grants = append(acl.Grants, meta.Grant{Grantee: groupLogDelivery, GranteeType: "Group", Permission: "WRITE"},
			meta.Grant{Grantee: groupLogDelivery, GranteeType: "Group", Permission: "READ_ACP"})
	default:
		return nil, false
	}
	return acl, true
}

// parseGrantHeader parses x-amz-grant-* values: id="...", uri="...",
// emailAddress="..." separated by commas.
func (s *Server) parseGrantHeader(v, perm string) ([]meta.Grant, error) {
	var out []meta.Grant
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, val, ok := strings.Cut(part, "=")
		if !ok {
			return nil, s3err.New(s3err.InvalidArgument).WithMessage("Invalid grant header")
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "id":
			if !s.knownCanonicalID(val) {
				return nil, s3err.New(s3err.InvalidArgument).WithMessage("Invalid id").WithExtra("ArgumentName", "CanonicalUser/ID").WithExtra("ArgumentValue", val)
			}
			out = append(out, meta.Grant{Grantee: val, GranteeType: "CanonicalUser", Permission: perm})
		case "uri":
			if val != groupAllUsers && val != groupAuthenticatedUsers && val != groupLogDelivery {
				return nil, s3err.New(s3err.InvalidArgument).WithMessage("Invalid group uri")
			}
			out = append(out, meta.Grant{Grantee: val, GranteeType: "Group", Permission: perm})
		case "emailaddress":
			return nil, s3err.New(s3err.UnresolvableGrantByEmailAddress)
		default:
			return nil, s3err.New(s3err.InvalidArgument).WithMessage("Invalid grant header")
		}
	}
	return out, nil
}

// parseACLHeaders builds an ACL from x-amz-acl / x-amz-grant-* headers.
// Returns nil when no ACL headers are present.
func (s *Server) parseACLHeaders(c *reqCtx, b *meta.Bucket) (*meta.ACL, error) {
	r := c.r
	owner, ownerDisplay := c.actor().CanonicalID, c.actor().DisplayName
	var bo, bod string
	if b != nil {
		bo, bod = b.Owner, b.OwnerDisplay
	}
	canned := r.Header.Get("x-amz-acl")
	var grants []meta.Grant
	for h, perm := range map[string]string{"x-amz-grant-read": "READ", "x-amz-grant-write": "WRITE", "x-amz-grant-read-acp": "READ_ACP",
		"x-amz-grant-write-acp": "WRITE_ACP", "x-amz-grant-full-control": "FULL_CONTROL"} {
		if v := r.Header.Get(h); v != "" {
			g, err := s.parseGrantHeader(v, perm)
			if err != nil {
				return nil, err
			}
			grants = append(grants, g...)
		}
	}
	if canned == "" && len(grants) == 0 {
		return nil, nil
	}
	if canned != "" && len(grants) > 0 {
		return nil, s3err.New(s3err.InvalidRequest).WithMessage("Specifying both Canned ACLs and Header Grants is not allowed")
	}
	if b != nil && b.Ownership == "BucketOwnerEnforced" {
		// ACLs disabled: only bucket-owner-full-control / private allowed.
		if canned != "" && canned != "bucket-owner-full-control" || len(grants) > 0 {
			return nil, s3err.New(s3err.AccessControlListNotSupported)
		}
		return nil, nil
	}
	if canned != "" {
		acl, ok := cannedACL(canned, owner, ownerDisplay, bo, bod)
		if !ok {
			return nil, s3err.New(s3err.InvalidArgument).WithMessage("invalid x-amz-acl value")
		}
		return acl, nil
	}
	return &meta.ACL{Owner: owner, OwnerDisplay: ownerDisplay, Grants: grants}, nil
}

// aclFromXML converts a request ACL body.
func (s *Server) aclFromXML(in *xmlAccessControlPolicyIn, owner string) (*meta.ACL, error) {
	if in.Owner.ID != "" && in.Owner.ID != owner {
		return nil, s3err.New(s3err.AccessDenied).WithMessage("The owner in the ACL does not match the resource owner")
	}
	acl := &meta.ACL{Owner: owner, OwnerDisplay: in.Owner.DisplayName}
	for _, g := range in.AccessControlList.Grants {
		switch g.Permission {
		case "FULL_CONTROL", "READ", "WRITE", "READ_ACP", "WRITE_ACP":
		default:
			return nil, s3err.New(s3err.MalformedACLError)
		}
		switch g.Grantee.Type {
		case "CanonicalUser":
			if g.Grantee.ID == "" {
				return nil, s3err.New(s3err.MalformedACLError)
			}
			if !s.knownCanonicalID(g.Grantee.ID) {
				return nil, s3err.New(s3err.InvalidArgument).WithMessage("Invalid id").WithExtra("ArgumentName", "CanonicalUser/ID").WithExtra("ArgumentValue", g.Grantee.ID)
			}
			acl.Grants = append(acl.Grants, meta.Grant{Grantee: g.Grantee.ID, GranteeType: "CanonicalUser", DisplayName: g.Grantee.DisplayName, Permission: g.Permission})
		case "Group":
			if g.Grantee.URI != groupAllUsers && g.Grantee.URI != groupAuthenticatedUsers && g.Grantee.URI != groupLogDelivery {
				return nil, s3err.New(s3err.MalformedACLError)
			}
			acl.Grants = append(acl.Grants, meta.Grant{Grantee: g.Grantee.URI, GranteeType: "Group", Permission: g.Permission})
		case "AmazonCustomerByEmail":
			return nil, s3err.New(s3err.UnresolvableGrantByEmailAddress)
		default:
			return nil, s3err.New(s3err.MalformedACLError)
		}
	}
	return acl, nil
}

// knownCanonicalID reports whether id is the canonical ID of an existing
// identity (AWS rejects grants to unknown IDs with InvalidArgument).
func (s *Server) knownCanonicalID(id string) bool {
	if id == iam.CanonicalID("root") {
		return true
	}
	users, err := s.iam.ListUsers()
	if err != nil {
		return true // fail open: cannot verify
	}
	for _, u := range users {
		if iam.CanonicalID(u.Name) == id {
			return true
		}
	}
	return false
}

// isPublicACL reports whether an ACL grants anything to AllUsers or
// AuthenticatedUsers (for Block Public Access).
func isPublicACL(a *meta.ACL) bool {
	if a == nil {
		return false
	}
	for _, g := range a.Grants {
		if g.GranteeType == "Group" && (g.Grantee == groupAllUsers || g.Grantee == groupAuthenticatedUsers) {
			return true
		}
	}
	return false
}

var _ = iam.CanonicalID
