#!/usr/bin/env bash
# Master key rotation lifecycle, end to end, entirely in Docker.
#
#   tests/rotation/run.sh
#
# Builds the OpenS3 image from the repository Dockerfile, runs the server
# from it, and drives `opens3 master` from the same image. The data is
# written and verified by examples/python/encrypted_data.py (boto3) in a
# python container: SSE-S3, SSE-KMS and SSE-C objects, an object under a
# bucket default-encryption rule, an SSE-S3 multipart upload left in
# progress, and an IAM user's access key. The lifecycle:
#
# The server runs with --tls self-signed (SSE-C needs a secure connection)
# and the python container trusts the generated certificate.
#
#   1. file mode: first start generates k1; create data; verify
#   2. rotate (k2 added, everything re-wrapped); verify
#   3. retire (k1 removed); verify; the saved k1 ring no longer opens the data
#   4. move to an environment key (rewrap, retire deletes the file); verify
#   5. rotate environment key A to B; verify; A is refused
#   6. move back to a key file (OPENS3_MASTER_KEY_OLD); verify
#
# Environment:
#   ROTATION_KEEP      set to 1 to keep the data directory after the run
#   ROTATION_PY_IMAGE  python image (default python:<pinned>-slim)
#   ROTATION_LOG_LEVEL server log level (default warn)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

PY_IMAGE="${ROTATION_PY_IMAGE:-python:3.13.15-slim}"
BOTO3_VERSION="1.43.93" # as pinned in examples/python/uv.lock
IMAGE="opens3-rotation-test:local"
NAME="opens3-rotation-test-$$"
ROOT_USER=opens3admin
ROOT_PASSWORD=opens3admin-rotation-test
UID_GID="$(id -u):$(id -g)"

RESULTS="$HERE/results"
rm -rf "$RESULTS"
mkdir -p "$RESULTS"
: >"$RESULTS/results.tsv"
: >"$RESULTS/server.log"
: >"$RESULTS/master.log"

log() { echo "[rotation] $*" >&2; }
command -v docker >/dev/null || { log "docker not found"; exit 1; }

STEP=0
FAILED=0
# step NAME CMD...: runs CMD, records PASS/FAIL, and stops the run on the
# first failure (later steps depend on earlier ones).
step() {
  local name="$1"; shift
  STEP=$((STEP + 1))
  local out
  if out="$("$@" 2>&1)"; then
    printf 'PASS\t%02d\t%s\n' "$STEP" "$name" >>"$RESULTS/results.tsv"
    log "$(printf '%02d PASS %s' "$STEP" "$name")"
  else
    printf 'FAIL\t%02d\t%s\n' "$STEP" "$name" >>"$RESULTS/results.tsv"
    log "$(printf '%02d FAIL %s' "$STEP" "$name")"
    echo "$out" >&2
    FAILED=1
    finish
  fi
}

# expect SUBSTRING CMD...: CMD must succeed and print SUBSTRING.
expect() {
  local want="$1"; shift
  local out
  out="$("$@" 2>&1)" || { echo "$out"; echo "--- command failed"; return 1; }
  echo "$out"
  grep -qF -- "$want" <<<"$out" || { echo "--- output lacks: $want"; return 1; }
}

# expect_fail SUBSTRING CMD...: CMD must fail and print SUBSTRING.
expect_fail() {
  local want="$1"; shift
  local out
  if out="$("$@" 2>&1)"; then echo "$out"; echo "--- command unexpectedly succeeded"; return 1; fi
  echo "$out"
  grep -qF -- "$want" <<<"$out" || { echo "--- output lacks: $want"; return 1; }
}

# --- containers -----------------------------------------------------------------
DATA="$(mktemp -d "${TMPDIR:-/tmp}/opens3-rotation.XXXXXX")"
mkdir -p "$DATA/data"
PORT=""
for _ in $(seq 1 50); do
  p=$((20000 + RANDOM % 20000))
  if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then PORT=$p; break; fi
done
[ -n "$PORT" ] || { log "no free port found"; exit 1; }

server_running() { docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null | grep -q true; }

# server_start [docker -e args...]: starts the server container on the shared
# data directory and waits for readiness.
server_start() {
  docker run -d --name "$NAME" --network host --user "$UID_GID" -v "$DATA/data:/data" \
    -e OPENS3_ROOT_USER="$ROOT_USER" -e OPENS3_ROOT_PASSWORD="$ROOT_PASSWORD" "$@" \
    "$IMAGE" server --root /data --address "127.0.0.1:$PORT" --tls self-signed --no-fsync --log-level "${ROTATION_LOG_LEVEL:-warn}" >/dev/null
  for _ in $(seq 1 100); do
    if [ -s "$DATA/data/tls/tls.crt" ] && curl -fs --cacert "$DATA/data/tls/tls.crt" "https://127.0.0.1:$PORT/opens3/health/ready" >/dev/null 2>&1; then return 0; fi
    server_running || { echo "server exited early:"; docker logs "$NAME" 2>&1; server_stop; return 1; }
    sleep 0.1
  done
  echo "server did not become ready"; docker logs "$NAME" 2>&1; server_stop; return 1
}

