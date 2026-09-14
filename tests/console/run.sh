#!/usr/bin/env bash
# Browser smoke test of the web console.
#
#   tests/console/run.sh [extra playwright test args, e.g. -g identity]
#
# Builds opens3, starts it on a free 127.0.0.1 port with a temporary data
# root, and drives the console at /console/ with Playwright Test
# (tests/console/smoke.spec.js) running in the pinned official Playwright
# image with --network host. @playwright/test is installed once into
# tests/console/.cache/playwright (git-ignored); later runs are offline.
# Results (JUnit + JSON reports; screenshots, videos and traces of failed
# tests) land in tests/console/results (git-ignored).
#
# Environment:
#   CONSOLE_BROWSERS   all (default: chromium, firefox and webkit) | comma list, e.g. chromium
#   CONSOLE_PW_IMAGE   Docker image (default mcr.microsoft.com/playwright:<pinned>)
#   CONSOLE_WORKERS    parallel Playwright workers (default 4)
#   CONSOLE_KEEP       set to 1 to keep the server data dir after the run
#   CONSOLE_LOG_LEVEL  server log level (default warn)
#   OPENS3_ROOT_USER / OPENS3_ROOT_PASSWORD  root credentials (defaults below)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
export PATH="$HOME/sdk/go/bin:$PATH"

PW_VERSION=1.63.0
PW_IMAGE="${CONSOLE_PW_IMAGE:-mcr.microsoft.com/playwright:v${PW_VERSION}-noble}"
export CONSOLE_BROWSERS="${CONSOLE_BROWSERS:-all}"
export OPENS3_ROOT_USER="${OPENS3_ROOT_USER:-opens3admin}"
export OPENS3_ROOT_PASSWORD="${OPENS3_ROOT_PASSWORD:-opens3admin}"

RESULTS="$HERE/results"
CACHE="$HERE/.cache/playwright"
rm -rf "$RESULTS"
mkdir -p "$RESULTS" "$CACHE/home" "$CACHE/work"

log() { echo "[console] $*" >&2; }

# --- 1. image and packages ------------------------------------------------------
command -v docker >/dev/null || { log "docker not found"; exit 1; }
if ! docker image inspect "$PW_IMAGE" >/dev/null 2>&1; then
  log "pulling $PW_IMAGE (large)"
  docker pull -q "$PW_IMAGE" >/dev/null
fi
run() {
  docker run --rm --network host --ipc=host --user "$(id -u):$(id -g)" \
    -v "$HERE:/suite:ro" -v "$RESULTS:/out" -v "$CACHE:/cache" \
    -e HOME=/cache/home -e npm_config_cache=/cache/npm -e npm_config_update_notifier=false \
    -w /cache "$PW_IMAGE" "$@"
}
want="@playwright/test@$PW_VERSION"
if [ "$(cat "$CACHE/installed" 2>/dev/null)" != "$want" ]; then
  log "installing $want into $CACHE"
  rm -rf "$CACHE/node_modules" "$CACHE/package.json" "$CACHE/package-lock.json" "$CACHE/installed"
  run sh -c 'npm init -y >/dev/null && npm install --no-audit --no-fund --save-exact '"$want"' >/dev/null'
  echo "$want" >"$CACHE/installed"
fi

# --- 2. server ------------------------------------------------------------------
log "building opens3"
BIN="$RESULTS/opens3"
(cd "$REPO" && go build -o "$BIN" ./cmd/opens3)

DATA="$(mktemp -d "${TMPDIR:-/tmp}/opens3-console.XXXXXX")"
PORT=""
for _ in $(seq 1 50); do
  p=$((20000 + RANDOM % 20000))
  if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then PORT=$p; break; fi
done
[ -n "$PORT" ] || { log "no free port found"; exit 1; }

SERVER_LOG="$RESULTS/server.log"
"$BIN" server --root "$DATA/data" --address "127.0.0.1:$PORT" --no-fsync --log-level "${CONSOLE_LOG_LEVEL:-warn}" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!
cleanup() {
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  if [ "${CONSOLE_KEEP:-0}" = 1 ]; then log "data dir kept at $DATA"; else rm -rf "$DATA"; fi
}
trap cleanup EXIT
for _ in $(seq 1 100); do
  if curl -fs "http://127.0.0.1:$PORT/opens3/health/ready" >/dev/null 2>&1; then break; fi
  kill -0 "$SERVER_PID" 2>/dev/null || { log "server exited early:"; cat "$SERVER_LOG" >&2; exit 1; }
  sleep 0.1
done
log "opens3 listening on 127.0.0.1:$PORT (log: $SERVER_LOG)"

# --- 3. tests -------------------------------------------------------------------
# The spec and config are copied next to the cached node_modules so that
# require('@playwright/test') resolves; the suite dir itself stays read-only.
log "playwright $PW_VERSION, browsers: $CONSOLE_BROWSERS"
START=$(date +%s)
set +e
docker run --rm --network host --ipc=host --user "$(id -u):$(id -g)" \
  -v "$HERE:/suite:ro" -v "$RESULTS:/out" -v "$CACHE:/cache" \
  -e HOME=/cache/home -e BASE_URL="http://127.0.0.1:$PORT" -e PW_RESULTS=/out \
  -e CONSOLE_BROWSERS -e CONSOLE_WORKERS="${CONSOLE_WORKERS:-4}" -e OPENS3_ROOT_USER -e OPENS3_ROOT_PASSWORD \
  -w /cache/work "$PW_IMAGE" \
  sh -c 'ln -sfn /cache/node_modules node_modules && cp /suite/playwright.config.js /suite/*.spec.js . && exec node node_modules/@playwright/test/cli.js test --config playwright.config.js "$@"' sh "$@" \
  2>&1 | tee "$RESULTS/playwright.log"
STATUS=${PIPESTATUS[0]}
set -e
log "finished in $(( $(date +%s) - START ))s (exit $STATUS); report: $RESULTS/junit.xml"
exit "$STATUS"
