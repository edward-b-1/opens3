package iam

// Built-in policies. The names follow MinIO's so that migrated users and
// tooling keep working.
var builtinPolicies = map[string]string{
	"readonly":     `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:GetObject","s3:GetObjectVersion","s3:GetObjectTagging","s3:GetObjectAttributes","s3:ListBucket","s3:ListBucketVersions","s3:ListAllMyBuckets","s3:GetBucketVersioning","s3:GetBucketTagging","s3:GetObjectRetention","s3:GetObjectLegalHold","s3:GetBucketObjectLockConfiguration"],"Resource":["arn:aws:s3:::*"]}]}`,
	"readwrite":    `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::*"]}]}`,
	"writeonly":    `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts","s3:ListBucketMultipartUploads"],"Resource":["arn:aws:s3:::*"]}]}`,
	"diagnostics":  `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["opens3:ServerInfo","opens3:Health","opens3:Metrics"],"Resource":["*"]}]}`,
	"consoleAdmin": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["iam:*","kms:*","sts:*","opens3:*"],"Resource":["*"]},{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::*"]}]}`,
}

// BuiltinPolicyNames lists the built-in policies.
func BuiltinPolicyNames() []string {
	return []string{"readonly", "readwrite", "writeonly", "diagnostics", "consoleAdmin"}
}
