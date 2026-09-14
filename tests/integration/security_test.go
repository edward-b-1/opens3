package integration

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// TestUnsignedAmzHeadersRejected: a holder of a presigned PUT URL cannot
// turn it into a copy, or attach an ACL, by adding headers the signer did
// not sign.
func TestUnsignedAmzHeadersRejected(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("sig")
	e.put("sig", "secret", "the secret object")
	ps := s3.NewPresignClient(e.s3)
	preq, err := ps.PresignPutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sig"), Key: aws.String("up")}, s3.WithPresignExpires(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	send := func(extra http.Header) *http.Response {
		hreq, _ := http.NewRequest("PUT", preq.URL, strings.NewReader("uploaded"))
		for k, v := range preq.SignedHeader {
			hreq.Header[k] = v
		}
		for k, v := range extra {
			hreq.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(hreq)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := send(http.Header{"x-amz-copy-source": {"/sig/secret"}}); resp.StatusCode != 403 {
		t.Fatalf("unsigned copy-source: %d", resp.StatusCode)
	}
	if _, err := e.s3.HeadObject(e.ctx, &s3.HeadObjectInput{Bucket: aws.String("sig"), Key: aws.String("up")}); err == nil {
		t.Fatal("the copy went through")
	}
	if resp := send(http.Header{"x-amz-acl": {"public-read"}}); resp.StatusCode != 403 {
		t.Fatalf("unsigned acl: %d", resp.StatusCode)
	}
	if resp := send(http.Header{"x-amz-tagging": {"a=b"}}); resp.StatusCode != 403 {
		t.Fatalf("unsigned tagging: %d", resp.StatusCode)
	}
	// Neutral headers are tolerated; the plain presigned PUT works.
	if resp := send(http.Header{"x-amz-user-agent": {"test"}}); resp.StatusCode != 200 {
		t.Fatalf("neutral unsigned header: %d", resp.StatusCode)
	}
	if b, _ := e.get("sig", "up"); b != "uploaded" {
		t.Fatal("presigned upload content")
	}
	// Header-signed requests: an x-amz header appended after signing is
	// refused too (the SDK itself signs every x-amz header it sends).
	tamper := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), UsePathStyle: true, Credentials: staticCreds(rootUser, rootPass, ""),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.Header.Set("x-amz-copy-source", "/sig/secret")
			return http.DefaultTransport.RoundTrip(r)
		})}})
	if _, err := tamper.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("sig"), Key: aws.String("tampered"), Body: strings.NewReader("x")}); errCode(err) != "AccessDenied" {
		t.Fatalf("appended header on a signed request: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRestrictedCredentialsCannotEscalate: credentials narrowed by a
// session policy cannot mint credentials without it, and a derived
// session can only narrow further.
func TestRestrictedCredentialsCannotEscalate(t *testing.T) {
	e := newEnv(t)
	ic := e.iam(rootUser, rootPass)
	if _, err := ic.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String("bob")}); err != nil {
		t.Fatal(err)
	}
	if _, err := ic.AttachUserPolicy(e.ctx, &iam.AttachUserPolicyInput{UserName: aws.String("bob"), PolicyArn: aws.String("arn:aws:iam::aws:policy/consoleAdmin")}); err != nil {
		t.Fatal(err)
	}
	ck, err := ic.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{UserName: aws.String("bob")})
	if err != nil {
		t.Fatal(err)
	}
	bobAK, bobSK := *ck.AccessKey.AccessKeyId, *ck.AccessKey.SecretAccessKey

	// A session narrowed to listing buckets.
	listOnly := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets","iam:*","sts:AssumeRole"],"Resource":"*"}]}`
	ar, err := e.sts(bobAK, bobSK, "").AssumeRole(e.ctx, &sts.AssumeRoleInput{RoleArn: aws.String("arn:aws:iam::000000000000:role/any"), RoleSessionName: aws.String("s"), Policy: aws.String(listOnly)})
	if err != nil {
		t.Fatal(err)
	}
	sAK, sSK, sTok := *ar.Credentials.AccessKeyId, *ar.Credentials.SecretAccessKey, *ar.Credentials.SessionToken
	if _, err := e.client(sAK, sSK, sTok).ListBuckets(e.ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatalf("session should list buckets: %v", err)
	}
	if _, err := e.client(sAK, sSK, sTok).CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String("nope")}); errCode(err) != "AccessDenied" {
		t.Fatalf("session should not create buckets: %v", err)
	}
	// Even though the session policy allows iam:*, the session may not mint
	// a permanent key or a console password for its user.
	sIAM := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), Credentials: staticCreds(sAK, sSK, sTok)})
	if _, err := sIAM.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{}); errCode(err) != "AccessDenied" {
		t.Fatalf("restricted session created a key: %v", err)
	}
	if _, err := sIAM.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{UserName: aws.String("bob")}); errCode(err) != "AccessDenied" {
		t.Fatalf("restricted session created a key by name: %v", err)
	}
	if _, err := sIAM.CreateLoginProfile(e.ctx, &iam.CreateLoginProfileInput{UserName: aws.String("bob"), Password: aws.String("Password-123456")}); errCode(err) != "AccessDenied" {
		t.Fatalf("restricted session set a password: %v", err)
	}
	// A second session derived with a broader policy stays narrowed.
	wide := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	ar2, err := e.sts(sAK, sSK, sTok).AssumeRole(e.ctx, &sts.AssumeRoleInput{RoleArn: aws.String("arn:aws:iam::000000000000:role/any"), RoleSessionName: aws.String("s2"), Policy: aws.String(wide)})
	if err != nil {
		t.Fatal(err)
	}
	c2 := e.client(*ar2.Credentials.AccessKeyId, *ar2.Credentials.SecretAccessKey, *ar2.Credentials.SessionToken)
	if _, err := c2.CreateBucket(e.ctx, &s3.CreateBucketInput{Bucket: aws.String("nope2")}); errCode(err) != "AccessDenied" {
		t.Fatalf("derived session widened its rights: %v", err)
	}
	if _, err := c2.ListBuckets(e.ctx, &s3.ListBucketsInput{}); err != nil {
		t.Fatalf("derived session lost allowed rights: %v", err)
	}

	// An unrestricted session (no policy) keeps the user's rights, and
	// creating a key for oneself needs the permission like anything else.
	ar3, err := e.sts(bobAK, bobSK, "").AssumeRole(e.ctx, &sts.AssumeRoleInput{RoleArn: aws.String("arn:aws:iam::000000000000:role/any"), RoleSessionName: aws.String("s3")})
	if err != nil {
		t.Fatal(err)
	}
	uIAM := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), Credentials: staticCreds(*ar3.Credentials.AccessKeyId, *ar3.Credentials.SecretAccessKey, *ar3.Credentials.SessionToken)})
	if _, err := uIAM.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{}); err != nil {
		t.Fatalf("unrestricted session with iam rights: %v", err)
	}
	if _, err := ic.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String("carol")}); err != nil {
		t.Fatal(err)
	}
	ck2, _ := ic.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{UserName: aws.String("carol")})
	cIAM := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), Credentials: staticCreds(*ck2.AccessKey.AccessKeyId, *ck2.AccessKey.SecretAccessKey, "")})
	if _, err := cIAM.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{}); errCode(err) != "AccessDenied" {
		t.Fatalf("user without iam rights created a key for itself: %v", err)
	}

	// Root's sessions belong to a synthetic user that can never own keys.
	rar, err := e.sts(rootUser, rootPass, "").AssumeRole(e.ctx, &sts.AssumeRoleInput{RoleArn: aws.String("arn:aws:iam::000000000000:role/any"), RoleSessionName: aws.String("r")})
	if err != nil {
		t.Fatal(err)
	}
	rIAM := iam.New(iam.Options{Region: "us-east-1", BaseEndpoint: aws.String(e.ts.URL), Credentials: staticCreds(*rar.Credentials.AccessKeyId, *rar.Credentials.SecretAccessKey, *rar.Credentials.SessionToken)})
	if _, err := rIAM.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{}); err == nil {
		t.Fatal("root session minted a permanent key")
	}
	if _, err := ic.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String("root")}); err == nil {
		t.Fatal("a user named root was created")
	}
}

