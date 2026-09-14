# OpenS3 administration API and CLI

OpenS3 is managed through a small JSON REST API mounted on the same
listener as the S3 API, under `/opens3/admin/v1/`, and through the
`opens3 admin` command that wraps it. The concepts (users, access keys /
service accounts, groups, named policies, KMS keys) mirror MinIO's admin
API so tooling written for `mc admin` maps one-to-one.

## Authentication and authorisation

Every admin request must be signed with AWS Signature Version 4 (header
or presigned query form, service `s3`, any region unless the server runs
with `--enforce-region`). The credentials are ordinary OpenS3
credentials: the root account, an IAM user key, a service account or an
STS session. Anonymous requests get `403 AccessDenied`.

Requests with a body must sign the payload: `x-amz-content-sha256` has to
be the hex SHA-256 of the body. `UNSIGNED-PAYLOAD` is only accepted for
empty bodies; a mismatching hash is rejected with
`400 XAmzContentSHA256Mismatch` before the operation runs.

Each endpoint is authorised by an AWS-style action (`iam:*`, `kms:*`, `s3:*`, `opens3:*` for operations AWS has no equivalent for; listed in the
table below). The root account may call everything. Other identities need
an identity policy (attached to the user or one of its groups) that allows
the action; bucket policies and ACLs never grant admin actions. Two
built-in policies cover the common cases:

- `consoleAdmin` — `iam:*, kms:*, sts:*, opens3:*` plus `s3:*` (full administrator)
- `diagnostics` — `opens3:ServerInfo`, `opens3:Health`, `opens3:Metrics`, `opens3:Metrics`

A custom policy can grant a subset, e.g. a user-management-only operator:

```json
{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
  "Action":["iam:ListUsers","iam:GetUser","iam:CreateUser","iam:DeleteUser",
            "iam:UpdateUser","iam:UpdateUser","iam:AttachUserPolicy",
            "iam:ListAccessKeys","iam:CreateAccessKey","iam:DeleteAccessKey","iam:UpdateAccessKey"],
  "Resource":["arn:aws:s3:::*"]}]}
```

## Conventions

- Request and response bodies are JSON (`Content-Type: application/json`).
- Errors are `{"code": "...", "message": "..."}` with the HTTP status:
  `400` invalid input (`InvalidArgument`, `InvalidRequest`,
  `XAmzContentSHA256Mismatch`), `403` authentication or authorisation
  failure (`AccessDenied`, `InvalidAccessKeyId`, `SignatureDoesNotMatch`,
  ...), `404 NotFound`, `409 AlreadyExists` / `BucketNotEmpty`,
  `413 EntityTooLarge`, `500 InternalError`.
- Mutations that have nothing to return answer `{"status":"ok"}`.
- Secret keys are returned exactly once: in the response of key creation,
  key rotation and never anywhere else (user, key and group records omit
  them).
- Timestamps are RFC 3339 in UTC.

## Endpoints

