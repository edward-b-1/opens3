# Changelog

All notable changes to OpenS3 are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions
follow [Semantic Versioning](https://semver.org/). Until v1.0.0 the
on-disk format (`docs/FORMAT.md`) and the API surface are not frozen: a
minor release may change them, and says so under "Changed" with
migration notes.

Release procedure: `docs/RELEASING.md`. Detailed per-version reports:
`docs/API-COVERAGE.md`, `docs/CONFORMANCE.md`, `docs/AWSCLI.md` in the
tagged tree.

## [Unreleased]

### Added

- Manual chapter 11, "Migrating from MinIO": the API-based procedure
  (rclone for the objects, identity export with `mc admin`, import with
  `examples/migrate/import_identities.py`, tags with `copy_tags.py`,
  verification with `opens3 fsck`), tested end to end by
  `make migration-test` against a MinIO built from source at a pinned
  release in Docker.

## [0.3.0] - 2026-09-14

The first release under the AGPL-3.0-or-later (see Changed), with a
further round of authorisation fixes: advisory OPENS3-2026-004 in
`SECURITY.md`. No on-disk format change since 0.2.1.

### Security

- Copy sources are bound to the source bucket's incarnation even when
  the source object does not exist at authorisation time.
- Administrative actions (`iam:`, `kms:`, `sts:`, `opens3:`) are
  evaluated against IAM resource ARNs, never the S3 ARN: a statement
  granting `Action: *` on `arn:aws:s3:::*` no longer covers them, and IAM
  API actions that target a user, group or policy are evaluated against
  that resource, so AWS-style self-only policies
  (`Resource: arn:aws:iam::*:user/${aws:username}`) work.
- Listing, deactivating and deleting one's own access keys through the
  IAM API go through authorisation like every other action, so a
  session policy applies; before, own keys bypassed it.
- `s3:ExistingObjectTag` conditions apply to DeleteObject and
  DeleteObjects; the existing object was not loaded for deletes before, so
  a deny keyed on a tag did not fire.

### Changed

- **Licence: AGPL-3.0-or-later**, from Apache License 2.0. Releases
  0.1.0 to 0.2.1 remain Apache-2.0. The reasoning is in `GOVERNANCE.md`
  section 1; the name is covered by the new `TRADEMARKS.md`.

## [0.2.1] - 2026-09-14

Security release: see advisory OPENS3-2026-003 in `SECURITY.md`. No
on-disk format change (a new optional flag on console session records).

### Security

- Only credentials issued by a console password login are accepted as
  console sessions. STS credentials obtained through `AssumeRole` were
  accepted before, which let a credential narrowed by a session policy
  reach the console's self-service key management and create or rotate
  an unrestricted key for its user. The console's credential paths also
  apply the same issuer check as the IAM and admin APIs.
- Request binding completed: UploadPartCopy binds its source and uses the
  source bucket's resource tags; browser form uploads, multipart
  initiation and bucket deletion check the bucket incarnation;
  DeleteObjectTagging loads the object so tag conditions apply and the
  version is bound. Copying tags needs `s3:PutObjectTagging` on the
  destination only (the source `s3:GetObjectTagging` requirement added
  in 0.2.0 was not AWS behaviour).
- Copy sources are bound to the source bucket incarnation as well as the
  object version; browser form uploads carry their bucket binding through
  to the write.
- Tags on an upload always need `s3:PutObjectTagging` from a policy, and
  bucket ACL WRITE no longer grants the standalone tagging operations, per
  AWS's ACL mapping (an ACL on an upload may still ride on an ACL grant,
  which the conformance suite expects).
- `opens3 fsck repair` refuses any path or bucket name in a report that
  would lead outside the data directory.
- Console folder downloads skip object keys that would extract outside
  the archive's directory.
- The readiness endpoint no longer echoes internal error text.
- S3 object responses carry `X-Content-Type-Options: nosniff`.

### Changed

- Documentation no longer claims default root credentials; the server has
  always required them. `docker-compose.yml` requires `OPENS3_ROOT_USER`
  as well as the password.

## [0.2.0] - 2026-09-14

Operational tooling release with further authorisation fixes: see
advisory OPENS3-2026-002 in `SECURITY.md`. No on-disk format change
since 0.1.1.

### Security

- Rotating an access key through the admin API is refused for
  credentials restricted by a session policy (the new secret would carry
  none of the restriction), like creating one.
- Copying an object copies its tags, which now needs
  `s3:GetObjectTagging` on the source and `s3:PutObjectTagging` on the
  destination, as on AWS; `x-amz-tagging-directive: REPLACE` copies
  without them.
- Object Lock retention and legal hold set on an upload always need
  their permission, even when the upload itself was granted by an ACL;
  `BlockPublicAcls` now applies to copies and browser form uploads as it
  did to PUT.
