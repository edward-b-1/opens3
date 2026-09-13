# OpenS3 identity, authentication and access control

OpenS3 uses the same identity model, request signing and policy language
as Amazon S3 and MinIO. Clients, SDKs and policy documents written for
either work unchanged. This document explains the model from first
principles, then lists exactly what the server supports.

## 1. Concepts in one page

| Term | What it is | AWS equivalent |
|------|-----------|----------------|
| Access key | A pair of strings: a public *access key ID* and a *secret access key*. A client proves it holds the secret by signing each request; the secret itself is never sent. | IAM access key |
| Root account | The credentials given at startup (`OPENS3_ROOT_USER` / `OPENS3_ROOT_PASSWORD`). Not stored in the database; allowed to do everything. | Account root user |
| User | A named identity that owns access keys, belongs to groups and has policies attached. | IAM user |
| Service account | An extra access key belonging to a user, optionally restricted by an inline *session policy*. Made for applications. | MinIO service account (AWS has no direct equivalent) |
| STS session | Temporary credentials (access key, secret and session token) with an expiry, issued by `AssumeRole`. | STS temporary credentials |
| Group | A named set of users with policies attached; members inherit them. | IAM group |
| Policy | A named JSON document that allows or denies actions on resources. | IAM managed policy |
| Bucket policy | A policy attached to a bucket rather than to an identity. It names the principals it applies to. | S3 bucket policy |
| ACL | A legacy per-bucket / per-object list of grants (READ, WRITE, ...). | S3 ACL |

Two questions are answered for every request:

1. **Authentication: who is calling?** The signature is checked and the
   access key is resolved to an identity (root, a user, or anonymous).
2. **Authorisation: may they do this?** The identity's policies, the
   bucket's policy and the ACLs are evaluated against the requested
   action and resource.

## 2. Authentication

### Credentials, not passwords

The S3 protocol has no username/password login. Every credential is an
access key pair. The root credentials are simply the first such pair: the
value of `OPENS3_ROOT_USER` is an access key ID and `OPENS3_ROOT_PASSWORD`
is its secret. Any S3 client accepts them in its "access key" and "secret
key" fields.

Secrets of users and service accounts must be at least 8 characters. The
root secret is not validated, so choose a long random one in production.

### Request signing

| Scheme | Where | Notes |
|--------|-------|-------|
| AWS Signature Version 4, header form | `Authorization: AWS4-HMAC-SHA256 ...` | The default for every modern SDK. Payload may be signed, `UNSIGNED-PAYLOAD`, or streamed as `aws-chunked` with per-chunk signatures and optional trailing checksums. |
| AWS Signature Version 4, query form (presigned URLs) | `?X-Amz-Algorithm=...&X-Amz-Signature=...` | Lets a URL be handed to someone without credentials. `X-Amz-Expires` is 1 to 604800 seconds (7 days). |
| AWS Signature Version 2 | `Authorization: AWS key:sig` or `?AWSAccessKeyId=...&Signature=...` | Accepted for legacy clients. |
| POST object form upload | Signed policy document in a browser form | Authenticated inside the handler once the form is parsed. |
| Anonymous | No `Authorization` header | Allowed through with no identity; access is then granted only by bucket policy or ACL. |

Signing service is `s3`. Any region is accepted unless the server runs
with `--enforce-region`. Temporary credentials also send their session
token in `x-amz-security-token` (or `X-Amz-Security-Token` in a presigned
URL); the token is compared in constant time and the key must not have
expired.

A signature mismatch returns `403 SignatureDoesNotMatch`; an unknown or
disabled access key returns `403 InvalidAccessKeyId`; an expired STS key
returns `400 ExpiredToken`.

### How an access key becomes an identity

1. If the access key ID equals the root access key, the caller is root.
2. Otherwise the key record is loaded. The key and its owning user must
   both be enabled. STS keys must present a matching session token and
   be unexpired.
3. The user's attached policies and the policies of every enabled group
   the user belongs to are collected (each named policy once). These are
   the *identity policies*.
