#!/usr/bin/env bash
# Shared helpers for the AWS CLI suites (suite.sh, transport.sh). Sourced,
# not executed. Expects OUT, RESULTS_TSV, AWS_BIN, SCRATCH to be set.
aws() { command "$AWS_BIN" "$@"; }

# --- helpers ---------------------------------------------------------------------
NPASS=0
NFAIL=0

# _cmdid ARGS... -> sets SVC and CMD from the first "aws SVC CMD" in the args
# (tokens before "aws", e.g. env VAR=... prefixes, are skipped). The caller
# may override with CASE_CMD="svc cmd" (for curl fetches of presigned URLs).
_cmdid() {
  SVC="" CMD=""
  if [ -n "${CASE_CMD:-}" ]; then
    SVC="${CASE_CMD%% *}" CMD="${CASE_CMD#* }"
    return
  fi
  local seen=0 tok
  for tok in "$@"; do
    if [ $seen = 0 ]; then
      case "$tok" in aws|*/aws) seen=1;; esac
      continue
    fi
    case "$tok" in --*) continue;; esac
    if [ -z "$SVC" ]; then SVC="$tok"; elif [ -z "$CMD" ]; then CMD="$tok"; break; fi
  done
}

_sanitize() { tr '\t\r\n' '   ' | cut -c1-300; }

# _record KIND NAME STATUS DETAIL ARGS...
_record() {
  local kind="$1" name="$2" status="$3" detail="$4"; shift 4
  _cmdid "$@"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$SVC" "$CMD" "$kind" "$status" "$(printf '%s' "$detail" | _sanitize)" >>"$RESULTS_TSV"
  if [ "$status" = PASS ]; then NPASS=$((NPASS + 1)); echo "  ok   $name ($SVC $CMD)"
  else NFAIL=$((NFAIL + 1)); echo "  FAIL $name ($SVC $CMD): $detail"; fi
}

# _run NAME ARGS... : runs the command, stdout to $OUT/cases/NAME.out, stderr to
# NAME.err, sets RC.
_run() {
  local name="$1"; shift
  "$@" >"$OUT/cases/$name.out" 2>"$OUT/cases/$name.err" </dev/null
  RC=$?
}

_err1() { head -c 400 "$OUT/cases/$1.err" | tr '\n' ' '; }

# ok NAME -- CMD...  : expects exit 0
ok() {
  local name="$1"; shift; [ "$1" = -- ] && shift
  _run "$name" "$@"
  if [ $RC -eq 0 ]; then _record ok "$name" PASS "" "$@"
  else _record ok "$name" FAIL "exit $RC: $(_err1 "$name")" "$@"; fi
}

# eq NAME EXPECTED -- CMD... : expects exit 0 and stdout == EXPECTED (trimmed)
eq() {
  local name="$1" want="$2"; shift 2; [ "$1" = -- ] && shift
  _run "$name" "$@"
  local got
  got="$(sed -e 's/[[:space:]]*$//' "$OUT/cases/$name.out" | tr -d '\r')"
  if [ $RC -ne 0 ]; then _record eq "$name" FAIL "exit $RC: $(_err1 "$name")" "$@"
  elif [ "$got" = "$want" ]; then _record eq "$name" PASS "" "$@"
  else _record eq "$name" FAIL "expected [$want] got [$(printf '%s' "$got" | head -c 200 | tr '\n' ' ')]" "$@"; fi
}

# _expect_error KIND NAME CODE -- CMD... : expects non-zero exit and "(CODE)" in stderr
_expect_error() {
  local kind="$1" name="$2" code="$3"; shift 3; [ "$1" = -- ] && shift
  _run "$name" "$@"
  if [ $RC -eq 0 ]; then _record "$kind" "$name" FAIL "expected error $code but exit 0" "$@"
  elif grep -q "($code)" "$OUT/cases/$name.err"; then _record "$kind" "$name" PASS "$code" "$@"
  else _record "$kind" "$name" FAIL "expected $code: $(_err1 "$name")" "$@"; fi
}
# fails NAME CODE -- CMD... : a negative case of a supported command
fails() { _expect_error fails "$@"; }
# unsupported NAME CODE -- CMD... : a command OpenS3 does not implement
unsupported() { _expect_error unsupported "$@"; }

# capture NAME -- CMD... : run and echo stdout (trimmed) without recording
capture() {
  local name="$1"; shift; [ "$1" = -- ] && shift
  "$@" 2>"$OUT/cases/$name.err" </dev/null | sed -e 's/[[:space:]]*$//' | tr -d '\r'
}

sha() { sha256sum "$1" | cut -c1-64; }
same_file() { [ "$(sha "$1")" = "$(sha "$2")" ]; }

# fails_with NAME PATTERN -- CMD... : expects non-zero exit and stderr matching
# the extended regex PATTERN (for client-side errors that carry no S3 code).
fails_with() {
  local name="$1" pat="$2"; shift 2; [ "$1" = -- ] && shift
  _run "$name" "$@"
  if [ $RC -eq 0 ]; then _record fails "$name" FAIL "expected an error matching /$pat/ but exit 0" "$@"
  elif grep -Eq "$pat" "$OUT/cases/$name.err"; then _record fails "$name" PASS "$(grep -Eo "$pat" "$OUT/cases/$name.err" | head -1)" "$@"
  else _record fails "$name" FAIL "expected /$pat/: $(_err1 "$name")" "$@"; fi
}
