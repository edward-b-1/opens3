# 8. Operations

## What is on disk

Everything is under the data root:

| Path | Contents |
|---|---|
| `meta/opens3.db` | Metadata: buckets, object versions, multipart state, users, keys, policies, encryption keys. One file, locked while the server runs. |
| `meta/master.keys` | The master key ring. Mode 0600. |
| `tls/tls.crt`, `tls/tls.key` | The self-signed certificate and key, when `--tls self-signed` is used. Delete the directory to generate a new certificate. |
| `data/<bucket>/xx/<id>` | One immutable file per uploaded object or part. Directories and files are owner-only. |
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

## Rotating the master key

`opens3 master` works on the data directory with the server stopped (the
database is locked while it runs). Every command takes `--root DIR`.

| Command | What it does |
|---|---|
| `status` | Lists the keys in the ring with their fingerprints and how many records each still protects. |
| `rotate` | Adds a new key to `meta/master.keys` and re-wraps every protected record under it. |
| `rewrap` | Re-wraps under the current key whatever is still under an older one; finishes an interrupted `rotate`. `--dry-run` only counts. |
| `retire` | Removes the older keys from the ring once no record needs them. Refuses while anything is still under an older key. |

The protected records are the key-check value, the named encryption keys,
every stored access-key secret and the data keys of SSE-S3 objects and
multipart uploads. Objects under a named key (SSE-KMS) are not touched:
their data keys are wrapped by the named key, which is itself re-wrapped.
SSE-C objects and plaintext objects are not affected.

A rotation is safe to interrupt: the new key is written to the ring before
any record uses it, and older keys stay in the ring, so a half-finished
`rotate` leaves everything readable and `rewrap` completes it.

After `rotate`, **back up the key file again**: earlier copies lack the
new key. The older keys stay in the ring on purpose. A backup of
`meta/opens3.db` taken before the rotation still has its records wrapped
under the old key, so restoring it needs that key. Run `retire` only when
no such backup needs to be restorable, or keep a copy of the pre-rotation
ring with those backups.

**Environment keys.** When the server takes its key from `OPENS3_MASTER_KEY`,
`rotate` needs the new material in `OPENS3_MASTER_KEY_NEW`; it re-wraps
everything and tells you to move the new value into `OPENS3_MASTER_KEY`
before starting the server. `OPENS3_MASTER_KEY_OLD` names a previous
environment key so that `rewrap` can finish a rotation that was
interrupted after the variables were swapped.

**Moving between the file and the environment.** To move from the key
file to an environment key, set `OPENS3_MASTER_KEY` to the new material,
run `rewrap` (the file's keys are read as fallbacks), then `retire`, which
deletes the file. To move the other way, unset `OPENS3_MASTER_KEY`, set
`OPENS3_MASTER_KEY_OLD` to the current material and run `rotate`: it
creates the file and re-wraps everything under it.

## Checking the data directory

`opens3 fsck check --root DIR` compares the metadata with the object
files: every object version, multipart upload and part must have its
file with the recorded size; every file must be referenced by something;
the index records must agree with each other; no bucket may be stuck in
deletion. It prints a report by problem kind and exits 1 when anything is
wrong. It changes nothing, so it can run on a snapshot or a copy while
the server runs; on the live directory the server must be stopped
because it holds the database lock. `--verify` also reads every object
and compares its content with the recorded ETag, decrypting SSE-S3 and
SSE-KMS objects with the master key, which is the only test that catches
silent corruption and takes as long as reading everything once. SSE-C
objects cannot be verified because the server holds no key.

`opens3 fsck repair --root DIR` fixes what can be fixed without losing
data: it deletes files nothing references, deletes or recreates dangling
pointers and indexes, removes part records for uploads that no longer
exist or whose file is gone, finishes an interrupted bucket deletion, and
recreates a missing bucket record so its objects become reachable again
(owned by root, private). It never removes an object whose file is
missing or damaged: those are reported, and stay visible so that a
backup can be restored over them or the object deleted deliberately.
`--dry-run` lists what would change.

Run a check after any unclean shutdown or disk incident, and `--verify`
on a schedule that suits the size of the store.

## Exporting objects

`opens3 export --root DIR --to OUTDIR` writes every object out as a plain
file, `OUTDIR/<bucket>/<key>`, decrypting SSE-S3 and SSE-KMS objects with
the master key. Nothing in OpenS3 is needed to read the result. A
manifest, `OUTDIR/manifest.jsonl`, has one JSON line per object with its
key, version, size, ETag, checksum, content type, metadata and tags, and
says why an object was not written: delete markers, and SSE-C objects,
whose key the server does not hold. `--bucket` and `--prefix` narrow the
export; `--all-versions` writes older versions as `<key>@<versionId>`
beside the current file; existing files in OUTDIR are kept unless
`--overwrite` is given, so an interrupted export can be resumed. Keys
that are not safe file paths (a `..` segment, for example) go under
`<bucket>/_unsafe/` with the key in the manifest.

This is the way out, and the last-resort recovery: it needs only the data
directory and the master key, not a working server.

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
  notification deliveries, purged credentials,
  `opens3_tls_certificate_not_after_seconds` (the certificate's expiry as
  a Unix timestamp, for an alert before it lapses) and
  `opens3_tls_certificate_reloads_total` by result, plus Go runtime and
  process metrics.
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
bundled CLI exists for what AWS has no command for. `opens3 master`
(above) is the other offline command: it needs the data directory, not a
running server.
