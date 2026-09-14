# 6. The console

The console is at `/console/` on the server's address. It is built into
the binary, needs no separate service, and every action goes through the
same authorisation as the API.

How it stays safe: signing in creates a temporary credential and stores
only that in a browser cookie that scripts cannot read; every request is
checked against the cookie and against a header that other websites cannot
set, so a malicious page cannot act on your behalf; and the console never
receives or displays a secret except at the moment a key is created.

## Signing in

Enter a user name and console password. Root uses `OPENS3_ROOT_USER` and
`OPENS3_ROOT_PASSWORD`. Other users need a console password set by an
administrator (Identity page, `aws iam create-login-profile`, or
`opens3 admin user password`). Access key pairs are refused at the console;
they are for the API.

A session lasts twelve hours and is a temporary credential like any other;
"Log out" ends it immediately.

## Pages

- **Buckets**: list, create (with versioning, Object Lock, tags), delete,
  and Settings per bucket: versioning, default encryption, tags, the
  policy editor with validation, and deletion.
- **Object browser**: folders (prefixes), upload by dialog or drag-and-drop
  with progress, download, delete, "Show versions", a details panel with
  metadata, tags, checksum and encryption, and file-type icons. Folders can
  be downloaded as a zip archive or deleted recursively, with a count and
  size shown before confirmation.
- **Identity** (administrators): users, access keys and service accounts
  with session policies and expiry, groups, policies with a JSON editor.
  Non-administrators see "My access keys" instead.
- **Encryption keys** (administrators): named keys for SSE-KMS.
- **Status**: version, region, buckets, disk usage.

## Settings and shortcuts

The gear button (or `,`) opens per-browser settings stored in local
storage: density (comfortable or compact), theme, date and size formats,
rows per page, folder-marker visibility. `/` focuses the filter, `r`
reloads, `u` opens upload, `Esc` closes dialogs. "Change password" in the
header changes your own console password.

## Plain HTTP

A banner appears when the console is used over `http://` from any address
other than the local machine, because passwords and secrets would travel
unencrypted. Chapter 2 covers TLS.
