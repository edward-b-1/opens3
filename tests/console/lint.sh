#!/usr/bin/env bash
# Lint the console's static JavaScript (internal/console/static) with ESLint.
#
#   tests/console/lint.sh
#
# ESLint runs in a pinned node:<version>-alpine Docker container with the
# static directory mounted read-only. The packages are installed once into
# tests/console/.cache/eslint (git-ignored); later runs are offline and
# take a few seconds. Fails when ESLint reports an error.
#
# Environment:
#   CONSOLE_NODE_IMAGE  Docker image (default node:<pinned>-alpine)
#   CONSOLE_LINT_FIX    set to 1 to run eslint --fix (writes into the static dir)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
STATIC="$REPO/internal/console/static"

NODE_IMAGE="${CONSOLE_NODE_IMAGE:-node:22.23.2-alpine}"
ESLINT_VERSION=10.10.0
GLOBALS_VERSION=17.12.0

CACHE="$HERE/.cache/eslint"
mkdir -p "$CACHE/home" "$CACHE/work"

log() { echo "[lint-js] $*" >&2; }

command -v docker >/dev/null || { log "docker not found"; exit 1; }
if ! docker image inspect "$NODE_IMAGE" >/dev/null 2>&1; then
  log "pulling $NODE_IMAGE"
  docker pull -q "$NODE_IMAGE" >/dev/null
fi

# ESLint only lints files below its config file's directory, so the static
# dir is mounted at /cache/work/static next to a copy of the config, with
# the cache's node_modules symlinked in. Everything but the cache is
# read-only (the static dir becomes writable for --fix). Run as the
# invoking user so the cache is not root-owned on the host.
STATIC_MODE=ro
[ "${CONSOLE_LINT_FIX:-0}" = 1 ] && STATIC_MODE=rw
run() {
  docker run --rm --user "$(id -u):$(id -g)" \
    -v "$HERE:/suite:ro" -v "$CACHE:/cache" -v "$STATIC:/cache/work/static:$STATIC_MODE" \
    -e HOME=/cache/home -e npm_config_cache=/cache/npm -e npm_config_update_notifier=false \
    -w /cache "$NODE_IMAGE" "$@"
}

# --- 1. install (once) -----------------------------------------------------
want="eslint@$ESLINT_VERSION globals@$GLOBALS_VERSION"
if [ "$(cat "$CACHE/installed" 2>/dev/null)" != "$want" ]; then
  log "installing $want into $CACHE"
  rm -rf "$CACHE/node_modules" "$CACHE/package.json" "$CACHE/package-lock.json" "$CACHE/installed"
  run sh -c 'npm init -y >/dev/null && npm install --no-audit --no-fund --save-exact '"$want"' >/dev/null'
  echo "$want" >"$CACHE/installed"
fi

# --- 2. lint ---------------------------------------------------------------
FIX=""
[ "$STATIC_MODE" = rw ] && FIX="--fix"
log "eslint $ESLINT_VERSION on $STATIC"
run sh -c 'ln -sfn /cache/node_modules /cache/work/node_modules && cp /suite/eslint.config.js /cache/work/ && cd /cache/work && node node_modules/eslint/bin/eslint.js --config eslint.config.js '"$FIX"' static'
log "ok"