- Operations are bound to the records they were authorised against: a
  copy reads exactly the source version that was checked, metadata
  updates (tags, ACL, retention, legal hold) act only on the version
  that was checked, and every write verifies the bucket is the
  incarnation that was checked. A replacement in between yields
  `NoSuchKey`/`NoSuchBucket` instead of acting on the replacement. Copy
  sources are evaluated with the source bucket's `aws:ResourceTag`
  values.
- Lifecycle version deletes and transitions are guarded by the scanned
  version's sequence; persistent notification queue files are created
  owner-only.

### Added

- `opens3 fsck check` and `opens3 fsck repair`: verify the data directory
  against its metadata (files, sizes, and with `--verify` content;
  pointers, indexes, interrupted bucket deletions) and repair what can be
  repaired without losing data.
- `opens3 export`: write objects out as plain files with a manifest,
  decrypting SSE-S3 and SSE-KMS with the master key; `--all-versions`,
  `--bucket`, `--prefix`.

## [0.1.1] - 2026-09-14

Security release: see advisory OPENS3-2026-001 in `SECURITY.md`. The
on-disk format gains an optional `del` flag on bucket records (readable
by v0.1.0, which ignores it) and object files are now created owner-only.

### Security

Fixes for findings of a code review of v0.1.0. Upgrade if you run v0.1.0
with more than one identity or hand out presigned URLs.

- Credentials narrowed by a session policy (service accounts with one,
  STS sessions created with or derived under one) could create a
  permanent access key or a console password for their user, with none
  of the restriction. They cannot any more; creating a key for oneself
  also needs `iam:CreateAccessKey` like any other key. A session derived
  from a restricted session by AssumeRole now keeps every parent
  restriction, so it can only narrow. Root's sessions can never own keys.
- Signed requests carrying `x-amz-*` headers outside their signed
  headers are refused (`AccessDenied`, as on AWS). Previously a presigned
  PUT URL could be turned into a server-side copy, or given an ACL or
  tags, by adding unsigned headers.
- Negated condition operators (`StringNotEquals`, `NotIpAddress`,
  `ArnNotLike`, ...) now match when the condition key is absent, as on
  AWS, so a deny written as "unless the header equals X" fires for
  requests without the header.
- Policy resource ARNs use the object key verbatim; `..`, `.` and
  repeated slashes were normalised before, letting a key be authorised
  under a name it was not stored under.
- The object served by GetObject, HeadObject and the object metadata
  operations is re-authorised when it is not the version that was
  checked (replaced between the check and the read).
- Copy sources are authorised with their tags (`s3:ExistingObjectTag`
  conditions apply); UploadPartCopy of a specific version needs
  `s3:GetObjectVersion`; DeleteObjects no longer requires a bucket-level
  `s3:DeleteObject` before its per-key checks, so object-scoped policies
  work.
- `X-Forwarded-Proto` and `X-Forwarded-For` are believed only from
  reverse proxies listed in `OPENS3_TRUSTED_PROXIES` / `--trusted-proxies`
  (default none); before, any client could satisfy `aws:SecureTransport`
  over plain HTTP by sending the header. SSE-C requests (object or copy
  source, including browser form uploads) are refused over a connection
  that is not secure, as on AWS; the guard was previously never enabled
  and covered only PUT.
- Setting an ACL, tags, a retention period or a legal hold on an upload
  (PutObject, CopyObject, CreateMultipartUpload, browser form upload) now
  needs the permission for that attribute (`s3:PutObjectAcl`,
  `s3:PutObjectTagging`, `s3:PutObjectRetention`, `s3:PutObjectLegalHold`),
  as on AWS; before, `s3:PutObject` alone let a write-only identity create
  a public-read object.
- CompleteMultipartUpload reads the part list inside the transaction that
  writes the object, so a part re-uploaded during completion can no longer
  leave the object pointing at a deleted file.
- Lifecycle expiry guards its delete by the object's sequence rather than
  its ETag, so a same-content replacement written after the scan is not
  expired on stale data.
- The data directory, metadata directory and object files are created
  owner-only (0700 / 0600); they were world-readable before.
- Bucket deletion marks the record as deleting until the data directory
  is gone (finished at start after a crash); the name cannot be recreated
  meanwhile, and an upload authorised against a deleted bucket cannot
  land in a new bucket of the same name. On-disk: a `del` flag in the
  bucket record.

### Changed

- A flag given on the command line now takes precedence over the
  `OPENS3_*` variable for the same setting (it was the other way round).
  Variables still override flag defaults.

### Added

- TLS certificate reload without restart: the certificate and key files
  are checked every minute and a changed pair is taken into service;
  `SIGHUP` reloads at once. A pair that fails to parse, whose key does
  not match or that has expired is refused and the current certificate
  kept. Metrics `opens3_tls_certificate_not_after_seconds` and
  `opens3_tls_certificate_reloads_total`.
