package integration

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func (e *env) iam(ak, sk string) *iam.Client {
	return iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), Credentials: credentials.NewStaticCredentialsProvider(ak, sk, "")})
}

func (e *env) sts(ak, sk, tok string) *sts.Client {
	return sts.New(sts.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), Credentials: credentials.NewStaticCredentialsProvider(ak, sk, tok)})
}

// TestIAMAPI drives the AWS IAM client (the same code path as `aws iam ...`)
// through the user, access key, group, policy and login profile lifecycle.
func TestIAMAPI(t *testing.T) {
	e := newEnv(t)
	ic := e.iam(rootUser, rootPass)

	// Caller identity for root.
	ci, err := e.sts(rootUser, rootPass, "").GetCallerIdentity(e.ctx, &sts.GetCallerIdentityInput{})
	if err != nil || !strings.HasSuffix(*ci.Arn, ":root") || *ci.Account != "000000000000" {
		t.Fatalf("caller identity: %v %+v", err, ci)
	}

	// Users.
	cu, err := ic.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String("alice")})
	if err != nil || *cu.User.UserName != "alice" || *cu.User.Arn != "arn:aws:iam::000000000000:user/alice" || len(*cu.User.UserId) != 21 {
		t.Fatalf("create user: %v %+v", err, cu)
	}
	if _, err := ic.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String("alice")}); errCode(err) != "EntityAlreadyExists" {
		t.Fatalf("duplicate user: %v", err)
	}
	if _, err := ic.GetUser(e.ctx, &iam.GetUserInput{UserName: aws.String("nobody")}); errCode(err) != "NoSuchEntity" {
		t.Fatalf("missing user: %v", err)
	}
	lu, err := ic.ListUsers(e.ctx, &iam.ListUsersInput{})
	if err != nil || len(lu.Users) != 1 {
		t.Fatalf("list users: %v %d", err, len(lu.Users))
	}

	// Access keys: generated, usable against S3, listed, deactivated, deleted.
	ck, err := ic.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{UserName: aws.String("alice")})
	if err != nil || len(*ck.AccessKey.AccessKeyId) != 20 || len(*ck.AccessKey.SecretAccessKey) != 40 || ck.AccessKey.Status != iamtypes.StatusTypeActive {
		t.Fatalf("create key: %v %+v", err, ck)
	}
	ak, sk := *ck.AccessKey.AccessKeyId, *ck.AccessKey.SecretAccessKey

	// Policy: create, get version (URL-encoded document), attach to user.
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets","s3:ListBucket","s3:GetObject"],"Resource":"*"}]}`
	cp, err := ic.CreatePolicy(e.ctx, &iam.CreatePolicyInput{PolicyName: aws.String("readers"), PolicyDocument: aws.String(doc)})
	if err != nil || *cp.Policy.Arn != "arn:aws:iam::000000000000:policy/readers" {
		t.Fatalf("create policy: %v %+v", err, cp)
	}
	if _, err := ic.CreatePolicy(e.ctx, &iam.CreatePolicyInput{PolicyName: aws.String("bad"), PolicyDocument: aws.String(`{"Statement":[{"Effect":"Allow","Action":"admin:AddUser","Resource":"*"}]}`)}); errCode(err) != "MalformedPolicyDocument" || !strings.Contains(err.Error(), "iam:CreateUser") {
		t.Fatalf("legacy admin action must be rejected with the AWS name: %v", err)
	}
	pv, err := ic.GetPolicyVersion(e.ctx, &iam.GetPolicyVersionInput{PolicyArn: cp.Policy.Arn, VersionId: aws.String("v1")})
	if err != nil || !strings.HasPrefix(*pv.PolicyVersion.Document, "%7B%22Version%22") {
		t.Fatalf("policy version: %v %v", err, pv.PolicyVersion)
	}
	if _, err := ic.AttachUserPolicy(e.ctx, &iam.AttachUserPolicyInput{UserName: aws.String("alice"), PolicyArn: cp.Policy.Arn}); err != nil {
		t.Fatal(err)
	}
	lap, _ := ic.ListAttachedUserPolicies(e.ctx, &iam.ListAttachedUserPoliciesInput{UserName: aws.String("alice")})
	if len(lap.AttachedPolicies) != 1 || *lap.AttachedPolicies[0].PolicyName != "readers" {
		t.Fatalf("attached: %+v", lap.AttachedPolicies)
	}
	// Built-in policies appear as AWS-managed.
	lp, _ := ic.ListPolicies(e.ctx, &iam.ListPoliciesInput{Scope: iamtypes.PolicyScopeTypeAws})
	if len(lp.Policies) < 3 || !strings.HasPrefix(*lp.Policies[0].Arn, "arn:aws:iam::aws:policy/") {
		t.Fatalf("aws-managed policies: %+v", lp.Policies)
	}
	if _, err := ic.AttachUserPolicy(e.ctx, &iam.AttachUserPolicyInput{UserName: aws.String("alice"), PolicyArn: aws.String("arn:aws:iam::aws:policy/readonly")}); err != nil {
		t.Fatalf("attach builtin: %v", err)
	}

	// The key works against S3 with the attached policy's permissions.
	e.mkBucket("iam-bucket")
	e.put("iam-bucket", "k", "v")
	ac := e.client(ak, sk, "")
	if _, err := ac.ListBuckets(e.ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatalf("alice list buckets: %v", err)
	}
	if _, err := ac.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("iam-bucket"), Key: aws.String("x"), Body: strings.NewReader("x")}); errCode(err) != "AccessDenied" {
		t.Fatalf("alice put should be denied: %v", err)
	}
	// Alice can see herself and her own keys, but not manage users.
	aic := e.iam(ak, sk)
	// Reading one's own user needs iam:GetUser like anything else (a
	// self-only policy grants it; see TestIAMSelfScopedReads).
	if _, err := aic.GetUser(e.ctx, &iam.GetUserInput{}); errCode(err) != "AccessDenied" {
		t.Fatalf("self get-user without permission: %v", err)
	}
	if _, err := aic.ListUsers(e.ctx, &iam.ListUsersInput{}); errCode(err) != "AccessDenied" {
		t.Fatalf("alice list users: %v", err)
	}
	// Listing one's own keys needs iam:ListAccessKeys like anything else
	// (the session policy of a narrowed credential must apply); a
	// self-only policy in the AWS style grants it.
	if _, err := aic.ListAccessKeys(e.ctx, &iam.ListAccessKeysInput{}); errCode(err) != "AccessDenied" {
		t.Fatalf("self list keys without permission: %v", err)
	}
	selfDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ListAccessKeys","Resource":"arn:aws:iam::*:user/${aws:username}"}]}`
	sp, err := ic.CreatePolicy(e.ctx, &iam.CreatePolicyInput{PolicyName: aws.String("self-keys"), PolicyDocument: aws.String(selfDoc)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ic.AttachUserPolicy(e.ctx, &iam.AttachUserPolicyInput{UserName: aws.String("alice"), PolicyArn: sp.Policy.Arn}); err != nil {
		t.Fatal(err)
	}
	if _, err := aic.ListAccessKeys(e.ctx, &iam.ListAccessKeysInput{}); err != nil {
		t.Fatalf("self list keys with a self-only policy: %v", err)
	}
	if _, err := aic.ListAccessKeys(e.ctx, &iam.ListAccessKeysInput{UserName: aws.String("root")}); errCode(err) != "AccessDenied" {
		t.Fatalf("self-only policy reached another user: %v", err)
	}
	if _, err := ic.DetachUserPolicy(e.ctx, &iam.DetachUserPolicyInput{UserName: aws.String("alice"), PolicyArn: sp.Policy.Arn}); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.DeletePolicy(e.ctx, &iam.DeletePolicyInput{PolicyArn: sp.Policy.Arn}); err != nil {
		t.Fatal(err)
	}
	if aci, err := e.sts(ak, sk, "").GetCallerIdentity(e.ctx, &sts.GetCallerIdentityInput{}); err != nil || *aci.Arn != "arn:aws:iam::000000000000:user/alice" {
		t.Fatalf("alice caller identity: %v", err)
	}
	// Deactivate: the key stops working.
	if _, err := ic.UpdateAccessKey(e.ctx, &iam.UpdateAccessKeyInput{UserName: aws.String("alice"), AccessKeyId: aws.String(ak), Status: iamtypes.StatusTypeInactive}); err != nil {
		t.Fatal(err)
	}
	if _, err := ac.ListBuckets(e.ctx, &s3.ListBucketsInput{}); errCode(err) != "InvalidAccessKeyId" {
		t.Fatalf("inactive key: %v", err)
	}

	// Groups.
	if _, err := ic.CreateGroup(e.ctx, &iam.CreateGroupInput{GroupName: aws.String("team")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.AddUserToGroup(e.ctx, &iam.AddUserToGroupInput{GroupName: aws.String("team"), UserName: aws.String("alice")}); err != nil {
		t.Fatal(err)
	}
	gg, err := ic.GetGroup(e.ctx, &iam.GetGroupInput{GroupName: aws.String("team")})
	if err != nil || len(gg.Users) != 1 {
		t.Fatalf("get group: %v", err)
	}
	if _, err := ic.AttachGroupPolicy(e.ctx, &iam.AttachGroupPolicyInput{GroupName: aws.String("team"), PolicyArn: cp.Policy.Arn}); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.DeleteGroup(e.ctx, &iam.DeleteGroupInput{GroupName: aws.String("team")}); errCode(err) != "DeleteConflict" {
		t.Fatalf("delete group with members: %v", err)
	}
	if _, err := ic.DeletePolicy(e.ctx, &iam.DeletePolicyInput{PolicyArn: cp.Policy.Arn}); errCode(err) != "DeleteConflict" {
		t.Fatalf("delete attached policy: %v", err)
	}

	// Login profile = console password; it must work for the console and never for the API.
	if _, err := ic.CreateLoginProfile(e.ctx, &iam.CreateLoginProfileInput{UserName: aws.String("alice"), Password: aws.String("correct horse battery")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.CreateLoginProfile(e.ctx, &iam.CreateLoginProfileInput{UserName: aws.String("alice"), Password: aws.String("another one")}); errCode(err) != "EntityAlreadyExists" {
		t.Fatalf("second login profile: %v", err)
	}
	if lp, err := ic.GetLoginProfile(e.ctx, &iam.GetLoginProfileInput{UserName: aws.String("alice")}); err != nil || *lp.LoginProfile.UserName != "alice" {
		t.Fatalf("get login profile: %v", err)
	}
	if _, err := e.srv.IAM.VerifyPassword("alice", "correct horse battery"); err != nil {
		t.Fatal("console password should verify")
	}
	if _, err := e.client("alice", "correct horse battery", "").ListBuckets(e.ctx, &s3.ListBucketsInput{}); errCode(err) != "InvalidAccessKeyId" {
		t.Fatalf("password must not work as an API credential: %v", err)
	}

	// Deleting a user requires the AWS clean-up order.
	if _, err := ic.DeleteUser(e.ctx, &iam.DeleteUserInput{UserName: aws.String("alice")}); errCode(err) != "DeleteConflict" {
		t.Fatalf("delete user with keys: %v", err)
	}
	ic.DeleteAccessKey(e.ctx, &iam.DeleteAccessKeyInput{UserName: aws.String("alice"), AccessKeyId: aws.String(ak)})
	ic.DeleteLoginProfile(e.ctx, &iam.DeleteLoginProfileInput{UserName: aws.String("alice")})
	ic.RemoveUserFromGroup(e.ctx, &iam.RemoveUserFromGroupInput{GroupName: aws.String("team"), UserName: aws.String("alice")})
	ic.DetachUserPolicy(e.ctx, &iam.DetachUserPolicyInput{UserName: aws.String("alice"), PolicyArn: cp.Policy.Arn})
	ic.DetachUserPolicy(e.ctx, &iam.DetachUserPolicyInput{UserName: aws.String("alice"), PolicyArn: aws.String("arn:aws:iam::aws:policy/readonly")})
	if _, err := ic.DeleteUser(e.ctx, &iam.DeleteUserInput{UserName: aws.String("alice")}); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	ic.DetachGroupPolicy(e.ctx, &iam.DetachGroupPolicyInput{GroupName: aws.String("team"), PolicyArn: cp.Policy.Arn})
	if _, err := ic.DeleteGroup(e.ctx, &iam.DeleteGroupInput{GroupName: aws.String("team")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.DeletePolicy(e.ctx, &iam.DeletePolicyInput{PolicyArn: cp.Policy.Arn}); err != nil {
		t.Fatal(err)
	}
	sum, err := ic.GetAccountSummary(e.ctx, &iam.GetAccountSummaryInput{})
	if err != nil || sum.SummaryMap["Users"] != 0 {
		t.Fatalf("summary: %v %v", err, sum.SummaryMap)
	}
	// Anonymous and unknown actions.
	anon := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), Credentials: aws.AnonymousCredentials{}})
	if _, err := anon.ListUsers(e.ctx, &iam.ListUsersInput{}); errCode(err) != "AccessDenied" {
		t.Fatalf("anonymous: %v", err)
	}
	if _, err := ic.ListServerCertificates(e.ctx, &iam.ListServerCertificatesInput{}); errCode(err) != "InvalidAction" {
		t.Fatalf("unsupported action: %v", err)
	}
}
