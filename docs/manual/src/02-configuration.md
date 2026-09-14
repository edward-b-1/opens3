# 2. Configuration

There is no configuration file. Settings come from command-line flags and
`OPENS3_*` environment variables. A flag typed on the command line wins
over the variable of the same setting; a variable wins over the flag's
default.

## Server flags

| Flag | Default | Meaning |
|---|---|---|
| `--root DIR` | `./data` | Data root: metadata, object bytes, master key. |
| `--address ADDR` | `:9000` | Listen address (`host:port`). |
| `--region NAME` | `us-east-1` | Region reported to clients (`GetBucketLocation`, bucket records). |
| `--enforce-region` | off | Reject signatures whose credential scope names another region. Off means any region is accepted, as most S3-compatible servers do. |
| `--tls self-signed` | | Serve HTTPS with a certificate the server generates and keeps under the data root (see below). |
| `--tls-cert FILE`, `--tls-key FILE` | | Serve HTTPS with your own certificate (see below). |
| `--no-fsync` | off | Skip fsync on writes. Only for benchmarks and tests: a crash can lose acknowledged data. |
| `--log-level LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `--log-json` | off | Structured JSON log lines instead of text. |

## Environment variables

| Variable | Meaning |
|---|---|
| `OPENS3_ROOT_USER`, `OPENS3_ROOT_PASSWORD` | Root credentials. Required. Also the root sign-in for the console. |
| `OPENS3_MASTER_KEY` | Optional master key material, at least 32 characters of random data (`openssl rand -base64 32`). When set, the key file is not used. See chapter 5. |
| `OPENS3_MASTER_KEY_NEW`, `OPENS3_MASTER_KEY_OLD` | Read only by `opens3 master` when rotating an environment key (chapter 8). |
| `OPENS3_ROOT`, `OPENS3_ADDRESS`, `OPENS3_REGION` | Same as the flags. |
| `OPENS3_TLS`, `OPENS3_TLS_CERT`, `OPENS3_TLS_KEY` | Same as the flags. |
| `OPENS3_HSTS`, `OPENS3_NO_HSTS` | `Strict-Transport-Security` is sent over TLS only when the certificate is not self-signed; `OPENS3_HSTS=1` forces it on, `OPENS3_NO_HSTS=1` forces it off. |
| `OPENS3_DOMAINS` | Comma-separated domains for virtual-host addressing: a request to `mybucket.s3.example.com` selects `mybucket`. |
| `OPENS3_ACCOUNT_ID` | The 12-digit account ID in ARNs (`arn:aws:iam::<id>:user/alice`). Default `000000000000`. |
| `OPENS3_DEFAULT_OBJECT_OWNERSHIP` | Ownership setting for new buckets: `BucketOwnerEnforced` (default; ACLs disabled, as on AWS since 2023), `BucketOwnerPreferred` or `ObjectWriter`. |
| `OPENS3_LIFECYCLE_INTERVAL` | How often lifecycle rules run, e.g. `1h` (default), `15m`; `0` or `off` disables the worker. |
| `OPENS3_PURGE_INTERVAL` | How often expired temporary credentials are swept (default `1h`). |
| `OPENS3_NOTIFY_WEBHOOK_<NAME>_ENDPOINT` and related | Notification targets; chapter 7. |

## TLS

The quickest way to encrypted transport is a certificate the server makes
for itself:

```sh
opens3 server --root /var/lib/opens3 --tls self-signed
```

On first start this generates an ECDSA key and a self-signed certificate
under `<root>/tls/` (`tls.crt` world-readable, `tls.key` owner-only) and
logs the certificate's SHA-256 fingerprint. Later starts reuse the pair,
so the fingerprint stays the same until the certificate expires (825
days), when the next start replaces it and logs that it did. The
certificate covers `localhost`, the machine's host name, its addresses,
the host in `--address`, and every `OPENS3_DOMAINS` entry with its bucket
wildcard. If the server later answers to a name the certificate lacks
(a new domain, a new address), the log says which; delete `<root>/tls/`
and restart to generate a certificate that covers it.

Clients trust the file, or pin the fingerprint:

```sh
aws --ca-bundle /var/lib/opens3/tls/tls.crt --endpoint-url https://s3.internal:9000 s3 ls
curl --cacert /var/lib/opens3/tls/tls.crt https://s3.internal:9000/opens3/health/ready
```

boto3 takes `verify="/path/to/tls.crt"`. Browsers show a warning for the
console until the certificate is imported into the operating system's
trust store; the fingerprint in the log is what to compare against the
one the browser shows.

To use your own certificate instead, provide the pair in PEM format:

```sh
export OPENS3_TLS_CERT=/etc/opens3/tls.crt OPENS3_TLS_KEY=/etc/opens3/tls.key
```

Either way, with TLS on:

- the listener requires TLS 1.2 or later and prefers TLS 1.3;
- a plain-HTTP request to the same port gets a clear answer instead of a
  handshake error: browsers (the console, or any request accepting HTML)
  get a temporary redirect to `https://`, which browsers do not cache; S3
  clients receive a 400 `InvalidRequest` error naming the `https://` URL,
  because SDKs do not follow redirects on signed requests and some loop on
  them;
- with a certificate from an authority, responses carry
  `Strict-Transport-Security` (two years), which makes browsers refuse
  plain HTTP to that host name from then on. With a self-signed certificate
  the header is not sent, because an experiment with TLS should not leave
  a two-year rule in every browser that visited; `OPENS3_HSTS=1` forces it
  on and `OPENS3_NO_HSTS=1` forces it off;
- console cookies are marked Secure.

For a public host name use a certificate from a public authority (Let's
Encrypt or your own CA) with `--tls-cert`/`--tls-key`. This version does
not obtain or renew public certificates itself; a reverse proxy that does
is a common arrangement. If you make your own certificate with openssl
rather than `--tls self-signed`, include every name and address clients
will use as subject alternative names, and the bucket wildcard when you
use virtual-host addressing.

## Virtual-host addressing

Set `OPENS3_DOMAINS=s3.example.com` and point a wildcard DNS record
`*.s3.example.com` at the server. Requests to `bucket.s3.example.com/key`
then address `bucket`; path-style `s3.example.com/bucket/key` keeps working.
The TLS certificate must cover the wildcard.

## Ports and paths on one listener

Everything is served on the one address:

| Path | What |
|---|---|
| `/` and `/<bucket>/...` | S3 API; also the IAM and STS Query APIs (POST to `/`) |
| `/console/` | Web console |
| `/opens3/admin/v1/` | Admin JSON API used by `opens3 admin` (chapter 8) |
| `/opens3/health/live`, `/opens3/health/ready` | Health probes (200, empty body) |
| `/opens3/metrics` | Prometheus metrics |

A bucket cannot be named `opens3` or `console` for this reason.
