#!/usr/bin/env bash
# MinIO to OpenS3 migration, end to end, entirely in Docker: the procedure
# the user manual describes (chapter 11), run against a real MinIO.
#
#   tests/migration/run.sh
#
# MinIO and its client are built from source at pinned releases
# (Dockerfile.minio; nothing is pulled from a MinIO registry). The MinIO
# is seeded with buckets, objects with metadata and tags, a versioned
# bucket, users, groups and policies. OpenS3 is built from the repository
# Dockerfile. Then, exactly as the manual says: rclone (a generic S3
# client) copies the objects with their metadata, copy_tags.py copies the
# tags, `mc admin` (the only MinIO-specific step, reading from MinIO)
# exports the identities and import_identities.py (boto3) recreates them
# in OpenS3, and a verifier compares the two servers object by object and
# identity by identity. Finally `opens3 fsck check --verify` inspects the
# result.
#
# Environment:
#   MIGRATION_KEEP        set to 1 to keep the OpenS3 data directory
#   MIGRATION_LOG_LEVEL   OpenS3 log level (default warn)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

MINIO_IMAGE="opens3-migration-minio:local" # built by Dockerfile.minio (minio + mc)
RCLONE_IMAGE="rclone/rclone:1.69.1"
PY_IMAGE="python:3.13.15-slim"
BOTO3_VERSION="1.43.93"
IMAGE="opens3-migration-test:local"
TAG="$$"
MINIO_NAME="opens3-migration-minio-$TAG"
OPENS3_NAME="opens3-migration-opens3-$TAG"
MINIO_VOL="opens3-migration-minio-$TAG"
MINIO_USER=minioadmin
MINIO_PASSWORD=minioadmin-migration-test
ROOT_USER=opens3admin
ROOT_PASSWORD=opens3admin-migration-test
UID_GID="$(id -u):$(id -g)"

RESULTS="$HERE/results"
rm -rf "$RESULTS"
mkdir -p "$RESULTS"
: >"$RESULTS/results.tsv"
log() { echo "[migration] $*" >&2; }
command -v docker >/dev/null || { log "docker not found"; exit 1; }

STEP=0
FAILED=0
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
expect() {
  local want="$1"; shift
  local out
  out="$("$@" 2>&1)" || { echo "$out"; echo "--- command failed"; return 1; }
  echo "$out"
  grep -qF -- "$want" <<<"$out" || { echo "--- output lacks: $want"; return 1; }
}

free_port() {
  local p
  for _ in $(seq 1 50); do
    p=$((20000 + RANDOM % 20000))
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then echo "$p"; return; fi
  done
  return 1
}
MPORT=$(free_port); MCPORT=$(free_port); OPORT=$(free_port)
MINIO="http://127.0.0.1:$MPORT"
OPENS3="http://127.0.0.1:$OPORT"

WORK="$RESULTS/work"
mkdir -p "$WORK/seed" "$WORK/export/policies" "$WORK/export/groups" "$WORK/py/site"
DATA="$(mktemp -d "${TMPDIR:-/tmp}/opens3-migration.XXXXXX")"
mkdir -p "$DATA/data"

cleanup() {
  docker rm -f "$MINIO_NAME" "$OPENS3_NAME" >/dev/null 2>&1 || true
  docker volume rm "$MINIO_VOL" >/dev/null 2>&1 || true
  if [ "${MIGRATION_KEEP:-0}" = 1 ]; then log "data dir kept at $DATA"; else rm -rf "$DATA"; fi
}
trap cleanup EXIT
finish() {
  local pass fail
  pass=$(grep -c '^PASS' "$RESULTS/results.tsv" || true)
  fail=$(grep -c '^FAIL' "$RESULTS/results.tsv" || true)
  log "$pass passed, $fail failed, $(( $(date +%s) - START ))s (results in $RESULTS)"
  exit "$FAILED"
}
START=$(date +%s)