func staticCreds(ak, sk, tok string) aws.CredentialsProvider {
	return credentials.NewStaticCredentialsProvider(ak, sk, tok)
}

// managedUser creates a user with one managed policy and returns an S3
// client for a fresh access key.
func (e *env) managedUser(name, policyDoc string) *s3.Client {
	e.t.Helper()
	ic := e.iam(rootUser, rootPass)
	if _, err := ic.CreateUser(e.ctx, &iam.CreateUserInput{UserName: aws.String(name)}); err != nil {
		e.t.Fatal(err)
	}
	cp, err := ic.CreatePolicy(e.ctx, &iam.CreatePolicyInput{PolicyName: aws.String(name + "-policy"), PolicyDocument: aws.String(policyDoc)})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := ic.AttachUserPolicy(e.ctx, &iam.AttachUserPolicyInput{UserName: aws.String(name), PolicyArn: cp.Policy.Arn}); err != nil {
		e.t.Fatal(err)
	}
	ck, err := ic.CreateAccessKey(e.ctx, &iam.CreateAccessKeyInput{UserName: aws.String(name)})
	if err != nil {
		e.t.Fatal(err)
	}
	return e.client(*ck.AccessKey.AccessKeyId, *ck.AccessKey.SecretAccessKey, "")
}

