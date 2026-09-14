# TODO

Open items agreed with the maintainer, worked through one at a time.
Move an item to "Done" with the commit that closed it.

## Open

1. **TLS.** The listener already serves HTTPS from `--tls-cert`/`--tls-key`
   (TLS 1.2 minimum, Go default ciphers, Secure cookies). Remaining, in
   order of value:
   1. `--tls self-signed`: generate and persist a key pair under the data
      root on first start, print the fingerprint, reuse afterwards.
   2. Automatic public certificates (`--tls acme --tls-domains ...
      --tls-email ...`) via Let's Encrypt HTTP-01 using
      `golang.org/x/crypto/acme/autocert`, cached under the data root.
      Decision pending: take the dependency, or leave public certificates
      to a reverse proxy.
   3. Reload certificate files when they change (no restart for external
      renewal tooling).
   4. Wildcard certificates for virtual-host bucket addressing
      (`*.s3.example.com`): document; support a provided wildcard
      certificate first; DNS-01 providers later if needed.
   5. Mutual TLS (client certificates), later.

2. **First tagged release (`v0.1.0`) and release tooling.** GOVERNANCE.md
   section 4 promises reproducible builds, Sigstore signatures and an SBOM;
   none of that exists yet. Needed: `make release` cross-compiling static
   binaries (linux/darwin, amd64/arm64) with `-trimpath` and a pinned
   toolchain, checksums, cosign signatures, SBOM (syft or `go version -m`
   based), the Docker image tagged and pushed, and a CHANGELOG. Reports
   (`API-COVERAGE`, `CONFORMANCE`, `AWSCLI`) should name the version.
3. **`opens3 fsck` / export tool.** docs/FORMAT.md promises recoverability
   without the server: walk the metadata, verify every referenced blob
   exists with the right size (and checksum where stored), report orphan
   blobs and dangling records, optionally repair, and export a bucket (or
   everything) to plain files. Read-only mode must work on a live data
   root's copy; repair needs the server stopped.
4. **Browser-level console smoke test.** The login-form field-name bug
   escaped every Go test because nothing exercises the page's JavaScript.
   Add a headless-browser test (Docker tier, alongside `make awscli`) that
   signs in, creates a bucket, uploads/downloads/deletes an object, creates
   a user and key, and changes settings.
5. **Audit log.** Structured JSON record per request (who, action, bucket,
   key, source address, status, latency, request id) to a file and/or
   webhook target; separate from access logging (PLAN phase 2).
6. **Phase 2 features** (docs/PLAN.md): replication worker, bucket quotas,
   website endpoint serving, access-log delivery, inventory reports, more
   notification targets, the remaining s3-tests failures.
7. **Erasure coding across local disks** (PLAN phase 4): the feature the
   MinIO audience most expects; a new blob backend with Reed-Solomon
   striping, bitrot detection, healing and disk-failure handling. Start
   only on a released, tested base.

8. **DSSE-KMS is accepted but single-layer.** `aws:kms:dsse` is treated as
   `aws:kms`; either implement the second AES layer or reject the value.

9. **Master key rotation and re-wrap.** `opens3 master-key rotate` (server
    stopped) adds a new key to the ring; `opens3 master-key rewrap` re-wraps
    every stored secret, KMS key and SSE-S3 data key under the newest key so
    old keys can be dropped. Later: an external key service (Vault or
    KMIP) so key custody is separate from the server.

## Deferred

- **MinIO admin API compatibility.** Serve MinIO's admin protocol under
  `/minio/admin/v3/` with MinIO's names and conventions so that `mc admin`
  and `madmin-go` scripts work unchanged against OpenS3. Part of the MinIO
  migration work, which is deferred; do not start without agreement.

## Done

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
