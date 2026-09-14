# 11. Migrating from MinIO

OpenS3 speaks the same S3 API as MinIO, so the way to move is to run both
servers side by side, copy the objects through the API, recreate the
identities, verify, and repoint clients. Nothing is read from MinIO's
disks directly: its on-disk format is its own, and copying through the
API works for every MinIO version and layout, decrypts objects MinIO
encrypted, and can be rehearsed and repeated. What it cannot carry is
described at the end.

Tools used: MinIO's own `mc` for the copy and the identity export, the
two scripts in `examples/migrate/` for what no tool does, and the AWS
CLI or `aws iam` for checks. `rclone sync --metadata` works in place of
`mc mirror` if you prefer it.

## 1. Stand OpenS3 up beside MinIO

Install OpenS3 (chapter 1) on the same network, with its own data
directory and root credentials, and give `mc` an alias for each server:

```sh
mc alias set minio  http://minio:9000  MINIO_ROOT_USER  MINIO_ROOT_PASSWORD
mc alias set opens3 http://opens3:9000 OPENS3_ROOT_USER OPENS3_ROOT_PASSWORD
```

With `--tls self-signed` on OpenS3, point `mc` at the certificate the
server generated: `mc --insecure` is the quick way, and `MC_CA_BUNDLE` or
importing `<root>/tls/tls.crt` into the trust store the proper one.

## 2. Copy the objects

```sh
for b in $(mc ls minio | awk '{print $NF}' | tr -d /); do
  mc mb --ignore-existing "opens3/$b"
  mc mirror --preserve --overwrite "minio/$b" "opens3/$b"
done
```

`--preserve` keeps content type and user metadata (it also records
MinIO's file attributes in a `mc-attrs` metadata entry, which is
harmless). Large objects are re-uploaded as multipart uploads, so their
ETags differ from MinIO's while the content is identical. The command is
safe to repeat: a second run copies only what changed, which is how the
final catch-up before the cutover is done.

Object tags are not copied by `mc mirror` or `rclone`. Copy them
afterwards:

```sh
examples/migrate/copy_tags.py --src http://minio:9000 --src-key K --src-secret S \
                              --dst http://opens3:9000 --dst-key K --dst-secret S
```

Bucket configuration, such as versioning, lifecycle rules, policies and
default encryption, is not copied by the mirror either. Enable versioning
on the destination buckets that need it before the copy, and apply the
rest with `aws s3api put-bucket-...` or in the console.

## 3. Recreate users, groups and policies

MinIO does not export secret keys, so users get new ones. Export with
`mc admin`:

```sh
mkdir -p export/policies export/groups
mc admin user list  minio --json > export/users.json
mc admin group list minio --json > export/groups.json
for p in $(mc admin policy list minio); do mc admin policy info minio "$p" > "export/policies/$p.json"; done
for g in $(mc admin group list minio);  do mc admin group info minio "$g" --json > "export/groups/$g.json"; done
```

Then import into OpenS3 with root (or an administrator's) credentials:

```sh
examples/migrate/import_identities.py --endpoint http://opens3:9000 \
    --access-key OPENS3_ROOT_USER --secret-key OPENS3_ROOT_PASSWORD \
    --export export --out new-keys.csv
```

This creates every custom policy as it is, since MinIO and OpenS3 use the
same policy language; maps MinIO's built-in policy names (`readonly`,
`readwrite`, `writeonly`, `diagnostics`, `consoleAdmin`) to OpenS3's
built-ins of the same names; creates the groups and users with the same
attachments and memberships; and issues each enabled user one new access
key pair, written to `new-keys.csv`. Hand each user their line and delete
the file. Users that were disabled in MinIO are created without a key.
Running the import again is safe: it changes nothing that already exists
and issues no further keys.

MinIO service accounts (`mc admin user svcacct`) are not exported by
these commands; create the equivalent service keys in OpenS3 by hand
(chapter 3). Console passwords are set separately, since MinIO has none.

## 4. Verify

Compare counts and sizes per bucket on both sides:

```sh
mc du minio/photos; mc du opens3/photos
mc diff minio/photos opens3/photos      # lists any object that differs
```

Then, with OpenS3 stopped, `opens3 fsck check --verify --root DIR` reads
every migrated object and confirms it matches its recorded checksum
(chapter 8). Sign in to the console as one of the migrated users to
confirm their policy behaves as expected.

## 5. Cut over

Rerun step 2 once more to copy anything written to MinIO since the first
pass, then repoint clients at OpenS3 with their new keys. Keep MinIO
read-only, or stopped, from that moment; when everything works, retire
it.

## What does not carry over

- **Version history.** Version IDs are minted by the server, so only the
  current version of each key is copied. Older versions can be copied as
  separate objects with `mc cp --version-id` if you need them, but not as
  versions of the same key.
- **Timestamps.** Last-Modified becomes the copy time. Record the original
  in user metadata if it matters.
- **Secret keys and console passwords**, as above.
- **`mc admin` scripts.** OpenS3 does not serve MinIO's admin API. Manage
  identities with `aws iam` (chapter 3) or `opens3 admin` (chapter 8).
- **Notification targets** other than webhooks, and site or bucket
  replication, which OpenS3 does not have yet.
- **Endpoint paths.** Health checks move from `/minio/health/live` to
  `/opens3/health/live` and `/opens3/health/ready`; Prometheus metrics
  from `/minio/v2/metrics/cluster` to `/opens3/metrics`.
- **Objects encrypted with SSE-C** cannot be copied by a tool that does
  not hold the customer's key; clients must move those themselves.

`tests/migration/run.sh` in the source repository runs this whole
procedure against a real MinIO in Docker, so the steps above are tested
against the MinIO release named in that script.
