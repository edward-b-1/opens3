# OpenS3

An open-source, Apache-2.0 licensed, S3-compatible object store in Go.

OpenS3 aims for the complete Amazon S3 API as it exists today, verified by
differential testing against AWS, in a single static binary with no
external dependencies, with every feature in the open-source build.

**Status:** phase 1 (core object store) in development. Not yet usable.
See `docs/PLAN.md` for the roadmap and `docs/SURVEY.md` for the survey of
S3, MinIO and the alternatives that motivates the project.

## Why

MinIO's community edition was archived in April 2026 after its console,
binaries and documentation were withdrawn. The alternatives each miss
something: Garage has no versioning or object lock, RustFS has a poor
security record, SeaweedFS went open-core, Ceph needs a rack. OpenS3's
commitments are written down in `GOVERNANCE.md`.

## Quick start (once phase 1 lands)

```sh
export OPENS3_ROOT_USER=admin OPENS3_ROOT_PASSWORD=change-me-now
opens3 server --root /var/lib/opens3 --address :9000
aws --endpoint-url http://localhost:9000 s3 mb s3://demo
```

## Building

Requires Go 1.27 or later.

```sh
make build      # bin/opens3
make ci         # vet + race tests; must pass before every commit
```

## Licence

Apache License 2.0. See `LICENSE`.
