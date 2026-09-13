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
2. **Identity model: user names versus access key pairs.** The console's
   "create user" dialog follows the MinIO convention (user name = first
   access key, secret typed in). The store already has the AWS shape (user
   record + generated key records). Decide the default presentation and
   whether to add a separate console password. Discussion in progress; see
   the notes below before changing code.
3. **One admin action vocabulary.** The admin API uses MinIO-style names
   (`admin:AddUser`, `admin:RemoveUser`, ...); the console invented its own
   (`admin:CreateUser`, ...). A narrow operator policy must list both. Plan:
   a single table of actions shared by both, with the console realigned to
   the documented API names.

## Notes on item 2

- Wire compatibility is unaffected by the choice: S3 clients only ever
  present an access key ID and a secret; user names never go over the wire.
- AWS: a user is an identity; it authenticates with up to two generated
  access keys (20-character ID beginning `AKIA`, 40-character secret) and,
  for the console, a separate password. MinIO: the user name is the access
  key, the "password" is its secret, service accounts are generated pairs.
- OpenS3 accepts any access key string of 3+ characters, so MinIO-style
  users (name == key) and AWS-style users (name + generated keys) can
  coexist; moving the default to generated pairs is backwards compatible.

## Done

(none yet)
