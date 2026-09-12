package console

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"gitlab.com/Birdsall/opens3/internal/iam"
	"gitlab.com/Birdsall/opens3/internal/policy"
)

// --- server info -----------------------------------------------------------

func (h *Handler) info(w http.ResponseWriter, r *http.Request, s *session) error {
	out := map[string]any{"version": h.d.Version, "region": h.d.Region, "goVersion": runtime.Version(), "uptimeSeconds": int64(time.Since(h.started).Seconds()),
		"startedAt": h.started.UTC(), "accountId": h.d.IAM.AccountID(), "admin": false}
	if h.allowedAdmin(s, "admin:ServerInfo") {
		out["admin"] = true
		host, _ := os.Hostname()
		out["hostname"] = host
		out["os"] = runtime.GOOS + "/" + runtime.GOARCH
		out["goroutines"] = runtime.NumGoroutine()
		if bs, err := h.d.Obj.ListBuckets(r.Context()); err == nil {
			out["buckets"] = len(bs)
		}
		if us, err := h.d.IAM.ListUsers(); err == nil {
			out["users"] = len(us)
		}
		if ks, err := h.d.IAM.ListKeys(""); err == nil {
			n := 0
			for _, k := range ks {
				if k.Kind != iam.KindSTS {
					n++
				}
			}
			out["accessKeys"] = n
		}
		if st, err := h.d.Obj.Blobs().Stats(r.Context()); err == nil {
			out["disk"] = map[string]int64{"total": st.TotalBytes, "free": st.FreeBytes, "used": st.UsedBytes}
		} else {
			out["diskError"] = err.Error()
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		out["memory"] = map[string]uint64{"heap": ms.HeapAlloc, "sys": ms.Sys}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// --- users -----------------------------------------------------------------

type userOut struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Policies []string  `json:"policies"`
	Groups   []string  `json:"groups"`
	Created  time.Time `json:"created"`
	Keys     int       `json:"keys"`
}

func userView(u *iam.User, keys int) userOut {
	return userOut{Name: u.Name, Enabled: u.Enabled, Policies: strs(u.Policies), Groups: strs(u.Groups), Created: u.Created, Keys: keys}
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:ListUsers"); err != nil {
		return err
	}
	users, err := h.d.IAM.ListUsers()
	if err != nil {
		return err
	}
	counts := map[string]int{}
	if keys, err := h.d.IAM.ListKeys(""); err == nil {
		for _, k := range keys {
			if k.Kind != iam.KindSTS {
				counts[k.User]++
			}
		}
	}
	out := []userOut{}
	for _, u := range users {
		out = append(out, userView(u, counts[u.Name]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
	return nil
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:CreateUser"); err != nil {
		return err
	}
	var in struct {
		Name     string   `json:"name"`
		Secret   string   `json:"secretKey"`
		Policies []string `json:"policies"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := h.d.IAM.CreateUser(strings.TrimSpace(in.Name), in.Secret, in.Policies); err != nil {
		return err
	}
	u, err := h.d.IAM.GetUser(strings.TrimSpace(in.Name))
	if err != nil {
		return err
	}
	keys := 0
	if in.Secret != "" {
		keys = 1
	}
	writeJSON(w, http.StatusCreated, userView(u, keys))
	return nil
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request, s *session) error {
	name := r.PathValue("name")
	if name != s.id.Name() {
		if err := h.admin(s, "admin:GetUser"); err != nil {
			return err
		}
	}
	u, err := h.d.IAM.GetUser(name)
	if err != nil {
		return err
	}
	keys, err := h.d.IAM.ListKeys(name)
	if err != nil {
		return err
	}
	ks := []keyOut{}
	for _, k := range keys {
		if k.Kind != iam.KindSTS {
			ks = append(ks, keyView(k))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userView(u, len(ks)), "keys": ks})
	return nil
}

func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:UpdateUser"); err != nil {
		return err
	}
	name := r.PathValue("name")
	var in struct {
		Enabled  *bool    `json:"enabled"`
		Policies []string `json:"policies"`
		Secret   string   `json:"secretKey"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if name == "root" {
		return iam.ErrRoot
	}
	err := h.d.IAM.UpdateUser(name, func(u *iam.User) error {
		if in.Enabled != nil {
			u.Enabled = *in.Enabled
		}
		if in.Policies != nil {
			u.Policies = in.Policies
		}
		return nil
	})
	if err != nil {
		return err
	}
	if in.Secret != "" {
		// Rotate (or create) the key named after the user.
		if _, err := h.d.IAM.GetKey(name); err == nil {
			err = h.d.IAM.SetSecret(name, in.Secret)
		} else {
			_, _, err = h.d.IAM.CreateKey(name, name, in.Secret, iam.KindUser, nil, nil, "")
		}
		if err != nil {
			return err
		}
	}
	u, err := h.d.IAM.GetUser(name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, userView(u, 0))
	return nil
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:DeleteUser"); err != nil {
		return err
	}
	name := r.PathValue("name")
	if name == "root" || name == s.id.Name() {
		return apiErr(http.StatusBadRequest, "InvalidRequest", "cannot delete the root account or yourself")
	}
	if err := h.d.IAM.DeleteUser(name); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// --- access keys -----------------------------------------------------------

type keyOut struct {
	AccessKey     string          `json:"accessKey"`
	User          string          `json:"user"`
	Kind          string          `json:"kind"`
	Enabled       bool            `json:"enabled"`
	Description   string          `json:"description,omitempty"`
	Created       time.Time       `json:"created"`
	Expires       *time.Time      `json:"expires,omitempty"`
	SessionPolicy json.RawMessage `json:"sessionPolicy,omitempty"`
}

func keyView(k *iam.Key) keyOut {
	return keyOut{AccessKey: k.AccessKey, User: k.User, Kind: k.Kind, Enabled: k.Enabled, Description: k.Description, Created: k.Created, Expires: k.Expires, SessionPolicy: k.SessionPolicy}
}

// keyAccess allows users to manage their own keys; anything else needs the
// admin permission.
func (h *Handler) keyAccess(s *session, owner, adminAction string) error {
	if owner != "" && owner == s.id.Name() && !s.id.IsRoot {
		return nil
	}
	return h.admin(s, adminAction)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request, s *session) error {
	user := r.URL.Query().Get("user")
	if err := h.keyAccess(s, user, "admin:ListKeys"); err != nil {
		return err
	}
	keys, err := h.d.IAM.ListKeys(user)
	if err != nil {
		return err
	}
	out := []keyOut{}
	for _, k := range keys {
		if k.Kind == iam.KindSTS && r.URL.Query().Get("sts") != "1" {
			continue
		}
		out = append(out, keyView(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
	return nil
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request, s *session) error {
	var in struct {
		User          string          `json:"user"`
		AccessKey     string          `json:"accessKey"`
		SecretKey     string          `json:"secretKey"`
		Kind          string          `json:"kind"`
		SessionPolicy json.RawMessage `json:"sessionPolicy"`
		Description   string          `json:"description"`
		Expires       *time.Time      `json:"expires"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.User == "" {
		in.User = s.id.Name()
	}
	if err := h.keyAccess(s, in.User, "admin:CreateKey"); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = iam.KindService
	}
	if in.Kind != iam.KindUser && in.Kind != iam.KindService {
		return badRequest("kind must be user or service")
	}
	if len(in.SessionPolicy) > 0 && string(in.SessionPolicy) == "null" {
		in.SessionPolicy = nil
	}
	if in.User == "root" && in.Kind == iam.KindUser {
		return badRequest("root can only own service accounts; its own key comes from the server configuration")
	}
	if in.User == "root" {
		// Ensure the synthetic root user exists for key ownership.
		if _, err := h.d.IAM.GetUser("root"); err != nil {
			return apiErr(http.StatusBadRequest, "InvalidRequest", "root has no user record yet; log in as root first")
		}
	}
	k, secret, err := h.d.IAM.CreateKey(in.User, strings.TrimSpace(in.AccessKey), in.SecretKey, in.Kind, in.SessionPolicy, in.Expires, in.Description)
	if err != nil {
		return err
	}
	out := keyView(k)
	writeJSON(w, http.StatusCreated, map[string]any{"key": out, "secretKey": secret})
	return nil
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request, s *session) error {
	ak := r.PathValue("ak")
	k, err := h.d.IAM.GetKey(ak)
	if err != nil {
		return err
	}
	if err := h.keyAccess(s, k.User, "admin:UpdateKey"); err != nil {
		return err
	}
	var in struct {
		Enabled       *bool           `json:"enabled"`
		SecretKey     string          `json:"secretKey"`
		Description   *string         `json:"description"`
		SessionPolicy json.RawMessage `json:"sessionPolicy"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if len(in.SessionPolicy) > 0 && string(in.SessionPolicy) != "null" {
		if _, err := policy.Parse(in.SessionPolicy); err != nil {
			return apiErr(http.StatusBadRequest, "MalformedPolicy", err.Error())
		}
	}
	err = h.d.IAM.UpdateKey(ak, func(k *iam.Key) error {
		if in.Enabled != nil {
			k.Enabled = *in.Enabled
		}
		if in.Description != nil {
			k.Description = *in.Description
		}
		if len(in.SessionPolicy) > 0 {
			if string(in.SessionPolicy) == "null" {
				k.SessionPolicy = nil
			} else {
				k.SessionPolicy = in.SessionPolicy
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if in.SecretKey != "" {
		if err := h.d.IAM.SetSecret(ak, in.SecretKey); err != nil {
			return err
		}
	}
	k, err = h.d.IAM.GetKey(ak)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, keyView(k))
	return nil
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request, s *session) error {
	ak := r.PathValue("ak")
	if ak == s.accessKey {
		return badRequest("this is your current console session; use logout instead")
	}
	k, err := h.d.IAM.GetKey(ak)
	if err != nil {
		return err
	}
	if err := h.keyAccess(s, k.User, "admin:DeleteKey"); err != nil {
		return err
	}
	if err := h.d.IAM.DeleteKey(ak); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// --- groups ----------------------------------------------------------------

type groupOut struct {
	Name     string    `json:"name"`
	Enabled  bool      `json:"enabled"`
	Members  []string  `json:"members"`
	Policies []string  `json:"policies"`
	Created  time.Time `json:"created"`
}

func groupView(g *iam.Group) groupOut {
	return groupOut{Name: g.Name, Enabled: g.Enabled, Members: strs(g.Members), Policies: strs(g.Policies), Created: g.Created}
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:ListGroups"); err != nil {
		return err
	}
	groups, err := h.d.IAM.ListGroups()
	if err != nil {
		return err
	}
	out := []groupOut{}
	for _, g := range groups {
		out = append(out, groupView(g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
	return nil
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:CreateGroup"); err != nil {
		return err
	}
	var in struct {
		Name     string   `json:"name"`
		Members  []string `json:"members"`
		Policies []string `json:"policies"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if err := h.d.IAM.CreateGroup(in.Name, in.Members, in.Policies); err != nil {
		return err
	}
	g, err := h.d.IAM.GetGroup(in.Name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, groupView(g))
	return nil
}

func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:UpdateGroup"); err != nil {
		return err
	}
	var in struct {
		Enabled  *bool    `json:"enabled"`
		Members  []string `json:"members"`
		Policies []string `json:"policies"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	name := r.PathValue("name")
	err := h.d.IAM.UpdateGroup(name, func(g *iam.Group) error {
		if in.Enabled != nil {
			g.Enabled = *in.Enabled
		}
		if in.Members != nil {
			g.Members = in.Members
		}
		if in.Policies != nil {
			g.Policies = in.Policies
		}
		return nil
	})
	if err != nil {
		return err
	}
	g, err := h.d.IAM.GetGroup(name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, groupView(g))
	return nil
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:DeleteGroup"); err != nil {
		return err
	}
	if err := h.d.IAM.DeleteGroup(r.PathValue("name")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// --- policies --------------------------------------------------------------

type policyOut struct {
	Name     string          `json:"name"`
	BuiltIn  bool            `json:"builtIn"`
	Created  time.Time       `json:"created"`
	Updated  time.Time       `json:"updated"`
	Document json.RawMessage `json:"document"`
}

func policyView(p *iam.Policy) policyOut {
	return policyOut{Name: p.Name, BuiltIn: p.BuiltIn, Created: p.Created, Updated: p.Updated, Document: p.Document}
}

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:ListPolicies"); err != nil {
		return err
	}
	pols, err := h.d.IAM.ListPolicies()
	if err != nil {
		return err
	}
	out := []policyOut{}
	for _, p := range pols {
		out = append(out, policyView(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out})
	return nil
}

func (h *Handler) getIAMPolicy(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:GetPolicy"); err != nil {
		return err
	}
	p, err := h.d.IAM.GetPolicy(r.PathValue("name"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, policyView(p))
	return nil
}

func (h *Handler) putIAMPolicy(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:PutPolicy"); err != nil {
		return err
	}
	var in struct {
		Document json.RawMessage `json:"document"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if _, err := policy.Parse(in.Document); err != nil {
		return apiErr(http.StatusBadRequest, "MalformedPolicy", strings.TrimPrefix(err.Error(), "policy: malformed policy document: "))
	}
	name := r.PathValue("name")
	if err := h.d.IAM.PutPolicy(name, in.Document); err != nil {
		return err
	}
	p, err := h.d.IAM.GetPolicy(name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, policyView(p))
	return nil
}

func (h *Handler) deleteIAMPolicy(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:DeletePolicy"); err != nil {
		return err
	}
	if err := h.d.IAM.DeletePolicy(r.PathValue("name")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

// --- KMS -------------------------------------------------------------------

func (h *Handler) listKMSKeys(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:ListKMSKeys"); err != nil {
		return err
	}
	keys, err := h.d.KMS.ListKeys()
	if err != nil {
		return err
	}
	type kmsOut struct {
		ID       string    `json:"id"`
		Created  time.Time `json:"created"`
		Disabled bool      `json:"disabled"`
		Default  bool      `json:"default"`
	}
	out := []kmsOut{}
	for _, k := range keys {
		out = append(out, kmsOut{ID: k.ID, Created: k.Created, Disabled: k.Disabled, Default: k.ID == "opens3-default-key"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
	return nil
}

func (h *Handler) createKMSKey(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:CreateKMSKey"); err != nil {
		return err
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := h.d.KMS.CreateKey(strings.TrimSpace(in.ID)); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": strings.TrimSpace(in.ID)})
	return nil
}

func (h *Handler) deleteKMSKey(w http.ResponseWriter, r *http.Request, s *session) error {
	if err := h.admin(s, "admin:DeleteKMSKey"); err != nil {
		return err
	}
	if err := h.d.KMS.DeleteKey(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}
