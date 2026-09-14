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
