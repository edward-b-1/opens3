# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's private
vulnerability reporting: https://github.com/edward-b-1/opens3/security/advisories/new
You will receive an acknowledgement within three working days and a fix
timeline within ten.

Do not report security issues in public issues or pull requests.

## What happens next

1. The report is triaged and reproduced.
2. A fix is developed on a private branch and backported to every supported
   release line.
3. A patch release is published together with an advisory describing the
   affected versions, impact, fix and any mitigation. A CVE is requested
   for anything that affects confidentiality, integrity or availability.
4. The reporter is credited unless they ask not to be.

## Advisories

### OPENS3-2026-001: authorisation weaknesses in v0.1.0

**Affected:** v0.1.0. **Fixed in:** v0.1.1. Upgrade; there is no
configuration-only mitigation for the first two items.

A code review of v0.1.0 found these authorisation defects, all fixed in
v0.1.1 (`CHANGELOG.md`, section 0.1.1, lists every change):

1. **Credentials narrowed by a session policy could escalate.** A service
   account key carrying a session policy, or an STS session created with
   one, could create a permanent access key or a console password for its
   user through the IAM API without any permission check, obtaining the
   user's full rights. A session derived from root could mint a permanent
   administrative key. Deployments that use only root and full-rights
   users are not affected.
2. **Unsigned headers were honoured on signed requests.** The holder of a
   presigned upload URL could add `x-amz-copy-source` to copy any object
   the signer could read into the destination, or add ACL and tagging
   headers. Deployments that hand out presigned URLs to untrusted parties
   are affected.
3. **Policy evaluation gaps.** Negated condition operators did not match
   when the key was absent; policy resource ARNs normalised `..` and
   repeated slashes in object keys; attribute headers on uploads bypassed
   their own permissions; copy sources were authorised without their
   tags; batch delete required a bucket-level permission.
4. **Forwarded headers were trusted from any client**, so a plain-HTTP
   client could satisfy `aws:SecureTransport`, and SSE-C keys were
   accepted over plain HTTP. v0.1.1 introduces `OPENS3_TRUSTED_PROXIES`.
5. **Two races and a lifecycle guard**: a part re-uploaded during
   multipart completion could corrupt the object; deleting and recreating
   a bucket could lose new files; lifecycle expiry could delete a fresh
   same-content replacement.

Object files were also created world-readable on the host; v0.1.1
creates them owner-only (existing files are not changed).

### OPENS3-2026-002: further authorisation gaps in v0.1.1

**Affected:** v0.1.0, v0.1.1. **Fixed in:** v0.2.0.

1. **Restricted credentials could still escalate by rotating a key.**
   v0.1.1 stopped credentials narrowed by a session policy from creating
   keys, but the admin API's key rotation was not covered: such a
   credential allowed `iam:UpdateAccessKey` could rotate its user's
   unrestricted key and use the new secret with full rights. Deployments
   that use session policies and grant `iam:*` inside them are affected.
2. **Copied tags bypassed the tagging permission**, and Object Lock
   settings on an upload could ride on a bucket ACL grant.
3. **Operations were not bound to the authorised record**: a copy, or a
   tag/ACL/retention/legal-hold update, authorised against one object
   version could act on a replacement written in the meantime; a write
   authorised against a bucket could land in a bucket of the same name
   created after the original was deleted. Microsecond windows, but real.

## Supported versions

Each minor release receives security fixes for at least twelve months after
the next minor release ships (see GOVERNANCE.md, section 3).

## Design commitments

- No default credentials: the server refuses to start without operator
  supplied root credentials.
- No hard-coded tokens for internal or administrative endpoints.
- All administrative APIs require Signature V4 authentication.
- Releases are built reproducibly and signed; see GOVERNANCE.md, section 4.