| Method and path | Action | Description |
|---|---|---|
| `GET info` | `opens3:ServerInfo` | Version, region, start time, uptime, bucket count, object count and bytes (computed by scanning metadata), disk stats. |
| `GET health` | `opens3:Health` | Readiness (same check as `/opens3/health/ready`); `{"status":"ok"}`. |
| `GET users` | `iam:ListUsers` | All users. |
| `GET users/{name}` | `iam:GetUser` | One user: `name, enabled, policies[], groups[], created`. |
| `PUT users/{name}` | `iam:CreateUser` | Create or update. Body `{"secret_key"?, "policies"?}`. On creation a key with access key = user name is created when `secret_key` is given (MinIO convention). On update `secret_key` rotates/creates that key; `policies` (when present) replaces the attached policies. |
| `DELETE users/{name}` | `iam:DeleteUser` | Delete the user, all its keys and group memberships. |
| `POST users/{name}/enable` | `iam:UpdateUser` | Enable; returns the user. |
| `POST users/{name}/disable` | `iam:UpdateUser` | Disable (all its keys stop working). |
| `PUT users/{name}/policies` | `iam:AttachUserPolicy` | Body `{"policies":[...]}` replaces the attached policies. |
| `GET keys?user=` | `iam:ListAccessKeys` | Access keys (all, or of one user), including STS sessions (`kind: sts`). |
| `GET keys/{ak}` | `iam:ListAccessKeys` | One key record. |
| `POST keys` | `iam:CreateAccessKey` | Body `{"user", "access_key"?, "secret_key"?, "kind": "user"\|"service", "session_policy"?, "expires"?, "description"?}`. Empty access/secret keys are generated. Returns `201` with `secret_key` (the only time it is shown). A `service` key may carry a `session_policy` that restricts the owner's permissions and an `expires` time. |
| `DELETE keys/{ak}` | `iam:DeleteAccessKey` | Delete the key. |
| `POST keys/{ak}/enable` | `iam:UpdateAccessKey` | Enable. |
| `POST keys/{ak}/disable` | `iam:UpdateAccessKey` | Disable. |
| `POST keys/{ak}/rotate` | `iam:UpdateAccessKey` | Body `{"secret_key"?}`; generated when empty. Returns the key with the new secret. |
| `GET groups` | `iam:ListGroups` | All groups. |
| `GET groups/{name}` | `iam:GetGroup` | `name, enabled, members[], policies[], created`. |
| `PUT groups/{name}` | `iam:CreateGroup` | Create or update. Body `{"members"?, "policies"?}`; on update a missing field is left unchanged, a present one replaces. |
| `DELETE groups/{name}` | `iam:DeleteGroup` | Delete the group (members keep their user records). |
| `POST groups/{name}/members` | `iam:AddUserToGroup` | Body `{"add":[...], "remove":[...]}`. |
| `GET policies` | `iam:ListPolicies` | Named policies without documents (`name, builtin, created, updated`). |
| `GET policies/{name}` | `iam:GetPolicy` | Includes `document`. |
| `PUT policies/{name}` | `iam:CreatePolicy` | Body is the raw IAM policy JSON document. Creates or replaces. Built-in policies (`readonly`, `readwrite`, `writeonly`, `diagnostics`, `consoleAdmin`) cannot be changed. |
| `DELETE policies/{name}` | `iam:DeletePolicy` | Delete and detach from every user and group. |
| `GET buckets?usage=true` | `s3:ListAllMyBuckets` | `name, created, owner, versioning, object_lock`; with `usage=true` also `objects` and `bytes` (scans the bucket's metadata). |
| `DELETE buckets/{name}?force=true` | `s3:DeleteBucket` | Delete a bucket; without `force` a non-empty bucket answers `409 BucketNotEmpty`. With `force` every version, upload and blob is removed. |
| `GET kms/keys` | `kms:ListKeys` | Named SSE-KMS keys of the local KMS. |
| `POST kms/keys` | `kms:CreateKey` | Body `{"id"}`. |
| `DELETE kms/keys/{id}` | `kms:ScheduleKeyDeletion` | Delete a key. Objects encrypted with it become unreadable; the default key cannot be deleted. |

Unknown paths under the prefix answer `404 NotFound` (after
authentication).

### Example (curl with `--aws-sigv4`)

```sh
curl -s --aws-sigv4 aws:amz:us-east-1:s3 --user "$OPENS3_ROOT_USER:$OPENS3_ROOT_PASSWORD" \
  -X PUT -H 'Content-Type: application/json' \
  -d '{"secret_key":"alicesecret1","policies":["readwrite"]}' \
  http://localhost:9000/opens3/admin/v1/users/alice
```

## Go client

`github.com/edward-b-1/OpenS3/internal/admin` exports a `Client`
(`admin.NewClient(endpoint, accessKey, secretKey)`) with one method per
endpoint (`Info`, `ListUsers`, `PutUser`, `CreateKey`, `RotateKey`,
`PutPolicy`, `ListBuckets`, `CreateKMSKey`, ...). Errors from the server
are returned as `*admin.Error` with `Status`, `Code` and `Message`. The
CLI and the tests are built on it.

## `opens3 admin` CLI

```
opens3 admin [--endpoint URL] [--access-key K] [--secret-key S] [--region R] [--json] <command>
```

Connection settings come from the flags or the environment:
`OPENS3_ENDPOINT` (default `http://localhost:9000`), `OPENS3_ACCESS_KEY`
and `OPENS3_SECRET_KEY`, falling back to `OPENS3_ROOT_USER` /
`OPENS3_ROOT_PASSWORD` so the CLI works out of the box on the server host.
Output is a human-readable table; `--json` prints the raw API response.
Flags may be given before or after positional arguments.

| Command | Description |
|---|---|
| `info` | Server version, uptime, usage and disk statistics. |
| `health` | Readiness check. |
| `user list` / `user info NAME` | List users / show one. |
| `user add NAME [--secret S] [--policy p1,p2]` | Create (or update) a user; with `--secret` a key named NAME is created. |
| `user rm NAME` | Delete a user and its keys. |
| `user enable NAME` / `user disable NAME` | Toggle a user. |
| `user policy NAME [p1,p2]` | Set the attached policies (none detaches all). |
| `key list [--user U]` | List access keys. |
| `key add USER [--access-key AK] [--secret S] [--service] [--policy-file F] [--expires 72h\|RFC3339] [--description D]` | Create a key; prints the secret once. `--service` creates a service account, `--policy-file` attaches a session policy. |
| `key rm AK` / `key enable AK` / `key disable AK` | Manage a key. |
| `key rotate AK [--secret S]` | Rotate the secret (generated if omitted). |
| `group list` / `group info NAME` | List groups / show one. |
| `group add NAME [--members u1,u2] [--policy p1,p2]` | Create or update a group. |
| `group rm NAME` | Delete a group. |
| `group members NAME [--add u1,u2] [--remove u3]` | Change membership. |
| `policy list` / `policy get NAME` | List policies / print a document. |
| `policy set NAME FILE` | Create or replace a policy from a JSON file (`-` reads stdin). |
| `policy rm NAME` | Delete a policy. |
| `bucket list [--usage]` | List buckets, optionally with object count and size. |
| `bucket rm NAME [--force]` | Delete a bucket; `--force` deletes its contents too. |
| `kms list` / `kms add ID` / `kms rm ID` | Manage SSE-KMS keys. |

Examples:

```sh
export OPENS3_ROOT_USER=opens3admin OPENS3_ROOT_PASSWORD=opens3admin
opens3 admin info
opens3 admin user add alice --secret alicesecret1 --policy readwrite
opens3 admin key add alice --service --policy-file ci-policy.json --expires 720h --description "CI runner"
opens3 admin policy set backups backups.json
opens3 admin group add ops --members alice --policy backups
opens3 admin bucket list --usage --json
opens3 admin bucket rm scratch --force
```

Exit codes: `0` success, `1` the server returned an error (printed as
`code (status): message`), `2` usage error.

## Users, keys and console passwords

`PUT users/{name}` creates or updates a user. On creation, `generate_key`
returns a generated access key pair once in `credentials`; `password` sets
the console password; `secret_key` creates a MinIO-style key whose ID is
the user name (kept for migrating MinIO credentials only). `PUT
users/{name}/password` and `DELETE users/{name}/password` set and remove
the console password. CLI: `opens3 admin user add NAME [--policy ...]
[--password P] [--no-key]` and `opens3 admin user password NAME (--password
P | --clear)`.
