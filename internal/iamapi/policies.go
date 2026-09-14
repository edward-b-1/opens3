package iamapi

import (
	"encoding/json"
	"strings"

	"github.com/edward-b-1/opens3/internal/iam"
)

func (h *Handler) policyShape(p *iam.Policy, attachments int) xmlPolicy {
	return xmlPolicy{PolicyName: p.Name, PolicyID: entityID("policy", p.Name), Arn: h.policyARN(p), Path: pathOf(p.Path), DefaultVersionID: "v1",
		AttachmentCount: attachments, IsAttachable: true, CreateDate: iso(p.Created), UpdateDate: iso(p.Updated)}
}

// attachmentCounts counts users and groups per policy name.
func (h *Handler) attachmentCounts() map[string]int {
	counts := map[string]int{}
	if users, err := h.IAM.ListUsers(); err == nil {
		for _, u := range users {
			for _, p := range u.Policies {
				counts[p]++
			}
		}
	}
	if groups, err := h.IAM.ListGroups(); err == nil {
		for _, g := range groups {
			for _, p := range g.Policies {
				counts[p]++
			}
		}
	}
	return counts
}

func (h *Handler) createPolicy(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionCreatePolicy); err != nil {
		return nil, err
	}
	name, err := requireParam(req, "PolicyName")
	if err != nil {
		return nil, err
	}
	doc, err := requireParam(req, "PolicyDocument")
	if err != nil {
		return nil, err
	}
	if _, err := h.IAM.GetPolicy(name); err == nil {
		return nil, errExists("A policy called " + name + " already exists. Duplicate names are not allowed.")
	}
	if !json.Valid([]byte(doc)) {
		return nil, errMalformedPolicy("Syntax errors in policy.")
	}
	if err := h.IAM.PutPolicy(name, json.RawMessage(doc)); err != nil {
		if iam.IsInvalid(err) {
			return nil, errMalformedPolicy(strings.TrimPrefix(err.Error(), "iam: invalid argument: "))
		}
		return nil, err
	}
	if path := req.Form.Get("Path"); path != "" && path != "/" {
		if err := h.IAM.SetPolicyPath(name, path); err != nil {
			return nil, err
		}
	}
	p, _ := h.IAM.GetPolicy(name)
	return struct {
		Policy xmlPolicy `xml:"Policy"`
	}{h.policyShape(p, 0)}, nil
}

func (h *Handler) lookupPolicy(req *Request) (*iam.Policy, error) {
	name, err := h.policyNameFromARN(req.Form.Get("PolicyArn"))
	if err != nil {
		return nil, err
	}
	p, err := h.IAM.GetPolicy(name)
	if err != nil {
		return nil, errNoSuchEntity("Policy " + req.Form.Get("PolicyArn") + " does not exist or is not attachable.")
	}
	return p, nil
}

func (h *Handler) getPolicy(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionGetPolicy); err != nil {
		return nil, err
	}
	p, err := h.lookupPolicy(req)
	if err != nil {
		return nil, err
	}
	return struct {
		Policy xmlPolicy `xml:"Policy"`
	}{h.policyShape(p, h.attachmentCounts()[p.Name])}, nil
}

func (h *Handler) getPolicyVersion(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionGetPolicyVersion); err != nil {
		return nil, err
	}
	p, err := h.lookupPolicy(req)
	if err != nil {
		return nil, err
	}
	if v := req.Form.Get("VersionId"); v != "" && v != "v1" {
		return nil, errNoSuchEntity("Policy version " + v + " does not exist.")
	}
	return struct {
		Document         string `xml:"PolicyVersion>Document"`
		VersionID        string `xml:"PolicyVersion>VersionId"`
		IsDefaultVersion bool   `xml:"PolicyVersion>IsDefaultVersion"`
		CreateDate       string `xml:"PolicyVersion>CreateDate"`
	}{urlEncode(string(p.Document)), "v1", true, iso(p.Updated)}, nil
}

func (h *Handler) listPolicies(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionListPolicies); err != nil {
		return nil, err
	}
	all, err := h.IAM.ListPolicies()
	if err != nil {
		return nil, err
	}
	scope := req.Form.Get("Scope")
	counts := h.attachmentCounts()
	onlyAttached := strings.EqualFold(req.Form.Get("OnlyAttached"), "true")
	byName := map[string]*iam.Policy{}
	var names []string
	for _, p := range all {
		if scope == "AWS" && !p.BuiltIn || scope == "Local" && p.BuiltIn {
			continue
		}
		if onlyAttached && counts[p.Name] == 0 || !matchesPathPrefix(req, p.Path) {
			continue
		}
		byName[p.Name] = p
		names = append(names, p.Name)
	}
	names, pg := paginate(req, names)
	out := struct {
		Policies    []xmlPolicy `xml:"Policies>member"`
		IsTruncated bool        `xml:"IsTruncated"`
		Marker      string      `xml:"Marker,omitempty"`
	}{Policies: []xmlPolicy{}, IsTruncated: pg.IsTruncated, Marker: pg.Marker}
	for _, n := range names {
		out.Policies = append(out.Policies, h.policyShape(byName[n], counts[n]))
	}
	return out, nil
}

