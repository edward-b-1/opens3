#!/usr/bin/env bash
# Run the AWS CLI end-to-end suite against a freshly built OpenS3.
#
#   tests/awscli/run.sh
#
# By default the whole suite (tests/awscli/suite.sh) runs inside ONE
# `amazon/aws-cli` Docker container (pinned tag below) with --network host so
# that the `aws` binary in the container talks to the server on the host.
#
# Environment:
#   OPENS3_AWSCLI     docker (default) | native: run suite.sh on the host with
#                     an `aws` found on PATH or in ~/.local/bin
#   AWS_CLI_BIN       path to a native aws binary; implies OPENS3_AWSCLI=native
#   AWSCLI_IMAGE      Docker image (default amazon/aws-cli:<pinned tag>)
#   AWSCLI_KEEP       set to 1 to keep the server data dir after the run
#   AWSCLI_NO_REPORT  set to 1 to skip report.py (docs/AWSCLI.md)
#   AWSCLI_LOG_LEVEL  server log level (default warn)
#   OPENS3_ROOT_USER / OPENS3_ROOT_PASSWORD  root credentials (defaults below)
#   OPENS3_DEFAULT_OBJECT_OWNERSHIP  ownership for new buckets (default ObjectWriter: ACLs enabled)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
export PATH="$HOME/sdk/go/bin:$HOME/.local/bin:$PATH"

AWSCLI_IMAGE="${AWSCLI_IMAGE:-amazon/aws-cli:2.36.44}"
MODE="${OPENS3_AWSCLI:-docker}"
[ -n "${AWS_CLI_BIN:-}" ] && MODE=native

export OPENS3_ROOT_USER="${OPENS3_ROOT_USER:-opens3admin}"
export OPENS3_ROOT_PASSWORD="${OPENS3_ROOT_PASSWORD:-opens3admin}"
# ACL commands (put-bucket-acl, put-object-acl, cp --acl) need ACLs enabled
# on new buckets, i.e. the pre-2023 AWS default.
export OPENS3_DEFAULT_OBJECT_OWNERSHIP="${OPENS3_DEFAULT_OBJECT_OWNERSHIP:-ObjectWriter}"

RESULTS="$HERE/results"
rm -rf "$RESULTS"
mkdir -p "$RESULTS"

log() { echo "[awscli] $*" >&2; }

# --- 1. CLI ---------------------------------------------------------------------
AWS_BIN=""
if [ "$MODE" = native ]; then
  AWS_BIN="${AWS_CLI_BIN:-$(command -v aws 2>/dev/null || true)}"
  [ -n "$AWS_BIN" ] && [ -x "$AWS_BIN" ] || { log "no native aws binary (install AWS CLI v2 into ~/.local or set AWS_CLI_BIN)"; exit 1; }
  CLI_VERSION="$("$AWS_BIN" --version 2>&1 | head -1)"
  log "native aws: $AWS_BIN ($CLI_VERSION)"
else
  MODE=docker
  command -v docker >/dev/null || { log "docker not found and OPENS3_AWSCLI!=native"; exit 1; }
  if ! docker image inspect "$AWSCLI_IMAGE" >/dev/null 2>&1; then
    log "pulling $AWSCLI_IMAGE"
    docker pull -q "$AWSCLI_IMAGE" >/dev/null
  fi
  CLI_VERSION="$(docker run --rm "$AWSCLI_IMAGE" --version 2>&1 | head -1)"
  log "docker aws: $AWSCLI_IMAGE ($CLI_VERSION)"
fi

# --- 2. server ------------------------------------------------------------------
log "building opens3"
BIN="$RESULTS/opens3"
(cd "$REPO" && go build -o "$BIN" ./cmd/opens3)

DATA="$(mktemp -d "${TMPDIR:-/tmp}/opens3-awscli.XXXXXX")"
PORT=""
for _ in $(seq 1 50); do
  p=$((20000 + RANDOM % 20000))
  if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then PORT=$p; break; fi
