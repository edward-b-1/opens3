# OpenS3 — Plan and Roadmap

OpenS3 is an Apache-2.0 licensed, S3-compatible object store written in Go.
The goal is feature parity with Amazon S3's public API and with the union of
the features offered by the open-source alternatives (MinIO before its
feature reduction, SeaweedFS, Garage, Ceph RGW, RustFS, Zenko CloudServer),
with no "enterprise only" tier.

## Decisions taken (12 September 2026)

- **Language: Go.** Chosen after the comparison in `SURVEY.md`. Go has no
  S3 server framework (Rust's `s3s` is the only one), so the API layer is
  our own; Versity's Apache-2.0 gateway is used as tested reference
  material, not as a base.
- **Licence: Apache-2.0**, contributions under a Developer Certificate of
  Origin (no CLA). See `../GOVERNANCE.md`.
- **Unique selling points** (detail in `SURVEY.md`, section 6):
  1. Verified fidelity: differential testing against real AWS S3, a
     machine-generated conformance matrix per release, the 2024–2026 API.
  2. In-place migration off MinIO (deferred; not started, MinIO not installed).
  3. A trust charter enforced by process: `GOVERNANCE.md`, `SECURITY.md`,
     stable branch, signed reproducible builds, documented on-disk format.
- **Phase 1 includes the conformance harness and the admin console**, since
  the first is the product and the second's absence is what started the
  MinIO exodus.

## Guiding principles

1. **Wire compatibility first.** Any S3 client (AWS SDKs, aws CLI, mc, rclone,
   s3cmd, Cyberduck, Veeam, Hadoop S3A, DuckDB, etc.) must work unchanged.
   Error codes, XML shapes, header names and edge-case semantics follow AWS.
2. **Conformance testing is the methodology.** We test with real SDKs
   (aws-sdk-go-v2 in-process), the aws CLI, and the Ceph `s3-tests` suite,
   not just hand-written assertions.
3. **Single binary, zero dependencies.** `opens3 server /data` must work with
   no external database. Embedded metadata store, filesystem data store.
4. **Everything is open.** IAM, policies, object lock, encryption, versioning,
   replication, lifecycle, notifications, metrics, admin API, console: all in
   the open-source product.
5. **Pluggable internals, stable interfaces.** Metadata KV, blob backend, KMS,
   notification targets and identity providers are interfaces so they can be
   swapped (bbolt → pebble, fs → erasure-coded cluster, local KMS → Vault).

## Architecture

```
                  ┌────────────────────────────────────────────────┐
  S3 clients ───▶ │ internal/s3api   HTTP router, XML, error codes │
                  │   ├─ internal/auth/sigv4   SigV4/presign/chunked│
                  │   ├─ internal/iam          users, keys, STS     │
                  │   └─ internal/policy       IAM + bucket policy  │
                  └──────────────┬─────────────────────────────────┘
                                 ▼
                  ┌────────────────────────────────────────────────┐
                  │ internal/object   object service (business     │
                  │   logic: versioning, multipart, lock, tags,    │
                  │   checksums, SSE, conditional writes, copy)    │
                  └───────┬──────────────────────┬─────────────────┘
                          ▼                      ▼
              ┌────────────────────┐   ┌──────────────────────┐
              │ internal/meta      │   │ internal/blob        │
              │ schema + KV layout │   │ fs backend, encrypt  │
              │ over internal/kv   │   │ wrapper, (EC later)  │
              └────────────────────┘   └──────────────────────┘
  Background: internal/lifecycle (expiry, abort MPU, transitions),
              internal/notify (events → webhook/nats/kafka/amqp/mqtt/redis),
              internal/replication (bucket replication to remote S3),
              internal/scrub (integrity scan / heal).
  Management: internal/admin (REST + `opens3 admin` CLI), Prometheus
              metrics, health endpoints, audit log, embedded console (later).
```

### Metadata layout (ordered KV)

| Key                                     | Value                         |
|-----------------------------------------|-------------------------------|
| `b/<bucket>`                            | Bucket record (all bucket configs) |
| `o/<bucket>/<key>\0<verkey>`            | ObjectVersion; `verkey` = hex(^seq) so newest sorts first |
| `n/<bucket>/<key>`                      | verkey of the `null` version (unversioned/suspended writes) |
| `u/<bucket>/<key>\0<uploadId>`          | Multipart upload record       |
| `ui/<bucket>/<uploadId>`                | key of upload (reverse index) |
| `p/<bucket>/<uploadId>/<part 5d>`       | Part reference                |
| `i/user/<accessKey>` …                  | IAM users, groups, policies, service accounts, STS tokens |

Listing latest objects = seek, take the first record per key, seek past the
key. Delimiter handling seeks to the successor of the common prefix, so
`ListObjectsV2` with a delimiter is O(number of results), not O(objects).

### Data layout (filesystem backend)

`<root>/data/<bucket>/<xx>/<blobId>` — one immutable blob per uploaded part
or object. Multipart completion references the part blobs in place (no
re-copy). Blobs are written to `<root>/tmp` then fsync'd and renamed.
SSE encrypts blobs in 64 KiB AES-256-GCM chunks so range reads stay cheap.

## Feature matrix

Status: `[ ]` planned, `[~]` in progress, `[x]` implemented and covered by tests. Everything below is a target for the open-source build; there is no other build.

### S3 API — bucket level
- [x] ListBuckets, CreateBucket, DeleteBucket, HeadBucket, GetBucketLocation
- [x] ListObjects (v1), ListObjectsV2, ListObjectVersions, ListMultipartUploads
- [x] Versioning (Enabled/Suspended), delete markers, `null` version semantics
- [x] Bucket tagging, policy (+ PolicyStatus), ACL (canned + grants), CORS
- [x] Lifecycle configuration (expiration, noncurrent, abort-incomplete-MPU, delete-marker cleanup, transitions recorded as storage-class change)
- [x] Default encryption (SSE-S3, SSE-KMS)
- [x] Object Lock configuration (default retention), retention & legal hold on objects
- [x] Notification configuration + event delivery
- [x] Website, logging, replication, public-access-block, ownership-controls, request-payment, accelerate, metrics/analytics/inventory/intelligent-tiering configuration (stored + returned)
- [x] DeleteObjects (multi-object delete, quiet mode)

### S3 API — object level
- [x] PutObject, GetObject, HeadObject, DeleteObject, CopyObject
- [x] Range, conditional GET (If-Match/None-Match/Modified-Since/Unmodified-Since)
- [x] Conditional writes (If-None-Match: *, If-Match on PUT/Complete)
- [x] Multipart: Create/UploadPart/UploadPartCopy/Complete/Abort/ListParts
- [x] Checksums: CRC32, CRC32C, SHA1, SHA256, CRC64NVME; full-object and composite; trailing checksums (aws-chunked)
- [x] Object tagging, ACL, metadata, storage class, response-* header overrides
- [x] GetObjectAttributes
- [x] SSE-S3, SSE-KMS (local KMS), SSE-C (incl. copy source SSE-C)
- [x] POST object (browser form upload with policy)
- [x] Presigned URLs (GET/PUT/any, SigV4 query auth)
- [ ] SelectObjectContent (CSV/JSON/Parquet) — phase 3
- [x] RestoreObject (accepted for archive classes; data is never archived so restore completes immediately)

### Auth / IAM
- [x] SigV4 header + query (presigned), aws-chunked signed streaming, unsigned-payload, trailers
- [x] SigV2 (legacy clients)
- [x] Root credentials, IAM users, groups, policies (AWS policy language with conditions & variables), service accounts, STS AssumeRole (temporary credentials)
- [x] Bucket policies + ACL evaluation (deny > allow), anonymous/public access
- [ ] OIDC / LDAP identity providers — phase 3

### Operations
- [x] Single binary `opens3 server`, YAML/env/flag config, TLS
- [x] AWS IAM Query API (`aws iam`, boto3) and STS GetCallerIdentity (`docs/IAM-API.md`)
- [x] Admin REST API (`/opens3/admin/v1`) + `opens3 admin` CLI
- [x] Prometheus metrics, health/readiness endpoints, structured logs
- [ ] Audit log (phase 2)
- [x] Lifecycle worker, notification dispatcher (webhook; NATS/Kafka/AMQP/MQTT/Redis targets phase 2)
- [ ] Bucket replication worker (async to remote S3) — phase 2
- [ ] Bucket quotas — phase 2
- [x] Embedded web console (`/console/`, phase 1 per the survey)
- [ ] Distributed mode: erasure coding (Reed-Solomon), bitrot detection, self-healing, rolling upgrades — phase 4
- [ ] Tiering to remote S3 / cold storage — phase 4
- [ ] S3 Tables / Iceberg catalog — phase 5

## Phases

| Phase | Scope | Status |
|-------|-------|--------|
| 0 | Plan, scaffolding, CI gate, conformance harness | done |
| 1 | Core object store: buckets, objects, versioning, multipart, SigV4, IAM/policies, listing, tagging, ACL, lifecycle/CORS/policy configs, SSE, object lock, checksums, notifications (webhook), admin API, metrics, console, conformance harness | done 12–13 Sep 2026: 600/637 s3-tests, aws-sdk-go-v2 suite, AWS CLI suite 442/442, AWS IAM API, console |
| 2 | Replication worker, bucket quotas, website endpoint serving, access-log delivery, audit log, inventory reports, more notification targets, remaining s3-tests failures, `opens3 fsck`/export tool | next |
| 3 | SelectObjectContent, OIDC/LDAP, console UI | |
| 4 | Distributed/erasure-coded backend, healing, tiering | |
| 5 | S3 Tables/Iceberg, batch operations | |

## Testing strategy

- `go test ./...` — unit tests for every package plus in-process integration
  tests that drive a real `aws-sdk-go-v2` S3 client against `httptest`.
- `make conformance` — runs the Ceph `s3-tests` suite (boto3) against a
  local server in Docker and regenerates `docs/CONFORMANCE.md`; the list of
  known-failing tests lives in `tests/s3tests/known-failures.txt` with a
  reason per entry and must only shrink.
- `make awscli` — the AWS CLI v2 end-to-end suite (pinned `amazon/aws-cli`
  image, one container per run) including transport scenarios; regenerates
  `docs/AWSCLI.md`.
- `make console-test` — ESLint and a Playwright browser smoke test of the
  console in Docker (`make lint-js` for the lint alone).
- `make rotation-test` — the master key rotation lifecycle in Docker:
  server and `opens3 master` from the Dockerfile, encrypted records written
  and verified by `examples/python/encrypted_data.py` across rotate, retire,
  and moves between the key file and `OPENS3_MASTER_KEY`.
- `go run ./tools/apicoverage` — regenerates `docs/API-COVERAGE.md`, the
  per-operation implementation matrix.