func (h *Handler) deletePolicy(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionDeletePolicy); err != nil {
		return nil, err
	}
	p, err := h.lookupPolicy(req)
	if err != nil {
		return nil, err
	}
	if p.BuiltIn {
		return nil, errInvalidInput("Cannot delete a built-in (AWS managed) policy.")
	}
	if h.attachmentCounts()[p.Name] > 0 {
		return nil, errConflict("Cannot delete a policy attached to entities.")
	}
	return nil, h.IAM.DeletePolicy(p.Name)
}

// --- attachments ---------------------------------------------------------------

func (h *Handler) attachUserPolicy(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionAttachUserPolicy); err != nil {
		return nil, err
	}
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	p, err := h.lookupPolicy(req)
	if err != nil {
		return nil, err
	}
	err = h.IAM.UpdateUser(name, func(u *iam.User) error {
		for _, x := range u.Policies {
			if x == p.Name {
				return nil
			}
		}
		u.Policies = append(u.Policies, p.Name)
		return nil
	})
	if iam.IsNotFound(err) {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	return nil, err
}

func (h *Handler) detachUserPolicy(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionDetachUserPolicy); err != nil {
		return nil, err
	}
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	pname, err := h.policyNameFromARN(req.Form.Get("PolicyArn"))
	if err != nil {
		return nil, err
	}
	found := false
	err = h.IAM.UpdateUser(name, func(u *iam.User) error {
		out := u.Policies[:0]
		for _, x := range u.Policies {
			if x == pname {
				found = true
				continue
			}
			out = append(out, x)
		}
		u.Policies = out
		return nil
	})
	if iam.IsNotFound(err) {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	if err == nil && !found {
		return nil, errNoSuchEntity("Policy " + req.Form.Get("PolicyArn") + " was not found.")
	}
	return nil, err
}

func (h *Handler) listAttachedUserPolicies(req *Request) (any, error) {
	name, self := h.callerOrParam(req)
	if !self {
		if err := h.authorize(req, iam.ActionListAttachedUser); err != nil {
			return nil, err
		}
	}
	u, err := h.IAM.GetUser(name)
	if err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	return h.attachedList(req, u.Policies)
}

func (h *Handler) attachedList(req *Request, names []string) (any, error) {
	names, pg := paginate(req, append([]string{}, names...))
	out := struct {
		Policies    []xmlAttachedPolicy `xml:"AttachedPolicies>member"`
		IsTruncated bool                `xml:"IsTruncated"`
		Marker      string              `xml:"Marker,omitempty"`
	}{Policies: []xmlAttachedPolicy{}, IsTruncated: pg.IsTruncated, Marker: pg.Marker}
	for _, n := range names {
		if p, err := h.IAM.GetPolicy(n); err == nil {
			out.Policies = append(out.Policies, xmlAttachedPolicy{PolicyName: n, PolicyArn: h.policyARN(p)})
		}
	}
	return out, nil
}

func (h *Handler) attachGroupPolicy(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionAttachGroupPolicy); err != nil {
		return nil, err
	}
	name, err := requireParam(req, "GroupName")
	if err != nil {
		return nil, err
	}
	p, err := h.lookupPolicy(req)
	if err != nil {
		return nil, err
	}
	err = h.IAM.UpdateGroup(name, func(g *iam.Group) error {
		for _, x := range g.Policies {
			if x == p.Name {
				return nil
			}
		}
		g.Policies = append(g.Policies, p.Name)
		return nil
	})
	if iam.IsNotFound(err) {
		return nil, errNoSuchEntity("The group with name " + name + " cannot be found.")
	}
	return nil, err
}

func (h *Handler) detachGroupPolicy(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionDetachGroupPolicy); err != nil {
		return nil, err
	}
	name, err := requireParam(req, "GroupName")
	if err != nil {
		return nil, err
	}
	pname, err := h.policyNameFromARN(req.Form.Get("PolicyArn"))
	if err != nil {
		return nil, err
	}
	found := false
	err = h.IAM.UpdateGroup(name, func(g *iam.Group) error {
		out := g.Policies[:0]
		for _, x := range g.Policies {
			if x == pname {
				found = true
				continue
			}
			out = append(out, x)
		}
		g.Policies = out
		return nil
	})
	if iam.IsNotFound(err) {
		return nil, errNoSuchEntity("The group with name " + name + " cannot be found.")
	}
	if err == nil && !found {
		return nil, errNoSuchEntity("Policy " + req.Form.Get("PolicyArn") + " was not found.")
	}
	return nil, err
}

func (h *Handler) listAttachedGroupPolicies(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionListAttachedGroup); err != nil {
		return nil, err
	}
	name, err := requireParam(req, "GroupName")
	if err != nil {
		return nil, err
	}
	g, err := h.IAM.GetGroup(name)
	if err != nil {
		return nil, errNoSuchEntity("The group with name " + name + " cannot be found.")
	}
	return h.attachedList(req, g.Policies)
}
