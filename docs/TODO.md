# TODO

Open items agreed with the maintainer, worked through one at a time.
Move an item to "Done" with the commit that closed it.

## Open

1. **Identity model: move to the AWS model (decided 13 Sep 2026).**
   - API credentials are generated key pairs: a 20-character key ID and a
     40-character secret, shown once, no prefix. The web UI must never let
     a user type either value (requested 13 Sep 2026); the admin API and
     CLI keep accepting a chosen key ID of three or more characters solely
     for migrating MinIO-style credentials, and any existing key keeps
     working. Users may hold several keys.
   - Console sign-in accepts a user name and console password only; key
     pairs are refused there with a message pointing at the API. The root
     account signs in with the root user name and root password from the
     environment.
   - A console password is optional per user: set in the create-user
     dialog or by an administrator later; users can change their own.
     Stored as PBKDF2-SHA256 with a per-user salt (standard library); never
     usable against the S3 API.
   - The create-user dialog and `opens3 admin user add` follow this: name,
     policies, optional console password, generated key pair (or a chosen
     one for MinIO-style migration).
   - Later, not now: MFA and password rules for console passwords.
2. **Administrative API: adopt the AWS interface and vocabulary (decided
   13 Sep 2026).** Implement the AWS IAM Query API (the protocol behind
   `aws iam ...` and boto3's `iam` client: SigV4-signed POST with
   `Action=CreateUser&Version=2010-05-08`, XML responses) as the canonical
   administrative interface, so the AWS CLI and SDKs manage users, access
   keys, groups, policies and console passwords ("login profiles") against
   OpenS3 unchanged. Action names in policies become AWS's (`iam:*`,
   `kms:*`, `sts:*`); the MinIO `admin:*` names are removed and a policy
   naming one is rejected with the AWS equivalent in the error. The
   `opens3 admin` CLI and the console move onto the same vocabulary. The
   current `/opens3/admin/v1` JSON API stays only for what AWS has no
   equivalent for (server info, forced bucket deletion, KMS key management
   until a KMS-protocol subset exists).
   Initial operation set: CreateUser, GetUser, ListUsers, DeleteUser,
   CreateAccessKey, ListAccessKeys, UpdateAccessKey, DeleteAccessKey,
   CreateGroup, GetGroup, ListGroups, DeleteGroup, AddUserToGroup,
   RemoveUserFromGroup, ListGroupsForUser, CreatePolicy, GetPolicy,
   GetPolicyVersion, ListPolicies, DeletePolicy, AttachUserPolicy,
   DetachUserPolicy, ListAttachedUserPolicies, AttachGroupPolicy,
   DetachGroupPolicy, ListAttachedGroupPolicies, CreateLoginProfile,
   UpdateLoginProfile, DeleteLoginProfile, GetLoginProfile,
   GetAccountSummary. Named policies get ARNs of the form
   `arn:aws:iam::<account>:policy/<name>`.

## Deferred

- **MinIO admin API compatibility.** Serve MinIO's admin protocol under
  `/minio/admin/v3/` with MinIO's names and conventions so that `mc admin`
  and `madmin-go` scripts work unchanged against OpenS3. Part of the MinIO
  migration work, which is deferred; do not start without agreement.

## Background for item 2

- Wire compatibility is unaffected: S3 clients only ever present a key ID
  and a secret; user names never go over the wire. MinIO's "user name and
  password" are a key ID and secret under another name.
- AWS: a user is an identity; it authenticates to the API with generated
  access keys and to the console with a separate password (+MFA); the two
  are never interchangeable. MinIO: the user name is the key ID and the
  console accepts only the key pair.
- Refusing key pairs at the console costs one administrative step per
  console user (setting a password) and buys the AWS separation: a leaked
  programmatic key cannot be used in a browser.

## Done

- Purge expired temporary credentials: hourly sweep and on startup
  (`OPENS3_PURGE_INTERVAL`), metric `opens3_iam_expired_credentials_purged_total`.
