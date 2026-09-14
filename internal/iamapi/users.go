package iamapi

import (
	"github.com/edward-b-1/opens3/internal/iam"
)

func (h *Handler) userShape(u *iam.User) xmlUser {
	return xmlUser{Path: pathOf(u.Path), UserName: u.Name, UserID: entityID("user", u.Name), Arn: h.userARN(u.Name), CreateDate: iso(u.Created)}
}

func (h *Handler) createUser(req *Request) (any, error) {
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeOn(req, iam.ActionCreateUser, h.userResource(name)); err != nil {
		return nil, err
	}
	if err := h.IAM.CreateUser(name, "", nil); err != nil {
		if iam.IsExists(err) {
			return nil, errExists("User with name " + name + " already exists.")
		}
		return nil, err
	}
	if p := req.Form.Get("Path"); p != "" && p != "/" {
		if err := h.IAM.UpdateUser(name, func(u *iam.User) error { u.Path = p; return nil }); err != nil {
			return nil, err
		}
	}
	u, err := h.IAM.GetUser(name)
	if err != nil {
		return nil, err
	}
	return struct {
		User xmlUser `xml:"User"`
	}{h.userShape(u)}, nil
}

// callerOrParam resolves UserName, defaulting to the caller.
func (h *Handler) callerOrParam(req *Request) (string, bool) {
	if n := req.Form.Get("UserName"); n != "" {
		return n, n == req.Identity.Name()
	}
	return req.Identity.Name(), true
}

func (h *Handler) getUser(req *Request) (any, error) {
	name, self := h.callerOrParam(req)
	if !self {
		if err := h.authorize(req, iam.ActionGetUser); err != nil {
			return nil, err
		}
	}
	if req.Identity.IsRoot && self {
		return struct {
			User xmlUser `xml:"User"`
		}{xmlUser{Path: "/", UserName: "root", UserID: entityID("user", "root"), Arn: req.Identity.ARN(), CreateDate: iso(h.IAM.Started())}}, nil
	}
	u, err := h.IAM.GetUser(name)
	if err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	return struct {
		User xmlUser `xml:"User"`
	}{h.userShape(u)}, nil
}

func (h *Handler) listUsers(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionListUsers); err != nil {
		return nil, err
	}
	users, err := h.IAM.ListUsers()
	if err != nil {
		return nil, err
	}
	byName := map[string]*iam.User{}
	var names []string
	for _, u := range users {
		if u.Name == "root" || !matchesPathPrefix(req, u.Path) {
			continue // synthetic record backing root sessions / outside the path prefix
		}
		byName[u.Name] = u
		names = append(names, u.Name)
	}
	names, pg := paginate(req, names)
	out := struct {
		Users       []xmlUser `xml:"Users>member"`
		IsTruncated bool      `xml:"IsTruncated"`
		Marker      string    `xml:"Marker,omitempty"`
	}{Users: []xmlUser{}, IsTruncated: pg.IsTruncated, Marker: pg.Marker}
	for _, n := range names {
		out.Users = append(out.Users, h.userShape(byName[n]))
	}
	return out, nil
}

func (h *Handler) updateUser(req *Request) (any, error) {
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeOn(req, iam.ActionUpdateUser, h.userResource(name)); err != nil {
		return nil, err
	}
	if req.Form.Get("NewUserName") != "" {
		return nil, errInvalidInput("Renaming users is not supported")
	}
	if _, err := h.IAM.GetUser(name); err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	return nil, nil
}

// deleteUser follows AWS: keys, login profile, group memberships and
// attached policies must be removed first.
func (h *Handler) deleteUser(req *Request) (any, error) {
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeOn(req, iam.ActionDeleteUser, h.userResource(name)); err != nil {
		return nil, err
	}
	u, err := h.IAM.GetUser(name)
	if err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	keys, err := h.IAM.ListKeys(name)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.Kind != iam.KindSTS {
			return nil, errConflict("Cannot delete entity, must delete access keys first.")
		}
	}
	if u.HasPassword() {
		return nil, errConflict("Cannot delete entity, must delete login profile first.")
	}
	if len(u.Groups) > 0 {
		return nil, errConflict("Cannot delete entity, must remove users from group first.")
	}
	if len(u.Policies) > 0 {
		return nil, errConflict("Cannot delete entity, must detach all policies first.")
	}
	if err := h.IAM.DeleteUser(name); err != nil {
		return nil, err
	}
	return nil, nil
}

// --- login profiles (console passwords) ---------------------------------------

func (h *Handler) loginProfile(u *iam.User) xmlLoginProfile {
	return xmlLoginProfile{UserName: u.Name, CreateDate: iso(u.PasswordSet)}
}

