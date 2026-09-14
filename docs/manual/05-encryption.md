# 5. Encryption

## Data in transit

Chapter 2 covers TLS. Without it, credentials, secrets and data cross the
network in the clear; the console warns when it is used over plain HTTP
from another machine.

## Data at rest: the four states

An object is stored in one of four states, chosen per request or by the
bucket's default. **Nothing is encrypted unless asked**; that is a
deliberate choice, unlike AWS since 2023.

| State | Request header | Who holds the key | Reading it back |
|---|---|---|---|
| Off | none | nobody; bytes stored as uploaded | plain GET |
| SSE-S3 | `x-amz-server-side-encryption: AES256` | the server: the object's data key is wrapped under the master key | plain GET; the server decrypts |
| SSE-KMS | `x-amz-server-side-encryption: aws:kms` plus optional `x-amz-server-side-encryption-aws-kms-key-id` | the server: wrapped under a named encryption key | plain GET; the server decrypts |
| SSE-C | `x-amz-server-side-encryption-customer-algorithm: AES256` plus the key and its MD5 | the client, on every request; the server keeps nothing | GET must present the same key |

In every encrypted state each object has its own random data key, and the
bytes are encrypted with AES-256-GCM in 64 KiB chunks so ranged reads stay
cheap. The metadata record stores which state and which key were used, so
a client needs no bookkeeping to read data back, except with SSE-C.

`aws:kms:dsse` (AWS's double encryption) is accepted but treated as
ordinary SSE-KMS.

## The master key

The master key wraps every SSE-S3 data key, every named encryption key,
and every stored access-key secret. It is generated on first start as 32
random bytes in `<root>/meta/master.keys`, a file readable only by the
server's user. Rotation is planned as adding a key to that file; older keys
stay to unwrap older data.

Alternatively set `OPENS3_MASTER_KEY` to at least 32 characters of random
material, for example from a secret manager injected at boot. The server
refuses short material so a password cannot be used. A key-check value in
the database makes the server refuse to start with a key that does not
match the data.

**Back the master key up, separately from the data.** Without it, every
encrypted object and every stored access-key secret is unrecoverable, while
plaintext objects are unaffected. Chapter 8 covers backups.

## Named encryption keys (SSE-KMS)

The Encryption keys page in the console, `opens3 admin kms`, and the admin
API create and delete named keys. `opens3-default-key` exists from the
first start and is used when a request says `aws:kms` without a key name.

Named keys let you bind data to a boundary: one key per tenant or project.
Deleting a key makes every object wrapped under it unreadable, permanently
and in every backup, which is the intended way to retire a tenant's data.
Nothing else needs a named key; SSE-S3 covers "just encrypt it".

## Bucket defaults

A bucket can encrypt everything uploaded without explicit headers. In the
console: bucket Settings, Default encryption. With the CLI:

```sh
aws s3api put-bucket-encryption --bucket b --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"aws:kms","KMSMasterKeyID":"payroll"}}]}'
```

Existing objects are not re-encrypted. A request with its own encryption
headers overrides the default.

## SSE-C: keys the server never sees

SSE-C cannot be a bucket default, because a default is what applies when
the client sends nothing, and with SSE-C there is no key to apply. What you
can do is require it, with a bucket policy that denies uploads lacking the
header:

```json
{"Version":"2012-10-17","Statement":[{
  "Effect":"Deny","Principal":"*","Action":"s3:PutObject",
  "Resource":"arn:aws:s3:::mybucket/*",
  "Condition":{"Null":{"s3:x-amz-server-side-encryption-customer-algorithm":"true"}}
}]}
```

The mirror image, forbidding SSE-C, denies when the key is present. AWS
disables SSE-C on new buckets since 2026 because attackers used it to
encrypt victims' data with keys only they held; consider forbidding it on
buckets that do not need it. Send SSE-C only over TLS.

## What at-rest encryption protects

Be precise about the threat, because server-side encryption is often
credited with more than it delivers:

- It protects storage that leaves the server without the master key: a
  disk sent for replacement, a decommissioned machine, a copy of the object
  files, a volume snapshot. With the key file in the data root, that means
  the object files copied without the metadata directory; with the key in
  the environment, the whole data directory.
- It does not protect against an attacker who controls the running server,
  who has the keys. That is equally true of AWS; AWS's advantage is that
  its keys live in a separate audited service. An external key service for
  OpenS3 is planned.
- Deleting a named key destroys its data everywhere at once, which no
  amount of disk scrubbing achieves.
- SSE-C protects even against the server, at the cost of the client
  managing keys.