4. If the key carries a session policy (service accounts and STS
   sessions), it is attached to the identity as a filter.

The identity is described in policy conditions and variables as:

| Value | Root | User |
|-------|------|------|
| `aws:username` | `root` | user name |
| `aws:PrincipalArn` | `arn:aws:iam::<account>:root` | `arn:aws:iam::<account>:user/<name>` |
| `aws:userid` | canonical ID | canonical ID |
| `aws:PrincipalType` | `Account` | `User`, or `AssumedRole` for STS sessions; `Anonymous` when unauthenticated |

The canonical ID is a 64-character hex string derived from the user name
(`sha256("opens3-canonical:" + name)`). It appears as the owner in
`ListBuckets`, in ACL grants and in `CanonicalUser` principals.

### The account ID

OpenS3 is a single-tenant server: there is exactly one account, and all
users, buckets and objects belong to it. The account ID only exists so
that ARNs look like AWS ARNs. It defaults to `000000000000` and can be
set with `OPENS3_ACCOUNT_ID`. `x-amz-expected-bucket-owner` is checked
against the bucket owner's canonical ID and against this account ID.

## 3. Identities in detail

### Root

- Configured with `OPENS3_ROOT_USER` and `OPENS3_ROOT_PASSWORD` (both
  required; the documented development defaults are `opens3admin` /
  `opens3admin`).
- Never stored in the metadata database, so it cannot be locked out by
  deleting or disabling records.
- Allowed to do everything unless a bucket policy explicitly denies it.
  Even then root may still get, put and delete the bucket policy, so a
  bad policy can always be repaired.
- No user may be named `root` or share the root access key ID.

Use root to create the first administrator, then keep it out of daily
use.

### Users

- Names are 1 to 128 characters from `A-Z a-z 0-9 - _ . @ + = ,`.
- Creating a user through the admin API, CLI or console with a secret
  also creates one long-lived access key whose ID equals the user name
  (the MinIO convention). A user may be created with no key and given
  keys later.
- A user has an enabled flag, a list of attached policy names and a list
  of group names.
- Disabling a user rejects every key that belongs to it, including its
  service accounts and STS sessions.
- Deleting a user deletes all of its keys and removes it from its groups.

### Access keys

Every key record has an owning user, a kind, an enabled flag, an optional
description and creation time. The secret is stored encrypted with the
server master key and is returned exactly once, when the key is created
or rotated.

