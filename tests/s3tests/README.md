# S3 conformance: Ceph s3-tests

`run.sh` runs the [Ceph s3-tests](https://github.com/ceph/s3-tests) suite
(boto3, ~900 tests) against a freshly built OpenS3 and produces
`docs/CONFORMANCE.md`. It is the project's external compatibility gate:
`known-failures.txt` lists the tests that currently fail and may only shrink.

## Requirements

- Go (`~/sdk/go/bin`), Docker (the user must be able to run `docker` without
  sudo), `git`, `curl`, Python 3 for `report.py` (stdlib only).
- Network access on first run: the suite is cloned (shallow, pinned commit)
  into `tests/s3tests/s3-tests/` and a `python:3.11-slim` runner image with the
  suite's dependencies is built once (`opens3-s3tests:<hash>`).

## Running

```sh
make conformance                              # default selection, writes docs/CONFORMANCE.md
tests/s3tests/run.sh -k "test_bucket_list"    # subset (extra args go to pytest)
tests/s3tests/run.sh s3tests/functional/test_iam.py
S3TESTS_MARKERS="not fails_on_aws" tests/s3tests/run.sh
```

What the script does:

1. `go build ./cmd/opens3`, then `go run ./tests/s3tests/mkusers` opens the
   temporary data directory *before* the server starts and creates the two
   extra IAM users the suite needs (`s3testsalt`, `s3teststenant`, both with
   the built-in `readwrite` policy) and prints their canonical IDs.
2. Starts the server on a free localhost port with `--no-fsync`
   and `OPENS3_DEFAULT_OBJECT_OWNERSHIP=ObjectWriter` (the suite exercises
   bucket/object ACLs on plain buckets, i.e. the pre-2023 AWS behaviour; the
   production default `BucketOwnerEnforced` disables ACLs like current AWS).
3. Writes `results/s3tests.conf` (main = root user, alt, tenant, iam sections).
4. Runs pytest in Docker with `--network host`, `pytest-timeout`
   (`S3TESTS_TIMEOUT`, default 180 s per test) and a junit XML report.
5. `report.py` turns `results/results.xml` into `docs/CONFORMANCE.md` (only for a
   full default run) and compares the failures with `known-failures.txt`:
   it exits non-zero on any failure that is not listed (a regression) and
   prints listed tests that now pass so the list can be trimmed.

Default selection: `s3tests/functional/test_s3.py` and `test_headers.py` with
markers `not fails_on_aws and not lifecycle_expiration and not
lifecycle_transition and not cloud_transition and not cloud_restore and not
s3select and not s3website and not bucket_logging and not storage_class`
(`S3TESTS_MARKERS` overrides). `fails_on_aws` marks behaviour that
contradicts AWS; the rest are features OpenS3 does not implement or that need
Ceph-only debug knobs (lifecycle expiration in seconds).

Outputs (git-ignored) live in `tests/s3tests/results/`: `results.xml`,
`pytest.log`, `server.log`, `s3tests.conf`. Set `S3TESTS_KEEP=1` to keep the
server data directory for post-mortem, `S3TESTS_LOG_LEVEL=debug` for a verbose
server log, `S3TESTS_NO_REPORT=1` to skip the report step.

## Updating known-failures.txt

Each line is a pytest node id (`s3tests/functional/test_s3.py::test_name`),
`#` starts a comment; keep the reason next to the test. After fixing a bug run
the affected tests (`run.sh -k pattern`), then a full run, and remove the
entries `report.py` reports as passing. Never add an entry without a reason.

## Pinning

`S3TESTS_REF` in `run.sh` pins the s3-tests commit; bump it deliberately and
re-baseline the known failures in the same commit.
