# Migrating from MinIO: helper scripts

The procedure is in the user manual, chapter 11 ("Migrating from MinIO").
These two scripts cover the steps no existing tool does; the object copy
itself is done with `mc mirror` or `rclone sync`.

| Script | What it does |
|---|---|
| `import_identities.py` | Creates MinIO's users, groups and policies in OpenS3 from `mc admin ... --json` exports and issues each user a new access key (MinIO does not export secrets). |
| `copy_tags.py` | Copies object tags, which `mc mirror` and `rclone` do not. |

Both need Python 3.10+ and boto3 (`pip install boto3`, or `uv run` with
the `pyproject.toml` here). `tests/migration/run.sh` at the repository
root runs the whole procedure against a real MinIO in Docker.
