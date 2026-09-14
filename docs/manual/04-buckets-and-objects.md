# 4. Buckets and objects

Everything here is the standard S3 API, so any client's documentation
applies. This chapter records what OpenS3 does with each feature.

## Buckets

Names follow the AWS rules: 3 to 63 characters, lower-case letters, digits,
dots and hyphens, not an IP address. `opens3` and `console` are reserved.
Buckets belong to the user who created them; the owner has full control of
the bucket's configuration.

Object ownership defaults to `BucketOwnerEnforced`, which disables ACLs, as
on AWS since 2023. Set `OPENS3_DEFAULT_OBJECT_OWNERSHIP=ObjectWriter` or use
`--object-ownership` at creation to enable ACLs for legacy tools.

Deleting a bucket requires it to be empty of objects and versions;
in-progress multipart uploads are aborted automatically. The admin API can
force-delete a bucket with its contents.

## Objects

Keys are any UTF-8 string up to 1024 bytes without a NUL byte. Unlike
MinIO, `a/b` and `a/b/c` can both exist. Object size is limited to 5 TiB;
single uploads up to 5 GiB; multipart parts 5 MiB to 5 GiB, up to 10,000
parts. User metadata (`x-amz-meta-*`) up to 2 KB; up to 10 tags.

Every object has an ETag (the MD5 for single uploads, MD5-of-MD5s with a
part count for multipart) and optionally an additional checksum in one of
CRC32, CRC32C, SHA-1, SHA-256 or CRC64-NVMe, computed on upload and
verifiable on download (`--checksum-mode ENABLED`). Trailing checksums and
full-object checksums for multipart uploads are supported.

Conditional operations work as on AWS: `If-Match` and `If-None-Match` on
reads, `If-None-Match: *` and `If-Match` on writes for create-only and
compare-and-swap semantics, and `If-Match` on deletes.

Range reads, part-number reads, `GetObjectAttributes`, copy with metadata
or tagging replacement, and the browser POST upload with a signed policy
all work.

## Versioning

```sh
aws s3api put-bucket-versioning --bucket b --versioning-configuration Status=Enabled
```

Once enabled, every write creates a new version and a delete inserts a
delete marker; the object disappears from listings but every version
remains, listed with `list-object-versions` and retrievable or deletable by
`--version-id`. Suspending stops new versions; writes then replace the
special `null` version. Versioning cannot be suspended while Object Lock is
on.

## Object Lock

Create the bucket with `--object-lock-enabled-for-bucket` (this enables
versioning), or enable it later on a versioned bucket. Then:

- **Retention** per version: `GOVERNANCE` (deletable by users holding
  `s3:BypassGovernanceRetention` who send the bypass header) or
  `COMPLIANCE` (nobody, not even root, until the date passes). A bucket
  default retention applies to every new version.
- **Legal hold**: an on/off flag that blocks deletion indefinitely.

Locked versions refuse deletion with `AccessDenied`; a delete without a
version ID still creates a delete marker, as on AWS.

## Lifecycle

Rules on a bucket expire current objects after N days or on a date, expire
noncurrent versions, remove expired delete markers, abort incomplete
multipart uploads, and record storage-class transitions. Filters by prefix,
tags and object size. The worker runs every `OPENS3_LIFECYCLE_INTERVAL`
(default hourly) and emits `s3:LifecycleExpiration:*` events. Transitions
change the recorded storage class only; there is no tiered storage yet, so
data stays where it is and `RestoreObject` completes immediately.

## Tags, CORS, website, logging, replication

Bucket and object tags work as on AWS and are usable in policy conditions
(`s3:ExistingObjectTag/<key>`, `aws:ResourceTag/<key>`) and lifecycle
filters.

CORS rules are enforced: browsers' preflight requests are answered from the
bucket's configuration, and matching responses carry the CORS headers.

Website, logging, replication, inventory, metrics, analytics, accelerate
and request-payment configurations are stored and returned exactly as
written, so tooling that sets them succeeds, but they have no effect yet:
there is no website endpoint, no log delivery and no replication worker.
`../API-COVERAGE.md` marks these "partial".

## Public access and ACLs

Block Public Access has the four AWS settings per bucket. `BlockPublicPolicy`
rejects a public bucket policy at upload; `RestrictPublicBuckets` ignores a
public policy for anonymous callers; `BlockPublicAcls` rejects public ACLs;
`IgnorePublicAcls` ignores existing ones.

ACLs (canned and explicit grants, bucket and object level) are honoured
only when the bucket's ownership setting allows them. Grants to the
`AllUsers` and `AuthenticatedUsers` groups make data public.

## Locking yourself out

A bucket policy can deny the owner access to the policy itself. AWS asks
for confirmation with the `x-amz-confirm-remove-self-bucket-access` header
before it applies such a policy against the account owner; OpenS3 does the
same. Without the header, root can always read, replace or delete a bucket
policy. With it, root is bound like everyone else, and the only way out is
to delete the bucket through the admin API.
