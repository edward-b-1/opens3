#!/usr/bin/env bash
# Compiles docs/manual/src/*.md into the single-file docs/MANUAL.md.
# Chapter files start with "# N. Title"; they become level-2 sections and a
# table of contents is generated from them.
set -euo pipefail
cd "$(dirname "$0")"
out=../MANUAL.md
{
  cat <<'HDR'
# OpenS3 User Manual

OpenS3 is an S3-compatible object store in a single binary: buckets and
objects with versioning, object lock, lifecycle rules, encryption,
notifications, a full identity system driven by the same tools as AWS, and
a built-in web console. This manual is for people who download OpenS3 and
run it for themselves. It is self-contained; the source distribution has
further reference material for developers.

_Generated from `docs/manual/src/` by `docs/manual/build.sh`; edit the
chapter files, not this file._

## Contents

HDR
  for f in src/[0-9]*.md; do
    title=$(sed -n '1s/^# //p' "$f")
    anchor=$(printf '%s' "$title" | tr 'A-Z' 'a-z' | sed 's/[^a-z0-9 -]//g; s/ /-/g')
    printf -- '- [%s](#%s)\n' "$title" "$anchor"
  done
  for f in src/[0-9]*.md; do
    printf '\n---\n\n'
    # Demote headings one level so the manual has a single H1.
    sed -e 's/^### /#### /' -e 's/^## /### /' -e 's/^# /## /' "$f"
  done
} > "$out"
echo "wrote $out ($(wc -l < "$out") lines)"