done
[ -n "$PORT" ] || { log "no free port found"; exit 1; }

SERVER_LOG="$RESULTS/server.log"
"$BIN" server --root "$DATA/data" --address "127.0.0.1:$PORT" --no-fsync --log-level "${AWSCLI_LOG_LEVEL:-warn}" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
cleanup() {
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  if [ "${AWSCLI_KEEP:-0}" = 1 ]; then log "data dir kept at $DATA"; else rm -rf "$DATA"; fi
}
trap cleanup EXIT
for _ in $(seq 1 100); do
  if curl -fs "http://127.0.0.1:$PORT/opens3/health/ready" >/dev/null 2>&1; then break; fi
  kill -0 "$SERVER_PID" 2>/dev/null || { log "server exited early:"; cat "$SERVER_LOG" >&2; exit 1; }
  sleep 0.1
done
log "opens3 listening on 127.0.0.1:$PORT (log: $SERVER_LOG)"

# --- 3. suite -------------------------------------------------------------------
# The CLI honours AWS_ENDPOINT_URL for every service (s3, iam, sts), and uses
# path-style addressing for an IP endpoint.
CLI_ENV=(
  AWS_ACCESS_KEY_ID="$OPENS3_ROOT_USER"
  AWS_SECRET_ACCESS_KEY="$OPENS3_ROOT_PASSWORD"
  AWS_DEFAULT_REGION=us-east-1
  AWS_ENDPOINT_URL="http://127.0.0.1:$PORT"
  AWS_PAGER=
  AWS_MAX_ATTEMPTS=2
  AWS_RETRY_MODE=standard
  OPENS3_ACCOUNT_ID="${OPENS3_ACCOUNT_ID:-000000000000}"
)
START=$(date +%s)
set +e
if [ "$MODE" = native ]; then
  SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/opens3-awscli-scratch.XXXXXX")"
  env "${CLI_ENV[@]}" AWS_BIN="$AWS_BIN" AWSCLI_OUT="$RESULTS" AWSCLI_SCRATCH="$SCRATCH" \
    bash "$HERE/suite.sh" 2>&1 | tee "$RESULTS/suite.log"
  SUITE_STATUS=${PIPESTATUS[0]}
  rm -rf "$SCRATCH"
else
  SCRATCH="$RESULTS/scratch"
  mkdir -p "$SCRATCH"
  ENV_ARGS=()
  for kv in "${CLI_ENV[@]}"; do ENV_ARGS+=(-e "$kv"); done
  # One container for the whole suite: the suite dir is read-only, results and
  # a scratch dir (also HOME, for the CLI's cache) are writable. Run as the
  # invoking user so the results are not root-owned on the host.
  docker run --rm --network host --user "$(id -u):$(id -g)" \
    -v "$HERE:/suite:ro" -v "$RESULTS:/out" -v "$SCRATCH:/scratch" \
    "${ENV_ARGS[@]}" -e HOME=/scratch -e AWS_BIN=aws -e AWSCLI_OUT=/out -e AWSCLI_SCRATCH=/scratch \
    --entrypoint bash "$AWSCLI_IMAGE" /suite/suite.sh 2>&1 | tee "$RESULTS/suite.log"
  SUITE_STATUS=${PIPESTATUS[0]}
  rm -rf "$SCRATCH"
fi
set -e

# --- 3b. transport scenarios ----------------------------------------------------
# A second server with a self-signed certificate; transport.sh runs the CLI
# against it and against the plain server with matching and mismatching
# schemes, appending to the same results file.
TLS_DIR="$RESULTS/tls"
mkdir -p "$TLS_DIR"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" -keyout "$TLS_DIR/tls.key" -out "$TLS_DIR/tls.crt" 2>/dev/null
TLS_PORT=""
for _ in $(seq 1 50); do
  p=$((20000 + RANDOM % 20000))
  [ "$p" = "$PORT" ] && continue
  if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then TLS_PORT=$p; break; fi
