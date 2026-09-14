#!/usr/bin/env bash
# Run the Ceph s3-tests conformance suite against a freshly built OpenS3.
#
#   tests/s3tests/run.sh [pytest args...]
#
# Examples:
#   tests/s3tests/run.sh                          # default selection
#   tests/s3tests/run.sh -k "test_bucket_list"    # subset
#   tests/s3tests/run.sh s3tests/functional/test_iam.py
#
# Environment:
#   S3TESTS_REF      s3-tests commit to check out (pinned below)
#   S3TESTS_MARKERS  pytest -m expression (default excludes unsupported areas)
#   S3TESTS_TIMEOUT  per-test timeout in seconds (default 180)
#   S3TESTS_KEEP     set to 1 to keep the server data dir after the run
#   S3TESTS_NO_REPORT set to 1 to skip report.py (regression check + docs)
#   OPENS3_ROOT_USER / OPENS3_ROOT_PASSWORD  root credentials (defaults below)
#   OPENS3_DEFAULT_OBJECT_OWNERSHIP  ownership for new buckets (default ObjectWriter: ACLs enabled)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
export PATH="$HOME/sdk/go/bin:$PATH"

S3TESTS_REPO="${S3TESTS_REPO:-https://github.com/ceph/s3-tests}"
S3TESTS_REF="${S3TESTS_REF:-5522d1c351f75bc00ae0f64f742f3f095f5939d9}"   # 2026-05-27
S3TESTS_TIMEOUT="${S3TESTS_TIMEOUT:-180}"
# Areas OpenS3 does not implement (or that need Ceph-only debug knobs) are
# excluded by marker. fails_on_aws marks Ceph-specific behaviour that
# contradicts AWS; we target AWS semantics so those are excluded too.
S3TESTS_MARKERS="${S3TESTS_MARKERS:-not fails_on_aws and not lifecycle_expiration and not lifecycle_transition and not cloud_transition and not cloud_restore and not s3select and not s3website and not bucket_logging and not storage_class}"
DEFAULT_TESTS=(s3tests/functional/test_s3.py s3tests/functional/test_headers.py)

# The suite exercises bucket/object ACLs on plain buckets, i.e. the legacy
# (pre-2023) AWS behaviour; ObjectWriter enables ACLs on new buckets.
export OPENS3_DEFAULT_OBJECT_OWNERSHIP="${OPENS3_DEFAULT_OBJECT_OWNERSHIP:-ObjectWriter}"
export OPENS3_ROOT_USER="${OPENS3_ROOT_USER:-s3testsmain}"
export OPENS3_ROOT_PASSWORD="${OPENS3_ROOT_PASSWORD:-s3testsmainsecretkey00000000000}"
ALT_USER=s3testsalt
ALT_SECRET=s3testsaltsecretkey0000000000000
TENANT_USER=s3teststenant
TENANT_SECRET=s3teststenantsecretkey000000000

SRC="$HERE/s3-tests"
RESULTS="$HERE/results"
mkdir -p "$RESULTS"

log() { echo "[s3tests] $*" >&2; }

# --- 1. checkout ------------------------------------------------------------
if [ ! -d "$SRC/.git" ]; then
  log "cloning $S3TESTS_REPO @ $S3TESTS_REF"
  rm -rf "$SRC"
  git init -q "$SRC"
  git -C "$SRC" remote add origin "$S3TESTS_REPO"
fi
if [ "$(git -C "$SRC" rev-parse HEAD 2>/dev/null || true)" != "$S3TESTS_REF" ]; then
  git -C "$SRC" fetch -q --depth 1 origin "$S3TESTS_REF"
  git -C "$SRC" checkout -q --detach FETCH_HEAD
fi

# --- 2. runner image ----------------------------------------------------------
IMAGE="opens3-s3tests:$(cat "$SRC/requirements.txt" "$HERE/Dockerfile" | sha256sum | cut -c1-12)"
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  log "building runner image $IMAGE"
  docker build -q -t "$IMAGE" -f "$HERE/Dockerfile" "$SRC" >/dev/null
fi

# --- 3. server ------------------------------------------------------------------
log "building opens3"
BIN="$RESULTS/opens3"
(cd "$REPO" && go build -o "$BIN" ./cmd/opens3)

DATA="$(mktemp -d "${TMPDIR:-/tmp}/opens3-s3tests.XXXXXX")"
PORT=""
for _ in $(seq 1 50); do
  p=$((20000 + RANDOM % 20000))
  if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then PORT=$p; break; fi
done
[ -n "$PORT" ] || { log "no free port found"; exit 1; }

log "creating users in $DATA"
eval "$(cd "$REPO" && go run ./tests/s3tests/mkusers -root "$DATA" -alt-user "$ALT_USER" -alt-secret "$ALT_SECRET" -tenant-user "$TENANT_USER" -tenant-secret "$TENANT_SECRET")"

