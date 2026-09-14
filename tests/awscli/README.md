# AWS CLI end-to-end suite

`run.sh` drives the real AWS CLI v2 against a freshly built OpenS3 and runs
every `aws s3`, `aws s3api`, `aws iam` and `aws sts` command OpenS3 supports
at least once — plus the ones it does not, expecting the documented error —
then writes `docs/AWSCLI.md`, a per-command coverage report.

## Requirements

- Go (`~/sdk/go/bin`), Docker (the user must be able to run `docker` without
  sudo), `curl`, Python 3 for `report.py` (stdlib only).
- Network access on first run to pull the pinned `amazon/aws-cli` image
  (`AWSCLI_IMAGE` in `run.sh`).

## Running

```sh
make awscli                          # Docker (default), writes docs/AWSCLI.md
tests/awscli/run.sh                  # same
OPENS3_AWSCLI=native tests/awscli/run.sh   # use an `aws` on PATH / ~/.local/bin
AWS_CLI_BIN=~/.local/bin/aws tests/awscli/run.sh   # explicit native binary
AWSCLI_KEEP=1 tests/awscli/run.sh    # keep the server data dir
```

What the script does:

1. `go build ./cmd/opens3` and starts the server on a free localhost port
   with `--no-fsync`, the root credentials from `OPENS3_ROOT_USER` /
   `OPENS3_ROOT_PASSWORD` (default `opens3admin`) and
   `OPENS3_DEFAULT_OBJECT_OWNERSHIP=ObjectWriter` so the ACL commands work.
2. Runs `suite.sh` **once, in one container** of the pinned `amazon/aws-cli`
   image with `--network host` (so the container's `aws` reaches the server
   on 127.0.0.1), this directory mounted read-only at `/suite`, `results/`
   at `/out` and a scratch dir at `/scratch` (also `HOME`). The endpoint and
   credentials are passed as `AWS_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID`,
   `AWS_SECRET_ACCESS_KEY`, `AWS_DEFAULT_REGION` — the CLI honours
   `AWS_ENDPOINT_URL` for every service and uses path-style addressing for
   an IP endpoint. In native mode the same script runs on the host.
3. `report.py` reads `results/results.tsv` and the `aws <service> help`
   pages the suite dumped to `results/help-*.txt`, writes `docs/AWSCLI.md`
   and exits non-zero on any failed case.

## suite.sh

Plain bash with four helpers; each case appends one line to `results.tsv`
(`name`, `service`, `command`, `kind`, `PASS|FAIL`, `detail`) and keeps the
command's stdout/stderr in `results/cases/<name>.{out,err}`:

```sh
ok NAME -- aws s3api put-object ...              # expects exit 0
eq NAME EXPECTED -- aws ... --query X --output text   # expects stdout == EXPECTED
fails NAME CODE -- aws ...                       # expects non-zero exit and "(CODE)" in stderr
unsupported NAME CODE -- aws ...                 # same as fails, for commands OpenS3 does not implement
```

The service/command are parsed from the first `aws SVC CMD` in the
arguments (an `env VAR=... aws ...` prefix is skipped, used to run as an IAM
user or with STS credentials); `CASE_CMD="s3 presign" ok ... -- curl ...`
attributes a non-`aws` check (a `curl` of a presigned URL, a file
comparison) to a command.

Coverage rules in the report: a command is **passed** when every case that
used it passed, **failed** when any did, **expected-unsupported** when only
`unsupported` cases used it, and **not exercised** when no case did.

## Files

- `results/` (git-ignored): `results.tsv`, `cases/`, `help-*.txt`,
  `version.txt`, `suite.log`, `server.log`, the built binary.
- `docs/AWSCLI.md`: the generated report.

## Transport scenarios

After the main suite, `run.sh` starts a second server with a self-signed
certificate and runs `transport.sh`: the CLI over `https://` with and
without trusting the certificate, `http://` against the TLS port (must
produce a parsed `InvalidRequest` error naming the https URL, not a
redirect loop), `https://` against the plain server, and the browser side
of the TLS port (console redirect, HTTPS console, HSTS). Results appear in
`docs/AWSCLI.md` under "aws transport". Shared assertion helpers live in
`lib.sh`.
