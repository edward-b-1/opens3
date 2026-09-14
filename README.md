# OpenS3

An open-source, Apache-2.0 licensed, S3-compatible object store in Go.

OpenS3 aims for the complete Amazon S3 API as it exists today, verified by
differential testing against AWS, in a single static binary with no
external dependencies, with every feature in the open-source build.

**Status:** phase 1 complete (September 2026): single-node object store
with versioning, multipart, object lock, SSE, IAM and policies, lifecycle,
notifications, admin API and CLI, web console. 598 of 637 Ceph s3-tests
pass (`docs/CONFORMANCE.md`); 95 of 117 S3 operations are implemented
(`docs/API-COVERAGE.md`). Not yet distributed or erasure-coded: that is
phase 4. Roadmap in `docs/PLAN.md`; the survey that motivates the project
in `docs/SURVEY.md`.

## Why

MinIO's community edition was archived in April 2026 after its console,
binaries and documentation were withdrawn. The alternatives each miss
something: Garage has no versioning or object lock, RustFS has a poor
security record, SeaweedFS went open-core, Ceph needs a rack. OpenS3's
commitments are written down in `GOVERNANCE.md`.

## Quick start

```sh
make build
export OPENS3_ROOT_USER=admin OPENS3_ROOT_PASSWORD=change-me-now
bin/opens3 server --root /var/lib/opens3 --address :9000
aws --endpoint-url http://localhost:9000 s3 mb s3://demo
bin/opens3 admin user add alice --secret alicesecret --policy readwrite
```

The console is at http://localhost:9000/console/, health at
`/opens3/health/ready`, Prometheus metrics at `/opens3/metrics`. A
random master key is generated into `<root>/meta/master.keys` on first
start: back it up, or supply your own with `OPENS3_MASTER_KEY`
(`docs/FORMAT.md`). For
TLS set `OPENS3_TLS_CERT` and `OPENS3_TLS_KEY`; plain-HTTP requests to the
same port are then redirected to https.
Docker: `docker compose up` with `OPENS3_ROOT_PASSWORD` set.

## Documentation

- **`docs/MANUAL.md`** — the user manual (single file): getting started, configuration, identity, buckets, encryption, console, operations, troubleshooting

- `docs/PLAN.md` — architecture, feature matrix, roadmap
- `docs/SURVEY.md` — Amazon S3, MinIO history, the alternatives, positioning
- `docs/API-COVERAGE.md`, `docs/CONFORMANCE.md` — what works, measured
- `docs/FORMAT.md` — on-disk format and recovery
- `docs/IAM.md` — identities, access keys, policies, ACLs, how requests are authorised
- `docs/IAM-API.md` — manage users, keys and policies with `aws iam`
- `docs/ADMIN.md`, `docs/CONSOLE.md`, `docs/NOTIFICATIONS.md`
- `GOVERNANCE.md`, `SECURITY.md`, `CONTRIBUTING.md`

## Source, issues and releases

https://github.com/edward-b-1/opens3

## Building

Requires Go 1.27 or later.

```sh
make build      # bin/opens3
make ci         # vet + race tests; must pass before every commit
```

## Licence

Apache License 2.0. See `LICENSE`.