SERVER_LOG="$RESULTS/server.log"
# TLS: the suite's SSE-C tests need a secure connection, as on AWS. The
# server generates its own certificate; the suite skips verification.
"$BIN" server --root "$DATA" --address "127.0.0.1:$PORT" --tls self-signed --no-fsync --log-level "${S3TESTS_LOG_LEVEL:-warn}" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
cleanup() {
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  if [ "${S3TESTS_KEEP:-0}" = 1 ]; then log "data dir kept at $DATA"; else rm -rf "$DATA"; fi
}
trap cleanup EXIT
for _ in $(seq 1 100); do
  if [ -s "$DATA/tls/tls.crt" ] && curl -fs --cacert "$DATA/tls/tls.crt" "https://127.0.0.1:$PORT/opens3/health/ready" >/dev/null 2>&1; then break; fi
  kill -0 "$SERVER_PID" 2>/dev/null || { log "server exited early:"; cat "$SERVER_LOG" >&2; exit 1; }
  sleep 0.1
done
log "opens3 listening on 127.0.0.1:$PORT (log: $SERVER_LOG)"

# --- 4. config ------------------------------------------------------------------
CONF="$RESULTS/s3tests.conf"
cat >"$CONF" <<CONF
[DEFAULT]
host = 127.0.0.1
port = $PORT
is_secure = True
ssl_verify = False

[fixtures]
bucket prefix = opens3-{random}-
iam name prefix = s3-tests-
iam path prefix = /s3-tests/

[s3 main]
display_name = root
user_id = $MAIN_USER_ID
email = main@opens3.test
api_name = default
access_key = $OPENS3_ROOT_USER
secret_key = $OPENS3_ROOT_PASSWORD
kms_keyid = testkey-1
kms_keyid2 = testkey-2

[s3 alt]
display_name = $ALT_USER
email = alt@opens3.test
user_id = $ALT_USER_ID
access_key = $ALT_USER
secret_key = $ALT_SECRET

[s3 tenant]
display_name = $TENANT_USER
email = tenant@opens3.test
user_id = $TENANT_USER_ID
access_key = $TENANT_USER
secret_key = $TENANT_SECRET
tenant = testx

[iam]
email = main@opens3.test
user_id = $MAIN_USER_ID
access_key = $OPENS3_ROOT_USER
secret_key = $OPENS3_ROOT_PASSWORD
display_name = root

[iam root]
access_key = $OPENS3_ROOT_USER
secret_key = $OPENS3_ROOT_PASSWORD
account_id = $ACCOUNT_ID
user_id = $MAIN_USER_ID
email = main@opens3.test

[iam alt root]
access_key = $ALT_USER
secret_key = $ALT_SECRET
account_id = $ACCOUNT_ID
user_id = $ALT_USER_ID
email = alt@opens3.test

[webidentity]
token = dummy
aud = dummy
sub = dummy
azp = dummy
user_token = dummy
thumbprint = dummy
KC_REALM = dummy
CONF

# --- 5. pytest ------------------------------------------------------------------
ARGS=("$@")
if [ ${#ARGS[@]} -eq 0 ]; then
  ARGS=("${DEFAULT_TESTS[@]}")
else
  # If the caller only passed options (no test paths), keep the default selection.
  has_path=0
  for a in "${ARGS[@]}"; do case "$a" in s3tests/*|*.py|*.py::*) has_path=1;; esac; done
  [ $has_path = 1 ] || ARGS+=("${DEFAULT_TESTS[@]}")
fi
JUNIT="$RESULTS/results.xml"
rm -f "$JUNIT"
log "running pytest: -m \"$S3TESTS_MARKERS\" ${ARGS[*]}"
START=$(date +%s)
set +e
# Tests that use `requests` directly (browser POST uploads) verify TLS
# regardless of ssl_verify, so they get the server's certificate as CA.
cp "$DATA/tls/tls.crt" "$RESULTS/tls.crt"
docker run --rm --network host \
  -v "$SRC:/s3-tests" -v "$RESULTS:/out" \
  -e S3TEST_CONF=/out/s3tests.conf -e PYTHONDONTWRITEBYTECODE=1 \
  -e REQUESTS_CA_BUNDLE=/out/tls.crt -e SSL_CERT_FILE=/out/tls.crt \
  "$IMAGE" \
  python -m pytest -p no:cacheprovider -q -rfE --tb=line \
    --timeout="$S3TESTS_TIMEOUT" --junit-xml=/out/results.xml \
    -m "$S3TESTS_MARKERS" "${ARGS[@]}" 2>&1 | tee "$RESULTS/pytest.log"
PYTEST_STATUS=${PIPESTATUS[0]}
set -e
log "pytest finished in $(( $(date +%s) - START ))s (exit $PYTEST_STATUS)"

# --- 6. report ------------------------------------------------------------------
if [ "${S3TESTS_NO_REPORT:-0}" = 1 ]; then exit "$PYTEST_STATUS"; fi
[ -f "$JUNIT" ] || { log "no junit output produced"; exit 1; }
REPORT_ARGS=(--junit "$JUNIT" --known "$HERE/known-failures.txt")
if [ ${#ARGS[@]} -eq ${#DEFAULT_TESTS[@]} ] && [ "${ARGS[*]}" = "${DEFAULT_TESTS[*]}" ]; then
  # Only a full default run is representative enough to regenerate the docs.
  REPORT_ARGS+=(--markdown "$REPO/docs/CONFORMANCE.md" --ref "$S3TESTS_REF" --duration "$(( $(date +%s) - START ))")
fi
python3 "$HERE/report.py" "${REPORT_ARGS[@]}"
