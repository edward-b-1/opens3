# 11. Migrating from MinIO or Silo

OpenS3 speaks the same S3 API as MinIO, so the way to move is to run both
servers side by side, copy the objects through the API, recreate the
identities, verify, and repoint clients. Nothing is read from MinIO's
disks directly: its on-disk format is its own, and copying through the
API works for every MinIO version and layout, decrypts objects MinIO
encrypted, and can be rehearsed and repeated. What it cannot carry is
described at the end.

Tools used: rclone, a generic S3 client, for the copy; MinIO's own `mc`
for one thing only, reading users and policies out of MinIO, which no
generic client can do (`mc admin` is MinIO's private API; `mc`'s other
commands are ordinary S3 and would work against OpenS3 too); the two
scripts in `examples/migrate/`, which write to OpenS3 through the
standard S3 and IAM APIs with boto3; and `aws` for checks. Nothing on the
OpenS3 side needs MinIO tooling.

## 1. Stand OpenS3 up beside MinIO

Install OpenS3 (chapter 1) on the same network, with its own data
directory and root credentials. Give rclone a remote for each server
(`rclone config`, or the environment variables below, which is what the
tested procedure uses):

```sh
export RCLONE_CONFIG_MINIO_TYPE=s3  RCLONE_CONFIG_MINIO_PROVIDER=Minio \
       RCLONE_CONFIG_MINIO_ENDPOINT=http://minio:9000 \
       RCLONE_CONFIG_MINIO_ACCESS_KEY_ID=MINIO_ROOT_USER RCLONE_CONFIG_MINIO_SECRET_ACCESS_KEY=MINIO_ROOT_PASSWORD
export RCLONE_CONFIG_OPENS3_TYPE=s3 RCLONE_CONFIG_OPENS3_PROVIDER=Other RCLONE_CONFIG_OPENS3_FORCE_PATH_STYLE=true \
       RCLONE_CONFIG_OPENS3_ENDPOINT=http://opens3:9000 \
       RCLONE_CONFIG_OPENS3_ACCESS_KEY_ID=OPENS3_ROOT_USER RCLONE_CONFIG_OPENS3_SECRET_ACCESS_KEY=OPENS3_ROOT_PASSWORD
```

and `mc` an alias for MinIO only:

```sh
mc alias set minio http://minio:9000 MINIO_ROOT_USER MINIO_ROOT_PASSWORD
```

With `--tls self-signed` on OpenS3, give rclone the certificate the
server generated: `RCLONE_CA_CERT=<root>/tls/tls.crt`.

## 2. Copy the objects

```sh
for b in $(rclone lsd minio: | awk '{print $NF}'); do
  rclone mkdir "opens3:$b"
  rclone sync --metadata "minio:$b" "opens3:$b"
done
```

`--metadata` keeps content type and user metadata, and records the
source object's modification time as `mtime` (and creation time as
`btime`) user metadata, since Last-Modified itself cannot be set. Large
objects are
re-uploaded as multipart uploads, so their ETags differ from MinIO's
while the content is identical. The command is safe to repeat: a second
run copies only what changed, which is how the final catch-up before the
cutover is done. (`mc mirror --preserve` does the same job if you would
rather stay with `mc`; its S3 commands are not MinIO-specific.)

Object tags are not copied by rclone or `mc mirror`. Copy them
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

This creates every custom policy, since MinIO and OpenS3 use the same
policy language, renaming MinIO's administrative actions (`admin:...`) to
OpenS3's (`iam:`, `kms:`, `opens3:`) and dropping the few with no
equivalent, such as MinIO's cluster operations and `s3tables`, which it
reports; maps MinIO's built-in policy names (`readonly`, `readwrite`,
`writeonly`, `diagnostics`, `consoleAdmin`) to OpenS3's built-ins of the
same names; creates the groups and users with the same
attachments and memberships; and issues each enabled user one new access
key pair, written to `new-keys.csv`. Hand each user their line and delete
the file. Users that were disabled in MinIO are created without a key.
Running the import again is safe: it changes nothing that already exists
and issues no further keys.

MinIO service accounts (`mc admin user svcacct`) are not exported by
these commands; create the equivalent service keys in OpenS3 by hand
(chapter 3). Console passwords are set separately, since MinIO has none.

## 4. Verify

Compare the two sides:

```sh
rclone check minio:photos opens3:photos    # lists any object that differs
rclone size  opens3:photos
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
- **Timestamps.** Last-Modified becomes the copy time; the original is
  in the `mtime` metadata rclone adds.
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

## Migrating from Silo

Silo (github.com/pgsty/silo) is the maintained fork of MinIO: it keeps
MinIO's S3 API, administration API, `MINIO_*` settings and on-disk
format, and ships MinIO's client renamed `mcli`. Everything above applies
unchanged. Point rclone at the Silo endpoint, and use `mcli` where the
steps say `mc` (`mcli alias set`, `mcli admin user list`, and so on);
the exports have the same shape and `import_identities.py` reads them as
is. If you moved from MinIO to Silo earlier, the identities you carried
across then come across again the same way.

`tests/migration/run.sh` in the source repository runs this whole
procedure in Docker against a MinIO, and again against a Silo, each
built from source at a pinned release, so the steps above are tested
against both.
