# OpenS3 User Manual

OpenS3 is an S3-compatible object store in a single binary: buckets and
objects with versioning, object lock, lifecycle rules, encryption,
notifications, a full identity system driven by the same tools as AWS, and
a built-in web console. This manual is for people who download OpenS3 and
run it for themselves. It is self-contained; the source distribution has
further reference material for developers.

_Generated from `docs/manual/src/` by `docs/manual/build.sh`; edit the
chapter files, not this file._

## Contents

- [1. Getting started](#1-getting-started)
- [2. Configuration](#2-configuration)
- [3. Identity and access](#3-identity-and-access)
- [4. Buckets and objects](#4-buckets-and-objects)
- [5. Encryption](#5-encryption)
- [6. The console](#6-the-console)
- [7. Notifications](#7-notifications)
- [8. Operations](#8-operations)
- [9. Compatibility](#9-compatibility)
- [10. Troubleshooting](#10-troubleshooting)

---

## 1. Getting started

### Requirements

- Linux or macOS, x86-64 or ARM64. One directory on a local filesystem for
  the data root; OpenS3 keeps everything there (chapter 8 shows the layout).
- Nothing else for the release binary. Go 1.27 or later to build from
  source; Docker is optional (used for the container image and the
  conformance test suites).

### Install

OpenS3 is one static binary. Releases are published at
https://github.com/edward-b-1/opens3/releases for Linux and macOS on amd64
and arm64 (and Linux armv7), with a SHA-256 checksums file that is signed
with Sigstore and an SPDX SBOM per archive. Download, verify and unpack:

```sh
V=0.1.0; OS=$(uname -s | tr A-Z a-z); ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
B=https://github.com/edward-b-1/opens3/releases/download/v$V
curl -sSfLO $B/opens3_${V}_${OS}_${ARCH}.tar.gz
curl -sSfLO $B/checksums.txt
curl -sSfLO $B/checksums.txt.sigstore.json
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity https://github.com/edward-b-1/opens3/.github/workflows/release.yml@refs/tags/v$V \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt
tar -xzf opens3_${V}_${OS}_${ARCH}.tar.gz opens3
sudo install opens3 /usr/local/bin/
opens3 version
```

The `cosign verify-blob` step (cosign v3, https://github.com/sigstore/cosign)
checks that the checksums file was signed by the OpenS3 release workflow
for that exact tag; `sha256sum` then checks the archive against it. On
macOS use `shasum -a 256 --ignore-missing -c checksums.txt`. Skipping the
signature check leaves you with an unverified download; do not skip it for
a production install.

Other ways to install:

- **Go:** `go install github.com/edward-b-1/opens3/cmd/opens3@latest`
  (or `@v0.1.0`) builds from the tagged source with your Go toolchain.
- **Container image:** `ghcr.io/edward-b-1/opens3:0.1.0` (also `:latest`),
  linux/amd64 and linux/arm64, running as a non-root user with `/data` as
  the data root and port 9000 exposed. The image is signed: `cosign verify
  ghcr.io/edward-b-1/opens3:0.1.0` with the same `--certificate-identity`
  and `--certificate-oidc-issuer` as above.

  ```sh
  docker run -d --name opens3 -p 9000:9000 -v opens3-data:/data \
    -e OPENS3_ROOT_USER=root -e OPENS3_ROOT_PASSWORD='a long random password' \
    ghcr.io/edward-b-1/opens3:0.1.0
  ```

- **From source:** `git clone https://github.com/edward-b-1/opens3.git &&
  cd opens3 && make build` produces `bin/opens3`; `make docker` builds the
  same container image locally, and `docker compose up` with
  `OPENS3_ROOT_PASSWORD` set runs it (see `docker-compose.yml`).

Until v1.0 the on-disk format and the API surface are not frozen; the
changelog (`CHANGELOG.md`) says when a release changes them and how to
migrate.

### First start

Root credentials are required; the server refuses to start without them.
Choose a root user name and a long random password:

```sh
export OPENS3_ROOT_USER=root
export OPENS3_ROOT_PASSWORD="$(openssl rand -base64 24)"
opens3 server --root /var/lib/opens3 --address :9000
```

On first start the server:

- creates the data root with `meta/` (the metadata database and the master
  key file), `data/` (object bytes) and `tmp/`;
- generates a random **master key** in `meta/master.keys` and logs its
  fingerprint with a warning to back it up. Every stored secret and every
  encrypted object depends on it (chapter 5 and chapter 8);
- creates the built-in policies and the default encryption key;
- listens on port 9000 for the S3 API, the IAM API, the admin API and the
  console.

Check it is up:

```sh
curl -i http://localhost:9000/opens3/health/ready     # 200, empty body
```

Open the console at `http://localhost:9000/console/` and sign in with the
root user name and password. Over plain HTTP from another machine the
console shows a warning banner. Adding `--tls self-signed` to the command
above serves HTTPS with a certificate the server generates itself;
chapter 2 covers TLS.

### First bucket with the AWS CLI

Any S3 client works. With the AWS CLI:

```sh
export AWS_ACCESS_KEY_ID=$OPENS3_ROOT_USER
export AWS_SECRET_ACCESS_KEY=$OPENS3_ROOT_PASSWORD
export AWS_DEFAULT_REGION=us-east-1
export AWS_ENDPOINT_URL=http://localhost:9000

aws s3 mb s3://demo
aws s3 cp README.md s3://demo/
aws s3 ls s3://demo/
aws sts get-caller-identity
```

`AWS_ENDPOINT_URL` is honoured by the AWS CLI v2 and recent SDKs; older
tools take `--endpoint-url`. Path-style and virtual-host-style addressing
are both supported (chapter 2, `OPENS3_DOMAINS`).

### First user

Do not use the root credentials in applications. Create a user, a key for
programs and, if the person needs the console, a password:

```sh
aws iam create-user --user-name alice
aws iam attach-user-policy --user-name alice --policy-arn arn:aws:iam::aws:policy/readwrite
aws iam create-access-key --user-name alice          # shows the secret once
aws iam create-login-profile --user-name alice --password 'a long passphrase'
```

The same can be done in the console under Identity. Chapter 3 explains the
model.

### Stopping and restarting

Stop with Ctrl-C or SIGTERM; in-flight requests are given thirty seconds.
Restart with the same `--root` and the same root credentials. Changing the
root password is safe: it is only a credential, nothing is derived from it.

### Where to look next

- Chapter 2 for every setting.
- Chapter 5 before you rely on encryption.
- Chapter 8 before you rely on backups.

---

## 2. Configuration

There is no configuration file. Settings come from command-line flags and
`OPENS3_*` environment variables. A flag typed on the command line wins
over the variable of the same setting; a variable wins over the flag's
default.

### Server flags

| Flag | Default | Meaning |
|---|---|---|
| `--root DIR` | `./data` | Data root: metadata, object bytes, master key. |
| `--address ADDR` | `:9000` | Listen address (`host:port`). |
| `--region NAME` | `us-east-1` | Region reported to clients (`GetBucketLocation`, bucket records). |
| `--enforce-region` | off | Reject signatures whose credential scope names another region. Off means any region is accepted, as most S3-compatible servers do. |
| `--tls self-signed` | | Serve HTTPS with a certificate the server generates and keeps under the data root (see below). |
| `--tls-cert FILE`, `--tls-key FILE` | | Serve HTTPS with your own certificate (see below). |
| `--no-fsync` | off | Skip fsync on writes. Only for benchmarks and tests: a crash can lose acknowledged data. |
| `--log-level LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `--log-json` | off | Structured JSON log lines instead of text. |

### Environment variables

| Variable | Meaning |
|---|---|
| `OPENS3_ROOT_USER`, `OPENS3_ROOT_PASSWORD` | Root credentials. Required. Also the root sign-in for the console. |
| `OPENS3_MASTER_KEY` | Optional master key material, at least 32 characters of random data (`openssl rand -base64 32`). When set, the key file is not used. See chapter 5. |
| `OPENS3_MASTER_KEY_NEW`, `OPENS3_MASTER_KEY_OLD` | Read only by `opens3 master` when rotating an environment key (chapter 8). |
| `OPENS3_ROOT`, `OPENS3_ADDRESS`, `OPENS3_REGION` | Same as the flags. |
| `OPENS3_TLS`, `OPENS3_TLS_CERT`, `OPENS3_TLS_KEY` | Same as the flags. |
| `OPENS3_HSTS`, `OPENS3_NO_HSTS` | `Strict-Transport-Security` is sent over TLS only when the certificate is not self-signed; `OPENS3_HSTS=1` forces it on, `OPENS3_NO_HSTS=1` forces it off. |
| `OPENS3_DOMAINS` | Comma-separated domains for virtual-host addressing: a request to `mybucket.s3.example.com` selects `mybucket`. |
| `OPENS3_ACCOUNT_ID` | The 12-digit account ID in ARNs (`arn:aws:iam::<id>:user/alice`). Default `000000000000`. |
| `OPENS3_DEFAULT_OBJECT_OWNERSHIP` | Ownership setting for new buckets: `BucketOwnerEnforced` (default; ACLs disabled, as on AWS since 2023), `BucketOwnerPreferred` or `ObjectWriter`. |
| `OPENS3_LIFECYCLE_INTERVAL` | How often lifecycle rules run, e.g. `1h` (default), `15m`; `0` or `off` disables the worker. |
| `OPENS3_PURGE_INTERVAL` | How often expired temporary credentials are swept (default `1h`). |
| `OPENS3_NOTIFY_WEBHOOK_<NAME>_ENDPOINT` and related | Notification targets; chapter 7. |

### TLS

The quickest way to encrypted transport is a certificate the server makes
for itself:

```sh
opens3 server --root /var/lib/opens3 --tls self-signed
```

On first start this generates an ECDSA key and a self-signed certificate
under `<root>/tls/` (`tls.crt` world-readable, `tls.key` owner-only) and
logs the certificate's SHA-256 fingerprint. Later starts reuse the pair,
so the fingerprint stays the same until the certificate expires (825
days), when the next start replaces it and logs that it did. The
certificate covers `localhost`, the machine's host name, its addresses,
the host in `--address`, and every `OPENS3_DOMAINS` entry with its bucket
wildcard. If the server later answers to a name the certificate lacks
(a new domain, a new address), the log says which; delete `<root>/tls/`
and restart to generate a certificate that covers it.

Clients trust the file, or pin the fingerprint:

```sh
aws --ca-bundle /var/lib/opens3/tls/tls.crt --endpoint-url https://s3.internal:9000 s3 ls
curl --cacert /var/lib/opens3/tls/tls.crt https://s3.internal:9000/opens3/health/ready
```

boto3 takes `verify="/path/to/tls.crt"`. Browsers show a warning for the
console until the certificate is imported into the operating system's
trust store; the fingerprint in the log is what to compare against the
one the browser shows.

To use your own certificate instead, provide the pair in PEM format:

```sh
export OPENS3_TLS_CERT=/etc/opens3/tls.crt OPENS3_TLS_KEY=/etc/opens3/tls.key
```

Either way, with TLS on:

- the listener requires TLS 1.2 or later and prefers TLS 1.3;
- a plain-HTTP request to the same port gets a clear answer instead of a
  handshake error: browsers (the console, or any request accepting HTML)
  get a temporary redirect to `https://`, which browsers do not cache; S3
  clients receive a 400 `InvalidRequest` error naming the `https://` URL,
  because SDKs do not follow redirects on signed requests and some loop on
  them;
- with a certificate from an authority, responses carry
  `Strict-Transport-Security` (two years), which makes browsers refuse
  plain HTTP to that host name from then on. With a self-signed certificate
  the header is not sent, because an experiment with TLS should not leave
  a two-year rule in every browser that visited; `OPENS3_HSTS=1` forces it
  on and `OPENS3_NO_HSTS=1` forces it off;
- console cookies are marked Secure.

For a public host name use a certificate from a public authority (Let's
Encrypt or your own CA) with `--tls-cert`/`--tls-key`. This version does
not obtain or renew public certificates itself; a reverse proxy that does
is a common arrangement. If you make your own certificate with openssl
rather than `--tls self-signed`, include every name and address clients
will use as subject alternative names, and the bucket wildcard when you
use virtual-host addressing.

### Virtual-host addressing

Set `OPENS3_DOMAINS=s3.example.com` and point a wildcard DNS record
`*.s3.example.com` at the server. Requests to `bucket.s3.example.com/key`
then address `bucket`; path-style `s3.example.com/bucket/key` keeps working.
The TLS certificate must cover the wildcard.

### Ports and paths on one listener

Everything is served on the one address:

| Path | What |
|---|---|
| `/` and `/<bucket>/...` | S3 API; also the IAM and STS Query APIs (POST to `/`) |
| `/console/` | Web console |
| `/opens3/admin/v1/` | Admin JSON API used by `opens3 admin` (chapter 8) |
| `/opens3/health/live`, `/opens3/health/ready` | Health probes (200, empty body) |
| `/opens3/metrics` | Prometheus metrics |

A bucket cannot be named `opens3` or `console` for this reason.

---

## 3. Identity and access

OpenS3 follows the AWS model: the same identities, the same request
signing (Signature Version 4) and the same policy language, so the AWS
CLI, the AWS SDKs and policies written for AWS work unchanged.

### The pieces

| Thing | What it is for |
|---|---|
| **Root account** | The user name and password from `OPENS3_ROOT_USER` / `OPENS3_ROOT_PASSWORD`. Full access to everything. Signs in to the console with that name and password; used as an S3 access key pair only for bootstrapping. Keep it for administration. |
| **User** | An identity with a name. Permissions come from the policies attached to it and to its groups. |
| **Access key** | A generated 20-character key ID and 40-character secret. Authenticates S3, IAM and STS requests with Signature Version 4. A user may hold several; keys can be deactivated, given an expiry, and deleted. The secret is shown once, at creation. |
| **Console password** | Optional, per user. Signs in to the web console only; never authenticates an API request. AWS calls it a login profile. |
| **Group** | A named set of users with its own policies; a user's permissions are the union of their own and their groups'. |
| **Policy** | An AWS policy document (JSON). Built-in ones are `readonly`, `readwrite`, `writeonly`, `diagnostics` and `consoleAdmin`. |
| **Temporary credentials** | Issued by STS `AssumeRole`: a key pair plus a session token, expiring after 15 minutes to 7 days, optionally narrowed by a session policy. Console sessions are these. |

A key ID may also be any string of three or more characters chosen by an
administrator through the admin API or CLI. That exists only to migrate
MinIO-style credentials, where the user name is the key; the console never
creates such keys.

### Creating users

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

### Writing policies

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

### Bucket policies and anonymous access

A bucket policy lives on the bucket and names principals. `"Principal":
"*"` includes anonymous requests, which is how a bucket is made public:

```sh
aws s3api put-bucket-policy --bucket pub --policy '{"Version":"2012-10-17","Statement":[
  {"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::pub/*"}]}'
```

`aws s3api get-bucket-policy-status` reports whether a policy is public.
Block Public Access on the bucket (chapter 4) can forbid public policies and
public ACLs regardless of what is written.

### Temporary credentials

```sh
aws sts assume-role --role-arn arn:aws:iam::000000000000:role/any --role-session-name job \
  --duration-seconds 3600 --policy file://narrow.json
```

`RoleArn` is accepted and ignored: the credentials are for the caller,
narrowed by the optional policy. Use the returned key, secret and session
token together; a session token with the wrong key is rejected. Expired
sessions are refused on use and swept hourly.

### The IAM commands that work

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

### Users managing themselves

Any user may read their own user record, list and create their own access
keys, change their own console password (`aws iam change-password`, or the
console's Change password button) and see their attached policies, without
administrative permissions.

---

## 4. Buckets and objects

Everything here is the standard S3 API, so any client's documentation
applies. This chapter records what OpenS3 does with each feature.

### Buckets

Names follow the AWS rules: 3 to 63 characters, lower-case letters, digits,
dots and hyphens, not an IP address. `opens3` and `console` are reserved.
Buckets belong to the user who created them; the owner has full control of
the bucket's configuration.

Object ownership defaults to `BucketOwnerEnforced`, which disables ACLs, as
on AWS since 2023. Set `OPENS3_DEFAULT_OBJECT_OWNERSHIP=ObjectWriter` or use
`--object-ownership` at creation to enable ACLs for legacy tools.

Deleting a bucket requires it to be empty of objects and versions;
in-progress multipart uploads are aborted automatically. The admin API can
force-delete a bucket with its contents.

### Objects

Keys are any UTF-8 string up to 1024 bytes without a NUL byte. Unlike
MinIO, `a/b` and `a/b/c` can both exist. Object size is limited to 5 TiB;
single uploads up to 5 GiB; multipart parts 5 MiB to 5 GiB, up to 10,000
parts. User metadata (`x-amz-meta-*`) up to 2 KB; up to 10 tags.

Every object has an ETag (the MD5 for single uploads, MD5-of-MD5s with a
part count for multipart) and optionally an additional checksum in one of
CRC32, CRC32C, SHA-1, SHA-256 or CRC64-NVMe, computed on upload and
verifiable on download (`--checksum-mode ENABLED`). Trailing checksums and
full-object checksums for multipart uploads are supported.

Conditional operations work as on AWS: `If-Match` and `If-None-Match` on
reads, `If-None-Match: *` and `If-Match` on writes for create-only and
compare-and-swap semantics, and `If-Match` on deletes.

Range reads, part-number reads, `GetObjectAttributes`, copy with metadata
or tagging replacement, and the browser POST upload with a signed policy
all work.

### Versioning

```sh
aws s3api put-bucket-versioning --bucket b --versioning-configuration Status=Enabled
```

Once enabled, every write creates a new version and a delete inserts a
delete marker; the object disappears from listings but every version
remains, listed with `list-object-versions` and retrievable or deletable by
`--version-id`. Suspending stops new versions; writes then replace the
special `null` version. Versioning cannot be suspended while Object Lock is
on.

### Object Lock

Create the bucket with `--object-lock-enabled-for-bucket` (this enables
versioning), or enable it later on a versioned bucket. Then:

- **Retention** per version: `GOVERNANCE` (deletable by users holding
  `s3:BypassGovernanceRetention` who send the bypass header) or
  `COMPLIANCE` (nobody, not even root, until the date passes). A bucket
  default retention applies to every new version.
- **Legal hold**: an on/off flag that blocks deletion indefinitely.

Locked versions refuse deletion with `AccessDenied`; a delete without a
version ID still creates a delete marker, as on AWS.

### Lifecycle

Rules on a bucket expire current objects after N days or on a date, expire
noncurrent versions, remove expired delete markers, abort incomplete
multipart uploads, and record storage-class transitions. Filters by prefix,
tags and object size. The worker runs every `OPENS3_LIFECYCLE_INTERVAL`
(default hourly) and emits `s3:LifecycleExpiration:*` events. Transitions
change the recorded storage class only; there is no tiered storage yet, so
data stays where it is and `RestoreObject` completes immediately.

### Tags, CORS, website, logging, replication

Bucket and object tags work as on AWS and are usable in policy conditions
(`s3:ExistingObjectTag/<key>`, `aws:ResourceTag/<key>`) and lifecycle
filters.

CORS rules are enforced: browsers' preflight requests are answered from the
bucket's configuration, and matching responses carry the CORS headers.

Website, logging, replication, inventory, metrics, analytics, accelerate
and request-payment configurations are stored and returned exactly as
written, so tooling that sets them succeeds, but they have no effect yet:
there is no website endpoint, no log delivery and no replication worker in
this version.

### Public access and ACLs

Block Public Access has the four AWS settings per bucket. `BlockPublicPolicy`
rejects a public bucket policy at upload; `RestrictPublicBuckets` ignores a
public policy for anonymous callers; `BlockPublicAcls` rejects public ACLs;
`IgnorePublicAcls` ignores existing ones.

ACLs (canned and explicit grants, bucket and object level) are honoured
only when the bucket's ownership setting allows them. Grants to the
`AllUsers` and `AuthenticatedUsers` groups make data public.

### Locking yourself out

A bucket policy can deny the owner access to the policy itself. AWS asks
for confirmation with the `x-amz-confirm-remove-self-bucket-access` header
before it applies such a policy against the account owner; OpenS3 does the
same. Without the header, root can always read, replace or delete a bucket
policy. With it, root is bound like everyone else, and the only way out is
to delete the bucket through the admin API.

---

## 5. Encryption

### Data in transit

Chapter 2 covers TLS. Without it, credentials, secrets and data cross the
network in the clear; the console warns when it is used over plain HTTP
from another machine.

### Data at rest: the four states

An object is stored in one of four states, chosen per request or by the
bucket's default. **Nothing is encrypted unless asked**; that is a
deliberate choice, unlike AWS since 2023.

| State | Request header | Who holds the key | Reading it back |
|---|---|---|---|
| Off | none | nobody; bytes stored as uploaded | plain GET |
| SSE-S3 | `x-amz-server-side-encryption: AES256` | the server: the object's data key is wrapped under the master key | plain GET; the server decrypts |
| SSE-KMS | `x-amz-server-side-encryption: aws:kms` plus optional `x-amz-server-side-encryption-aws-kms-key-id` | the server: wrapped under a named encryption key | plain GET; the server decrypts |
| SSE-C | `x-amz-server-side-encryption-customer-algorithm: AES256` plus the key and its MD5 | the client, on every request; the server keeps nothing | GET must present the same key |

In every encrypted state each object has its own random data key, and the
bytes are encrypted with AES-256-GCM in 64 KiB chunks so ranged reads stay
cheap. The metadata record stores which state and which key were used, so
a client needs no bookkeeping to read data back, except with SSE-C.

`aws:kms:dsse` (AWS's double encryption) is accepted but treated as
ordinary SSE-KMS.

### The master key

The master key wraps every SSE-S3 data key, every named encryption key,
and every stored access-key secret. It is generated on first start as 32
random bytes in `<root>/meta/master.keys`, a file readable only by the
server's user. The file can hold several keys: the newest one is used for
new data and older ones remain able to read older data, which is how a key
is rotated (chapter 8, "Rotating the master key").

Alternatively set `OPENS3_MASTER_KEY` to at least 32 characters of random
material, for example from a secret manager injected at boot. The server
refuses short material so a password cannot be used. A key-check value in
the database makes the server refuse to start with a key that does not
match the data.

**Back the master key up, separately from the data.** Without it, every
encrypted object and every stored access-key secret is unrecoverable, while
plaintext objects are unaffected. Chapter 8 covers backups.

### Named encryption keys (SSE-KMS)

The Encryption keys page in the console, `opens3 admin kms`, and the admin
API create and delete named keys. `opens3-default-key` exists from the
first start and is used when a request says `aws:kms` without a key name.

Named keys let you bind data to a boundary: one key per tenant or project.
Deleting a key makes every object wrapped under it unreadable, permanently
and in every backup, which is the intended way to retire a tenant's data.
Nothing else needs a named key; SSE-S3 covers "just encrypt it".

### Bucket defaults

A bucket can encrypt everything uploaded without explicit headers. In the
console: bucket Settings, Default encryption. With the CLI:

```sh
aws s3api put-bucket-encryption --bucket b --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"aws:kms","KMSMasterKeyID":"payroll"}}]}'
```

Existing objects are not re-encrypted. A request with its own encryption
headers overrides the default.

### SSE-C: keys the server never sees

SSE-C cannot be a bucket default, because a default is what applies when
the client sends nothing, and with SSE-C there is no key to apply. What you
can do is require it, with a bucket policy that denies uploads lacking the
header:

```json
{"Version":"2012-10-17","Statement":[{
  "Effect":"Deny","Principal":"*","Action":"s3:PutObject",
  "Resource":"arn:aws:s3:::mybucket/*",
  "Condition":{"Null":{"s3:x-amz-server-side-encryption-customer-algorithm":"true"}}
}]}
```

The mirror image, forbidding SSE-C, denies when the key is present. AWS
disables SSE-C on new buckets since 2026 because attackers used it to
encrypt victims' data with keys only they held; consider forbidding it on
buckets that do not need it. Send SSE-C only over TLS.

### What at-rest encryption protects

Be precise about the threat, because server-side encryption is often
credited with more than it delivers:

- It protects storage that leaves the server without the master key: a
  disk sent for replacement, a decommissioned machine, a copy of the object
  files, a volume snapshot. With the key file in the data root, that means
  the object files copied without the metadata directory; with the key in
  the environment, the whole data directory.
- It does not protect against an attacker who controls the running server,
  who has the keys. That is equally true of AWS; AWS's advantage is that
  its keys live in a separate audited service. This version has no
  external key service; keep the master key out of the data directory
  with `OPENS3_MASTER_KEY` if that matters to you.
- Deleting a named key destroys its data everywhere at once, which no
  amount of disk scrubbing achieves.
- SSE-C protects even against the server, at the cost of the client
  managing keys.

---

## 6. The console

The console is at `/console/` on the server's address. It is built into
the binary, needs no separate service, and every action goes through the
same authorisation as the API.

How it stays safe: signing in creates a temporary credential and stores
only that in a browser cookie that scripts cannot read; every request is
checked against the cookie and against a header that other websites cannot
set, so a malicious page cannot act on your behalf; and the console never
receives or displays a secret except at the moment a key is created.

### Signing in

Enter a user name and console password. Root uses `OPENS3_ROOT_USER` and
`OPENS3_ROOT_PASSWORD`. Other users need a console password set by an
administrator (Identity page, `aws iam create-login-profile`, or
`opens3 admin user password`). Access key pairs are refused at the console;
they are for the API.

A session lasts twelve hours and is a temporary credential like any other;
"Log out" ends it immediately.

### Pages

- **Buckets**: list, create (with versioning, Object Lock, tags), delete,
  and Settings per bucket: versioning, default encryption, tags, the
  policy editor with validation, and deletion.
- **Object browser**: folders (prefixes), upload by dialog or drag-and-drop
  with progress, download, delete, "Show versions", a details panel with
  metadata, tags, checksum and encryption, and file-type icons. Folders can
  be downloaded as a zip archive or deleted recursively, with a count and
  size shown before confirmation.
- **Identity** (administrators): users, access keys and service accounts
  with session policies and expiry, groups, policies with a JSON editor.
  Non-administrators see "My access keys" instead.
- **Encryption keys** (administrators): named keys for SSE-KMS.
- **Status**: version, region, buckets, disk usage.

### Settings and shortcuts

The gear button (or `,`) opens per-browser settings stored in local
storage: density (comfortable or compact), theme, date and size formats,
rows per page, folder-marker visibility. `/` focuses the filter, `r`
reloads, `u` opens upload, `Esc` closes dialogs. "Change password" in the
header changes your own console password.

### Plain HTTP

A banner appears when the console is used over `http://` from any address
other than the local machine, because passwords and secrets would travel
unencrypted. Chapter 2 covers TLS.

---

## 7. Notifications

Buckets can announce events (object created, removed, tagged, restored,
lifecycle actions and more) to targets. Targets are configured by the
operator; buckets choose which events go where.

### Configure a target

```sh
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_ENDPOINT=https://hooks.example.com/opens3
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_AUTH_TOKEN=secret        # optional Bearer token
export OPENS3_NOTIFY_WEBHOOK_PRIMARY_QUEUE_DIR=/var/lib/opens3/queue/primary   # optional: persist undelivered events
```

The target's ARN is `arn:opens3:sqs::PRIMARY:webhook`; MinIO-style
`arn:minio:sqs::PRIMARY:webhook` is accepted too.

### Subscribe a bucket

```sh
aws s3api put-bucket-notification-configuration --bucket b --notification-configuration '{
  "QueueConfigurations":[{"Id":"all-creates","QueueArn":"arn:opens3:sqs::PRIMARY:webhook",
    "Events":["s3:ObjectCreated:*"],
    "Filter":{"Key":{"FilterRules":[{"Name":"prefix","Value":"uploads/"}]}}}]}'
```

An ARN that names no configured target is rejected.

### Delivery

Events are the AWS `Records` JSON, version 2.1, posted as they happen, at
least once. One event looks like this:

```json
{"Records":[{"eventVersion":"2.1","eventSource":"aws:s3","awsRegion":"us-east-1",
  "eventTime":"2026-09-14T09:15:02.417Z","eventName":"ObjectCreated:Put",
  "userIdentity":{"principalId":"alice"},"requestParameters":{"sourceIPAddress":""},
  "responseElements":{"x-amz-request-id":"...","x-amz-id-2":"..."},
  "s3":{"s3SchemaVersion":"1.0","configurationId":"all-creates",
    "bucket":{"name":"b","ownerIdentity":{"principalId":"..."},"arn":"arn:aws:s3:::b"},
    "object":{"key":"uploads/photo.jpg","size":48213,"eTag":"...","versionId":"...","sequencer":"...",
      "contentType":"image/jpeg","userMetadata":{}}}}]}
```

Event names you can subscribe to: `s3:ObjectCreated:*` (`Put`, `Post`,
`Copy`, `CompleteMultipartUpload`), `s3:ObjectRemoved:*` (`Delete`,
`DeleteMarkerCreated`), `s3:ObjectTagging:*`, `s3:ObjectAcl:Put`,
`s3:ObjectRetention:Put`, `s3:ObjectRestore:*`, `s3:LifecycleExpiration:*`,
`s3:LifecycleTransition`. Webhooks are retried with backoff; without a queue directory a
target that is down for long loses events (counted in the
`opens3_notify_events_total` metric); with one, events wait on disk and are
replayed. Webhooks are the only target type in this version.

---

## 8. Operations

### What is on disk

Everything is under the data root:

| Path | Contents |
|---|---|
| `meta/opens3.db` | Metadata: buckets, object versions, multipart state, users, keys, policies, encryption keys. One file, locked while the server runs. |
| `meta/master.keys` | The master key ring. Mode 0600. |
| `tls/tls.crt`, `tls/tls.key` | The self-signed certificate and key, when `--tls self-signed` is used. Delete the directory to generate a new certificate. |
| `data/<bucket>/xx/<id>` | One immutable file per uploaded object or part. |
| `tmp/` | In-flight uploads; cleaned at start. |

Object files are the uploaded bytes as-is (or ciphertext when encrypted),
so plaintext objects can be reassembled without the server from the
metadata alone.

### Backups

The server holds the metadata database open with an exclusive lock, so a
consistent backup is one of:

1. Stop the server and copy the data root. Simplest and consistent.
2. Snapshot the filesystem or volume with the server running (ZFS, LVM,
   cloud volumes). Consistent as long as the snapshot is atomic, which it
   is for those.
3. Copy `data/` while running and `meta/` while stopped. Object files are
   immutable once written, so `data/` can be copied live; only the
   metadata needs the stop.

**Keep a copy of `meta/master.keys` (or the value of `OPENS3_MASTER_KEY`)
somewhere other than the backup of the data root.** A backup that contains
the key beside the data is not encrypted at rest in any meaningful sense;
a backup without the key cannot be read if the key is lost.

Restore by putting the data root back and starting the server with the
same root credentials and, if used, the same `OPENS3_MASTER_KEY`. A key
mismatch is refused at start.

### Rotating the master key

`opens3 master` works on the data directory with the server stopped (the
database is locked while it runs). Every command takes `--root DIR`.

| Command | What it does |
|---|---|
| `status` | Lists the keys in the ring with their fingerprints and how many records each still protects. |
| `rotate` | Adds a new key to `meta/master.keys` and re-wraps every protected record under it. |
| `rewrap` | Re-wraps under the current key whatever is still under an older one; finishes an interrupted `rotate`. `--dry-run` only counts. |
| `retire` | Removes the older keys from the ring once no record needs them. Refuses while anything is still under an older key. |

The protected records are the key-check value, the named encryption keys,
every stored access-key secret and the data keys of SSE-S3 objects and
multipart uploads. Objects under a named key (SSE-KMS) are not touched:
their data keys are wrapped by the named key, which is itself re-wrapped.
SSE-C objects and plaintext objects are not affected.

A rotation is safe to interrupt: the new key is written to the ring before
any record uses it, and older keys stay in the ring, so a half-finished
`rotate` leaves everything readable and `rewrap` completes it.

After `rotate`, **back up the key file again**: earlier copies lack the
new key. The older keys stay in the ring on purpose. A backup of
`meta/opens3.db` taken before the rotation still has its records wrapped
under the old key, so restoring it needs that key. Run `retire` only when
no such backup needs to be restorable, or keep a copy of the pre-rotation
ring with those backups.

**Environment keys.** When the server takes its key from `OPENS3_MASTER_KEY`,
`rotate` needs the new material in `OPENS3_MASTER_KEY_NEW`; it re-wraps
everything and tells you to move the new value into `OPENS3_MASTER_KEY`
before starting the server. `OPENS3_MASTER_KEY_OLD` names a previous
environment key so that `rewrap` can finish a rotation that was
interrupted after the variables were swapped.

**Moving between the file and the environment.** To move from the key
file to an environment key, set `OPENS3_MASTER_KEY` to the new material,
run `rewrap` (the file's keys are read as fallbacks), then `retire`, which
deletes the file. To move the other way, unset `OPENS3_MASTER_KEY`, set
`OPENS3_MASTER_KEY_OLD` to the current material and run `rotate`: it
creates the file and re-wraps everything under it.

### Upgrades

Stop, replace the binary, start. The on-disk format is versioned; a
release that changes it says so in its notes and reads the previous
version. The console's assets are inside the binary
and browsers revalidate them on each load, so a normal reload picks up the
new version.

### Monitoring

- `/opens3/health/live` and `/opens3/health/ready` return 200 with no body;
  ready returns 503 with a message if the data root is unusable. Use them
  as container probes.
- `/opens3/metrics` is Prometheus text: `opens3_s3_requests_total` by
  operation and status, request latency histograms, bytes in and out,
  notification deliveries, purged credentials, plus Go runtime and process
  metrics.
- `opens3 admin info` or the console's Status page: version, uptime,
  bucket and object counts, disk usage.

### Logs

Structured log lines on stderr (`--log-json` for JSON). At `info` you see
startup (address, master key fingerprint), console logins and failures,
plain-HTTP redirects, lifecycle and purge activity, and TLS handshake
problems reported by clients. `--log-level debug` adds per-request detail.
This version has no separate audit log.

### Housekeeping that runs by itself

- Lifecycle rules: every `OPENS3_LIFECYCLE_INTERVAL` (default hourly).
- Expired temporary credentials: swept on start and every
  `OPENS3_PURGE_INTERVAL` (default hourly).
- Interrupted uploads: `tmp/` is emptied at start; multipart uploads
  abandoned by clients are removed by a lifecycle rule with
  `AbortIncompleteMultipartUpload`, or with the bucket.

### Administration without the console

`opens3 admin` talks to the server with the root (or an administrator's)
access key, taken from `--access-key`/`--secret-key`, or from
`OPENS3_ACCESS_KEY`/`OPENS3_SECRET_KEY`, or from the root variables. The
endpoint comes from `--endpoint` or `OPENS3_ENDPOINT` (default
`http://localhost:9000`). `--json` prints raw JSON.

| Command | What it does |
|---|---|
| `info`, `health` | Version, uptime, bucket and object counts, disk usage; readiness |
| `user list \| info NAME \| add NAME [--policy p1,p2] [--password P] [--no-key] \| rm \| enable \| disable \| policy NAME [p1,p2] \| password NAME (--password P \| --clear)` | Users. `add` prints the generated key pair once |
| `key list [--user U] \| add USER [--service] [--policy-file F] [--expires DUR] \| rm AK \| enable AK \| disable AK \| rotate AK` | Access keys and service accounts |
| `group list \| info \| add NAME [--members u1,u2] [--policy p1] \| rm \| members NAME [--add u] [--remove u]` | Groups |
| `policy list \| get NAME \| set NAME FILE \| rm NAME` | Policies (`FILE` may be `-` for stdin) |
| `bucket list [--usage] \| rm NAME [--force]` | Buckets; `--force` deletes contents too |
| `kms list \| add ID \| rm ID` | Named encryption keys |

Anything AWS tooling can do, do through `aws iam` (chapter 3); the
bundled CLI exists for what AWS has no command for. `opens3 master`
(above) is the other offline command: it needs the data directory, not a
running server.

---

## 9. Compatibility

OpenS3 aims to be wire-compatible with Amazon S3: same operations, same
XML, same headers, same error codes, same edge cases. Rather than claim
it, each release ships three reports generated by the test suites:

- an API coverage table listing every S3 operation and whether it is
  implemented: 95 of 117 in this version; the rest are AWS-only features
  such as directory buckets, S3 Metadata tables and annotations;
- the result of the Ceph `s3-tests` suite, the community's standard S3
  conformance suite, run against a fresh server: 600 of 637 tests pass and
  every failure is listed with its reason;
- the result of running every `aws s3`, `s3api`, `iam` and `sts`
  subcommand through the official AWS CLI: 442 cases, none failing.

They are in the `docs/` directory of the source distribution.

### Clients known to work

The AWS CLI v2, the AWS SDKs (the Go SDK v2 and boto3 are exercised in the
test suites; `examples/python` has boto3 scripts), and anything else that
speaks Signature Version 4, including legacy Signature Version 2 clients.

### Differences from AWS

- No encryption by default (chapter 5).
- One account. ARNs use `OPENS3_ACCOUNT_ID`; there are no cross-account
  semantics, roles, or MFA.
- Storage classes are recorded but all data lives on the same disks;
  `RestoreObject` completes immediately.
- Website, logging, replication, inventory, analytics and metrics
  configurations are stored but not acted on yet.
- `SelectObjectContent` returns `NotImplemented`; AWS closed it to new
  customers in 2024.
- Any region name is accepted in signatures unless `--enforce-region`.
- `aws:kms:dsse` is single-layer.

### Differences from MinIO

- Keys `a/b` and `a/b/c` can coexist.
- Bucket and object ACLs, per-bucket CORS, and `If-Match` on delete are
  implemented; MinIO refused them.
- Administrative actions use AWS's names (`iam:*`, `kms:*`), and
  administration is done with `aws iam` rather than `mc admin`; `mc admin`
  does not work against this version.
- The console signs in with a user name and password, not an access key.
- Users have a name separate from their keys; MinIO-style keys whose ID is
  the user name are still accepted for migrated credentials.

---

## 10. Troubleshooting

**`curl http://host:9000/` returns AccessDenied.** Expected: the root path
is the ListBuckets operation, which S3 never allows anonymously. Use a
signed client, or the console at `/console/`. Health probes at
`/opens3/health/ready` return 200 with an empty body.

**`ERR_CERT_AUTHORITY_INVALID` in the browser, or `tls: unknown certificate`
in the server log.** The client does not trust the server's certificate,
which is normal for a self-signed one. Trust it on the client (chapter 2)
or click through the browser warning. The log line comes from the client
telling the server it refused the certificate.

**`This server requires HTTPS. Use https://...`, or the AWS CLI reports
"maximum recursion depth exceeded".** The client's endpoint URL says
`http://` but the server has TLS on. Change the endpoint to `https://`
(and trust the certificate, above). The recursion error is what botocore
produces when an S3 request meets a redirect; current versions of OpenS3
answer S3 clients with the `InvalidRequest` message instead.

**"the console signs in with a user name and console password; access keys
work only with the S3 API".** The sign-in form was sent an access key pair.
Use the user name and console password; for root, the configured root
credentials.

**"invalid user name or password".** Wrong password, unknown user, disabled
user, or a user with no console password set. Administrators set one on
the Identity page or with `aws iam create-login-profile`.

**`InvalidAccessKeyId`.** The key does not exist, is deactivated, has
expired, or is a console password being used as an API credential.

**`SignatureDoesNotMatch`.** Wrong secret, or the request was altered
between signing and arrival (a proxy rewriting headers or the path), or a
clock more than fifteen minutes off (`RequestTimeTooSkewed`).

**`AccessDenied` on an operation root can do.** The user lacks a policy
granting it; check with `aws iam list-attached-user-policies` and the
bucket policy. Administrative actions need `iam:*`/`kms:*` from an
identity policy, never from a bucket policy.

**"master key does not match this data directory" at start.**
`OPENS3_MASTER_KEY` differs from the one the data root was created with, or
`meta/master.keys` was replaced. Restore the original key; there is no
way to read the data without it. After a rotation with an environment
key, make sure `OPENS3_MASTER_KEY` holds the new value; `opens3 master
status --root DIR` shows which keys the data directory accepts.

**`AccessDenied` deleting a version.** Object Lock retention or legal hold.
Governance retention can be bypassed with permission and the bypass flag;
compliance cannot.

**`DeleteConflict` from `aws iam delete-user`.** As on AWS, delete the
user's access keys, login profile, group memberships and policy
attachments first.

**Console changes not visible after an upgrade.** Rebuild the binary
(`make build`), restart, reload the page.

**Bucket policy rejected with "action ... is not supported; use iam:..."**
The document uses MinIO's `admin:` action names; the message gives the AWS
name to use.

**Chrome keeps changing `http://` to `https://` after TLS was turned off.**
Chrome remembers HTTPS for a host in two places: an HSTS rule, if the
server ever sent `Strict-Transport-Security` (delete the host at
`chrome://net-internals/#hsts`), and its HTTP cache, if it received a
permanent redirect (clear "Cached images and files", or reload once with
DevTools open and "Disable cache" ticked). Current versions of OpenS3 send
neither for a self-signed certificate: the redirect is temporary and
uncached, and HSTS is only sent with a certificate from an authority. An
incognito window, which has neither memory, shows whether the server is
fine.
