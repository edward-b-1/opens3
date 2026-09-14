package iamapi

import "github.com/edward-b-1/OpenS3/internal/iam"

func (h *Handler) groupShape(g *iam.Group) xmlGroup {
	return xmlGroup{Path: pathOf(g.Path), GroupName: g.Name, GroupID: entityID("group", g.Name), Arn: h.groupARN(g.Name), CreateDate: iso(g.Created)}
}

func (h *Handler) createGroup(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionCreateGroup); err != nil {
		return nil, err
	}
	name, err := requireParam(req, "GroupName")
	if err != nil {
		return nil, err
	}
	if err := h.IAM.CreateGroup(name, nil, nil); err != nil {
		if iam.IsExists(err) {
			return nil, errExists("Group with name " + name + " already exists.")
		}
		return nil, err
	}
	if p := req.Form.Get("Path"); p != "" && p != "/" {
		if err := h.IAM.UpdateGroup(name, func(g *iam.Group) error { g.Path = p; return nil }); err != nil {
			return nil, err
		}
	}
	g, _ := h.IAM.GetGroup(name)
	return struct {
		Group xmlGroup `xml:"Group"`
	}{h.groupShape(g)}, nil
}

func (h *Handler) getGroup(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionGetGroup); err != nil {
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
	members, pg := paginate(req, append([]string{}, g.Members...))
	out := struct {
		Group       xmlGroup  `xml:"Group"`
		Users       []xmlUser `xml:"Users>member"`
		IsTruncated bool      `xml:"IsTruncated"`
		Marker      string    `xml:"Marker,omitempty"`
	}{Group: h.groupShape(g), Users: []xmlUser{}, IsTruncated: pg.IsTruncated, Marker: pg.Marker}
	for _, m := range members {
		if u, err := h.IAM.GetUser(m); err == nil {
			out.Users = append(out.Users, h.userShape(u))
		}
	}
	return out, nil
}

func (h *Handler) listGroups(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionListGroups); err != nil {
		return nil, err
	}
	all, err := h.IAM.ListGroups()
	if err != nil {
		return nil, err
	}
	var groups []*iam.Group
	for _, g := range all {
		if matchesPathPrefix(req, g.Path) {
			groups = append(groups, g)
		}
	}
	return h.groupList(req, groups)
}

func (h *Handler) groupList(req *Request, groups []*iam.Group) (any, error) {
	byName := map[string]*iam.Group{}
	var names []string
	for _, g := range groups {
		byName[g.Name] = g
		names = append(names, g.Name)
	}
	names, pg := paginate(req, names)
	out := struct {
		Groups      []xmlGroup `xml:"Groups>member"`
		IsTruncated bool       `xml:"IsTruncated"`
		Marker      string     `xml:"Marker,omitempty"`
	}{Groups: []xmlGroup{}, IsTruncated: pg.IsTruncated, Marker: pg.Marker}
	for _, n := range names {
		out.Groups = append(out.Groups, h.groupShape(byName[n]))
	}
	return out, nil
}

func (h *Handler) listGroupsForUser(req *Request) (any, error) {
	name, self := h.callerOrParam(req)
	if !self {
		if err := h.authorize(req, iam.ActionListGroupsForUser); err != nil {
			return nil, err
		}
	}
	u, err := h.IAM.GetUser(name)
	if err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	var groups []*iam.Group
	for _, gn := range u.Groups {
		if g, err := h.IAM.GetGroup(gn); err == nil {
			groups = append(groups, g)
		}
	}
	return h.groupList(req, groups)
}

func (h *Handler) deleteGroup(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionDeleteGroup); err != nil {
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
	if len(g.Members) > 0 {
		return nil, errConflict("Cannot delete entity, must remove users from group first.")
	}
	if len(g.Policies) > 0 {
		return nil, errConflict("Cannot delete entity, must detach all policies first.")
	}
	return nil, h.IAM.DeleteGroup(name)
}

func (h *Handler) addUserToGroup(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionAddUserToGroup); err != nil {
		return nil, err
	}
	gname, err := requireParam(req, "GroupName")
	if err != nil {
		return nil, err
	}
	uname, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	if _, err := h.IAM.GetUser(uname); err != nil {
		return nil, errNoSuchEntity("The user with name " + uname + " cannot be found.")
	}
	err = h.IAM.UpdateGroup(gname, func(g *iam.Group) error {
		for _, m := range g.Members {
			if m == uname {
				return nil
			}
		}
		g.Members = append(g.Members, uname)
		return nil
	})
	if iam.IsNotFound(err) {
		return nil, errNoSuchEntity("The group with name " + gname + " cannot be found.")
	}
	return nil, err
}

func (h *Handler) removeUserFromGroup(req *Request) (any, error) {
	if err := h.authorize(req, iam.ActionRemoveUserFromGrp); err != nil {
		return nil, err
	}
	gname, err := requireParam(req, "GroupName")
	if err != nil {
		return nil, err
	}
	uname, err := requireParam(req, "UserName")
	if err != nil {
		return nil, err
	}
	found := false
	err = h.IAM.UpdateGroup(gname, func(g *iam.Group) error {
		out := g.Members[:0]
		for _, m := range g.Members {
			if m == uname {
				found = true
				continue
			}
			out = append(out, m)
		}
		g.Members = out
		return nil
	})
	if iam.IsNotFound(err) {
		return nil, errNoSuchEntity("The group with name " + gname + " cannot be found.")
	}
	if err == nil && !found {
		return nil, errNoSuchEntity("The user with name " + uname + " cannot be found in group " + gname + ".")
	}
	return nil, err
}
