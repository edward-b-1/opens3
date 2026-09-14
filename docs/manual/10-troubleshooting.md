# 10. Troubleshooting

**`curl http://host:9000/` returns AccessDenied.** Expected: the root path
is the ListBuckets operation, which S3 never allows anonymously. Use a
signed client, or the console at `/console/`. Health probes at
`/opens3/health/ready` return 200 with an empty body.

**`ERR_CERT_AUTHORITY_INVALID` in the browser, or `tls: unknown certificate`
in the server log.** The client does not trust the server's certificate,
which is normal for a self-signed one. Trust it on the client (chapter 2)
or click through the browser warning. The log line comes from the client
telling the server it refused the certificate.

**`client sent an HTTP request to an HTTPS server` in the log.** Something
used `http://` against a TLS port. Since TLS item 4 the server redirects
such requests; the message means the client did not follow. Fix the
client's endpoint URL.

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
way to read the data without it.

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
