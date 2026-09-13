# OpenS3 web console

The console is a web UI built into the `opens3` binary and served at
`/console/` on the same port as the S3 API. It has no build step, no
external dependencies and no network access requirements: the whole
application (plain HTML, CSS and JavaScript) is embedded in the binary
with `go:embed` and works offline. Light and dark themes follow the
operating-system preference.

## Logging in

1. Start the server: `go run ./cmd/opens3 server --root /tmp/x` (or the
   `opens3` binary). Root credentials come from `OPENS3_ROOT_USER` /
   `OPENS3_ROOT_PASSWORD` (default `opens3admin` / `opens3admin`).
2. Open <http://localhost:9000/console/>.
3. Enter any OpenS3 access key and secret: the root credentials, an IAM
   user's key, or a service account. Temporary STS credentials cannot be
   used to log in.

What you can do depends on the policies attached to your identity: the
console never grants more than the S3 API would for the same key.

Keyboard: `/` focuses the filter box of the current page, `r` reloads
the page, `u` opens the upload dialog in the object browser, `Esc`
closes dialogs.

## What it does

| Page | Features |
|------|----------|
| Buckets | list, create (with versioning, Object Lock, tags), delete; per-bucket settings: versioning, tags, bucket policy editor with JSON validation and a public-access indicator, overview of encryption, ownership, public-access block, lifecycle/CORS/notification presence |
| Object browser | folder navigation by `/` delimiter with breadcrumbs, filter, multi-select, upload (multiple files, drag and drop, progress), "new folder", download, open inline (safe types only), delete (single, batch, specific versions), show versions and delete markers, details panel with metadata, tags, checksum, encryption, retention and legal hold |
| Identity | users (create, enable/disable, attach policies, rotate secret, delete), access keys and service accounts (with optional session policy, expiry, description; enable/disable; rotate; delete), groups (members, policies), policies (JSON editor; built-in policies are read-only) |
| Encryption keys | list, create and delete named keys of the built-in KMS (SSE-KMS) |
| Status | version, region, uptime, counts, disk usage, host details, endpoint list |

Users who are not administrators still see the Buckets, object browser and
Status pages and can manage their own access keys and service accounts.

## Security model

- **Authentication.** `POST /console/api/login` verifies the access key
  and secret against the IAM store (constant-time comparison) and then
  issues temporary credentials with the same STS machinery the S3 API
  uses (`AssumeRole`, 12 hours). The STS access key and session token are
  stored in a cookie named `opens3_console` that is `HttpOnly`,
  `SameSite=Strict`, scoped to `/console/`, and `Secure` when the server
  runs TLS (or sits behind a proxy sending `X-Forwarded-Proto: https`).
  The secret key of the session never leaves the server and the user's
  long-lived secret is never stored.
- **Every request re-resolves the session.** Disabling a user, deleting
  the session key, or the session expiring takes effect on the next
  request. Logging out deletes the STS key server-side and clears the
  cookie.
- **Authorisation.** Every action is evaluated by `iam.Store.Authorize`
  with the S3 action name (`s3:ListBucket`, `s3:GetObject`,
  `s3:PutObject`, `s3:DeleteObjectVersion`, `s3:PutBucketPolicy`, ...)
  and the bucket's owner, policy, ACL, public-access block and ownership
  setting, exactly like the S3 API. Identity management uses `admin:*`
  actions (`admin:ListUsers`, `admin:CreateKey`, `admin:PutPolicy`,
  `admin:ServerInfo`, ...) which only identity policies can grant; the
  built-in `consoleAdmin` policy grants all of them, `diagnostics`
  grants `admin:ServerInfo`. A session logged in as root acts as root.
  Service-account session policies are inherited by the console session
  and keep it narrowed.
- **CSRF.** Mutating requests (anything but GET/HEAD/OPTIONS, including
  login) must carry the header `X-OpenS3-Console: 1`, which cross-origin
  HTML forms cannot set, and are rejected when `Sec-Fetch-Site` reports a
  cross-site request or `Origin` does not match the request host. The
  `SameSite=Strict` cookie is a second, independent layer.
- **Browser hardening.** The app is served with a Content-Security-Policy
  (`default-src 'self'`, no inline scripts), `X-Frame-Options: DENY`,
  `X-Content-Type-Options: nosniff`, and API responses are `no-store`.
  Downloads are sent as attachments; the "Open" action only renders a
  short allow-list of inert content types inline (images, plain text,
  PDF, audio/video), never HTML or SVG.
- **No secrets in the UI.** Secret keys are shown exactly once when a key
  is created; KMS key material and session secrets are never returned.

## API

The console talks to `/console/api/...` with JSON. It is an internal
interface for the UI and may change between releases; use the S3 API and
the admin API for automation. Errors have the shape
`{"error": {"code": "AccessDenied", "message": "..."}}` with an HTTP
status matching the S3 error where one exists.

## Disabling

The console has no separate listener. To keep it off the public
internet, put the S3 endpoint behind a reverse proxy that does not
forward `/console/`, or bind the server to an internal address.

## Folders

S3 has no folders; the browser shows key prefixes as folders. Two
operations act on a whole prefix, as the MinIO console offered and the
AWS console offers for deletion:

- **Download folder** streams a zip archive of every current object under
  the prefix (`GET /console/api/buckets/{bucket}/zip?prefix=...`). Entry
  names are relative to the parent of the prefix. Objects the caller may not
  read and SSE-C objects (which need the customer key) are left out and
  listed in `OPENS3-SKIPPED.txt` inside the archive. "Download all" on a
  bucket's root zips the whole bucket.
- **Delete folder** (`POST /console/api/buckets/{bucket}/delete-prefix`)
  lists and deletes everything under the prefix in pages of a thousand. The
  console asks for confirmation with the object count and size from a dry
  run first. With "Show versions" on, every version and delete marker is
  removed permanently; otherwise current objects are deleted, which in a
  versioned bucket creates delete markers. Objects protected by object lock
  are reported as failures and left in place. Folders can also be selected
  with the checkboxes and removed with "Delete selected".
