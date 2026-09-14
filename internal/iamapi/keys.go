package iamapi

import (
	"strings"

	"github.com/edward-b-1/opens3/internal/iam"
)

func keyStatus(k *iam.Key) string {
	if k.Enabled {
		return "Active"
	}
	return "Inactive"
}

func (h *Handler) createAccessKey(req *Request) (any, error) {
	name, _ := h.callerOrParam(req)
	// Creating a key needs the permission even for oneself, and credentials
	// under a session policy may not create keys at all: the new key would
	// carry none of the restriction.
	if err := h.authorize(req, iam.ActionCreateAccessKey); err != nil {
		return nil, err
	}
	if err := iam.CheckCredentialIssuer(req.Identity); err != nil {
		return nil, errAccessDeniedMsg(err.Error())
	}
	if name == "root" {
		return nil, errInvalidInput("root cannot own access keys; its credentials come from the server configuration. Create a user instead.")
	}
	if _, err := h.IAM.GetUser(name); err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	k, secret, err := h.IAM.CreateKey(name, "", "", iam.KindUser, nil, nil, "")
	if err != nil {
		return nil, err
	}
	return struct {
		AccessKey xmlAccessKey `xml:"AccessKey"`
	}{xmlAccessKey{UserName: name, AccessKeyID: k.AccessKey, Status: keyStatus(k), SecretAccessKey: secret, CreateDate: iso(k.Created)}}, nil
}

func (h *Handler) listAccessKeys(req *Request) (any, error) {
	name, self := h.callerOrParam(req)
	if !self {
		if err := h.authorize(req, iam.ActionListAccessKeys); err != nil {
			return nil, err
		}
	}
	if req.Identity.IsRoot && self {
		name = "root"
	} else if _, err := h.IAM.GetUser(name); err != nil {
		return nil, errNoSuchEntity("The user with name " + name + " cannot be found.")
	}
	keys, err := h.IAM.ListKeys(name)
	if err != nil {
		return nil, err
	}
	byID := map[string]*iam.Key{}
	var ids []string
	for _, k := range keys {
		if k.Kind == iam.KindSTS {
			continue
		}
		byID[k.AccessKey] = k
		ids = append(ids, k.AccessKey)
	}
	ids, pg := paginate(req, ids)
	out := struct {
		Keys        []xmlAccessKey `xml:"AccessKeyMetadata>member"`
		IsTruncated bool           `xml:"IsTruncated"`
		Marker      string         `xml:"Marker,omitempty"`
	}{Keys: []xmlAccessKey{}, IsTruncated: pg.IsTruncated, Marker: pg.Marker}
	for _, id := range ids {
		k := byID[id]
		out.Keys = append(out.Keys, xmlAccessKey{UserName: k.User, AccessKeyID: k.AccessKey, Status: keyStatus(k), CreateDate: iso(k.Created)})
	}
	return out, nil
}

// ownedKey finds a key and checks it belongs to UserName (if given) or the caller.
func (h *Handler) ownedKey(req *Request, action string) (*iam.Key, error) {
	id, err := requireParam(req, "AccessKeyId")
	if err != nil {
		return nil, err
	}
	k, err := h.IAM.GetKey(id)
	if err != nil {
		return nil, errNoSuchEntity("The Access Key with id " + id + " cannot be found.")
	}
	if n := req.Form.Get("UserName"); n != "" && n != k.User {
		return nil, errNoSuchEntity("The Access Key with id " + id + " cannot be found.")
	}
	if k.User != req.Identity.Name() || req.Identity.IsRoot {
		if err := h.authorize(req, action); err != nil {
			return nil, err
		}
	}
	return k, nil
}

func (h *Handler) updateAccessKey(req *Request) (any, error) {
	k, err := h.ownedKey(req, iam.ActionUpdateAccessKey)
	if err != nil {
		return nil, err
	}
	status := req.Form.Get("Status")
	if !strings.EqualFold(status, "Active") && !strings.EqualFold(status, "Inactive") {
		return nil, errValidation("Value '" + status + "' at 'status' failed to satisfy constraint: Member must satisfy enum value set: [Active, Inactive]")
	}
	return nil, h.IAM.UpdateKey(k.AccessKey, func(k *iam.Key) error { k.Enabled = strings.EqualFold(status, "Active"); return nil })
}

func (h *Handler) deleteAccessKey(req *Request) (any, error) {
	k, err := h.ownedKey(req, iam.ActionDeleteAccessKey)
	if err != nil {
		return nil, err
	}
	return nil, h.IAM.DeleteKey(k.AccessKey)
}
