package iam

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	db, err := kv.OpenBolt(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db, Config{RootAccessKey: "rootuser", RootSecretKey: "rootsecret", Wrapper: kms.TestMaster()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUsersKeysGroupsPolicies(t *testing.T) {
	s := openStore(t)
	if sec, err := s.LookupSecret("rootuser"); err != nil || sec != "rootsecret" {
		t.Fatal("root lookup")
	}
	if err := s.CreateUser("alice", "alicesecret", []string{"readonly"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("alice", "xxxxxxxx", nil); err != ErrExists {
		t.Fatal("dup user")
	}
	if err := s.CreateUser("bob", "bobsecret", []string{"nope"}); err == nil {
		t.Fatal("unknown policy")
	}
	if sec, err := s.LookupSecret("alice"); err != nil || sec != "alicesecret" {
		t.Fatalf("alice secret: %v", err)
	}
	id, err := s.Resolve("alice", "")
	if err != nil || id.Name() != "alice" || len(id.Policies) != 1 {
		t.Fatalf("resolve: %v %+v", err, id)
	}
	if id.ARN() != "arn:aws:iam::000000000000:user/alice" {
		t.Fatal(id.ARN())
	}
	// Disable.
	s.UpdateUser("alice", func(u *User) error { u.Enabled = false; return nil })
	if _, err := s.Resolve("alice", ""); err != ErrDisabled {
		t.Fatalf("disabled: %v", err)
	}
	s.UpdateUser("alice", func(u *User) error { u.Enabled = true; return nil })

	// Service account with session policy.
	sp := json.RawMessage(`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`)
	k, secret, err := s.CreateKey("alice", "", "", KindService, sp, nil, "svc")
	if err != nil || len(secret) < 8 {
		t.Fatal(err)
	}
	if sec, _ := s.LookupSecret(k.AccessKey); sec != secret {
		t.Fatal("service secret")
	}
	sid, _ := s.Resolve(k.AccessKey, "")
	if sid.Name() != "alice" || len(sid.SessionPolicy) == 0 {
		t.Fatal("service identity")
	}

	// Groups.
	s.PutPolicy("writers", json.RawMessage(`{"Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::*"}]}`))
	if err := s.CreateGroup("team", []string{"alice"}, []string{"writers"}); err != nil {
		t.Fatal(err)
	}
	id, _ = s.Resolve("alice", "")
	if len(id.Policies) != 2 || len(id.User.Groups) != 1 {
		t.Fatalf("group policies: %d groups %v", len(id.Policies), id.User.Groups)
	}
	s.DeletePolicy("writers")
	id, _ = s.Resolve("alice", "")
	if len(id.Policies) != 1 {
		t.Fatal("policy detach")
	}
	if err := s.DeletePolicy("readonly"); err != ErrBuiltin {
		t.Fatal("builtin delete")
	}
	// STS.
	ak, sk, tok, exp, err := s.AssumeRole(id, nil, time.Hour)
	if err != nil || exp.Before(time.Now()) {
		t.Fatal(err)
	}
	if sec, _ := s.LookupSecret(ak); sec != sk {
		t.Fatal("sts secret")
	}
	if _, err := s.Resolve(ak, "wrong"); err != ErrBadToken {
		t.Fatal("token check")
	}
	if sid, err := s.Resolve(ak, tok); err != nil || sid.PrincipalType() != "AssumedRole" {
		t.Fatalf("sts resolve: %v", err)
	}
	// Delete user removes keys and memberships.
	s.DeleteUser("alice")
	if _, err := s.LookupSecret(k.AccessKey); err != ErrNotFound {
		t.Fatal("service key should be gone")
	}
	g, _ := s.GetGroup("team")
	if len(g.Members) != 0 {
		t.Fatal("membership should be gone")
	}
}

func TestAuthorize(t *testing.T) {
	s := openStore(t)
	s.CreateUser("ro", "rosecret1", []string{"readonly"})
	s.CreateUser("rw", "rwsecret1", []string{"readwrite"})
	s.CreateUser("none", "nonesecret", nil)
	root, _ := s.Resolve("rootuser", "")
	ro, _ := s.Resolve("ro", "")
	rw, _ := s.Resolve("rw", "")
	none, _ := s.Resolve("none", "")
	owner := root.CanonicalID()

	base := Request{Bucket: "b", Key: "k", BucketOwner: owner, ObjectOwner: owner, Ownership: "BucketOwnerEnforced"}
	get := base
	get.Action = "s3:GetObject"
	put := base
	put.Action = "s3:PutObject"

	check := func(name string, r Request, id *Identity, want bool) {
		t.Helper()
		r.Identity = id
		if got := s.Authorize(r); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
	check("root get", get, root, true)
	check("ro get", get, ro, true)
	check("ro put", put, ro, false)
	check("rw put", put, rw, true)
	check("none get", get, none, false)
	check("anon get", get, nil, false)

	// Bucket policy grants anonymous read and denies rw.
	pol := json.RawMessage(`{"Statement":[
	  {"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"},
	  {"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::000000000000:user/rw"},"Action":"s3:PutObject","Resource":"arn:aws:s3:::b/*"}]}`)
	get.BucketPolicy, put.BucketPolicy = pol, pol
	check("anon get via policy", get, nil, true)
	check("none get via policy", get, none, true)
	check("rw put denied by policy", put, rw, false)
	check("root put still ok", put, root, true)
	// RestrictPublicBuckets blocks anonymous.
	get.PublicAccessBlock = &meta.PublicAccessBlock{RestrictPublicBuckets: true}
	check("anon blocked by PAB", get, nil, false)
	check("none still ok", get, none, true)

	// Deny root via policy, and root escape hatch.
	deny := json.RawMessage(`{"Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*","Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"]}]}`)
	get.BucketPolicy, get.PublicAccessBlock = deny, nil
	check("root denied by policy", get, root, false)
	del := base
	del.Action = "s3:DeleteBucketPolicy"
	del.BucketPolicy = deny
	check("root can delete policy", del, root, true)

	// Session policy restricts.
	sp := json.RawMessage(`{"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::other/*"}]}`)
	k, _, _ := s.CreateKey("rw", "", "", KindService, sp, nil, "")
	svc, _ := s.Resolve(k.AccessKey, "")
	g2 := base
	g2.Action = "s3:GetObject"
	check("service account outside session policy", g2, svc, false)
	g2.Bucket = "other"
	check("service account inside session policy", g2, svc, true)

	// ACLs when ownership allows them.
	acl := base
	acl.Action = "s3:GetObject"
	acl.Ownership = "ObjectWriter"
	acl.ObjectACL = &meta.ACL{Owner: owner, Grants: []meta.Grant{{Grantee: "http://acs.amazonaws.com/groups/global/AllUsers", GranteeType: "Group", Permission: "READ"}}}
	check("anon read via object ACL", acl, nil, true)
	acl.PublicAccessBlock = &meta.PublicAccessBlock{IgnorePublicAcls: true}
	check("anon read via ACL ignored", acl, nil, false)
	acl.PublicAccessBlock = nil
	acl.ObjectACL.Grants[0].Permission = "READ_ACP"
	check("READ_ACP does not grant read", acl, nil, false)
	acl.Action = "s3:GetObjectAcl"
	check("READ_ACP grants GetObjectAcl", acl, nil, true)
	// Object owner implicit full control.
	acl.ObjectOwner = none.CanonicalID()
	acl.Action = "s3:GetObject"
	check("object owner reads own object", acl, none, true)

	// Condition keys in bucket policy.
	cond := base
	cond.Action = "s3:GetObject"
	cond.BucketPolicy = json.RawMessage(`{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*","Condition":{"StringEquals":{"aws:username":"none"}}}]}`)
	check("username variable condition", cond, none, true)
	check("username variable condition other", cond, ro, true) // ro has readonly policy anyway
	cond.BucketPolicy = json.RawMessage(`{"Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*","Condition":{"StringNotEquals":{"aws:username":"none"}}}]}`)
	check("deny others", cond, ro, false)
	check("allow none via identity? no policy -> false", cond, none, false)

	// admin actions.
	s.CreateUser("adm", "admsecret1", []string{"consoleAdmin"})
	adm, _ := s.Resolve("adm", "")
	a := Request{Action: "iam:ListUsers"}
	check("admin allowed", a, adm, true)
	check("admin denied for rw", a, rw, false)
	check("admin root", a, root, true)
}

func TestConsolePasswords(t *testing.T) {
	s := openStore(t)
	if err := s.CreateUser("carol", "", []string{"readonly"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyPassword("carol", "anything"); err != ErrBadPassword {
		t.Fatalf("no password set: %v", err)
	}
	if err := s.SetPassword("carol", "short"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := s.SetPassword("carol", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.GetUser("carol")
	if !u.HasPassword() || string(u.PasswordHash) == "correct horse battery" {
		t.Fatal("password must be stored hashed")
	}
	id, err := s.VerifyPassword("carol", "correct horse battery")
	if err != nil || id.Name() != "carol" || len(id.Policies) != 1 {
		t.Fatalf("verify: %v", err)
	}
	if _, err := s.VerifyPassword("carol", "wrong"); err != ErrBadPassword {
		t.Fatal("wrong password accepted")
	}
	if _, err := s.VerifyPassword("nobody", "x"); err != ErrBadPassword {
		t.Fatal("unknown user")
	}
	// A password is never an API credential.
	if _, err := s.LookupSecret("carol"); err != ErrNotFound {
		t.Fatalf("password must not be an access key: %v", err)
	}
	// Root signs in with the configured root credentials.
	if id, err := s.VerifyPassword("rootuser", "rootsecret"); err != nil || !id.IsRoot {
		t.Fatalf("root: %v", err)
	}
	if _, err := s.VerifyPassword("rootuser", "nope"); err != ErrBadPassword {
		t.Fatal("root wrong password")
	}
	s.UpdateUser("carol", func(u *User) error { u.Enabled = false; return nil })
	if _, err := s.VerifyPassword("carol", "correct horse battery"); err != ErrDisabled {
		t.Fatal("disabled user")
	}
	s.UpdateUser("carol", func(u *User) error { u.Enabled = true; return nil })
	if err := s.ClearPassword("carol"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyPassword("carol", "correct horse battery"); err != ErrBadPassword {
		t.Fatal("cleared password still works")
	}
	// Generated credentials have the documented shape.
	if ak := GenerateAccessKey(); len(ak) != 20 || strings.ToUpper(ak) != ak {
		t.Fatalf("access key %q", ak)
	}
	if sk := GenerateSecretKey(); len(sk) != 40 {
		t.Fatalf("secret key %q", sk)
	}
}

// TestBucketWriteACLDoesNotGrantTagging: bucket ACL WRITE maps to object
// creation and deletion (AWS's ACL mapping), not to the tagging
// operations, which need a policy; the bucket owner keeps them as the
// resource owner.
func TestBucketWriteACLDoesNotGrantTagging(t *testing.T) {
	db, err := kv.OpenBolt(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := Open(db, Config{RootAccessKey: "root", RootSecretKey: "rootsecret", Wrapper: kms.TestMaster()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser("writer", "writersecret000", nil); err != nil {
		t.Fatal(err)
	}
	id, err := st.Resolve("writer", "")
	if err != nil {
		t.Fatal(err)
	}
	acl := &meta.ACL{Owner: "owner", Grants: []meta.Grant{{Grantee: id.CanonicalID(), GranteeType: "CanonicalUser", Permission: "WRITE"}}}
	req := func(action string) Request {
		return Request{Identity: id, Action: action, Bucket: "b", Key: "k", BucketOwner: "owner", BucketACL: acl, Ownership: "ObjectWriter", Conditions: map[string][]string{}}
	}
	if !st.Authorize(req("s3:PutObject")) || !st.Authorize(req("s3:DeleteObject")) {
		t.Fatal("bucket WRITE must allow object creation and deletion")
	}
	if st.Authorize(req("s3:PutObjectTagging")) || st.Authorize(req("s3:DeleteObjectTagging")) {
		t.Fatal("bucket WRITE must not grant the tagging operations")
	}
}