| Kind | Created by | Session policy | Expiry | Session token |
|------|-----------|----------------|--------|---------------|
| `user` | user creation, admin "add key" | no | no | no |
| `service` | admin "add key" with kind service, console "Access keys" | optional | optional | no |
| `sts` | `AssumeRole`, console login | optional (inherits the parent key's) | always, 15 minutes to 7 days | yes |

A user may list, create, rotate and delete its own keys and service
accounts in the console without any admin permission. Managing other
users' keys requires the admin actions listed in section 6.

### Service accounts

A service account is a key of kind `service` for an existing user. It
authenticates as that user and inherits the user's policies. If it
carries a session policy, the effective permission is the intersection of
the user's policies and the session policy: the session policy cannot
grant anything the user does not already have, but it can take
permissions away. Use one per application so each can be rotated or
revoked independently.

### Temporary credentials (STS)

`POST /` with form parameter `Action=AssumeRole` (the endpoint MinIO
uses) issues temporary credentials for the calling identity:

| Parameter | Meaning |
|-----------|---------|
| `DurationSeconds` | 900 to 604800; default 3600 |
| `Policy` | Optional inline session policy (URL-encoded JSON) that narrows the session |
| `RoleSessionName` | Echoed in `AssumedRoleUser`; not otherwise used |
| `RoleArn` | Ignored: there are no roles to switch to (see below) |

The response is the standard `AssumeRoleResponse` XML with
`AccessKeyId`, `SecretAccessKey`, `SessionToken` and `Expiration`. The
session acts as the caller (root or user) with the caller's policies,
narrowed by the session policy. A session created from a service account
that has a session policy keeps that policy. Expired sessions are
rejected on use.

Unlike AWS, `AssumeRole` does not switch to a separately defined role
with its own permissions: OpenS3 has no role objects. It is a way to get
short-lived, optionally narrower credentials for yourself.

### Groups

A group is a name, an enabled flag, a list of member user names and a
list of attached policy names. Members inherit the group's policies.
Disabling a group stops its policies from applying without changing
membership. Deleting a group removes it from every member.

### Named policies

Policies are stored by name. Five built-in policies exist and cannot be
edited or deleted:

| Name | Grants |
|------|--------|
| `readonly` | Read and list on all buckets and objects: `s3:GetBucketLocation`, `s3:GetObject`, `s3:GetObjectVersion`, `s3:GetObjectTagging`, `s3:GetObjectAttributes`, `s3:ListBucket`, `s3:ListBucketVersions`, `s3:ListAllMyBuckets`, `s3:GetBucketVersioning`, `s3:GetBucketTagging`, `s3:GetObjectRetention`, `s3:GetObjectLegalHold`, `s3:GetBucketObjectLockConfiguration` |
| `readwrite` | `s3:*` on all buckets and objects |
| `writeonly` | `s3:PutObject`, `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts`, `s3:ListBucketMultipartUploads` on all buckets |
| `diagnostics` | `admin:ServerInfo`, `admin:Prometheus`, `admin:Health`, `admin:Metrics` |
| `consoleAdmin` | `admin:*` and `s3:*`: a full administrator |

The names match MinIO's so that migrated users and scripts keep working.
Deleting a custom policy detaches it from every user and group.

## 4. Authorisation

### Decision order

Every S3 operation is mapped to one IAM action (section 5). The decision
for a request is:

1. **Explicit deny wins.** A matching `Deny` statement in the bucket
   policy, in any identity policy or in the session policy rejects the
   request. Root is only subject to bucket-policy denies, and may always
   manage the bucket policy itself.
2. **Root is allowed.**
3. **Session policy filter.** If the key has a session policy, that
   policy must itself allow the action; otherwise the request is denied
   regardless of what identity or bucket policies say.
4. **Bucket policy allow** grants the request.
5. **Identity policy allow** (user or group policy) grants the request.
6. **ACL grants** are consulted last (section 4.3).
7. Otherwise the request is denied with `403 AccessDenied`.

`admin:*` actions are granted only by identity policies. Bucket policies
and ACLs never grant them.

Three operations are special:

- `ListBuckets` is allowed for every authenticated identity; the result
  is filtered to buckets the caller may list or that it owns.
- `CreateBucket` requires an authenticated identity with an identity
  policy allowing `s3:CreateBucket` (root always may).
- A `GetObject` or `HeadObject` on a missing key returns `404 NoSuchKey`
  instead of `403` when the caller could list the bucket with that key
  as prefix, matching AWS.

### Block Public Access

Each bucket's public-access-block settings are honoured:
`RestrictPublicBuckets` makes public bucket-policy statements apply only
to authenticated principals, and `IgnorePublicAcls` ignores ACL grants to
`AllUsers` and `AuthenticatedUsers`. `GetBucketPolicyStatus` reports a
policy as public when it allows something to principal `*` without a
limiting condition on source IP, principal or user ID.

### ACLs

ACLs are the original S3 permission system and are evaluated only when no
policy decided the request. A bucket and every object each have an owner
(canonical ID) and a list of grants:

| Grantee | Matches |
|---------|---------|
| Canonical user ID | that user |
| Group `http://acs.amazonaws.com/groups/global/AllUsers` | everyone, including anonymous |
| Group `http://acs.amazonaws.com/groups/global/AuthenticatedUsers` | any authenticated identity |

| Permission | On a bucket allows | On an object allows |
|------------|--------------------|---------------------|
| `READ` | list objects, versions and multipart uploads; get location | get the object, its tags, attributes, retention, legal hold; list its parts |
| `WRITE` | put, delete and tag objects; abort uploads | (not used) |
| `READ_ACP` | read the bucket ACL | read the object ACL |
| `WRITE_ACP` | write the bucket ACL | write the object ACL |
| `FULL_CONTROL` | all of the above | all of the above |

The owner of a bucket or object always has full control of it. Every
other bucket-configuration operation requires the bucket owner when
decided by ACL.

Canned ACLs `private`, `public-read`, `public-read-write`,
`authenticated-read`, `bucket-owner-read` and
`bucket-owner-full-control` are accepted on bucket and object creation,
as are explicit `x-amz-grant-*` headers and a full ACL body.

Object Ownership controls ACL behaviour per bucket, as on AWS:
`BucketOwnerEnforced` disables ACLs entirely (only `private` /
`bucket-owner-full-control` may be sent, and only the owner has ACL
access); `BucketOwnerPreferred` and `ObjectWriter` keep ACLs active.
The server default is set with `OPENS3_DEFAULT_OBJECT_OWNERSHIP`.

Prefer policies over ACLs for anything new; ACLs remain for
compatibility with clients that use them.

## 5. The policy language

Policies are JSON documents in the AWS IAM policy grammar.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "HomeDirectory",
      "Effect": "Allow",
      "Action": ["s3:ListBucket"],
      "Resource": ["arn:aws:s3:::shared"],
      "Condition": {"StringLike": {"s3:prefix": ["${aws:username}/*"]}}
    },
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      "Resource": ["arn:aws:s3:::shared/${aws:username}/*"]
    },
    {
      "Effect": "Deny",
      "Action": "s3:*",
      "Resource": "*",
      "Condition": {"Bool": {"aws:SecureTransport": "false"}}
    }
  ]
}
```

### Elements

| Element | Supported |
|---------|-----------|
| `Version`, `Id`, `Sid` | Accepted; `Version` is stored but not enforced |
| `Effect` | `Allow` or `Deny` |
| `Action`, `NotAction` | String or list. Wildcards `*` and `?`; matched case-insensitively. `s3:*`, `admin:*` and `*` are valid |
| `Resource`, `NotResource` | String or list of ARNs `arn:aws:s3:::bucket` or `arn:aws:s3:::bucket/key`. Wildcards and policy variables allowed |
| `Principal`, `NotPrincipal` | Required in bucket policies, forbidden in identity policies. Forms: `"*"`, `{"AWS": "*"}`, `{"AWS": "arn:aws:iam::<account>:user/<name>"}` (wildcards allowed), `{"AWS": ["..."]}`, `{"CanonicalUser": "<canonical id>"}`, `{"Federated": "..."}`, `{"Service": "..."}` (accepted, never matches) |
| `Condition` | Map of operator to map of key to value(s); see below |

Bucket policies are validated on `PutBucketPolicy`: every statement needs
a principal, every resource must belong to the bucket, and actions must
be `s3:` actions.

### Condition operators

All standard operators, each optionally suffixed with `IfExists` and
optionally prefixed with `ForAnyValue:` or `ForAllValues:`.

| Family | Operators |
|--------|-----------|
| String | `StringEquals`, `StringNotEquals`, `StringEqualsIgnoreCase`, `StringNotEqualsIgnoreCase`, `StringLike`, `StringNotLike` |
| Numeric | `NumericEquals`, `NumericNotEquals`, `NumericLessThan`, `NumericLessThanEquals`, `NumericGreaterThan`, `NumericGreaterThanEquals` |
| Date | `DateEquals`, `DateNotEquals`, `DateLessThan`, `DateLessThanEquals`, `DateGreaterThan`, `DateGreaterThanEquals` (RFC 3339 or epoch seconds) |
| Boolean | `Bool` |
| Binary | `BinaryEquals` |
| IP | `IpAddress`, `NotIpAddress` (single address or CIDR, IPv4 and IPv6) |
| ARN | `ArnEquals`, `ArnLike`, `ArnNotEquals`, `ArnNotLike` |
| Existence | `Null` |

A missing key fails the condition unless the operator has `IfExists`, is
`Null`, or is prefixed with `ForAllValues:`. Multiple keys under one
operator are ANDed; multiple values for one key are ORed (negated
operators use AND). Values may contain policy variables.

### Condition keys

Global keys, available on every request:

| Key | Value |
|-----|-------|
| `aws:SourceIp` | Client IP taken from the TCP connection (`X-Forwarded-For` is not yet trusted) |
| `aws:SecureTransport` | `true` over TLS or when `X-Forwarded-Proto: https` |
| `aws:CurrentTime`, `aws:EpochTime` | Request time |
| `aws:Referer`, `aws:UserAgent` | Request headers |
| `aws:username`, `aws:userid`, `aws:PrincipalArn`, `aws:PrincipalType` | Caller identity (section 2) |
| `aws:ResourceTag/<key>` | Tags of the bucket being accessed |

S3 keys, present when the request carries the corresponding parameter:

| Key | Source |
|-----|--------|
| `s3:prefix`, `s3:delimiter`, `s3:max-keys`, `s3:versionid` | Query parameters of list and versioned operations |
| `s3:authType` | `REST-HEADER`, `REST-QUERY-STRING` or `POST` |
| `s3:signatureversion` | `AWS4-HMAC-SHA256` or `AWS` |
| `s3:signatureAge` | Milliseconds since the request was signed |
| `s3:TlsVersion` | e.g. `1.3` |
| `s3:x-amz-acl`, `s3:x-amz-grant-read`, `s3:x-amz-grant-write`, `s3:x-amz-grant-read-acp`, `s3:x-amz-grant-write-acp`, `s3:x-amz-grant-full-control` | ACL headers on PUT |
| `s3:x-amz-storage-class`, `s3:x-amz-website-redirect-location`, `s3:x-amz-metadata-directive`, `s3:x-amz-copy-source` | Object headers on PUT / copy |
| `s3:x-amz-server-side-encryption`, `s3:x-amz-server-side-encryption-aws-kms-key-id`, `s3:x-amz-server-side-encryption-customer-algorithm` | Encryption headers |
| `s3:object-lock-mode`, `s3:object-lock-retain-until-date`, `s3:object-lock-remaining-retention-days`, `s3:object-lock-legal-hold` | Object Lock headers |
| `s3:if-match`, `s3:if-none-match` | Conditional request headers |
| `s3:RequestObjectTag/<key>` | Tags in `x-amz-tagging` on PUT |
| `s3:ExistingObjectTag/<key>` | Tags of the object being accessed |

Condition key names are matched case-insensitively.

### Policy variables

`${aws:username}`, `${aws:userid}` and `${aws:PrincipalType}` may appear
in `Resource`, `NotResource` and condition values. `${*}`, `${?}` and
`${$}` produce the literal characters.

### Supported S3 actions

Each S3 operation maps to exactly one action. Actions not listed are not
checked because the corresponding operation is not implemented (see
`docs/API-COVERAGE.md`).

Bucket actions (resource `arn:aws:s3:::bucket`):

| Action | Operations |
|--------|-----------|
| `s3:ListAllMyBuckets` | ListBuckets (always allowed when authenticated; results filtered) |
| `s3:CreateBucket` | CreateBucket |
| `s3:DeleteBucket` | DeleteBucket |
| `s3:ListBucket` | ListObjects, ListObjectsV2, HeadBucket (conditions `s3:prefix`, `s3:delimiter`, `s3:max-keys`) |
| `s3:ListBucketVersions` | ListObjectVersions |
| `s3:ListBucketMultipartUploads` | ListMultipartUploads |
| `s3:GetBucketLocation` | GetBucketLocation |
| `s3:GetBucketAcl`, `s3:PutBucketAcl` | Bucket ACL |
| `s3:GetBucketPolicy`, `s3:PutBucketPolicy`, `s3:DeleteBucketPolicy`, `s3:GetBucketPolicyStatus` | Bucket policy |
| `s3:GetBucketVersioning`, `s3:PutBucketVersioning` | Versioning |
| `s3:GetBucketTagging`, `s3:PutBucketTagging` | Bucket tags (delete uses `s3:PutBucketTagging`) |
| `s3:GetBucketCORS`, `s3:PutBucketCORS` | CORS |
| `s3:GetLifecycleConfiguration`, `s3:PutLifecycleConfiguration` | Lifecycle |
| `s3:GetEncryptionConfiguration`, `s3:PutEncryptionConfiguration` | Default encryption |
| `s3:GetBucketObjectLockConfiguration`, `s3:PutBucketObjectLockConfiguration` | Object Lock |
| `s3:GetBucketNotification`, `s3:PutBucketNotification` | Event notifications |
| `s3:GetBucketWebsite`, `s3:PutBucketWebsite`, `s3:DeleteBucketWebsite` | Static website |
| `s3:GetBucketLogging`, `s3:PutBucketLogging` | Access logging |
| `s3:GetReplicationConfiguration`, `s3:PutReplicationConfiguration` | Replication |
| `s3:GetBucketPublicAccessBlock`, `s3:PutBucketPublicAccessBlock` | Block Public Access |
| `s3:GetBucketOwnershipControls`, `s3:PutBucketOwnershipControls` | Object Ownership |
| `s3:GetBucketRequestPayment`, `s3:PutBucketRequestPayment` | Request payment |
| `s3:GetAccelerateConfiguration`, `s3:PutAccelerateConfiguration` | Transfer acceleration |
| `s3:GetMetricsConfiguration`, `s3:PutMetricsConfiguration` | Metrics configuration |
| `s3:GetAnalyticsConfiguration`, `s3:PutAnalyticsConfiguration` | Analytics configuration |
| `s3:GetInventoryConfiguration`, `s3:PutInventoryConfiguration` | Inventory configuration |
| `s3:GetIntelligentTieringConfiguration`, `s3:PutIntelligentTieringConfiguration` | Intelligent-Tiering configuration |

Object actions (resource `arn:aws:s3:::bucket/key`):

| Action | Operations |
|--------|-----------|
| `s3:GetObject` | GetObject, HeadObject, SelectObjectContent (checked, then answered `NotImplemented`) |
| `s3:GetObjectVersion` | GetObject / HeadObject with `versionId` |
| `s3:PutObject` | PutObject, CopyObject (destination), CreateMultipartUpload, UploadPart, UploadPartCopy, CompleteMultipartUpload, POST object |
| `s3:DeleteObject` | DeleteObject, DeleteObjects |
| `s3:DeleteObjectVersion` | DeleteObject with `versionId` |
| `s3:AbortMultipartUpload` | AbortMultipartUpload |
| `s3:ListMultipartUploadParts` | ListParts |
| `s3:GetObjectAcl`, `s3:PutObjectAcl` | Object ACL |
| `s3:GetObjectVersionAcl`, `s3:PutObjectVersionAcl` | Object ACL with `versionId` |
| `s3:GetObjectTagging`, `s3:PutObjectTagging`, `s3:DeleteObjectTagging` | Object tags |
| `s3:GetObjectVersionTagging`, `s3:PutObjectVersionTagging`, `s3:DeleteObjectVersionTagging` | Object tags with `versionId` |
| `s3:GetObjectAttributes` | GetObjectAttributes |
| `s3:GetObjectRetention`, `s3:PutObjectRetention` | Object Lock retention |
| `s3:GetObjectLegalHold`, `s3:PutObjectLegalHold` | Object Lock legal hold |
| `s3:BypassGovernanceRetention` | Required in addition when `x-amz-bypass-governance-retention: true` is sent |
| `s3:RestoreObject` | RestoreObject |
| `s3:GetObjectTorrent` | GetObjectTorrent (checked, then answered `NotImplemented`) |

Copying an object also requires `s3:GetObject` (or `s3:GetObjectVersion`)
on the source.

## 6. Admin actions

Management operations are IAM actions in the `admin:` namespace and are
granted only by identity policies. The admin REST API and `opens3 admin`
CLI check these (see `docs/ADMIN.md` for the endpoints):

| Group | Actions |
|-------|---------|
| Server | `admin:ServerInfo`, `admin:Health`, `admin:Prometheus`, `admin:Metrics` |
| Users | `admin:ListUsers`, `admin:GetUser`, `admin:AddUser`, `admin:RemoveUser`, `admin:EnableUser`, `admin:DisableUser`, `admin:SetUserPolicies` |
| Keys | `admin:ListKeys`, `admin:GetKey`, `admin:AddKey`, `admin:RemoveKey`, `admin:EnableKey`, `admin:DisableKey`, `admin:RotateKey` |
| Groups | `admin:ListGroups`, `admin:GetGroup`, `admin:AddGroup`, `admin:RemoveGroup`, `admin:UpdateGroupMembers` |
| Policies | `admin:ListPolicies`, `admin:GetPolicy`, `admin:AddPolicy`, `admin:RemovePolicy` |
| Buckets | `admin:ListBuckets`, `admin:RemoveBucket` |
| KMS | `admin:ListKMSKeys`, `admin:CreateKMSKey`, `admin:DeleteKMSKey` |

The web console checks its own set of admin actions for the same screens:

| Screen | Actions |
|--------|---------|
| Status | `admin:ServerInfo` |
| Users | `admin:ListUsers`, `admin:GetUser`, `admin:CreateUser`, `admin:UpdateUser`, `admin:DeleteUser` |
| Access keys (other users') | `admin:ListKeys`, `admin:CreateKey`, `admin:UpdateKey`, `admin:DeleteKey` |
| Groups | `admin:ListGroups`, `admin:CreateGroup`, `admin:UpdateGroup`, `admin:DeleteGroup` |
| Policies | `admin:ListPolicies`, `admin:GetPolicy`, `admin:PutPolicy`, `admin:DeletePolicy` |
| Encryption keys | `admin:ListKMSKeys`, `admin:CreateKMSKey`, `admin:DeleteKMSKey` |

A policy that grants `admin:*` covers both. A narrower operator policy
must list the actions of each surface the operator will use.

## 7. Where things are stored

All identity records live in the metadata database under the `i/`
namespace (`i/u/<name>`, `i/k/<accessKey>`, `i/g/<name>`, `i/p/<name>`). Secret keys are encrypted with the server master key
(`OPENS3_MASTER_KEY`), so the master key must be backed up alongside the
data; see `docs/FORMAT.md`.

## 8. Recipes

**Bootstrap an administrator, then stop using root**

```sh
opens3 admin user add alice --secret 'a-long-random-secret' --policy consoleAdmin
```

**A read-only reporting user**

```sh
opens3 admin user add reports --secret 'another-long-secret' --policy readonly
```

**An application key that can only write to one bucket**

Create a service account for a user with a session policy; it can do no
more than the user, and no more than the policy:

```sh
cat > uploader.json <<'JSON'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
  "Action":["s3:PutObject","s3:AbortMultipartUpload","s3:ListMultipartUploadParts"],
  "Resource":["arn:aws:s3:::uploads/*"]}]}
JSON
opens3 admin key add alice --service --policy-file uploader.json --description uploader
```

**Public read of one bucket (bucket policy)**

```json
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*",
  "Action":["s3:GetObject"],"Resource":["arn:aws:s3:::www/*"]}]}
```

**Per-user home directories in a shared bucket**

See the example in section 5: `s3:ListBucket` limited by `s3:prefix`
plus object actions on `arn:aws:s3:::shared/${aws:username}/*`.

## 9. Differences from AWS IAM

- One account, no cross-account access, no organisations.
- No IAM roles. `AssumeRole` returns temporary credentials for the
  caller, optionally narrowed; `RoleArn` is ignored.
- No federation yet: OIDC and LDAP identity providers are planned
  (`docs/PLAN.md`, phase 3). Every caller needs a key in this store.
- No permission boundaries, service-control policies or resource-based
  policies other than bucket policies and ACLs.
- Console login uses an access key and secret, because those are the
  only credentials that exist.