// TestAuthorizationContexts covers the copy-source and batch-delete
// authorisation paths: source tags are evaluated, a versioned source needs
// GetObjectVersion, and batch delete is authorised per key only.
func TestAuthorizationContexts(t *testing.T) {
	e := newEnv(t)
	e.mkBucket("src")
	e.mkBucket("dst")
	if _, err := e.s3.PutObject(e.ctx, &s3.PutObjectInput{Bucket: aws.String("src"), Key: aws.String("tagged"), Body: strings.NewReader("t"), Tagging: aws.String("env=prod")}); err != nil {
		t.Fatal(err)
	}
	e.put("src", "plain", "p")

	// Copy: the source's existing tags are part of the source authorisation.
	eve := e.managedUser("eve", `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::src/*","Condition":{"StringEquals":{"s3:ExistingObjectTag/env":"prod"}}},
		{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::dst/*"}]}`)
	if _, err := eve.CopyObject(e.ctx, &s3.CopyObjectInput{Bucket: aws.String("dst"), Key: aws.String("c1"), CopySource: aws.String("/src/tagged")}); err != nil {
		t.Fatalf("copy of a tagged source: %v", err)
	}
	if _, err := eve.CopyObject(e.ctx, &s3.CopyObjectInput{Bucket: aws.String("dst"), Key: aws.String("c2"), CopySource: aws.String("/src/plain")}); errCode(err) != "AccessDenied" {
		t.Fatalf("copy of an untagged source: %v", err)
	}

	// UploadPartCopy with a version needs s3:GetObjectVersion.
	e.mkBucket("vsrc")
	if _, err := e.s3.PutBucketVersioning(e.ctx, &s3.PutBucketVersioningInput{Bucket: aws.String("vsrc"), VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled}}); err != nil {
		t.Fatal(err)
	}
	vo := e.put("vsrc", "v", strings.Repeat("v", 16))
	fay := e.managedUser("fay", `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::vsrc/*"},
		{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::dst/*"}]}`)
	mp, err := fay.CreateMultipartUpload(e.ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("dst"), Key: aws.String("mp")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fay.UploadPartCopy(e.ctx, &s3.UploadPartCopyInput{Bucket: aws.String("dst"), Key: aws.String("mp"), UploadId: mp.UploadId, PartNumber: aws.Int32(1),
		CopySource: aws.String("/vsrc/v?versionId=" + *vo.VersionId)}); errCode(err) != "AccessDenied" {
		t.Fatalf("versioned part copy without GetObjectVersion: %v", err)
	}
	if _, err := fay.UploadPartCopy(e.ctx, &s3.UploadPartCopyInput{Bucket: aws.String("dst"), Key: aws.String("mp"), UploadId: mp.UploadId, PartNumber: aws.Int32(1),
		CopySource: aws.String("/vsrc/v")}); err != nil {
		t.Fatalf("unversioned part copy: %v", err)
	}
	fay.AbortMultipartUpload(e.ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String("dst"), Key: aws.String("mp"), UploadId: mp.UploadId})

	// Batch delete with a policy scoped to the objects only.
	e.mkBucket("batchdel")
	e.put("batchdel", "a", "a")
	e.put("batchdel", "b", "b")
	dan := e.managedUser("dan", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:DeleteObject","Resource":"arn:aws:s3:::batchdel/*"}]}`)
	out, err := dan.DeleteObjects(e.ctx, &s3.DeleteObjectsInput{Bucket: aws.String("batchdel"), Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: aws.String("a")}, {Key: aws.String("b")}}}})
	if err != nil || len(out.Errors) != 0 || len(out.Deleted) != 2 {
		t.Fatalf("batch delete with an object-scoped policy: %v %+v", err, out)
	}
	// And no rights at all: every key is refused individually, not the call.
	none := e.managedUser("nobody", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListAllMyBuckets","Resource":"*"}]}`)
	e.put("batchdel", "c", "c")
	out, err = none.DeleteObjects(e.ctx, &s3.DeleteObjectsInput{Bucket: aws.String("batchdel"), Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: aws.String("c")}}}})
	if err != nil || len(out.Errors) != 1 || *out.Errors[0].Code != "AccessDenied" {
		t.Fatalf("batch delete without rights: %v %+v", err, out)
	}
	if b, _ := e.get("batchdel", "c"); b != "c" {
		t.Fatal("object deleted without rights")
	}
}
