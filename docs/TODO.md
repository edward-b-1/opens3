# TODO

Open items agreed with the maintainer, worked through one at a time.
Move an item to "Done" with the commit that closed it.

## Open

1. **Purge expired STS sessions.** `iam.Store.PurgeExpired` exists but nothing
   calls it, so every console login and AssumeRole leaves a dead key record
   in `meta/opens3.db` (`i/k/<accessKey>`). Expired keys are already
   rejected on use, so this is housekeeping, not security. Plan: a server
   goroutine that purges on startup and then hourly; console logout already
   deletes its own session key.
2. **Identity model: move to the AWS model (decided 13 Sep 2026).**
   - API credentials are generated key pairs by default: a 20-character key
     ID with an OpenS3-specific type prefix (`AKOS` long-lived, `ASOS`
     temporary) and a 40-character secret, shown once. Any chosen key ID of
     three or more characters stays accepted, so existing and migrated
     MinIO-style credentials keep working; users may hold several keys.
   - Console sign-in accepts a user name and console password only; key
     pairs are refused there with a message pointing at the API. The root
     account signs in with the root user name and root password from the
     environment.
   - A console password is optional per user: set in the create-user
     dialog or by an administrator later; users can change their own.
     Stored as PBKDF2-SHA256 with a per-user salt (standard library); never
     usable against the S3 API.
   - The create-user dialog and `opens3 admin user add` follow this: name,
     policies, optional console password, generated key pair (or a chosen
     one for MinIO-style migration).
   - Later, not now: MFA and password rules for console passwords.
3. **One admin action vocabulary.** The admin API uses MinIO-style names
   (`admin:AddUser`, `admin:RemoveUser`, ...); the console invented its own
   (`admin:CreateUser`, ...). A narrow operator policy must list both. Plan:
   a single table of actions shared by both, with the console realigned to
   the documented API names.

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

(none yet)