func (h *Handler) createLoginProfile(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionCreateLoginProfile); err != nil {
		return nil, err
	}
	if err := iam.CheckCredentialIssuer(req.Identity); err != nil {
		return nil, errAccessDeniedMsg(err.Error())
	}
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	pw, err := requireParam(req, "Password")
	if err != nil {
		return nil, err
	}
	u, err := h.IAM.GetUser(name)
	if err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	if u.HasPassword() {
		return nil, errExists("Login Profile for User " + name + " already exists.")
	}
	if err := h.IAM.SetPassword(name, pw); err != nil {
		return nil, &Error{400, "PasswordPolicyViolation", err.Error()}
	}
	u, _ = h.IAM.GetUser(name)
	return struct {
		LoginProfile xmlLoginProfile `xml:"LoginProfile"`
	}{h.loginProfile(u)}, nil
}

func (h *Handler) updateLoginProfile(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionUpdateLoginProfile); err != nil {
		return nil, err
	}
	if err := iam.CheckCredentialIssuer(req.Identity); err != nil {
		return nil, errAccessDeniedMsg(err.Error())
	}
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	u, err := h.IAM.GetUser(name)
	if err != nil || !u.HasPassword() {
		return nil, errNoSuchEntity("Login Profile for User " + name + " cannot be found.")
	}
	if pw := req.Form.Get("Password"); pw != "" {
		if err := h.IAM.SetPassword(name, pw); err != nil {
			return nil, &Error{400, "PasswordPolicyViolation", err.Error()}
		}
	}
	return nil, nil
}

func (h *Handler) deleteLoginProfile(req *Request) (any, error) {
	name, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeOn(req, iam.ActionDeleteLoginProfile, h.userResource(name)); err != nil {
		return nil, err
	}
	u, err := h.IAM.GetUser(name)
	if err != nil || !u.HasPassword() {
		return nil, errNoSuchEntity("Login Profile for User " + name + " cannot be found.")
	}
	return nil, h.IAM.ClearPassword(name)
}

func (h *Handler) getLoginProfile(req *Request) (any, error) {
	name, self := h.callerOrParam(req)
	if !self {
		if err := h.authorize(req, iam.ActionGetLoginProfile); err != nil {
			return nil, err
		}
	}
	u, err := h.IAM.GetUser(name)
	if err != nil || !u.HasPassword() {
		return nil, errNoSuchEntity("Login Profile for User " + name + " cannot be found.")
	}
	return struct {
		LoginProfile xmlLoginProfile `xml:"LoginProfile"`
	}{h.loginProfile(u)}, nil
}

// changePassword changes the caller's own console password.
func (h *Handler) changePassword(req *Request) (any, error) {
	if req.Identity.IsRoot {
		return nil, errInvalidInput("The root password comes from the server configuration")
	}
	if err := iam.CheckCredentialIssuer(req.Identity); err != nil {
		return nil, errAccessDeniedMsg(err.Error())
	}
	old, err := requireParam(req, "OldPassword")
	if err != nil {
		return nil, err
	}
	nw, err := requireParam(req, "NewPassword")
	if err != nil {
		return nil, err
	}
	name := req.Identity.Name()
	if _, err := h.IAM.VerifyPassword(name, old); err != nil {
		return nil, &Error{400, "InvalidUserType", "Either the old password is incorrect or the user has no console password"}
	}
	if err := h.IAM.SetPassword(name, nw); err != nil {
		return nil, &Error{400, "PasswordPolicyViolation", err.Error()}
	}
	return nil, nil
}

func (h *Handler) getAccountSummary(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionGetAccountSummary); err != nil {
		return nil, err
	}
	users, _ := h.IAM.ListUsers()
	groups, _ := h.IAM.ListGroups()
	policies, _ := h.IAM.ListPolicies()
	keys, _ := h.IAM.ListKeys("")
	nUsers := 0
	for _, u := range users {
		if u.Name != "root" {
			nUsers++
		}
	}
	nPol := 0
	for _, p := range policies {
		if !p.BuiltIn {
			nPol++
		}
	}
	nKeys := 0
	for _, k := range keys {
		if k.Kind != iam.KindSTS {
			nKeys++
		}
	}
	type entry struct {
		Key   string `xml:"key"`
		Value int    `xml:"value"`
	}
	return struct {
		Entries []entry `xml:"SummaryMap>entry"`
	}{[]entry{{"Users", nUsers}, {"UsersQuota", 5000}, {"Groups", len(groups)}, {"GroupsQuota", 300}, {"Policies", nPol}, {"PoliciesQuota", 1500},
		{"AccessKeysPerUserQuota", 100}, {"AccountAccessKeysPresent", nKeys}, {"AccountMFAEnabled", 0}}}, nil
}
