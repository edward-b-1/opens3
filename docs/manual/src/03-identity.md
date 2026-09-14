# 3. Identity and access

OpenS3 follows the AWS model: the same identities, the same request
signing (Signature Version 4) and the same policy language, so the AWS
CLI, the AWS SDKs and policies written for AWS work unchanged.

## The pieces

| Thing | What it is for |
|---|---|
| **Root account** | The user name and password from `OPENS3_ROOT_USER` / `OPENS3_ROOT_PASSWORD`. Full access to everything. Signs in to the console with that name and password; used as an S3 access key pair only for bootstrapping. Keep it for administration. |
| **User** | An identity with a name. Permissions come from the policies attached to it and to its groups. |
| **Access key** | A generated 20-character key ID and 40-character secret. Authenticates S3, IAM and STS requests with Signature Version 4. A user may hold several; keys can be deactivated, given an expiry, and deleted. The secret is shown once, at creation. |
| **Console password** | Optional, per user. Signs in to the web console only; never authenticates an API request. AWS calls it a login profile. |
| **Group** | A named set of users with its own policies; a user's permissions are the union of their own and their groups'. |
| **Policy** | An AWS policy document (JSON). Built-in ones are `readonly`, `readwrite`, `writeonly`, `diagnostics` and `consoleAdmin`. |
| **Temporary credentials** | Issued by STS `AssumeRole`: a key pair plus a session token, expiring after 15 minutes to 7 days, optionally narrowed by a session policy. Console sessions are these. |

Credentials narrowed by a session policy cannot create access keys or set
console passwords, for their own user or anyone else, because the result
would carry none of the restriction. A session derived from such
credentials with a further `AssumeRole` keeps every restriction of its
parents and can only narrow them.

A key ID may also be any string of three or more characters chosen by an
administrator through the admin API or CLI. That exists only to migrate
MinIO-style credentials, where the user name is the key; the console never
creates such keys.

## Creating users

With the AWS CLI (chapter 1 shows the environment):

```sh
aws iam create-user --user-name alice
aws iam create-access-key --user-name alice
aws iam create-login-profile --user-name alice --password 'a long passphrase'
aws iam attach-user-policy --user-name alice --policy-arn arn:aws:iam::aws:policy/readonly
```

With the console: Identity, Users, Create user. The dialog takes the name,
policies and an optional console password, and shows the generated key
pair once.

With the bundled CLI: `opens3 admin user add alice --policy readonly --password '...'`
(chapter 8).

## Writing policies

Policies use the AWS language: `Version`, `Statement`, `Effect`, `Action`,
`Resource`, `Condition`, and `Principal` in bucket policies. Actions are
AWS's names: `s3:GetObject`, `s3:ListBucket`, `s3:PutObject`, and for
administration `iam:CreateUser`, `iam:CreateAccessKey`, `kms:CreateKey`,
`sts:AssumeRole`. Three OpenS3-specific actions cover what AWS has no
equivalent for: `opens3:ServerInfo`, `opens3:Health`, `opens3:Metrics`.
MinIO's `admin:*` names are rejected with the AWS name in the error.

A policy granting read access to one prefix of one bucket:

```json
{"Version":"2012-10-17","Statement":[
  {"Effect":"Allow","Action":["s3:ListBucket"],"Resource":"arn:aws:s3:::reports",
   "Condition":{"StringLike":{"s3:prefix":["2026/*"]}}},
  {"Effect":"Allow","Action":["s3:GetObject"],"Resource":"arn:aws:s3:::reports/2026/*"}
]}
```

Create it and attach it:

```sh
aws iam create-policy --policy-name reports-2026 --policy-document file://p.json
aws iam attach-user-policy --user-name alice --policy-arn arn:aws:iam::000000000000:policy/reports-2026
```

How a request is decided, in order: an explicit `Deny` anywhere wins; then
the bucket policy; then the user's identity policies; then ACLs where the
bucket allows them; otherwise denied. Administrative actions (`iam:`,
`kms:`, `sts:`, `opens3:`) come only from identity policies. Root is allowed
everything except what a bucket policy explicitly denies, and can always
change a bucket policy unless the owner confirmed the lock-out (chapter 4).

## Bucket policies and anonymous access

A bucket policy lives on the bucket and names principals. `"Principal":
"*"` includes anonymous requests, which is how a bucket is made public:

```sh
aws s3api put-bucket-policy --bucket pub --policy '{"Version":"2012-10-17","Statement":[
  {"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::pub/*"}]}'
```

`aws s3api get-bucket-policy-status` reports whether a policy is public.
Block Public Access on the bucket (chapter 4) can forbid public policies and
public ACLs regardless of what is written.

## Temporary credentials

```sh
aws sts assume-role --role-arn arn:aws:iam::000000000000:role/any --role-session-name job \
  --duration-seconds 3600 --policy file://narrow.json
```

`RoleArn` is accepted and ignored: the credentials are for the caller,
narrowed by the optional policy. Use the returned key, secret and session
token together; a session token with the wrong key is rejected. Expired
sessions are refused on use and swept hourly.

## The IAM commands that work

Everything below is available through `aws iam`, boto3's `iam` client and
Terraform. Roles, MFA, identity providers, server certificates and policy
versions beyond `v1` are not part of OpenS3 and return `InvalidAction`.

| Area | Commands |
|---|---|
| Users | create-user, get-user, list-users, update-user (no renaming), delete-user |
| Access keys | create-access-key, list-access-keys, update-access-key (Active/Inactive), delete-access-key |
| Console passwords | create-login-profile, get-login-profile, update-login-profile, delete-login-profile, change-password |
| Groups | create-group, get-group, list-groups, delete-group, add-user-to-group, remove-user-from-group, list-groups-for-user |
| Policies | create-policy, get-policy, get-policy-version, list-policies (`--scope AWS` lists the built-in ones), delete-policy |
| Attachments | attach-user-policy, detach-user-policy, list-attached-user-policies, attach-group-policy, detach-group-policy, list-attached-group-policies |
| Account | get-account-summary; `aws sts get-caller-identity`, `aws sts assume-role` |

Policy ARNs are `arn:aws:iam::<account>:policy/<name>` for policies you
create and `arn:aws:iam::aws:policy/<name>` for the built-in ones; a bare
name is accepted wherever an ARN is expected. Deleting follows AWS's
order: a user with keys, a console password, group memberships or attached
policies, and a policy that is attached, return `DeleteConflict` until
those are removed.

The built-in policies:

| Name | Grants |
|---|---|
| `readonly` | Get, head and list on every bucket and object |
| `readwrite` | Every `s3:` action on every bucket |
| `writeonly` | Put objects and manage multipart uploads, no reads |
| `diagnostics` | Server info, health and metrics |
| `consoleAdmin` | Every `s3:`, `iam:`, `kms:`, `sts:` and `opens3:` action: a full administrator |

## Users managing themselves

Any user may read their own user record, list and create their own access
keys, change their own console password (`aws iam change-password`, or the
console's Change password button) and see their attached policies, without
administrative permissions.
