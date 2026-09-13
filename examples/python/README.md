# Python (boto3) examples and smoke tests for OpenS3

Standalone boto3 scripts that push real data at a running OpenS3 server and
read it back. They do not depend on anything else in this repository.

## Setup

Everything is run with [uv](https://docs.astral.sh/uv/); it creates the
virtualenv and installs boto3 on first use, nothing else to install.

```sh
cd examples/python
cp .env.example .env        # then edit endpoint / credentials if needed
uv run upload_data.py --generate
```

`.env` keys (environment variables of the same name take precedence):

| key                    | default                 |
|------------------------|-------------------------|
| `OPENS3_ENDPOINT`      | `http://localhost:9000` |
| `OPENS3_ACCESS_KEY`    | `root`                  |
| `OPENS3_SECRET_KEY`    | `password`              |
| `OPENS3_REGION`        | `us-east-1`             |
| `OPENS3_BUCKET_PREFIX` | `pyclient-test`         |

## Scripts

| script             | what it does |
|--------------------|--------------|
| `generate_data.py` | writes a deterministic test corpus to `./generated` plus `manifest.json` (sizes, SHA-256) |
| `upload_data.py`   | creates `<prefix>-data` and `<prefix>-copies`, uploads everything (PutObject, transfer-manager multipart, hand-driven multipart, CopyObject, DeleteObjects), HEAD-checks each object |
| `verify_data.py`   | paginated + delimiter listings, full downloads with hash comparison, range GETs, `If-None-Match` 304, 404 on missing key |
| `cleanup.py`       | empties and deletes every bucket starting with the prefix (`--dry-run`, `--keep-buckets`) |
| `run_all.py`       | runs the four above in order (`--keep` skips cleanup) |

```sh
uv run run_all.py            # full cycle
uv run upload_data.py --generate --skip-large   # quick run without the 20 MiB blobs
OPENS3_ENDPOINT=http://devbox4.lan:9000 uv run verify_data.py --no-save
```

`generated/` and `downloads/` are git-ignored scratch directories.