done
TLS_DATA="$(mktemp -d "${TMPDIR:-/tmp}/opens3-awscli-tls.XXXXXX")"
# OPENS3_HSTS=1: the test certificate is self-signed, which turns HSTS off by default.
OPENS3_TLS_CERT="$TLS_DIR/tls.crt" OPENS3_TLS_KEY="$TLS_DIR/tls.key" OPENS3_HSTS=1 \
  "$BIN" server --root "$TLS_DATA/data" --address "127.0.0.1:$TLS_PORT" --no-fsync --log-level "${AWSCLI_LOG_LEVEL:-warn}" >"$RESULTS/server-tls.log" 2>&1 &
TLS_PID=$!
for _ in $(seq 1 100); do
  if curl -fs --cacert "$TLS_DIR/tls.crt" "https://127.0.0.1:$TLS_PORT/opens3/health/ready" >/dev/null 2>&1; then break; fi
  kill -0 "$TLS_PID" 2>/dev/null || { log "tls server exited early:"; cat "$RESULTS/server-tls.log" >&2; exit 1; }
  sleep 0.1
done
log "tls server listening on 127.0.0.1:$TLS_PORT"
set +e
if [ "$MODE" = native ]; then
  SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/opens3-awscli-scratch.XXXXXX")"
  env "${CLI_ENV[@]}" PLAIN_PORT="$PORT" TLS_PORT="$TLS_PORT" TLS_CA="$TLS_DIR/tls.crt" \
    AWS_BIN="$AWS_BIN" AWSCLI_OUT="$RESULTS" AWSCLI_SCRATCH="$SCRATCH" \
    bash "$HERE/transport.sh" 2>&1 | tee -a "$RESULTS/suite.log"
  TRANSPORT_STATUS=${PIPESTATUS[0]}
  rm -rf "$SCRATCH"
else
  SCRATCH="$RESULTS/scratch"
  mkdir -p "$SCRATCH"
  docker run --rm --network host --user "$(id -u):$(id -g)" \
    -v "$HERE:/suite:ro" -v "$RESULTS:/out" -v "$SCRATCH:/scratch" \
    "${ENV_ARGS[@]}" -e PLAIN_PORT="$PORT" -e TLS_PORT="$TLS_PORT" -e TLS_CA=/out/tls/tls.crt \
    -e HOME=/scratch -e AWS_BIN=aws -e AWSCLI_OUT=/out -e AWSCLI_SCRATCH=/scratch \
    --entrypoint bash "$AWSCLI_IMAGE" /suite/transport.sh 2>&1 | tee -a "$RESULTS/suite.log"
  TRANSPORT_STATUS=${PIPESTATUS[0]}
  rm -rf "$SCRATCH"
fi
set -e
kill "$TLS_PID" 2>/dev/null || true
wait "$TLS_PID" 2>/dev/null || true
rm -rf "$TLS_DATA"
[ "$TRANSPORT_STATUS" -eq 0 ] || SUITE_STATUS=$TRANSPORT_STATUS
DURATION=$(( $(date +%s) - START ))
log "suite finished in ${DURATION}s (exit $SUITE_STATUS)"

# --- 4. report ------------------------------------------------------------------
if [ "${AWSCLI_NO_REPORT:-0}" = 1 ]; then exit "$SUITE_STATUS"; fi
[ -f "$RESULTS/results.tsv" ] || { log "no results produced"; exit 1; }
python3 "$HERE/report.py" --results "$RESULTS" --markdown "$REPO/docs/AWSCLI.md" \
  --version "$CLI_VERSION" --mode "$MODE" --image "$([ "$MODE" = docker ] && echo "$AWSCLI_IMAGE" || echo "$AWS_BIN")" \
  --duration "$DURATION"
