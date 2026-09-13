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