- `--tls self-signed` (`OPENS3_TLS=self-signed`): the server generates a
  certificate and key under `<root>/tls` on first start, reuses them on
  later starts, replaces them when expired, and logs the SHA-256
  fingerprint and the names covered. Clients trust the certificate file
  or pin the fingerprint. `--tls-cert`/`--tls-key` are unchanged.

## [0.1.0] - 2026-09-14

First release: phase 1 of `docs/PLAN.md`, a single-node S3-compatible
object store in one static binary. 600 of 637 Ceph s3-tests pass; 95 of
117 S3 operations are implemented; the AWS CLI suite passes 457 of 457
scenarios.

### Added

- **Buckets:** create, delete, head, list, location; ListObjects v1 and
  v2, ListObjectVersions, ListMultipartUploads; versioning (Enabled and
  Suspended) with delete markers and `null` version semantics; tagging,
  bucket policy and PolicyStatus, ACL (canned and grants), CORS;
  lifecycle rules (expiration, noncurrent versions, abort incomplete
  multipart uploads, delete-marker cleanup, transitions recorded as a
  storage-class change); default encryption (SSE-S3, SSE-KMS); Object
  Lock with default retention; notification configuration; website,
  logging, replication, public-access-block, ownership-controls,
  request-payment, accelerate, metrics, analytics, inventory and
  intelligent-tiering configurations stored and returned; DeleteObjects
  with quiet mode.
- **Objects:** Put, Get, Head, Delete, Copy; range and conditional reads;
  conditional writes (`If-None-Match: *`, `If-Match` on PUT and
  CompleteMultipartUpload); multipart upload (create, upload part, upload
  part copy, complete, abort, list parts); checksums CRC32, CRC32C, SHA1,
  SHA256 and CRC64NVME, full-object and composite, including trailing
  checksums in `aws-chunked` uploads; tagging, ACL, user metadata,
  storage class, `response-*` header overrides; GetObjectAttributes;
  retention and legal hold; RestoreObject; browser POST form uploads
  with policy; presigned URLs.
- **Encryption:** SSE-S3, SSE-KMS with a built-in KMS, SSE-C including
  SSE-C copy sources; a master key ring under the data root
  (`OPENS3_MASTER_KEY` or generated on first start), rotated offline with
  `opens3 master rotate | rewrap | retire`, which re-wrap every stored
  secret, named key and SSE-S3 data key under the new key.
- **Authentication and identity:** AWS Signature Version 4 in headers and
  query strings, signed and unsigned streaming payloads with trailers,
  Signature Version 2 for legacy clients; root credentials, IAM users,
  groups, policies in the AWS policy language with conditions and
  variables, service accounts, STS AssumeRole temporary credentials with
  hourly purge of expired ones; bucket policy and ACL evaluation
  (explicit deny wins), anonymous access; AWS IAM Query API and STS
  GetCallerIdentity on the S3 endpoint so `aws iam` and boto3 manage
  users, keys, groups, policies and login profiles.
- **Operations:** `opens3 server` configured by flags and environment,
  TLS with HTTP-to-HTTPS redirect and HSTS, structured logs, Prometheus
  metrics, health and readiness endpoints, lifecycle worker, webhook
  notification dispatcher, admin REST API (`/opens3/admin/v1`) and the
  `opens3 admin` CLI, `opens3 version`.
- **Console:** embedded web console at `/console/` for buckets, objects,
  folders, identity, keys, policies, encryption and server status, with
  an ESLint and Playwright test suite.
- **Distribution:** Docker image (`gcr.io/distroless/static`, non-root),
  `docker-compose.yml`; release tooling with GoReleaser: reproducible
  `-trimpath` builds for linux and darwin on amd64 and arm64 (plus
  linux/armv7), SHA-256 checksums, SPDX SBOMs, keyless Sigstore
  signatures on the checksums file and the container image,
  `ghcr.io/edward-b-1/opens3`.
- **Documentation:** user manual (`docs/MANUAL.md`), architecture and
  roadmap (`docs/PLAN.md`), on-disk format (`docs/FORMAT.md`), identity
  (`docs/IAM.md`, `docs/IAM-API.md`), admin API, console, notifications,
  governance, security policy and contribution guide.

### Not yet implemented

- Distributed mode and erasure coding (phase 4): OpenS3 0.1 is a
  single-node store on one local filesystem.
- SelectObjectContent, OIDC and LDAP identity providers (phase 3).
- Replication worker, bucket quotas, website endpoint serving,
  access-log delivery, audit log, inventory reports, notification
  targets other than webhooks (phase 2).
- DSSE-KMS is accepted and treated as SSE-KMS (single layer).

[Unreleased]: https://github.com/edward-b-1/opens3/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/edward-b-1/opens3/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/edward-b-1/opens3/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/edward-b-1/opens3/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/edward-b-1/opens3/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/edward-b-1/opens3/releases/tag/v0.1.0
