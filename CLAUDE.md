# OpenS3 — open-source S3-compatible object store in Go

Goal: wire-compatible drop-in replacement for Amazon S3 / MinIO with the full
feature set in the open-source product. Plan and feature matrix: docs/PLAN.md.

## Build / test

- Go lives at `~/sdk/go/bin` (no system Go, no sudo): `export PATH=$HOME/sdk/go/bin:$PATH`.
- CI gate (no hosted CI — run before every commit): `make ci`
  (= `go vet ./... && go test -race ./...`). Integration tests in
  `tests/integration` drive a real aws-sdk-go-v2 client against an in-process server.
- Conformance: `make conformance` runs the Ceph s3-tests suite against a
  local server in Docker (~2 min) and regenerates docs/CONFORMANCE.md;
  `tests/s3tests/known-failures.txt` may only shrink. Run it after any
  change to internal/s3api or internal/object. `go run ./tools/apicoverage`
  regenerates docs/API-COVERAGE.md after router changes.
- Console: `make console-test` (ESLint + Playwright browser smoke test in
  Docker, about a minute) must be run after any change to internal/console/static.
- Migration: `make migration-test` and `make migration-test-silo`
  (Docker; MinIO and Silo built from source at pinned releases, the
  manual's migration procedure end to end) after any change to
  examples/migrate or the S3/IAM API surface MinIO tooling relies on.
- Master key: `make rotation-test` (Docker; server + `opens3 master` from
  the Dockerfile, boto3 data via examples/python/encrypted_data.py, ~1 min)
  after any change to internal/kms, internal/masterkey or cmd/opens3/master.go.
- Subsystems wire themselves in via internal/server hooks
  (RegisterExtension/RegisterMount/RegisterStopper) from their own file in
  internal/server; do not grow server.go.
- Run locally: `go run ./cmd/opens3 server --address :9000 --root ./data`
  (root credentials are required: set `OPENS3_ROOT_USER` and
  `OPENS3_ROOT_PASSWORD`; there are no defaults).

## Conventions

- Module `github.com/edward-b-1/opens3`. Std library first; keep the
  dependency list short (bbolt, prometheus client, aws-sdk-go-v2 for tests).
- Every S3 error must use an `s3err` code with the AWS HTTP status and
  message text; every XML response shape lives in `internal/s3api/xml*.go`.
- Metadata KV key layout is documented in docs/PLAN.md — update it when
  adding a namespace.
- Commit as `Birdsall <4034065-Birdsall@users.noreply.gitlab.com>`.
