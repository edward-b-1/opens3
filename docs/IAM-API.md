# IAM API

OpenS3 serves the AWS IAM Query API on the same endpoint as S3, so the AWS
CLI, the AWS SDKs and Terraform manage users, access keys, groups, policies
and console passwords without OpenS3-specific tooling:

```sh
export AWS_ACCESS_KEY_ID=$OPENS3_ROOT_USER AWS_SECRET_ACCESS_KEY=$OPENS3_ROOT_PASSWORD
aws --endpoint-url http://localhost:9000 sts get-caller-identity
aws --endpoint-url http://localhost:9000 iam create-user --user-name alice
aws --endpoint-url http://localhost:9000 iam create-access-key --user-name alice
aws --endpoint-url http://localhost:9000 iam create-login-profile --user-name alice --password 'correct horse battery'
aws --endpoint-url http://localhost:9000 iam create-policy --policy-name readers --policy-document file://readers.json
aws --endpoint-url http://localhost:9000 iam attach-user-policy --user-name alice \
    --policy-arn arn:aws:iam::000000000000:policy/readers
```

Requests are SigV4-signed POSTs to `/` with form-encoded `Action=...`
parameters; responses are the AWS XML shapes. The account ID is
`OPENS3_ACCOUNT_ID` (default `000000000000`).

## Model

- **Users** are identities. The root account is not a user; `GetUser`
  without a name returns it as `arn:aws:iam::<account>:root`.
- **Access keys** are generated (20-character ID, 40-character secret,
  shown once) and authenticate the S3 and IAM APIs. `UpdateAccessKey` with
  `Status=Inactive` disables a key immediately.
- **Login profiles** are console passwords. They sign in to the web console
  only and never authenticate an API request. `ChangePassword` lets a user
  change their own.
- **Policies** are AWS policy documents. Locally created ones have ARNs
  `arn:aws:iam::<account>:policy/<name>`; the built-in `readonly`,
  `readwrite`, `writeonly`, `diagnostics` and `consoleAdmin` policies appear
  as AWS-managed (`arn:aws:iam::aws:policy/<name>`) and cannot be deleted.
  A bare policy name is accepted wherever an ARN is expected.
- **Groups** carry policies and members; a user's effective permissions
  are the union of the policies attached to the user and to its groups.
- Deletion follows AWS: a user with access keys, a login profile, group
  memberships or attached policies, a group with members or policies, and an
  attached policy all return `DeleteConflict` until cleaned up.
- Administrative actions use AWS names: `iam:*`, `kms:*`, `sts:*`, and
  `opens3:*` for the few operations AWS has no equivalent for
  (`opens3:ServerInfo`, `opens3:Health`, `opens3:Metrics`). MinIO's
  `admin:*` names are rejected with the AWS equivalent in the error.
  Administrative permissions come only from identity policies, never from
  bucket policies or ACLs. Users may always read their own user record,
  keys, groups and attached policies, and manage their own keys.

## Supported actions

| Action | Notes |
|---|---|
| CreateUser, GetUser, ListUsers, UpdateUser, DeleteUser | `UpdateUser` accepts but does not rename; `Path` is always `/`; tags are ignored |
| CreateAccessKey, ListAccessKeys, UpdateAccessKey, DeleteAccessKey | root cannot own keys (its credentials come from the configuration) |
| CreateGroup, GetGroup, ListGroups, DeleteGroup, AddUserToGroup, RemoveUserFromGroup, ListGroupsForUser | |
| CreatePolicy, GetPolicy, GetPolicyVersion, ListPolicies, DeletePolicy | single version `v1`; `GetPolicyVersion` returns the document URL-encoded as AWS does; `Scope`, `OnlyAttached`, `Marker`, `MaxItems` honoured |
| AttachUserPolicy, DetachUserPolicy, ListAttachedUserPolicies, AttachGroupPolicy, DetachGroupPolicy, ListAttachedGroupPolicies | |
| CreateLoginProfile, GetLoginProfile, UpdateLoginProfile, DeleteLoginProfile, ChangePassword | passwords must be at least 8 characters |
| GetAccountSummary | counts and fixed quotas |
| STS: AssumeRole, GetCallerIdentity | `AssumeRole` ignores `RoleArn` and issues temporary credentials for the caller, optionally narrowed by `Policy` |

Unsupported actions (roles, inline policies, MFA, SAML/OIDC providers,
server certificates, service-specific credentials, tags, account aliases,
password policies, policy versions beyond `v1`) return `InvalidAction`.
The `/opens3/admin/v1` JSON API remains for what AWS has no equivalent
for: server info, forced bucket deletion and KMS key management.
