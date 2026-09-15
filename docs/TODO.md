# TODO

Open items agreed with the maintainer, worked through one at a time.
Move an item to "Done" with the commit that closed it.

Status (14 Sep 2026, v0.2.0): everything below "Open" is deferred by
decision, not dropped; the next one to pick up is the audit log (item
4), then phase 2 features by demand, then the erasure-coding design.

## Open

1. **TLS.** The listener already serves HTTPS from `--tls-cert`/`--tls-key`
   (TLS 1.2 minimum, Go default ciphers, Secure cookies). Remaining, in
   order of value:
   1. Automatic public certificates (`--tls acme --tls-domains ...
      --tls-email ...`) via Let's Encrypt HTTP-01 using
      `golang.org/x/crypto/acme/autocert`, cached under the data root.
      Decision pending: take the dependency, or leave public certificates
      to a reverse proxy.
   2. Wildcard certificates for virtual-host bucket addressing
      (`*.s3.example.com`): document; support a provided wildcard
      certificate first; DNS-01 providers later if needed.
   3. Mutual TLS (client certificates), later.

4. **Audit log.** Structured JSON record per request (who, action, bucket,
   key, source address, status, latency, request id) to a file and/or
   webhook target; separate from access logging (PLAN phase 2).
5. **Phase 2 features** (docs/PLAN.md): replication worker, bucket quotas,
   website endpoint serving, access-log delivery, inventory reports, more
   notification targets, the remaining s3-tests failures.
6. **Erasure coding across local disks** (PLAN phase 4): the feature the
   MinIO audience most expects; a new blob backend with Reed-Solomon
   striping, bitrot detection, healing and disk-failure handling. Start
   only on a released, tested base.

7. **DSSE-KMS is accepted but single-layer.** `aws:kms:dsse` is treated as
   `aws:kms`; either implement the second AES layer or reject the value.

8. **External key service.** Vault or KMIP as the master key source so
    key custody is separate from the server (rotation and re-wrap are
    done; see Done).

## Deferred

- **MinIO admin API compatibility.** Serve MinIO's admin protocol under
  `/minio/admin/v3/` with MinIO's names and conventions so that `mc admin`
  and `madmin-go` scripts work unchanged against OpenS3. Deferred; do not
  start without agreement.
- **In-place MinIO data migration** (reading MinIO's on-disk format) is
  retired in favour of the API-based procedure in the manual, chapter 11,
  with `examples/migrate/` and `make migration-test` (MinIO built from
  source in Docker; nothing pulled from MinIO's registries). A future
  `opens3 migrate` command may wrap that procedure in one step (objects,
  tags, identities, verification); the on-disk converter is not planned.

## Done

- `opens3 fsck check | repair` (missing and mismatched files, orphan
  files, dangling and missing pointers and indexes, orphan part records,
  interrupted bucket deletions, records without a bucket; `--verify`
  reads and hashes every object) and `opens3 export` (plain files plus a
  manifest, decrypting SSE-S3/KMS, all versions on request).

- TLS certificate reload without restart: files polled every minute,
  `SIGHUP` for an immediate reload, self-signed certificates regenerated
  live when expired; a bad pair is refused and the current certificate
  kept; expiry gauge and reload counter metrics.

- `--tls self-signed` / `OPENS3_TLS=self-signed`: a certificate generated
  under `<root>/tls` on first start (825 days, replaced when expired),
  fingerprint and names logged, missing names reported; the AWS CLI
  transport scenarios run against it.

- **v0.1.0 released** (14 Sep 2026): the GoReleaser workflow ran on a
  rehearsal tag first (`v0.0.1-rc1`, tag deleted afterwards) and then on
  `v0.1.0`; archives, checksums, SBOMs, the keyless checksums signature
  and the signed multi-arch `ghcr.io/edward-b-1/opens3` image were
  verified with cosign from a download. Pre-release tags are marked as
  such and do not move the image's `latest` tag.

- Master key rotation: `opens3 master status | rotate | rewrap | retire`
  (server stopped) with re-wrapping of the key-check value, named keys,
  access-key secrets and SSE-S3 data keys of objects and multipart
  uploads; moves between the key file and `OPENS3_MASTER_KEY` in either
  direction. Older keys stay in the ring until `retire` so pre-rotation
  metadata backups remain restorable. `make rotation-test` runs the whole
  lifecycle in Docker against boto3-written encrypted data.

- Console lint (`make lint-js`, ESLint no-undef) and Playwright smoke test
  (`make console-test`, Chromium/Firefox/WebKit, fails on any page error).

- Console: bucket default encryption (none / SSE-S3 / SSE-KMS with a
  named key). Decision: objects are not encrypted by default.

- AWS IAM Query API on the S3 endpoint (`docs/IAM-API.md`): users, access
  keys, groups, policies, attachments, login profiles, account summary,
  STS GetCallerIdentity; AWS action vocabulary (`iam:*`, `kms:*`, `sts:*`,
  `opens3:*`) across the admin API and console, MinIO `admin:*` names
  rejected with the AWS equivalent.

- TLS item 4: plain-HTTP connections on the TLS port are redirected to
  https (first-byte sniff, 301 for GET/HEAD, 308 otherwise) and
  `Strict-Transport-Security` is sent over TLS (`OPENS3_NO_HSTS=1`
  disables it).

- Identity model moved to the AWS model: generated 20/40-character key
  pairs (never chosen in the console), optional PBKDF2 console passwords,
  console sign-in by user name and password only, root via the configured
  root credentials, `opens3 admin user add/password`.

- Purge expired temporary credentials: hourly sweep and on startup
  (`OPENS3_PURGE_INTERVAL`), metric `opens3_iam_expired_credentials_purged_total`.