# mc (from the MinIO image) talks to MinIO through the MC_HOST_src alias;
# it is used to seed MinIO and to export its identities, never against
# OpenS3.
mcsh() {
  docker run --rm --network host -v "$WORK:/work" \
    -e MC_HOST_src="http://$MINIO_USER:$MINIO_PASSWORD@127.0.0.1:$MPORT" \
    --entrypoint sh "$MINIO_IMAGE" -c "$1"
}
# rclone, a generic S3 client, does the copy between the two servers.
rclone() {
  docker run --rm --network host --user "$UID_GID" -v "$WORK:/work" \
    -e RCLONE_CONFIG_SRC_TYPE=s3 -e RCLONE_CONFIG_SRC_PROVIDER=Minio -e RCLONE_CONFIG_SRC_ENDPOINT="$MINIO" \
    -e RCLONE_CONFIG_SRC_ACCESS_KEY_ID="$MINIO_USER" -e RCLONE_CONFIG_SRC_SECRET_ACCESS_KEY="$MINIO_PASSWORD" \
    -e RCLONE_CONFIG_DST_TYPE=s3 -e RCLONE_CONFIG_DST_PROVIDER=Other -e RCLONE_CONFIG_DST_ENDPOINT="$OPENS3" \
    -e RCLONE_CONFIG_DST_ACCESS_KEY_ID="$ROOT_USER" -e RCLONE_CONFIG_DST_SECRET_ACCESS_KEY="$ROOT_PASSWORD" \
    -e RCLONE_CONFIG_DST_FORCE_PATH_STYLE=true -e RCLONE_CONFIG_SRC_FORCE_PATH_STYLE=true \
    "$RCLONE_IMAGE" "$@"
}
py() {
  docker run --rm --network host --user "$UID_GID" -v "$WORK:/work" -v "$REPO/examples/migrate:/migrate:ro" -v "$HERE:/suite:ro" -w /work \
    -e HOME=/work -e PYTHONPATH=/work/py/site -e PYTHONDONTWRITEBYTECODE=1 \
    "$PY_IMAGE" python "$@"
}

# --- images ---------------------------------------------------------------------
build_image() { docker build -q -t "$IMAGE" "$REPO"; }
build_minio() {
  # Cached after the first build (several minutes: MinIO is large).
  docker image inspect "$MINIO_IMAGE" >/dev/null 2>&1 && { echo "using cached $MINIO_IMAGE"; return 0; }
  docker build -q -t "$MINIO_IMAGE" -f "$HERE/Dockerfile.minio" "$HERE"
}
pull_images() {
  for i in "$RCLONE_IMAGE" "$PY_IMAGE"; do
    docker image inspect "$i" >/dev/null 2>&1 || docker pull -q "$i"
  done
  docker run --rm --user "$UID_GID" -v "$WORK:/work" -e HOME=/work "$PY_IMAGE" \
    pip install -q --no-warn-script-location --target /work/py/site "boto3==$BOTO3_VERSION"
}
step "build the OpenS3 image" build_image
step "build MinIO and mc from source (Dockerfile.minio)" build_minio
step "pull rclone and python with boto3" pull_images

# --- servers --------------------------------------------------------------------
start_minio() {
  docker volume create "$MINIO_VOL" >/dev/null
  docker run -d --name "$MINIO_NAME" --network host -v "$MINIO_VOL:/data" \
    -e MINIO_ROOT_USER="$MINIO_USER" -e MINIO_ROOT_PASSWORD="$MINIO_PASSWORD" \
    "$MINIO_IMAGE" server /data --address "127.0.0.1:$MPORT" --console-address "127.0.0.1:$MCPORT" >/dev/null
  for _ in $(seq 1 200); do
    curl -fs "$MINIO/minio/health/live" >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  docker logs "$MINIO_NAME" 2>&1 | tail -20
  return 1
}
start_opens3() {
  docker run -d --name "$OPENS3_NAME" --network host --user "$UID_GID" -v "$DATA/data:/data" \
    -e OPENS3_ROOT_USER="$ROOT_USER" -e OPENS3_ROOT_PASSWORD="$ROOT_PASSWORD" \
    "$IMAGE" server --root /data --address "127.0.0.1:$OPORT" --no-fsync --log-level "${MIGRATION_LOG_LEVEL:-warn}" >/dev/null
  for _ in $(seq 1 100); do
    curl -fs "$OPENS3/opens3/health/ready" >/dev/null 2>&1 && return 0
    sleep 0.1
  done
  docker logs "$OPENS3_NAME" 2>&1 | tail -20
  return 1
}
step "start MinIO" start_minio
step "start OpenS3" start_opens3

