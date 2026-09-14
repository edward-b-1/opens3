package s3api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/edward-b-1/opens3/internal/iam"
	"github.com/edward-b-1/opens3/internal/kms"
	"github.com/edward-b-1/opens3/internal/kv"
	"github.com/edward-b-1/opens3/internal/meta"
	"github.com/edward-b-1/opens3/internal/s3err"
)

// TestReauthorizeServed: a request authorised against a public object
// version must not receive a private version stored in between.
func TestReauthorizeServed(t *testing.T) {
	db, err := kv.OpenBolt(filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := iam.Open(db, iam.Config{RootAccessKey: "root", RootSecretKey: "rootsecret", Wrapper: kms.TestMaster()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{iam: st}
	public := &meta.Object{Bucket: "b", Key: "k", VersionID: "v1", Seq: 1, Owner: "owner",
		ACL: &meta.ACL{Owner: "owner", Grants: []meta.Grant{{Grantee: "http://acs.amazonaws.com/groups/global/AllUsers", GranteeType: "Group", Permission: "READ"}}}}
	private := &meta.Object{Bucket: "b", Key: "k", VersionID: "v2", Seq: 2, Owner: "owner",
		ACL: &meta.ACL{Owner: "owner", Grants: []meta.Grant{{Grantee: "owner", GranteeType: "CanonicalUser", Permission: "FULL_CONTROL"}}}}
	c := &reqCtx{r: httptest.NewRequest("GET", "/b/k", nil), bucket: "b", key: "k", op: &operation{name: "GetObject", action: "s3:GetObject", level: 2, needsObject: true},
		bkt: &meta.Bucket{Name: "b", Owner: "owner", Ownership: "ObjectWriter"}, objMeta: public}
	// Anonymous caller: the same version passes, the private replacement is refused.
	if err := s.reauthorizeServed(c, public); err != nil {
		t.Fatalf("same version: %v", err)
	}
	if err := s.reauthorizeServed(c, private); s3err.From(err).Code != s3err.AccessDenied {
		t.Fatalf("private replacement: %v", err)
	}
	// A replacement that is also public is fine, and becomes the authorised object.
	public2 := *public
	public2.VersionID, public2.Seq = "v3", 3
	if err := s.reauthorizeServed(c, &public2); err != nil || c.objMeta != &public2 {
		t.Fatalf("public replacement: %v", err)
	}
	// Root is never refused.
	c.identity = &iam.Identity{IsRoot: true}
	if err := s.reauthorizeServed(c, private); err != nil {
		t.Fatalf("root: %v", err)
	}
}
