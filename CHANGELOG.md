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

### Security

Fixes for findings of a code review of v0.1.0. Upgrade if you run v0.1.0
with more than one identity.

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

[Unreleased]: https://github.com/edward-b-1/opens3/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/edward-b-1/opens3/releases/tag/v0.1.0