server_stop() {
  if docker inspect "$NAME" >/dev/null 2>&1; then
    docker stop -t 10 "$NAME" >/dev/null 2>&1 || true
    { echo "----- $(date -u +%FT%TZ)"; docker logs "$NAME" 2>&1; } >>"$RESULTS/server.log"
    docker rm -f "$NAME" >/dev/null 2>&1 || true
  fi
}

# server_must_fail SUBSTRING [docker -e args...]: a foreground start that must
# exit non-zero with SUBSTRING in its log.
server_must_fail() {
  local want="$1"; shift
  expect_fail "$want" docker run --rm --user "$UID_GID" -v "$DATA/data:/data" \
    -e OPENS3_ROOT_USER="$ROOT_USER" -e OPENS3_ROOT_PASSWORD="$ROOT_PASSWORD" "$@" \
    "$IMAGE" server --root /data --address "127.0.0.1:$PORT" --tls self-signed --no-fsync
}

# master [docker -e args...] -- CMD: runs `opens3 master CMD --root /data`.
master() {
  local envs=()
  while [ "$1" != "--" ]; do envs+=("$1"); shift; done
  shift
  local out st
  out="$(docker run --rm --user "$UID_GID" -v "$DATA/data:/data" "${envs[@]}" "$IMAGE" master "$@" --root /data 2>&1)"
  st=$?
  { echo "----- opens3 master $*"; echo "$out"; } >>"$RESULTS/master.log"
  echo "$out"
  return $st
}

# Same data directory mounted from a copy (for the negative check).
master_on() {
  local dir="$1"; shift
  docker run --rm --user "$UID_GID" -v "$dir:/data" "$IMAGE" master "$@" --root /data
}

PY="$RESULTS/py"
mkdir -p "$PY/site" "$PY/generated"
cp "$REPO/examples/python/s3conf.py" "$REPO/examples/python/encrypted_data.py" "$PY/"
py() {
  docker run --rm --network host --user "$UID_GID" -v "$PY:/py" -v "$DATA/data/tls:/tls:ro" -w /py \
    -e HOME=/py -e PYTHONPATH=/py/site -e PYTHONDONTWRITEBYTECODE=1 \
    -e OPENS3_ENDPOINT="https://127.0.0.1:$PORT" -e OPENS3_TLS_VERIFY=/tls/tls.crt -e OPENS3_ACCESS_KEY="$ROOT_USER" -e OPENS3_SECRET_KEY="$ROOT_PASSWORD" \
    -e OPENS3_REGION=us-east-1 -e OPENS3_BUCKET_PREFIX=rotation \
    "$PY_IMAGE" python encrypted_data.py "$@"
}
create() { expect "wrote /py/generated/encrypted-manifest.json" py; }
verify() { expect "PASSED: every encrypted record reads back" py --verify; }

finish() {
  server_stop
  if [ "${ROTATION_KEEP:-0}" = 1 ]; then log "data dir kept at $DATA"; else rm -rf "$DATA"; fi
  local pass fail
  pass=$(grep -c '^PASS' "$RESULTS/results.tsv" || true)
  fail=$(grep -c '^FAIL' "$RESULTS/results.tsv" || true)
  log "$pass passed, $fail failed, $(( $(date +%s) - START ))s (results in $RESULTS)"
  exit "$FAILED"
}
trap 'server_stop; [ "${ROTATION_KEEP:-0}" = 1 ] || rm -rf "$DATA"' EXIT
START=$(date +%s)

# --- 0. images ------------------------------------------------------------------
build_image() { docker build -q -t "$IMAGE" "$REPO"; }
pull_python() {
  docker image inspect "$PY_IMAGE" >/dev/null 2>&1 || docker pull -q "$PY_IMAGE"
  docker run --rm --user "$UID_GID" -v "$PY:/py" -e HOME=/py "$PY_IMAGE" \
    pip install -q --no-warn-script-location --target /py/site "boto3==$BOTO3_VERSION"
}
step "build the OpenS3 image from the Dockerfile" build_image
step "python image with boto3 $BOTO3_VERSION" pull_python