# --- seed MinIO ------------------------------------------------------------------
seed() {
  head -c 3000000 /dev/urandom >"$WORK/seed/photo.bin"
  head -c 12000000 /dev/urandom >"$WORK/seed/large.bin" # above mc's multipart threshold
  printf 'quarterly report v1\n' >"$WORK/seed/report-v1.txt"
  printf 'quarterly report v2, revised\n' >"$WORK/seed/report-v2.txt"
  printf '{"hello":"world"}' >"$WORK/seed/data.json"
  cat >"$WORK/seed/photos-ro.json" <<'EOF'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket"],"Resource":["arn:aws:s3:::photos","arn:aws:s3:::photos/*"]}]}
EOF
  mcsh '
set -e
mc mb src/photos src/docs src/archive
mc version enable src/archive
mc cp --attr "x-amz-meta-owner=alice;x-amz-meta-camera=x100" --tags "env=prod&team=blue" /work/seed/photo.bin src/photos/2026/photo.bin
mc cp /work/seed/large.bin src/photos/2026/large.bin
mc cp --attr "Content-Type=application/json" --tags "kind=config" /work/seed/data.json src/docs/config/data.json
mc cp /work/seed/report-v1.txt src/archive/report.txt
mc cp /work/seed/report-v2.txt src/archive/report.txt
mc admin policy create src photos-ro /work/seed/photos-ro.json
mc admin user add src alice alice-secret-000
mc admin user add src bob bob-secret-0000
mc admin policy attach src photos-ro --user alice
mc admin policy attach src readwrite --user bob
mc admin group add src analysts alice
mc admin policy attach src readonly --group analysts
mc ls --recursive src
'
}
step "seed MinIO with buckets, objects, versions, users, groups and policies" seed

# --- the migration, as the manual describes it ------------------------------------
copy_objects() {
  local buckets
  buckets="$(rclone lsd src: | tr -s ' ' | cut -d ' ' -f 6)"
  [ -n "$buckets" ] || { echo "no buckets listed on the source"; return 1; }
  for b in $buckets; do
    echo "== bucket $b"
    rclone mkdir "dst:$b"
    rclone sync --metadata "src:$b" "dst:$b"
  done
  rclone lsd dst:
}
step "copy the objects with rclone sync --metadata" copy_objects
step "copy the object tags (rclone does not)" py /migrate/copy_tags.py --src "$MINIO" --src-key "$MINIO_USER" --src-secret "$MINIO_PASSWORD" --dst "$OPENS3" --dst-key "$ROOT_USER" --dst-secret "$ROOT_PASSWORD"
export_identities() {
  mcsh '
set -e
mc admin user list src --json > /work/export/users.json
mc admin group list src --json > /work/export/groups.json
for p in $(mc admin policy list src); do mc admin policy info src "$p" > "/work/export/policies/$p.json"; done
for g in $(mc admin group list src); do mc admin group info src "$g" --json > "/work/export/groups/$g.json"; done
ls -la /work/export /work/export/policies /work/export/groups
'
}
step "export users, groups and policies from MinIO with mc admin" export_identities
step "import the identities into OpenS3 (new keys issued)" expect "keys issued" py /migrate/import_identities.py --endpoint "$OPENS3" --access-key "$ROOT_USER" --secret-key "$ROOT_PASSWORD" --export /work/export --out /work/new-keys.csv
step "a second import changes nothing" expect "0 keys issued" py /migrate/import_identities.py --endpoint "$OPENS3" --access-key "$ROOT_USER" --secret-key "$ROOT_PASSWORD" --export /work/export --out /work/new-keys-2.csv

# --- verification ----------------------------------------------------------------
step "objects, metadata, tags and identities match" expect "VERIFIED" py /suite/verify.py --src "$MINIO" --src-key "$MINIO_USER" --src-secret "$MINIO_PASSWORD" --dst "$OPENS3" --dst-key "$ROOT_USER" --dst-secret "$ROOT_PASSWORD" --keys /work/new-keys.csv
stop_opens3() { docker rm -f "$OPENS3_NAME" >/dev/null; }
step "stop OpenS3" stop_opens3
step "opens3 fsck check --verify on the migrated data" expect "No problems found" docker run --rm --user "$UID_GID" -v "$DATA/data:/data" "$IMAGE" fsck check --verify --root /data

finish
