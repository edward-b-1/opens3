# 8. Operations

## What is on disk

Everything is under the data root:

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
version. The console's assets are inside the binary
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
This version has no separate audit log.

## Housekeeping that runs by itself

- Lifecycle rules: every `OPENS3_LIFECYCLE_INTERVAL` (default hourly).
- Expired temporary credentials: swept on start and every
  `OPENS3_PURGE_INTERVAL` (default hourly).
- Interrupted uploads: `tmp/` is emptied at start; multipart uploads
  abandoned by clients are removed by a lifecycle rule with
  `AbortIncompleteMultipartUpload`, or with the bucket.

## Administration without the console

`opens3 admin` talks to the server with the root (or an administrator's)
access key, taken from `--access-key`/`--secret-key`, or from
`OPENS3_ACCESS_KEY`/`OPENS3_SECRET_KEY`, or from the root variables. The
endpoint comes from `--endpoint` or `OPENS3_ENDPOINT` (default
`http://localhost:9000`). `--json` prints raw JSON.

| Command | What it does |
|---|---|
| `info`, `health` | Version, uptime, bucket and object counts, disk usage; readiness |
| `user list \| info NAME \| add NAME [--policy p1,p2] [--password P] [--no-key] \| rm \| enable \| disable \| policy NAME [p1,p2] \| password NAME (--password P \| --clear)` | Users. `add` prints the generated key pair once |
| `key list [--user U] \| add USER [--service] [--policy-file F] [--expires DUR] \| rm AK \| enable AK \| disable AK \| rotate AK` | Access keys and service accounts |
| `group list \| info \| add NAME [--members u1,u2] [--policy p1] \| rm \| members NAME [--add u] [--remove u]` | Groups |
| `policy list \| get NAME \| set NAME FILE \| rm NAME` | Policies (`FILE` may be `-` for stdin) |
| `bucket list [--usage] \| rm NAME [--force]` | Buckets; `--force` deletes contents too |
| `kms list \| add ID \| rm ID` | Named encryption keys |

Anything AWS tooling can do, do through `aws iam` (chapter 3); the
bundled CLI exists for what AWS has no command for.
