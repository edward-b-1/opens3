# 8. Operations

## What is on disk

Everything is under the data root (`../FORMAT.md` is the contract):

| Path | Contents |
|---|---|
| `meta/opens3.db` | Metadata: buckets, object versions, multipart state, users, keys, policies, encryption keys. One file, locked while the server runs. |
| `meta/master.keys` | The master key ring. Mode 0600. |
| `data/<bucket>/xx/<id>` | One immutable file per uploaded object or part. |
| `tmp/` | In-flight uploads; cleaned at start. |

Object files are the uploaded bytes as-is (or ciphertext when encrypted),
so plaintext objects can be reassembled without the server from the
metadata alone.

## Backups

The server holds the metadata database open with an exclusive lock, so a
consistent backup is one of:

1. Stop the server and copy the data root. Simplest and consistent.
2. Snapshot the filesystem or volume with the server running (ZFS, LVM,
   cloud volumes). Consistent as long as the snapshot is atomic, which it
   is for those.
3. Copy `data/` while running and `meta/` while stopped. Object files are
   immutable once written, so `data/` can be copied live; only the
   metadata needs the stop.

**Keep a copy of `meta/master.keys` (or the value of `OPENS3_MASTER_KEY`)
somewhere other than the backup of the data root.** A backup that contains
the key beside the data is not encrypted at rest in any meaningful sense;
a backup without the key cannot be read if the key is lost.

Restore by putting the data root back and starting the server with the
same root credentials and, if used, the same `OPENS3_MASTER_KEY`. A key
mismatch is refused at start.

## Upgrades

Stop, replace the binary, start. The on-disk format is versioned; a
release that changes it says so in its notes and reads the previous
version (`../GOVERNANCE.md`). The console's assets are inside the binary
and browsers revalidate them on each load, so a normal reload picks up the
new version.

## Monitoring

- `/opens3/health/live` and `/opens3/health/ready` return 200 with no body;
  ready returns 503 with a message if the data root is unusable. Use them
  as container probes.
- `/opens3/metrics` is Prometheus text: `opens3_s3_requests_total` by
  operation and status, request latency histograms, bytes in and out,
  notification deliveries, purged credentials, plus Go runtime and process
  metrics.
- `opens3 admin info` or the console's Status page: version, uptime,
  bucket and object counts, disk usage.

## Logs

Structured log lines on stderr (`--log-json` for JSON). At `info` you see
startup (address, master key fingerprint), console logins and failures,
plain-HTTP redirects, lifecycle and purge activity, and TLS handshake
problems reported by clients. `--log-level debug` adds per-request detail.
An audit log of every API request is planned.

## Housekeeping that runs by itself

- Lifecycle rules: every `OPENS3_LIFECYCLE_INTERVAL` (default hourly).
- Expired temporary credentials: swept on start and every
  `OPENS3_PURGE_INTERVAL` (default hourly).
- Interrupted uploads: `tmp/` is emptied at start; multipart uploads
  abandoned by clients are removed by a lifecycle rule with
  `AbortIncompleteMultipartUpload`, or with the bucket.

## Administration without the console

`opens3 admin` talks to the admin JSON API with the root (or an
administrator's) access key: users, keys, groups, policies, buckets
(including forced deletion), encryption keys, server info. `../ADMIN.md`
lists commands and endpoints. Anything AWS tooling can do, it should do
through `aws iam` (chapter 3).
