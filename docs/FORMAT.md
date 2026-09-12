# On-disk format

Format version: **1**. This document is the contract that lets a user
recover their data with ordinary tools if OpenS3 is unavailable, and that
lets future versions read old data. Any change bumps the version and adds a
migration note here before it ships (GOVERNANCE.md, section 3).

## Layout of the data root

```
<root>/
  meta/opens3.db        bbolt database: every metadata record (see below)
  data/<bucket>/<xx>/<blobId>   one immutable file per uploaded part/object
  tmp/<blobId>          in-flight blobs; removed on commit or at startup
```

`<xx>` is the first two hex characters of the 32-hex-character blob ID, so
each bucket directory fans out into at most 256 shards.

Blobs are written to `tmp/`, `fsync`ed, renamed into place, and the parent
directory is `fsync`ed. A blob is therefore either complete or absent.

## Object data

- A single-part object is exactly one blob containing the bytes as
  uploaded (no framing, no compression).
- A multipart object references one blob per part in `Parts[]` of its
  metadata record; the object's bytes are the concatenation in part order.
  Completing an upload does not rewrite data.
- **Encrypted blobs** (SSE-S3, SSE-KMS, SSE-C) are AES-256-GCM in
  64 KiB chunks: for plaintext chunk *i* the file holds
  `ciphertext(64 KiB or the tail) || 16-byte GCM tag`. The GCM nonce is
  `part.Nonce (8 random bytes) || uint32 big-endian chunk index`. The
  per-object data key is stored wrapped in the metadata record
  (`sse.w`), never on the data path. Ciphertext length is
  `plain + 16 * ceil(plain / 65536)`.

## Metadata database

`meta/opens3.db` is a single bbolt B+tree database with one top-level bucket
named `kv`. Keys are byte strings; values are JSON with short field names
(see `internal/meta/types.go` for the exact schema and `iam/types.go`,
`kms/kms.go` for identity records). Key namespaces:

| Key | Value |
|---|---|
| `b/<bucket>` | Bucket record: owner, region, versioning state, object lock, tags, policy JSON, ACL, default encryption, public access block, ownership, and raw XML of stored configurations (lifecycle, CORS, notification, website, logging, replication, ...). |
| `o/<bucket>/<key>\0<verkey>` | One object version or delete marker. `verkey` is the 16-hex-digit representation of `^seq` so that the newest version sorts first for a key. `seq` is a nanosecond timestamp made strictly increasing within a process. |
| `n/<bucket>/<key>` | The `verkey` of the key's `null` version (written while versioning is off or suspended). |
| `u/<bucket>/<key>\0<uploadId>` | Multipart upload record (attributes to apply on completion, checksum algorithm, wrapped SSE key). |
| `ui/<bucket>/<uploadId>` | Reverse index: the key of an upload. |
| `p/<bucket>/<uploadId>/<part 5 digits>` | Uploaded part: blob ID, size, MD5, checksum, nonce. |
| `i/u/<name>` | IAM user. |
| `i/k/<accessKey>` | Access key: owner, kind (user / service / sts), secret wrapped with the KMS master key, session policy, session token, expiry. |
| `i/g/<name>` | IAM group. |
| `i/p/<name>` | Named policy document. |
| `i/kms/<id>` | KMS named key material, wrapped with the master key. |

Object keys may contain any byte except NUL (`\0`), which separates the key
from the version suffix. Bucket names never contain `/`.

Version IDs are 32 hex characters: the 16-hex `seq` followed by 16 random
hex characters; the record key is derived from the ID without a lookup.
The literal string `null` is the version ID of null versions.

## Keys and secrets

- The **KMS master key** is `SHA-256("opens3-sse-v1:kms-master:" || material)`
  where `material` is `OPENS3_MASTER_KEY`, or `derived:<root password>` when
  unset. Everything wrapped (`i/k/*.sw`, `i/kms/*.w`, `sse.w` for SSE-S3)
  depends on it. **Back it up.** Losing it makes encrypted objects and
  stored secrets unrecoverable; the S3 API and plaintext objects keep
  working.
- SSE-S3 data keys are wrapped with the master key with the bucket name as
  AAD; SSE-KMS with the named key material and the encryption context as
  AAD; SSE-C with the customer's key and the AAD `ssec`.

## Recovery with ordinary tools

Plaintext objects can be reassembled without OpenS3: read the object record
from the database (`bbolt` CLI or any Go program using `go.etcd.io/bbolt`),
then `cat data/<bucket>/<xx>/<blob>` for each part in order. The metadata
JSON is readable by eye; `internal/meta/types.go` gives the field names.
A `opens3 fsck`/export tool is planned for phase 2.