# --- 1. file mode: first start, data, verify ------------------------------------
step "first start generates the key file" server_start
step "create encrypted records (SSE-S3, SSE-KMS, SSE-C, default rule, upload, IAM key)" create
step "verify before any rotation" verify
step "stop the server" server_stop
status_k1_only() {
  local out
  out="$(master -- status)" || { echo "$out"; return 1; }
  echo "$out"
  grep -qE '^  k1 .*\(current\)$' <<<"$out" || { echo "--- k1 is not the current key"; return 1; }
  ! grep -qE '^  k2 ' <<<"$out" || { echo "--- k2 is still listed"; return 1; }
}
step "status shows k1 as the only key" status_k1_only
cp "$DATA/data/meta/master.keys" "$RESULTS/master.keys.k1"

# --- 2. rotate ------------------------------------------------------------------
step "rotate adds k2 and re-wraps every record" expect "Added key k2" master -- rotate
rotated_status() {
  local out
  out="$(master -- status)" || { echo "$out"; return 1; }
  echo "$out"
  grep -qE '^  k2 .*\(current\)$' <<<"$out" || { echo "--- k2 is not current"; return 1; }
  grep -qE '^  k1 .* 0$' <<<"$out" || { echo "--- k1 still protects records"; return 1; }
  grep -qF "opens3 master retire" <<<"$out" || { echo "--- retire not suggested"; return 1; }
}
step "status: k2 current, k1 protects nothing" rotated_status
step "rewrap --dry-run finds nothing left" expect "Dry run: 0 records" master -- rewrap --dry-run
step "start on the rotated ring" server_start
step "verify after rotation (completes the pending upload)" verify
step "create a fresh pending upload and key for the next step" create
step "stop the server" server_stop

# --- 3. retire ------------------------------------------------------------------
step "retire removes k1" expect "Removed k1" master -- retire
one_key_left() { [ "$(grep -c '"id"' "$DATA/data/meta/master.keys")" = 1 ] && ! grep -q '"k1"' "$DATA/data/meta/master.keys"; }
step "key file holds only k2" one_key_left
step "start on the pruned ring" server_start
step "verify after retire" verify
step "stop the server" server_stop
old_ring_rejected() {
  local copy
  copy="$(mktemp -d "${TMPDIR:-/tmp}/opens3-rotation-copy.XXXXXX")"
  cp -a "$DATA/data/." "$copy/"
  cp "$RESULTS/master.keys.k1" "$copy/meta/master.keys"
  expect_fail "does not match" master_on "$copy" status
  local st=$?
  rm -rf "$copy"
  return $st
}
step "the retired k1 ring no longer opens the data" old_ring_rejected

# --- 4. move to an environment key ----------------------------------------------
KEY_A="$(openssl rand -base64 32)"
KEY_B="$(openssl rand -base64 32)"
step "rewrap under OPENS3_MASTER_KEY (file keys as fallback)" expect "Re-wrapped" master -e OPENS3_MASTER_KEY="$KEY_A" -- rewrap
step "retire deletes the key file" expect "Removed /data/meta/master.keys" master -e OPENS3_MASTER_KEY="$KEY_A" -- retire
no_key_file() { [ ! -e "$DATA/data/meta/master.keys" ]; }
step "key file is gone" no_key_file
step "start without a key generates a mismatching file and is refused" server_must_fail "does not match"
rm -f "$DATA/data/meta/master.keys"
step "start with OPENS3_MASTER_KEY" server_start -e OPENS3_MASTER_KEY="$KEY_A"
step "verify under the environment key" verify
step "create a fresh pending upload and key for the next step" create
step "stop the server" server_stop

# --- 5. rotate between environment keys -----------------------------------------
step "rotate without OPENS3_MASTER_KEY_NEW is refused" expect_fail "OPENS3_MASTER_KEY_NEW" master -e OPENS3_MASTER_KEY="$KEY_A" -- rotate
step "rotate A to B" expect "Set OPENS3_MASTER_KEY to the value of OPENS3_MASTER_KEY_NEW" \
  master -e OPENS3_MASTER_KEY="$KEY_A" -e OPENS3_MASTER_KEY_NEW="$KEY_B" -- rotate
step "start with the old key A is refused" server_must_fail "does not match" -e OPENS3_MASTER_KEY="$KEY_A"
step "start with the new key B" server_start -e OPENS3_MASTER_KEY="$KEY_B"
step "verify under key B" verify
step "create a fresh pending upload and key for the next step" create
step "stop the server" server_stop

# --- 6. back to a key file ------------------------------------------------------
step "rotate with OPENS3_MASTER_KEY_OLD creates the key file" expect "Created /data/meta/master.keys" \
  master -e OPENS3_MASTER_KEY_OLD="$KEY_B" -- rotate
step "retire reports the old environment key unneeded" expect "OPENS3_MASTER_KEY_OLD is no longer needed" \
  master -e OPENS3_MASTER_KEY_OLD="$KEY_B" -- retire
step "start on the new key file" server_start
step "verify under the key file" verify
step "stop the server" server_stop
step "status shows k1 as the only key again" status_k1_only

finish
