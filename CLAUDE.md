# OpenS3 — open-source S3-compatible object store in Go

Goal: wire-compatible drop-in replacement for Amazon S3 / MinIO with the full
feature set in the open-source product. Plan and feature matrix: docs/PLAN.md.

## Build / test

- Go lives at `~/sdk/go/bin` (no system Go, no sudo): `export PATH=$HOME/sdk/go/bin:$PATH`.
- CI gate (no hosted CI — run before every commit): `make ci`
  (= `go vet ./... && go test -race ./...`). Integration tests in
  `tests/integration` drive a real aws-sdk-go-v2 client against an in-process server.
- Conformance: `tests/s3tests/run.sh` runs the Ceph s3-tests suite against a
  local server in Docker; `tests/s3tests/known-failures.txt` may only shrink.
- Run locally: `go run ./cmd/opens3 server --address :9000 --root ./data`
  (root credentials default to `opens3admin` / `opens3admin`, override with
  `OPENS3_ROOT_USER` / `OPENS3_ROOT_PASSWORD`).

## Conventions

- Module `gitlab.com/Birdsall/opens3`. Std library first; keep the
  dependency list short (bbolt, prometheus client, aws-sdk-go-v2 for tests).
- Every S3 error must use an `s3err` code with the AWS HTTP status and
  message text; every XML response shape lives in `internal/s3api/xml*.go`.
- Metadata KV key layout is documented in docs/PLAN.md — update it when
  adding a namespace.
- Commit as `Birdsall <4034065-Birdsall@users.noreply.gitlab.com>`.
