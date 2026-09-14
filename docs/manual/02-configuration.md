# 2. Configuration

There is no configuration file. Settings come from command-line flags and
`OPENS3_*` environment variables; the environment wins when both are set.

## Server flags

| Flag | Default | Meaning |
|---|---|---|
| `--root DIR` | `./data` | Data root: metadata, object bytes, master key. |
| `--address ADDR` | `:9000` | Listen address (`host:port`). |
| `--region NAME` | `us-east-1` | Region reported to clients (`GetBucketLocation`, bucket records). |
| `--enforce-region` | off | Reject signatures whose credential scope names another region. Off means any region is accepted, as most S3-compatible servers do. |
| `--tls-cert FILE`, `--tls-key FILE` | | Serve HTTPS (see below). |
| `--no-fsync` | off | Skip fsync on writes. Only for benchmarks and tests: a crash can lose acknowledged data. |
| `--log-level LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `--log-json` | off | Structured JSON log lines instead of text. |

## Environment variables

| Variable | Meaning |
|---|---|
| `OPENS3_ROOT_USER`, `OPENS3_ROOT_PASSWORD` | Root credentials. Required. Also the root sign-in for the console. |
| `OPENS3_MASTER_KEY` | Optional master key material, at least 32 characters of random data (`openssl rand -base64 32`). When set, the key file is not used. See chapter 5. |
| `OPENS3_ROOT`, `OPENS3_ADDRESS`, `OPENS3_REGION` | Same as the flags. |
| `OPENS3_TLS_CERT`, `OPENS3_TLS_KEY` | Same as the flags. |
| `OPENS3_NO_HSTS` | `1` disables the `Strict-Transport-Security` header sent over TLS. |
| `OPENS3_DOMAINS` | Comma-separated domains for virtual-host addressing: a request to `mybucket.s3.example.com` selects `mybucket`. |
| `OPENS3_ACCOUNT_ID` | The 12-digit account ID in ARNs (`arn:aws:iam::<id>:user/alice`). Default `000000000000`. |
| `OPENS3_DEFAULT_OBJECT_OWNERSHIP` | Ownership setting for new buckets: `BucketOwnerEnforced` (default; ACLs disabled, as on AWS since 2023), `BucketOwnerPreferred` or `ObjectWriter`. |
| `OPENS3_LIFECYCLE_INTERVAL` | How often lifecycle rules run, e.g. `1h` (default), `15m`; `0` or `off` disables the worker. |
| `OPENS3_PURGE_INTERVAL` | How often expired temporary credentials are swept (default `1h`). |
| `OPENS3_NOTIFY_WEBHOOK_<NAME>_ENDPOINT` and related | Notification targets; chapter 7. |

## TLS

Provide a certificate and key in PEM format:

```sh
export OPENS3_TLS_CERT=/etc/opens3/tls.crt OPENS3_TLS_KEY=/etc/opens3/tls.key
```

With TLS on:

- the listener requires TLS 1.2 or later and prefers TLS 1.3;
- a plain-HTTP request to the same port is redirected to `https://` (301 for
  GET and HEAD, 308 otherwise), so a stray `http://` URL gets a clear answer
  rather than a handshake error;
- responses carry `Strict-Transport-Security` (two years) unless
  `OPENS3_NO_HSTS=1`. The header applies to the whole host name, so disable
  it if the same host also serves something over plain HTTP;
- console cookies are marked Secure.

For an internal deployment a self-signed certificate is enough. Include
every name and address clients will use as subject alternative names:

```sh
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -days 3650 -subj "/CN=s3.internal" \
  -addext "subjectAltName=DNS:s3.internal,DNS:*.s3.internal,IP:10.0.0.5" \
  -keyout tls.key -out tls.crt
chmod 600 tls.key
```

Clients then need to trust it: import `tls.crt` into the operating system
trust store, or pass it explicitly (`aws --ca-bundle tls.crt`, boto3
`verify="tls.crt"`, `curl --cacert tls.crt`). For a public host name use a
certificate from a public authority; automatic issuance is planned
(`../TODO.md`).

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
| `/opens3/admin/v1/` | Admin JSON API (`../ADMIN.md`) |
| `/opens3/health/live`, `/opens3/health/ready` | Health probes (200, empty body) |
| `/opens3/metrics` | Prometheus metrics |

A bucket cannot be named `opens3` or `console` for this reason.
