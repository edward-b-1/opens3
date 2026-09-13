#!/usr/bin/env bash
# AWS CLI end-to-end suite for OpenS3. Runs every `aws s3`, `aws s3api`,
# `aws iam` and `aws sts` command OpenS3 supports at least once (and the ones
# it does not, expecting the documented error) and records one line per case
# in $AWSCLI_OUT/results.tsv:
#
#   name <TAB> service <TAB> command <TAB> kind <TAB> status <TAB> detail
#
#   kind   ok | eq | fails | unsupported (what the case expects)
#   status PASS | FAIL
#
# Environment (set by run.sh): AWS_BIN, AWS_ENDPOINT_URL, AWS_ACCESS_KEY_ID,
# AWS_SECRET_ACCESS_KEY, AWS_DEFAULT_REGION, AWSCLI_OUT, AWSCLI_SCRATCH,
# OPENS3_ACCOUNT_ID. Needs bash, curl, python3, coreutils (the amazon/aws-cli
# image has all of them).
set -uo pipefail

AWS_BIN="${AWS_BIN:-aws}"
OUT="${AWSCLI_OUT:-$(pwd)/results}"
SCRATCH="${AWSCLI_SCRATCH:-$(mktemp -d)}"
ACCOUNT="${OPENS3_ACCOUNT_ID:-000000000000}"
export AWS_PAGER=""
mkdir -p "$OUT/cases" "$SCRATCH"
RESULTS_TSV="$OUT/results.tsv"
: >"$RESULTS_TSV"
cd "$SCRATCH"

aws() { command "$AWS_BIN" "$@"; }

# --- 0. CLI metadata for the report ----------------------------------------------
aws --version >"$OUT/version.txt" 2>&1
for svc in s3 s3api iam sts; do
  aws "$svc" help >"$OUT/help-$svc.txt" 2>&1 </dev/null || true
done

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
future() { python3 -c 'import datetime,sys;print((datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=int(sys.argv[1]))).strftime("%Y-%m-%dT%H:%M:%SZ"))' "${1:-1}"; }

# purge_versions BUCKET [extra delete-objects args]: delete every version and
# delete marker so a versioned / object-lock bucket can be deleted.
purge_versions() {
  local b="$1"; shift
  aws s3api list-object-versions --bucket "$b" --output json \
    --query '{Objects: [Versions[].{Key:Key,VersionId:VersionId}, DeleteMarkers[].{Key:Key,VersionId:VersionId}][]}' >"$SCRATCH/purge-$b.json" 2>/dev/null
  if grep -q '"Key"' "$SCRATCH/purge-$b.json"; then
    aws s3api delete-objects --bucket "$b" --delete "file://$SCRATCH/purge-$b.json" "$@" >/dev/null 2>&1
  fi
}

SUFFIX="$(date +%s)-$RANDOM"
B="awscli-main-$SUFFIX"     # plain bucket for object commands
CB="awscli-conf-$SUFFIX"    # bucket for bucket-configuration commands
VB="awscli-vers-$SUFFIX"    # versioned bucket
LB="awscli-lock-$SUFFIX"    # object-lock bucket
HB="awscli-s3-$SUFFIX"      # for the high-level `aws s3` commands
ROOT_AK="$AWS_ACCESS_KEY_ID" ROOT_SK="$AWS_SECRET_ACCESS_KEY"

printf 'hello world\n' >hello.txt
printf 'second version\n' >second.txt
dd if=/dev/urandom of=big.bin bs=1M count=10 status=none      # > 8 MiB: multipart via `aws s3 cp`
dd if=/dev/urandom of=part.bin bs=1M count=5 status=none      # >= 5 MiB: a non-final multipart part
mkdir -p tree/sub && printf 'a\n' >tree/a.txt && printf 'b\n' >tree/b.txt && printf 'c\n' >tree/sub/c.txt

echo "== s3api: buckets"
ok create-bucket -- aws s3api create-bucket --bucket "$B"
ok create-bucket-conf -- aws s3api create-bucket --bucket "$CB" --acl private
ok create-bucket-versioned -- aws s3api create-bucket --bucket "$VB"
ok create-bucket-lock -- aws s3api create-bucket --bucket "$LB" --object-lock-enabled-for-bucket
# us-east-1 (the legacy region) answers a re-create by the same owner with 200, as AWS does.
ok create-bucket-exists-us-east-1 -- aws s3api create-bucket --bucket "$B"
fails create-bucket-badname InvalidBucketName -- aws s3api create-bucket --bucket "Bad_Name"
ok head-bucket -- aws s3api head-bucket --bucket "$B"
fails head-bucket-missing 404 -- aws s3api head-bucket --bucket "awscli-missing-$SUFFIX"
ok wait-bucket-exists -- aws s3api wait bucket-exists --bucket "$B"
ok list-buckets -- aws s3api list-buckets
eq list-buckets-contains "$B" -- aws s3api list-buckets --query "Buckets[?Name=='$B'].Name" --output text
eq list-buckets-prefix "$B" -- aws s3api list-buckets --prefix "awscli-main-" --query "Buckets[].Name" --output text
eq get-bucket-location None -- aws s3api get-bucket-location --bucket "$B" --query LocationConstraint --output text
fails get-bucket-location-missing NoSuchBucket -- aws s3api get-bucket-location --bucket "awscli-missing-$SUFFIX"

echo "== s3api: versioning"
eq get-bucket-versioning-unset None -- aws s3api get-bucket-versioning --bucket "$VB" --query Status --output text
ok put-bucket-versioning -- aws s3api put-bucket-versioning --bucket "$VB" --versioning-configuration Status=Enabled
eq get-bucket-versioning Enabled -- aws s3api get-bucket-versioning --bucket "$VB" --query Status --output text
eq get-bucket-versioning-lock Enabled -- aws s3api get-bucket-versioning --bucket "$LB" --query Status --output text

echo "== s3api: bucket tagging"
ok put-bucket-tagging -- aws s3api put-bucket-tagging --bucket "$CB" --tagging 'TagSet=[{Key=env,Value=test},{Key=team,Value=storage}]'
eq get-bucket-tagging test -- aws s3api get-bucket-tagging --bucket "$CB" --query "TagSet[?Key=='env'].Value" --output text
ok delete-bucket-tagging -- aws s3api delete-bucket-tagging --bucket "$CB"
fails get-bucket-tagging-none NoSuchTagSet -- aws s3api get-bucket-tagging --bucket "$CB"

echo "== s3api: bucket policy"
cat >policy-public.json <<EOF
{"Version":"2012-10-17","Statement":[{"Sid":"PublicRead","Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::$CB/*"}]}
EOF
fails get-bucket-policy-none NoSuchBucketPolicy -- aws s3api get-bucket-policy --bucket "$CB"
eq get-bucket-policy-status-none False -- aws s3api get-bucket-policy-status --bucket "$CB" --query PolicyStatus.IsPublic --output text
ok put-bucket-policy -- aws s3api put-bucket-policy --bucket "$CB" --policy file://policy-public.json
ok get-bucket-policy -- aws s3api get-bucket-policy --bucket "$CB"
eq get-bucket-policy-status True -- aws s3api get-bucket-policy-status --bucket "$CB" --query PolicyStatus.IsPublic --output text
fails put-bucket-policy-malformed MalformedPolicy -- aws s3api put-bucket-policy --bucket "$CB" --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Maybe"}]}'
ok delete-bucket-policy -- aws s3api delete-bucket-policy --bucket "$CB"

echo "== s3api: cors"
ok put-bucket-cors -- aws s3api put-bucket-cors --bucket "$CB" --cors-configuration '{"CORSRules":[{"AllowedOrigins":["https://example.com"],"AllowedMethods":["GET","PUT"],"AllowedHeaders":["*"],"MaxAgeSeconds":3000}]}'
eq get-bucket-cors https://example.com -- aws s3api get-bucket-cors --bucket "$CB" --query 'CORSRules[0].AllowedOrigins[0]' --output text
ok delete-bucket-cors -- aws s3api delete-bucket-cors --bucket "$CB"
fails get-bucket-cors-none NoSuchCORSConfiguration -- aws s3api get-bucket-cors --bucket "$CB"

echo "== s3api: lifecycle"
ok put-bucket-lifecycle-configuration -- aws s3api put-bucket-lifecycle-configuration --bucket "$CB" --lifecycle-configuration '{"Rules":[{"ID":"expire-tmp","Status":"Enabled","Filter":{"Prefix":"tmp/"},"Expiration":{"Days":7}},{"ID":"abort-mpu","Status":"Enabled","Filter":{},"AbortIncompleteMultipartUpload":{"DaysAfterInitiation":2}}]}'
eq get-bucket-lifecycle-configuration expire-tmp -- aws s3api get-bucket-lifecycle-configuration --bucket "$CB" --query 'Rules[0].ID' --output text
ok delete-bucket-lifecycle -- aws s3api delete-bucket-lifecycle --bucket "$CB"
fails get-bucket-lifecycle-none NoSuchLifecycleConfiguration -- aws s3api get-bucket-lifecycle-configuration --bucket "$CB"

echo "== s3api: encryption"
ok put-bucket-encryption -- aws s3api put-bucket-encryption --bucket "$CB" --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":false}]}'
eq get-bucket-encryption AES256 -- aws s3api get-bucket-encryption --bucket "$CB" --query 'ServerSideEncryptionConfiguration.Rules[0].ApplyServerSideEncryptionByDefault.SSEAlgorithm' --output text
ok delete-bucket-encryption -- aws s3api delete-bucket-encryption --bucket "$CB"
fails get-bucket-encryption-none ServerSideEncryptionConfigurationNotFoundError -- aws s3api get-bucket-encryption --bucket "$CB"

echo "== s3api: object lock configuration"
ok put-object-lock-configuration -- aws s3api put-object-lock-configuration --bucket "$LB" --object-lock-configuration '{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"GOVERNANCE","Days":1}}}'
eq get-object-lock-configuration GOVERNANCE -- aws s3api get-object-lock-configuration --bucket "$LB" --query 'ObjectLockConfiguration.Rule.DefaultRetention.Mode' --output text
fails get-object-lock-configuration-none ObjectLockConfigurationNotFoundError -- aws s3api get-object-lock-configuration --bucket "$CB"

echo "== s3api: bucket acl"
ok put-bucket-acl-canned -- aws s3api put-bucket-acl --bucket "$CB" --acl public-read
eq get-bucket-acl-allusers http://acs.amazonaws.com/groups/global/AllUsers -- aws s3api get-bucket-acl --bucket "$CB" --query "Grants[?Permission=='READ'].Grantee.URI | [0]" --output text
ok put-bucket-acl-grant -- aws s3api put-bucket-acl --bucket "$CB" --grant-read 'uri=http://acs.amazonaws.com/groups/global/AuthenticatedUsers' --grant-full-control "id=$(capture owner-id -- aws s3api get-bucket-acl --bucket "$CB" --query Owner.ID --output text)"
ok put-bucket-acl-private -- aws s3api put-bucket-acl --bucket "$CB" --acl private
eq get-bucket-acl-private 1 -- aws s3api get-bucket-acl --bucket "$CB" --query 'length(Grants)' --output text

echo "== s3api: notification"
ok get-bucket-notification-configuration-empty -- aws s3api get-bucket-notification-configuration --bucket "$CB"
ok put-bucket-notification-configuration -- aws s3api put-bucket-notification-configuration --bucket "$CB" --notification-configuration '{}'
ok get-bucket-notification-configuration -- aws s3api get-bucket-notification-configuration --bucket "$CB"

echo "== s3api: website"
ok put-bucket-website -- aws s3api put-bucket-website --bucket "$CB" --website-configuration '{"IndexDocument":{"Suffix":"index.html"},"ErrorDocument":{"Key":"error.html"}}'
eq get-bucket-website index.html -- aws s3api get-bucket-website --bucket "$CB" --query IndexDocument.Suffix --output text
ok delete-bucket-website -- aws s3api delete-bucket-website --bucket "$CB"
fails get-bucket-website-none NoSuchWebsiteConfiguration -- aws s3api get-bucket-website --bucket "$CB"

echo "== s3api: logging"
ok get-bucket-logging-empty -- aws s3api get-bucket-logging --bucket "$CB"
ok put-bucket-logging -- aws s3api put-bucket-logging --bucket "$CB" --bucket-logging-status "{\"LoggingEnabled\":{\"TargetBucket\":\"$B\",\"TargetPrefix\":\"logs/\"}}"
eq get-bucket-logging logs/ -- aws s3api get-bucket-logging --bucket "$CB" --query LoggingEnabled.TargetPrefix --output text
ok put-bucket-logging-disable -- aws s3api put-bucket-logging --bucket "$CB" --bucket-logging-status '{}'

echo "== s3api: replication (versioned bucket)"
fails put-bucket-replication-unversioned InvalidRequest -- aws s3api put-bucket-replication --bucket "$CB" --replication-configuration "{\"Role\":\"arn:aws:iam::$ACCOUNT:role/replication\",\"Rules\":[{\"ID\":\"r1\",\"Status\":\"Enabled\",\"Priority\":1,\"Filter\":{},\"DeleteMarkerReplication\":{\"Status\":\"Disabled\"},\"Destination\":{\"Bucket\":\"arn:aws:s3:::$B\"}}]}"
ok put-bucket-replication -- aws s3api put-bucket-replication --bucket "$VB" --replication-configuration "{\"Role\":\"arn:aws:iam::$ACCOUNT:role/replication\",\"Rules\":[{\"ID\":\"r1\",\"Status\":\"Enabled\",\"Priority\":1,\"Filter\":{},\"DeleteMarkerReplication\":{\"Status\":\"Disabled\"},\"Destination\":{\"Bucket\":\"arn:aws:s3:::$B\"}}]}"
eq get-bucket-replication r1 -- aws s3api get-bucket-replication --bucket "$VB" --query 'ReplicationConfiguration.Rules[0].ID' --output text
ok delete-bucket-replication -- aws s3api delete-bucket-replication --bucket "$VB"
fails get-bucket-replication-none ReplicationConfigurationNotFoundError -- aws s3api get-bucket-replication --bucket "$VB"

echo "== s3api: accelerate / request payment"
ok put-bucket-accelerate-configuration -- aws s3api put-bucket-accelerate-configuration --bucket "$CB" --accelerate-configuration Status=Enabled
eq get-bucket-accelerate-configuration Enabled -- aws s3api get-bucket-accelerate-configuration --bucket "$CB" --query Status --output text
ok put-bucket-request-payment -- aws s3api put-bucket-request-payment --bucket "$CB" --request-payment-configuration Payer=Requester
eq get-bucket-request-payment Requester -- aws s3api get-bucket-request-payment --bucket "$CB" --query Payer --output text
ok put-bucket-request-payment-owner -- aws s3api put-bucket-request-payment --bucket "$CB" --request-payment-configuration Payer=BucketOwner

echo "== s3api: metrics / analytics / inventory / intelligent-tiering"
ok put-bucket-metrics-configuration -- aws s3api put-bucket-metrics-configuration --bucket "$CB" --id m1 --metrics-configuration '{"Id":"m1","Filter":{"Prefix":"docs/"}}'
eq get-bucket-metrics-configuration m1 -- aws s3api get-bucket-metrics-configuration --bucket "$CB" --id m1 --query MetricsConfiguration.Id --output text
eq list-bucket-metrics-configurations m1 -- aws s3api list-bucket-metrics-configurations --bucket "$CB" --query 'MetricsConfigurationList[].Id' --output text
ok delete-bucket-metrics-configuration -- aws s3api delete-bucket-metrics-configuration --bucket "$CB" --id m1
fails get-bucket-metrics-configuration-none NoSuchConfiguration -- aws s3api get-bucket-metrics-configuration --bucket "$CB" --id m1

ok put-bucket-analytics-configuration -- aws s3api put-bucket-analytics-configuration --bucket "$CB" --id a1 --analytics-configuration "{\"Id\":\"a1\",\"StorageClassAnalysis\":{\"DataExport\":{\"OutputSchemaVersion\":\"V_1\",\"Destination\":{\"S3BucketDestination\":{\"Format\":\"CSV\",\"Bucket\":\"arn:aws:s3:::$B\",\"Prefix\":\"analytics/\"}}}}}"
eq get-bucket-analytics-configuration a1 -- aws s3api get-bucket-analytics-configuration --bucket "$CB" --id a1 --query AnalyticsConfiguration.Id --output text
eq list-bucket-analytics-configurations a1 -- aws s3api list-bucket-analytics-configurations --bucket "$CB" --query 'AnalyticsConfigurationList[].Id' --output text
ok delete-bucket-analytics-configuration -- aws s3api delete-bucket-analytics-configuration --bucket "$CB" --id a1

ok put-bucket-inventory-configuration -- aws s3api put-bucket-inventory-configuration --bucket "$CB" --id i1 --inventory-configuration "{\"Id\":\"i1\",\"IsEnabled\":true,\"IncludedObjectVersions\":\"All\",\"Schedule\":{\"Frequency\":\"Daily\"},\"Destination\":{\"S3BucketDestination\":{\"Bucket\":\"arn:aws:s3:::$B\",\"Format\":\"CSV\"}}}"
eq get-bucket-inventory-configuration i1 -- aws s3api get-bucket-inventory-configuration --bucket "$CB" --id i1 --query InventoryConfiguration.Id --output text
eq list-bucket-inventory-configurations i1 -- aws s3api list-bucket-inventory-configurations --bucket "$CB" --query 'InventoryConfigurationList[].Id' --output text
ok delete-bucket-inventory-configuration -- aws s3api delete-bucket-inventory-configuration --bucket "$CB" --id i1

ok put-bucket-intelligent-tiering-configuration -- aws s3api put-bucket-intelligent-tiering-configuration --bucket "$CB" --id t1 --intelligent-tiering-configuration '{"Id":"t1","Status":"Enabled","Tierings":[{"Days":90,"AccessTier":"ARCHIVE_ACCESS"}]}'
eq get-bucket-intelligent-tiering-configuration t1 -- aws s3api get-bucket-intelligent-tiering-configuration --bucket "$CB" --id t1 --query IntelligentTieringConfiguration.Id --output text
eq list-bucket-intelligent-tiering-configurations t1 -- aws s3api list-bucket-intelligent-tiering-configurations --bucket "$CB" --query 'IntelligentTieringConfigurationList[].Id' --output text
ok delete-bucket-intelligent-tiering-configuration -- aws s3api delete-bucket-intelligent-tiering-configuration --bucket "$CB" --id t1

echo "== s3api: public access block / ownership controls"
ok put-public-access-block -- aws s3api put-public-access-block --bucket "$CB" --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
eq get-public-access-block True -- aws s3api get-public-access-block --bucket "$CB" --query PublicAccessBlockConfiguration.BlockPublicPolicy --output text
fails put-bucket-policy-blocked AccessDenied -- aws s3api put-bucket-policy --bucket "$CB" --policy file://policy-public.json
ok delete-public-access-block -- aws s3api delete-public-access-block --bucket "$CB"
fails get-public-access-block-none NoSuchPublicAccessBlockConfiguration -- aws s3api get-public-access-block --bucket "$CB"
ok put-bucket-ownership-controls -- aws s3api put-bucket-ownership-controls --bucket "$CB" --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerPreferred}]'
eq get-bucket-ownership-controls BucketOwnerPreferred -- aws s3api get-bucket-ownership-controls --bucket "$CB" --query 'OwnershipControls.Rules[0].ObjectOwnership' --output text
ok delete-bucket-ownership-controls -- aws s3api delete-bucket-ownership-controls --bucket "$CB"
fails get-bucket-ownership-controls-none OwnershipControlsNotFoundError -- aws s3api get-bucket-ownership-controls --bucket "$CB"

echo "== s3api: objects"
ok put-object -- aws s3api put-object --bucket "$B" --key hello.txt --body hello.txt --content-type text/plain --metadata owner=alice,purpose=test --tagging "env=test&tier=gold" --cache-control max-age=60
ok put-object-checksum-sha256 -- aws s3api put-object --bucket "$B" --key sha.txt --body hello.txt --checksum-algorithm SHA256
ok put-object-checksum-crc32c -- aws s3api put-object --bucket "$B" --key crc.txt --body hello.txt --checksum-algorithm CRC32C
ok put-object-checksum-crc64 -- aws s3api put-object --bucket "$B" --key crc64.txt --body hello.txt --checksum-algorithm CRC64NVME
ok put-object-sse -- aws s3api put-object --bucket "$B" --key sse.txt --body hello.txt --server-side-encryption AES256
ok put-object-storage-class -- aws s3api put-object --bucket "$B" --key cold.txt --body hello.txt --storage-class GLACIER
ok put-object-big -- aws s3api put-object --bucket "$B" --key big.bin --body big.bin
fails put-object-missing-bucket NoSuchBucket -- aws s3api put-object --bucket "awscli-missing-$SUFFIX" --key x --body hello.txt
ok put-object-if-none-match -- aws s3api put-object --bucket "$B" --key once.txt --body hello.txt --if-none-match '*'
fails put-object-if-none-match-exists PreconditionFailed -- aws s3api put-object --bucket "$B" --key once.txt --body hello.txt --if-none-match '*'
for k in docs/a.txt docs/b.txt docs/sub/c.txt img/1.png; do aws s3api put-object --bucket "$B" --key "$k" --body hello.txt >/dev/null; done

ok get-object -- aws s3api get-object --bucket "$B" --key hello.txt get-hello.txt
CASE_CMD="s3api get-object" ok get-object-content-matches -- same_file hello.txt get-hello.txt
ok get-object-range -- aws s3api get-object --bucket "$B" --key hello.txt --range bytes=0-4 get-range.txt
CASE_CMD="s3api get-object" eq get-object-range-content hello -- cat get-range.txt
ok get-object-big -- aws s3api get-object --bucket "$B" --key big.bin get-big.bin
CASE_CMD="s3api get-object" ok get-object-big-content -- same_file big.bin get-big.bin
ok get-object-checksum-mode -- aws s3api get-object --bucket "$B" --key sha.txt --checksum-mode ENABLED get-sha.txt
eq get-object-response-override attachment -- aws s3api get-object --bucket "$B" --key hello.txt --response-content-disposition attachment get-hello2.txt --query ContentDisposition --output text
fails get-object-missing NoSuchKey -- aws s3api get-object --bucket "$B" --key nope.txt get-nope.txt
fails get-object-if-match PreconditionFailed -- aws s3api get-object --bucket "$B" --key hello.txt --if-match '"0000"' get-nope.txt
ok head-object -- aws s3api head-object --bucket "$B" --key hello.txt
eq head-object-metadata alice -- aws s3api head-object --bucket "$B" --key hello.txt --query Metadata.owner --output text
eq head-object-content-type text/plain -- aws s3api head-object --bucket "$B" --key hello.txt --query ContentType --output text
eq head-object-sse AES256 -- aws s3api head-object --bucket "$B" --key sse.txt --query ServerSideEncryption --output text
eq head-object-storage-class GLACIER -- aws s3api head-object --bucket "$B" --key cold.txt --query StorageClass --output text
fails head-object-missing 404 -- aws s3api head-object --bucket "$B" --key nope.txt
ok wait-object-exists -- aws s3api wait object-exists --bucket "$B" --key hello.txt

ok copy-object -- aws s3api copy-object --bucket "$B" --key copy.txt --copy-source "$B/hello.txt"
eq copy-object-metadata-copied alice -- aws s3api head-object --bucket "$B" --key copy.txt --query Metadata.owner --output text
ok copy-object-replace -- aws s3api copy-object --bucket "$B" --key copy2.txt --copy-source "$B/hello.txt" --metadata-directive REPLACE --metadata owner=bob --content-type text/html
eq copy-object-metadata-replaced bob -- aws s3api head-object --bucket "$B" --key copy2.txt --query Metadata.owner --output text
ok copy-object-tagging-replace -- aws s3api copy-object --bucket "$B" --key copy3.txt --copy-source "$B/hello.txt" --tagging-directive REPLACE --tagging "copied=yes"
fails copy-object-missing NoSuchKey -- aws s3api copy-object --bucket "$B" --key copy4.txt --copy-source "$B/nope.txt"

echo "== s3api: object tagging / acl / attributes"
eq get-object-tagging gold -- aws s3api get-object-tagging --bucket "$B" --key hello.txt --query "TagSet[?Key=='tier'].Value" --output text
ok put-object-tagging -- aws s3api put-object-tagging --bucket "$B" --key hello.txt --tagging 'TagSet=[{Key=colour,Value=blue}]'
eq get-object-tagging-replaced blue -- aws s3api get-object-tagging --bucket "$B" --key hello.txt --query "TagSet[0].Value" --output text
ok delete-object-tagging -- aws s3api delete-object-tagging --bucket "$B" --key hello.txt
eq get-object-tagging-empty 0 -- aws s3api get-object-tagging --bucket "$B" --key hello.txt --query 'length(TagSet)' --output text
ok get-object-acl -- aws s3api get-object-acl --bucket "$B" --key hello.txt
ok put-object-acl -- aws s3api put-object-acl --bucket "$B" --key hello.txt --acl public-read
eq get-object-acl-public http://acs.amazonaws.com/groups/global/AllUsers -- aws s3api get-object-acl --bucket "$B" --key hello.txt --query "Grants[?Permission=='READ'].Grantee.URI | [0]" --output text
ok get-object-attributes -- aws s3api get-object-attributes --bucket "$B" --key sha.txt --object-attributes ETag Checksum ObjectSize StorageClass ObjectParts
eq get-object-attributes-size 12 -- aws s3api get-object-attributes --bucket "$B" --key sha.txt --object-attributes ObjectSize --query ObjectSize --output text

echo "== s3api: restore / unsupported object operations"
ok restore-object -- aws s3api restore-object --bucket "$B" --key cold.txt --restore-request Days=1
fails restore-object-standard InvalidObjectState -- aws s3api restore-object --bucket "$B" --key hello.txt --restore-request Days=1
unsupported select-object-content NotImplemented -- aws s3api select-object-content --bucket "$B" --key hello.txt --expression "select * from s3object" --expression-type SQL --input-serialization '{"CSV":{}}' --output-serialization '{"CSV":{}}' select.out
unsupported get-object-torrent NotImplemented -- aws s3api get-object-torrent --bucket "$B" --key hello.txt torrent.out
unsupported rename-object NotImplemented -- aws s3api rename-object --bucket "$B" --key renamed.txt --rename-source "$B/hello.txt"
unsupported put-object-annotation NotImplemented -- aws s3api put-object-annotation --bucket "$B" --key hello.txt --annotation-name note --annotation-payload hello.txt
unsupported get-object-annotation NotImplemented -- aws s3api get-object-annotation --bucket "$B" --key hello.txt --annotation-name note annotation.out
unsupported list-object-annotations NotImplemented -- aws s3api list-object-annotations --bucket "$B" --key hello.txt
unsupported delete-object-annotation NotImplemented -- aws s3api delete-object-annotation --bucket "$B" --key hello.txt --annotation-name note
unsupported update-object-encryption NotImplemented -- aws s3api update-object-encryption --bucket "$B" --key hello.txt --object-encryption '{"SSEKMS":{"KMSKeyArn":"arn:aws:kms:us-east-1:000000000000:key/none","BucketKeyEnabled":false}}'
# write-get-object-response is not exercisable: the CLI targets the S3 Object Lambda host "<route>.<endpoint>".
fails rename-object-no-side-effect 404 -- aws s3api head-object --bucket "$B" --key renamed.txt
CASE_CMD="s3api update-object-encryption" eq update-object-encryption-no-side-effect '"6f5902ac237024bdd0c176cb93063dc4"' -- aws s3api head-object --bucket "$B" --key hello.txt --query ETag --output text
CASE_CMD="s3api delete-object-annotation" ok delete-object-annotation-no-side-effect -- aws s3api head-object --bucket "$B" --key hello.txt

echo "== s3api: unsupported bucket operations"
unsupported create-session NotImplemented -- aws s3api create-session --bucket "$B"
# ListDirectoryBuckets is signed for the "s3express" service; the signature check rejects it.
unsupported list-directory-buckets AuthorizationHeaderMalformed -- aws s3api list-directory-buckets
unsupported get-bucket-abac NotImplemented -- aws s3api get-bucket-abac --bucket "$B"
unsupported put-bucket-abac NotImplemented -- aws s3api put-bucket-abac --bucket "$B" --abac-status Status=Enabled
unsupported create-bucket-metadata-configuration NotImplemented -- aws s3api create-bucket-metadata-configuration --bucket "$B" --metadata-configuration '{"JournalTableConfiguration":{"RecordExpiration":{"Expiration":"DISABLED"}}}'
unsupported get-bucket-metadata-configuration NotImplemented -- aws s3api get-bucket-metadata-configuration --bucket "$B"
unsupported delete-bucket-metadata-configuration NotImplemented -- aws s3api delete-bucket-metadata-configuration --bucket "$B"
unsupported create-bucket-metadata-table-configuration NotImplemented -- aws s3api create-bucket-metadata-table-configuration --bucket "$B" --metadata-table-configuration '{"S3TablesDestination":{"TableBucketArn":"arn:aws:s3tables:us-east-1:000000000000:bucket/t","TableName":"t"}}'
unsupported get-bucket-metadata-table-configuration NotImplemented -- aws s3api get-bucket-metadata-table-configuration --bucket "$B"
unsupported delete-bucket-metadata-table-configuration NotImplemented -- aws s3api delete-bucket-metadata-table-configuration --bucket "$B"
unsupported update-bucket-metadata-inventory-table-configuration NotImplemented -- aws s3api update-bucket-metadata-inventory-table-configuration --bucket "$B" --inventory-table-configuration '{"ConfigurationState":"DISABLED"}'
unsupported update-bucket-metadata-journal-table-configuration NotImplemented -- aws s3api update-bucket-metadata-journal-table-configuration --bucket "$B" --journal-table-configuration '{"RecordExpiration":{"Expiration":"DISABLED"}}'
unsupported update-bucket-metadata-annotation-table-configuration NotImplemented -- aws s3api update-bucket-metadata-annotation-table-configuration --bucket "$B" --annotation-table-configuration '{"ConfigurationState":"DISABLED"}'

echo "== s3api: listing"
ok list-objects -- aws s3api list-objects --bucket "$B"
eq list-objects-prefix 3 -- aws s3api list-objects --bucket "$B" --prefix docs/ --query 'length(Contents)' --output text
eq list-objects-delimiter docs/sub/ -- aws s3api list-objects --bucket "$B" --prefix docs/ --delimiter / --query 'CommonPrefixes[0].Prefix' --output text
ok list-objects-v2 -- aws s3api list-objects-v2 --bucket "$B"
eq list-objects-v2-delimiter docs/sub/ -- aws s3api list-objects-v2 --bucket "$B" --prefix docs/ --delimiter / --query 'CommonPrefixes[0].Prefix' --output text
eq list-objects-v2-max-keys True -- aws s3api list-objects-v2 --bucket "$B" --max-keys 2 --query IsTruncated --output text
TOKEN="$(capture v2-token -- aws s3api list-objects-v2 --bucket "$B" --max-keys 2 --query NextContinuationToken --output text)"
ok list-objects-v2-continuation -- aws s3api list-objects-v2 --bucket "$B" --max-keys 2 --continuation-token "$TOKEN"
eq list-objects-v2-start-after big.bin -- aws s3api list-objects-v2 --bucket "$B" --start-after a --max-keys 1 --query 'Contents[0].Key' --output text
ok list-objects-v2-paginate -- aws s3api list-objects-v2 --bucket "$B" --page-size 2 --max-items 3
CASE_CMD="s3api list-objects-v2" eq list-objects-v2-paginate-count 3 -- python3 -c "import json,sys;print(len(json.load(open(sys.argv[1]))['Contents']))" "$OUT/cases/list-objects-v2-paginate.out"
fails list-objects-v2-missing NoSuchBucket -- aws s3api list-objects-v2 --bucket "awscli-missing-$SUFFIX"

echo "== s3api: versioned bucket"
V1="$(capture v1 -- aws s3api put-object --bucket "$VB" --key doc.txt --body hello.txt --query VersionId --output text)"
V2="$(capture v2 -- aws s3api put-object --bucket "$VB" --key doc.txt --body second.txt --query VersionId --output text)"
CASE_CMD="s3api put-object" ok put-object-versioned -- test -n "$V1" -a -n "$V2" -a "$V1" != "$V2"
eq list-object-versions 2 -- aws s3api list-object-versions --bucket "$VB" --prefix doc.txt --query 'length(Versions)' --output text
eq list-object-versions-latest "$V2" -- aws s3api list-object-versions --bucket "$VB" --prefix doc.txt --query 'Versions[?IsLatest].VersionId | [0]' --output text
ok get-object-version -- aws s3api get-object --bucket "$VB" --key doc.txt --version-id "$V1" get-v1.txt
CASE_CMD="s3api get-object" ok get-object-version-content -- same_file hello.txt get-v1.txt
eq head-object-version "$V1" -- aws s3api head-object --bucket "$VB" --key doc.txt --version-id "$V1" --query VersionId --output text
ok copy-object-version -- aws s3api copy-object --bucket "$VB" --key doc-copy.txt --copy-source "$VB/doc.txt?versionId=$V1"
ok put-object-tagging-version -- aws s3api put-object-tagging --bucket "$VB" --key doc.txt --version-id "$V1" --tagging 'TagSet=[{Key=v,Value=one}]'
eq get-object-tagging-version one -- aws s3api get-object-tagging --bucket "$VB" --key doc.txt --version-id "$V1" --query 'TagSet[0].Value' --output text
eq delete-object-marker True -- aws s3api delete-object --bucket "$VB" --key doc.txt --query DeleteMarker --output text
eq list-object-versions-marker 1 -- aws s3api list-object-versions --bucket "$VB" --prefix doc.txt --query 'length(DeleteMarkers)' --output text
fails get-object-deleted NoSuchKey -- aws s3api get-object --bucket "$VB" --key doc.txt get-deleted.txt
ok delete-object-version -- aws s3api delete-object --bucket "$VB" --key doc.txt --version-id "$V1"
eq list-object-versions-after 1 -- aws s3api list-object-versions --bucket "$VB" --prefix doc.txt --query 'length(Versions)' --output text
ok put-bucket-versioning-suspend -- aws s3api put-bucket-versioning --bucket "$VB" --versioning-configuration Status=Suspended
eq get-bucket-versioning-suspended Suspended -- aws s3api get-bucket-versioning --bucket "$VB" --query Status --output text

echo "== s3api: object lock (retention / legal hold)"
LV="$(capture lock-put -- aws s3api put-object --bucket "$LB" --key locked.txt --body hello.txt --query VersionId --output text)"
eq get-object-retention-default GOVERNANCE -- aws s3api get-object-retention --bucket "$LB" --key locked.txt --query Retention.Mode --output text
ok put-object-retention -- aws s3api put-object-retention --bucket "$LB" --key locked.txt --retention "Mode=GOVERNANCE,RetainUntilDate=$(future 2)"
eq get-object-retention GOVERNANCE -- aws s3api get-object-retention --bucket "$LB" --key locked.txt --query Retention.Mode --output text
ok put-object-legal-hold -- aws s3api put-object-legal-hold --bucket "$LB" --key locked.txt --legal-hold Status=ON
eq get-object-legal-hold ON -- aws s3api get-object-legal-hold --bucket "$LB" --key locked.txt --query LegalHold.Status --output text
fails delete-object-locked AccessDenied -- aws s3api delete-object --bucket "$LB" --key locked.txt --version-id "$LV"
ok put-object-legal-hold-off -- aws s3api put-object-legal-hold --bucket "$LB" --key locked.txt --legal-hold Status=OFF
fails delete-object-retained AccessDenied -- aws s3api delete-object --bucket "$LB" --key locked.txt --version-id "$LV"
ok delete-object-bypass -- aws s3api delete-object --bucket "$LB" --key locked.txt --version-id "$LV" --bypass-governance-retention
ok put-object-with-lock -- aws s3api put-object --bucket "$LB" --key locked2.txt --body hello.txt --object-lock-mode GOVERNANCE --object-lock-retain-until-date "$(future)" --object-lock-legal-hold-status OFF
fails get-object-retention-none InvalidRequest -- aws s3api get-object-retention --bucket "$B" --key hello.txt
fails get-object-legal-hold-none InvalidRequest -- aws s3api get-object-legal-hold --bucket "$B" --key hello.txt

echo "== s3api: multipart"
UP="$(capture mpu -- aws s3api create-multipart-upload --bucket "$B" --key mpu.bin --content-type application/octet-stream --metadata source=mpu --query UploadId --output text)"
CASE_CMD="s3api create-multipart-upload" ok create-multipart-upload -- test -n "$UP"
E1="$(capture part1 -- aws s3api upload-part --bucket "$B" --key mpu.bin --upload-id "$UP" --part-number 1 --body part.bin --query ETag --output text)"
CASE_CMD="s3api upload-part" ok upload-part -- test -n "$E1"
E2="$(capture part2 -- aws s3api upload-part-copy --bucket "$B" --key mpu.bin --upload-id "$UP" --part-number 2 --copy-source "$B/big.bin" --copy-source-range bytes=0-5242879 --query CopyPartResult.ETag --output text)"
CASE_CMD="s3api upload-part-copy" ok upload-part-copy -- test -n "$E2"
E3="$(capture part3 -- aws s3api upload-part --bucket "$B" --key mpu.bin --upload-id "$UP" --part-number 3 --body hello.txt --query ETag --output text)"
eq list-parts 3 -- aws s3api list-parts --bucket "$B" --key mpu.bin --upload-id "$UP" --query 'length(Parts)' --output text
eq list-multipart-uploads mpu.bin -- aws s3api list-multipart-uploads --bucket "$B" --query 'Uploads[].Key' --output text
ok complete-multipart-upload -- aws s3api complete-multipart-upload --bucket "$B" --key mpu.bin --upload-id "$UP" --multipart-upload "{\"Parts\":[{\"ETag\":$E1,\"PartNumber\":1},{\"ETag\":$E2,\"PartNumber\":2},{\"ETag\":$E3,\"PartNumber\":3}]}"
eq complete-multipart-upload-size $((5 * 1048576 * 2 + 12)) -- aws s3api head-object --bucket "$B" --key mpu.bin --query ContentLength --output text
eq complete-multipart-upload-parts 3 -- aws s3api get-object-attributes --bucket "$B" --key mpu.bin --object-attributes ObjectParts --query ObjectParts.TotalPartsCount --output text
ok get-object-part-number -- aws s3api get-object --bucket "$B" --key mpu.bin --part-number 3 get-part3.txt
CASE_CMD="s3api get-object" ok get-object-part-number-content -- same_file hello.txt get-part3.txt
UP2="$(capture mpu2 -- aws s3api create-multipart-upload --bucket "$B" --key abort.bin --query UploadId --output text)"
aws s3api upload-part --bucket "$B" --key abort.bin --upload-id "$UP2" --part-number 1 --body hello.txt >/dev/null 2>&1
ok abort-multipart-upload -- aws s3api abort-multipart-upload --bucket "$B" --key abort.bin --upload-id "$UP2"
fails list-parts-aborted NoSuchUpload -- aws s3api list-parts --bucket "$B" --key abort.bin --upload-id "$UP2"
eq list-multipart-uploads-empty 0 -- aws s3api list-multipart-uploads --bucket "$B" --query 'length(Uploads || `[]`)' --output text

echo "== s3api: delete"
ok delete-object -- aws s3api delete-object --bucket "$B" --key copy3.txt
ok delete-object-missing -- aws s3api delete-object --bucket "$B" --key nope.txt
ok delete-objects -- aws s3api delete-objects --bucket "$B" --delete 'Objects=[{Key=copy.txt},{Key=copy2.txt},{Key=nope2.txt}],Quiet=false'
eq delete-objects-count 3 -- aws s3api delete-objects --bucket "$B" --delete 'Objects=[{Key=docs/a.txt},{Key=docs/b.txt},{Key=docs/sub/c.txt}]' --query 'length(Deleted)' --output text
ok delete-objects-quiet -- aws s3api delete-objects --bucket "$B" --delete 'Objects=[{Key=img/1.png}],Quiet=true'
fails delete-bucket-not-empty BucketNotEmpty -- aws s3api delete-bucket --bucket "$B"

echo "== s3: high-level commands"
ok s3-mb -- aws s3 mb "s3://$HB"
ok s3-ls-buckets -- aws s3 ls
CASE_CMD="s3 ls" ok s3-ls-buckets-contains -- bash -c "aws s3 ls | grep -q ' $HB\$'"
ok s3-cp-up -- aws s3 cp hello.txt "s3://$HB/hello.txt"
ok s3-cp-up-metadata -- aws s3 cp hello.txt "s3://$HB/meta.txt" --metadata owner=alice --content-type text/plain --cache-control max-age=10
eq s3-cp-up-metadata-check alice -- aws s3api head-object --bucket "$HB" --key meta.txt --query Metadata.owner --output text
ok s3-cp-up-sse -- aws s3 cp hello.txt "s3://$HB/sse.txt" --sse AES256
eq s3-cp-up-sse-check AES256 -- aws s3api head-object --bucket "$HB" --key sse.txt --query ServerSideEncryption --output text
ok s3-cp-up-sse-kms -- aws s3 cp hello.txt "s3://$HB/kms.txt" --sse aws:kms
eq s3-cp-up-sse-kms-check aws:kms -- aws s3api head-object --bucket "$HB" --key kms.txt --query ServerSideEncryption --output text
ok s3-cp-up-storage-class -- aws s3 cp hello.txt "s3://$HB/ia.txt" --storage-class STANDARD_IA
ok s3-cp-up-acl -- aws s3 cp hello.txt "s3://$HB/public.txt" --acl public-read
ok s3-cp-up-big -- aws s3 cp big.bin "s3://$HB/big.bin"
eq s3-cp-up-big-multipart True -- bash -c "aws s3api head-object --bucket $HB --key big.bin --query 'contains(ETag, \`-\`)' --output text"
ok s3-cp-down -- aws s3 cp "s3://$HB/hello.txt" down-hello.txt
CASE_CMD="s3 cp" ok s3-cp-down-content -- same_file hello.txt down-hello.txt
ok s3-cp-down-big -- aws s3 cp "s3://$HB/big.bin" down-big.bin
CASE_CMD="s3 cp" ok s3-cp-down-big-content -- same_file big.bin down-big.bin
ok s3-cp-s3-to-s3 -- aws s3 cp "s3://$HB/hello.txt" "s3://$HB/hello-copy.txt"
ok s3-cp-recursive-up -- aws s3 cp tree "s3://$HB/tree/" --recursive
eq s3-cp-recursive-count 3 -- aws s3api list-objects-v2 --bucket "$HB" --prefix tree/ --query 'length(Contents)' --output text
ok s3-cp-recursive-down -- aws s3 cp "s3://$HB/tree/" tree-down --recursive
CASE_CMD="s3 cp" ok s3-cp-recursive-down-content -- same_file tree/sub/c.txt tree-down/sub/c.txt
ok s3-cp-stdin -- bash -c "printf 'from stdin\n' | aws s3 cp - s3://$HB/stdin.txt"
CASE_CMD="s3 cp" eq s3-cp-stdout "from stdin" -- aws s3 cp "s3://$HB/stdin.txt" -
ok s3-ls-bucket -- aws s3 ls "s3://$HB"
CASE_CMD="s3 ls" ok s3-ls-bucket-prefix-dir -- bash -c "aws s3 ls s3://$HB/ | grep -q 'PRE tree/'"
CASE_CMD="s3 ls" ok s3-ls-prefix -- bash -c "aws s3 ls s3://$HB/tree/ | grep -q 'a.txt'"
ok s3-ls-recursive -- aws s3 ls "s3://$HB/tree/" --recursive --human-readable --summarize
fails s3-ls-missing NoSuchBucket -- aws s3 ls "s3://awscli-missing-$SUFFIX"
ok s3-mv-up -- aws s3 mv second.txt "s3://$HB/moved.txt"
CASE_CMD="s3 mv" ok s3-mv-up-local-gone -- test ! -e second.txt
ok s3-mv-s3-to-s3 -- aws s3 mv "s3://$HB/moved.txt" "s3://$HB/moved2.txt"
fails s3-mv-s3-source-gone 404 -- aws s3api head-object --bucket "$HB" --key moved.txt
ok s3-mv-down -- aws s3 mv "s3://$HB/moved2.txt" moved-down.txt
mkdir -p syncdir && printf '1\n' >syncdir/one.txt && printf '2\n' >syncdir/two.txt
ok s3-sync-up -- aws s3 sync syncdir "s3://$HB/sync/"
eq s3-sync-up-count 2 -- aws s3api list-objects-v2 --bucket "$HB" --prefix sync/ --query 'length(Contents)' --output text
rm syncdir/two.txt && printf '3\n' >syncdir/three.txt
ok s3-sync-up-delete -- aws s3 sync syncdir "s3://$HB/sync/" --delete
CASE_CMD="s3 sync" eq s3-sync-up-delete-count "one.txt three.txt" -- bash -c "aws s3api list-objects-v2 --bucket $HB --prefix sync/ --query 'Contents[].Key' --output text | sed 's#sync/##g' | tr '\\t' ' '"
ok s3-sync-down -- aws s3 sync "s3://$HB/sync/" syncdown
CASE_CMD="s3 sync" ok s3-sync-down-content -- same_file syncdir/three.txt syncdown/three.txt
printf 'stale\n' >syncdown/stale.txt
ok s3-sync-down-delete -- aws s3 sync "s3://$HB/sync/" syncdown --delete
CASE_CMD="s3 sync" ok s3-sync-down-delete-check -- test ! -e syncdown/stale.txt
ok s3-sync-exclude -- aws s3 sync tree "s3://$HB/tree2/" --exclude "*" --include "*.txt" --exclude "sub/*"
eq s3-sync-exclude-count 2 -- aws s3api list-objects-v2 --bucket "$HB" --prefix tree2/ --query 'length(Contents)' --output text
URL="$(capture presign -- aws s3 presign "s3://$HB/hello.txt" --expires-in 120)"
CASE_CMD="s3 presign" ok s3-presign -- test -n "$URL"
CASE_CMD="s3 presign" ok s3-presign-curl -- curl -fsS -o presigned.txt "$URL"
CASE_CMD="s3 presign" ok s3-presign-content -- same_file hello.txt presigned.txt
CASE_CMD="s3 presign" ok s3-presign-tampered -- bash -c "! curl -fsS -o /dev/null '${URL}x'"
ok s3-website -- aws s3 website "s3://$HB" --index-document index.html --error-document error.html
eq s3-website-check index.html -- aws s3api get-bucket-website --bucket "$HB" --query IndexDocument.Suffix --output text
ok s3-rm -- aws s3 rm "s3://$HB/hello-copy.txt"
fails s3-rm-check 404 -- aws s3api head-object --bucket "$HB" --key hello-copy.txt
ok s3-rm-recursive -- aws s3 rm "s3://$HB/tree/" --recursive
eq s3-rm-recursive-check 0 -- aws s3api list-objects-v2 --bucket "$HB" --prefix tree/ --query 'length(Contents || `[]`)' --output text
fails s3-rb-not-empty BucketNotEmpty -- aws s3 rb "s3://$HB"
ok s3-rb-force -- aws s3 rb "s3://$HB" --force
fails s3-rb-gone NoSuchBucket -- aws s3 rb "s3://$HB"

echo "== iam: users and access keys"
USER="awscli-alice-$SUFFIX"
ok create-user -- aws iam create-user --user-name "$USER"
fails create-user-exists EntityAlreadyExists -- aws iam create-user --user-name "$USER"
eq get-user "arn:aws:iam::$ACCOUNT:user/$USER" -- aws iam get-user --user-name "$USER" --query User.Arn --output text
eq get-user-self "arn:aws:iam::$ACCOUNT:root" -- aws iam get-user --query User.Arn --output text
fails get-user-missing NoSuchEntity -- aws iam get-user --user-name "awscli-nobody-$SUFFIX"
eq list-users "$USER" -- aws iam list-users --query "Users[?UserName=='$USER'].UserName" --output text
ok update-user -- aws iam update-user --user-name "$USER" --new-path /
ok wait-user-exists -- aws iam wait user-exists --user-name "$USER"
KEY_JSON="$(capture key -- aws iam create-access-key --user-name "$USER" --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)"
AK="$(printf '%s' "$KEY_JSON" | cut -f1)"; SK="$(printf '%s' "$KEY_JSON" | cut -f2)"
CASE_CMD="iam create-access-key" ok create-access-key -- test "${#AK}" = 20 -a "${#SK}" = 40
eq list-access-keys "$AK" -- aws iam list-access-keys --user-name "$USER" --query 'AccessKeyMetadata[0].AccessKeyId' --output text
# The user has no policy yet: it can authenticate; ListBuckets is allowed for every
# authenticated identity but filtered to the buckets its policies grant, i.e. none.
eq user-s3-ls-empty "" -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws s3 ls
fails user-get-object-denied AccessDenied -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws s3api get-object --bucket "$B" --key sha.txt user-denied.txt
eq user-get-caller-identity "arn:aws:iam::$ACCOUNT:user/$USER" -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws sts get-caller-identity --query Arn --output text
eq user-get-own-user "$USER" -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws iam get-user --query User.UserName --output text
fails user-create-user-denied AccessDenied -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws iam create-user --user-name "awscli-mallory-$SUFFIX"
ok update-access-key-inactive -- aws iam update-access-key --user-name "$USER" --access-key-id "$AK" --status Inactive
eq list-access-keys-inactive Inactive -- aws iam list-access-keys --user-name "$USER" --query 'AccessKeyMetadata[0].Status' --output text
fails user-inactive-key InvalidAccessKeyId -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws sts get-caller-identity
ok update-access-key-active -- aws iam update-access-key --user-name "$USER" --access-key-id "$AK" --status Active
fails delete-user-conflict-keys DeleteConflict -- aws iam delete-user --user-name "$USER"
fails create-access-key-root InvalidInput -- aws iam create-access-key

echo "== iam: policies"
cat >readers.json <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets","s3:ListBucket","s3:GetObject"],"Resource":"*"}]}
EOF
cat >minio.json <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["admin:*"],"Resource":"*"}]}
EOF
POL="awscli-readers-$SUFFIX"
POLARN="arn:aws:iam::$ACCOUNT:policy/$POL"
ok create-policy -- aws iam create-policy --policy-name "$POL" --policy-document file://readers.json --description "readers"
fails create-policy-exists EntityAlreadyExists -- aws iam create-policy --policy-name "$POL" --policy-document file://readers.json
fails create-policy-minio-admin MalformedPolicyDocument -- aws iam create-policy --policy-name "awscli-minio-$SUFFIX" --policy-document file://minio.json
fails create-policy-malformed MalformedPolicyDocument -- aws iam create-policy --policy-name "awscli-bad-$SUFFIX" --policy-document '{"Statement":[{"Effect":"Allow"}]}'
eq get-policy "$POL" -- aws iam get-policy --policy-arn "$POLARN" --query Policy.PolicyName --output text
eq get-policy-version v1 -- aws iam get-policy-version --policy-arn "$POLARN" --version-id v1 --query PolicyVersion.VersionId --output text
CASE_CMD="iam get-policy-version" ok get-policy-version-document -- bash -c "aws iam get-policy-version --policy-arn $POLARN --version-id v1 --query PolicyVersion.Document --output json | grep -q ListAllMyBuckets"
eq list-policies "$POL" -- aws iam list-policies --scope Local --query "Policies[?PolicyName=='$POL'].PolicyName" --output text
eq list-policies-aws arn:aws:iam::aws:policy/readonly -- aws iam list-policies --scope AWS --query "Policies[?PolicyName=='readonly'].Arn" --output text
eq list-policies-only-attached 0 -- aws iam list-policies --only-attached --scope Local --query "length(Policies[?PolicyName=='$POL'])" --output text
ok list-policies-paged -- aws iam list-policies --max-items 2 --page-size 2
ok attach-user-policy -- aws iam attach-user-policy --user-name "$USER" --policy-arn "$POLARN"
eq list-attached-user-policies "$POL" -- aws iam list-attached-user-policies --user-name "$USER" --query 'AttachedPolicies[].PolicyName' --output text
eq get-policy-attachment-count 1 -- aws iam get-policy --policy-arn "$POLARN" --query Policy.AttachmentCount --output text
ok user-s3-ls-allowed -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws s3 ls
ok user-get-object-allowed -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws s3api get-object --bucket "$B" --key hello.txt user-get.txt
fails user-put-object-denied AccessDenied -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws s3api put-object --bucket "$B" --key user.txt --body hello.txt
fails delete-policy-attached DeleteConflict -- aws iam delete-policy --policy-arn "$POLARN"
fails delete-user-conflict-policy DeleteConflict -- aws iam delete-user --user-name "$USER"
ok detach-user-policy -- aws iam detach-user-policy --user-name "$USER" --policy-arn "$POLARN"
fails delete-policy-builtin InvalidInput -- aws iam delete-policy --policy-arn arn:aws:iam::aws:policy/readonly
fails get-policy-missing NoSuchEntity -- aws iam get-policy --policy-arn "arn:aws:iam::$ACCOUNT:policy/awscli-nope-$SUFFIX"

echo "== iam: groups"
GROUP="awscli-readers-$SUFFIX"
ok create-group -- aws iam create-group --group-name "$GROUP"
fails create-group-exists EntityAlreadyExists -- aws iam create-group --group-name "$GROUP"
eq get-group "arn:aws:iam::$ACCOUNT:group/$GROUP" -- aws iam get-group --group-name "$GROUP" --query Group.Arn --output text
eq list-groups "$GROUP" -- aws iam list-groups --query "Groups[?GroupName=='$GROUP'].GroupName" --output text
ok add-user-to-group -- aws iam add-user-to-group --group-name "$GROUP" --user-name "$USER"
eq get-group-members "$USER" -- aws iam get-group --group-name "$GROUP" --query 'Users[].UserName' --output text
eq list-groups-for-user "$GROUP" -- aws iam list-groups-for-user --user-name "$USER" --query 'Groups[].GroupName' --output text
ok attach-group-policy -- aws iam attach-group-policy --group-name "$GROUP" --policy-arn "$POLARN"
eq list-attached-group-policies "$POL" -- aws iam list-attached-group-policies --group-name "$GROUP" --query 'AttachedPolicies[].PolicyName' --output text
ok user-s3-ls-via-group -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws s3 ls
fails delete-group-conflict DeleteConflict -- aws iam delete-group --group-name "$GROUP"
fails delete-user-conflict-group DeleteConflict -- aws iam delete-user --user-name "$USER"
ok detach-group-policy -- aws iam detach-group-policy --group-name "$GROUP" --policy-arn "$POLARN"
ok remove-user-from-group -- aws iam remove-user-from-group --group-name "$GROUP" --user-name "$USER"
ok delete-group -- aws iam delete-group --group-name "$GROUP"
fails get-group-missing NoSuchEntity -- aws iam get-group --group-name "$GROUP"
ok delete-policy -- aws iam delete-policy --policy-arn "$POLARN"

echo "== iam: login profiles"
fails create-login-profile-short PasswordPolicyViolation -- aws iam create-login-profile --user-name "$USER" --password 'short'
ok create-login-profile -- aws iam create-login-profile --user-name "$USER" --password 'Correct-Horse-1' --no-password-reset-required
fails create-login-profile-exists EntityAlreadyExists -- aws iam create-login-profile --user-name "$USER" --password 'Correct-Horse-2'
eq get-login-profile "$USER" -- aws iam get-login-profile --user-name "$USER" --query LoginProfile.UserName --output text
ok update-login-profile -- aws iam update-login-profile --user-name "$USER" --password 'Correct-Horse-2'
ok change-password -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws iam change-password --old-password 'Correct-Horse-2' --new-password 'Correct-Horse-3'
fails change-password-wrong-old InvalidUserType -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws iam change-password --old-password 'wrong-old-password' --new-password 'Correct-Horse-4'
fails delete-user-conflict-login DeleteConflict -- aws iam delete-user --user-name "$USER"
ok delete-login-profile -- aws iam delete-login-profile --user-name "$USER"
fails get-login-profile-none NoSuchEntity -- aws iam get-login-profile --user-name "$USER"

echo "== iam: account / roles / unsupported"
ok get-account-summary -- aws iam get-account-summary
eq get-account-summary-users 1 -- aws iam get-account-summary --query 'SummaryMap.Users' --output text
ok list-roles -- aws iam list-roles
ok list-open-id-connect-providers -- aws iam list-open-id-connect-providers
ok list-saml-providers -- aws iam list-saml-providers
unsupported create-role InvalidAction -- aws iam create-role --role-name r --assume-role-policy-document '{"Version":"2012-10-17","Statement":[]}'
unsupported get-role InvalidAction -- aws iam get-role --role-name r
unsupported delete-role InvalidAction -- aws iam delete-role --role-name r
unsupported attach-role-policy InvalidAction -- aws iam attach-role-policy --role-name r --policy-arn arn:aws:iam::aws:policy/readonly
unsupported put-user-policy InvalidAction -- aws iam put-user-policy --user-name "$USER" --policy-name inline --policy-document file://readers.json
unsupported get-user-policy InvalidAction -- aws iam get-user-policy --user-name "$USER" --policy-name inline
unsupported list-user-policies InvalidAction -- aws iam list-user-policies --user-name "$USER"
unsupported delete-user-policy InvalidAction -- aws iam delete-user-policy --user-name "$USER" --policy-name inline
unsupported put-group-policy InvalidAction -- aws iam put-group-policy --group-name g --policy-name inline --policy-document file://readers.json
unsupported list-group-policies InvalidAction -- aws iam list-group-policies --group-name g
unsupported create-policy-version InvalidAction -- aws iam create-policy-version --policy-arn "$POLARN" --policy-document file://readers.json
unsupported list-policy-versions InvalidAction -- aws iam list-policy-versions --policy-arn "$POLARN"
unsupported delete-policy-version InvalidAction -- aws iam delete-policy-version --policy-arn "$POLARN" --version-id v2
unsupported set-default-policy-version InvalidAction -- aws iam set-default-policy-version --policy-arn "$POLARN" --version-id v1
unsupported list-entities-for-policy InvalidAction -- aws iam list-entities-for-policy --policy-arn "$POLARN"
unsupported update-group InvalidAction -- aws iam update-group --group-name g --new-group-name g2
unsupported tag-user InvalidAction -- aws iam tag-user --user-name "$USER" --tags Key=a,Value=b
unsupported list-user-tags InvalidAction -- aws iam list-user-tags --user-name "$USER"
unsupported untag-user InvalidAction -- aws iam untag-user --user-name "$USER" --tag-keys a
unsupported get-access-key-last-used InvalidAction -- aws iam get-access-key-last-used --access-key-id "$AK"
unsupported create-account-alias InvalidAction -- aws iam create-account-alias --account-alias opens3
unsupported list-account-aliases InvalidAction -- aws iam list-account-aliases
unsupported delete-account-alias InvalidAction -- aws iam delete-account-alias --account-alias opens3
unsupported get-account-password-policy InvalidAction -- aws iam get-account-password-policy
unsupported update-account-password-policy InvalidAction -- aws iam update-account-password-policy --minimum-password-length 8
unsupported delete-account-password-policy InvalidAction -- aws iam delete-account-password-policy
unsupported get-account-authorization-details InvalidAction -- aws iam get-account-authorization-details
unsupported generate-credential-report InvalidAction -- aws iam generate-credential-report
unsupported get-credential-report InvalidAction -- aws iam get-credential-report
unsupported list-mfa-devices InvalidAction -- aws iam list-mfa-devices
unsupported list-virtual-mfa-devices InvalidAction -- aws iam list-virtual-mfa-devices
unsupported create-virtual-mfa-device InvalidAction -- aws iam create-virtual-mfa-device --virtual-mfa-device-name d --outfile mfa.out --bootstrap-method QRCodePNG
unsupported list-instance-profiles InvalidAction -- aws iam list-instance-profiles
unsupported create-instance-profile InvalidAction -- aws iam create-instance-profile --instance-profile-name p
unsupported list-server-certificates InvalidAction -- aws iam list-server-certificates
unsupported list-ssh-public-keys InvalidAction -- aws iam list-ssh-public-keys
unsupported list-signing-certificates InvalidAction -- aws iam list-signing-certificates
unsupported list-service-specific-credentials InvalidAction -- aws iam list-service-specific-credentials
unsupported create-service-specific-credential InvalidAction -- aws iam create-service-specific-credential --user-name "$USER" --service-name codecommit.amazonaws.com
unsupported create-open-id-connect-provider InvalidAction -- aws iam create-open-id-connect-provider --url https://example.com --client-id-list x
unsupported create-saml-provider InvalidAction -- aws iam create-saml-provider --name s --saml-metadata-document "$(python3 -c 'print("<EntityDescriptor>" + "x" * 1000 + "</EntityDescriptor>")')"
unsupported create-service-linked-role InvalidAction -- aws iam create-service-linked-role --aws-service-name x.amazonaws.com
unsupported simulate-custom-policy InvalidAction -- aws iam simulate-custom-policy --policy-input-list file://readers.json --action-names s3:GetObject
unsupported simulate-principal-policy InvalidAction -- aws iam simulate-principal-policy --policy-source-arn "arn:aws:iam::$ACCOUNT:user/$USER" --action-names s3:GetObject
unsupported get-context-keys-for-custom-policy InvalidAction -- aws iam get-context-keys-for-custom-policy --policy-input-list file://readers.json
unsupported list-policies-granting-service-access InvalidAction -- aws iam list-policies-granting-service-access --arn "arn:aws:iam::$ACCOUNT:user/$USER" --service-namespaces s3
unsupported generate-service-last-accessed-details InvalidAction -- aws iam generate-service-last-accessed-details --arn "arn:aws:iam::$ACCOUNT:user/$USER"
unsupported list-organizations-features InvalidAction -- aws iam list-organizations-features
unsupported put-user-permissions-boundary InvalidAction -- aws iam put-user-permissions-boundary --user-name "$USER" --permissions-boundary arn:aws:iam::aws:policy/readonly
unsupported delete-user-permissions-boundary InvalidAction -- aws iam delete-user-permissions-boundary --user-name "$USER"
unsupported set-security-token-service-preferences InvalidAction -- aws iam set-security-token-service-preferences --global-endpoint-token-version v2Token
unsupported upload-ssh-public-key InvalidAction -- aws iam upload-ssh-public-key --user-name "$USER" --ssh-public-key-body "ssh-rsa AAAA"
unsupported tag-policy InvalidAction -- aws iam tag-policy --policy-arn arn:aws:iam::aws:policy/readonly --tags Key=a,Value=b
unsupported list-policy-tags InvalidAction -- aws iam list-policy-tags --policy-arn arn:aws:iam::aws:policy/readonly

echo "== sts"
eq get-caller-identity-root "arn:aws:iam::$ACCOUNT:root" -- aws sts get-caller-identity --query Arn --output text
eq get-caller-identity-account "$ACCOUNT" -- aws sts get-caller-identity --query Account --output text
CREDS="$(capture assume -- aws sts assume-role --role-arn "arn:aws:iam::$ACCOUNT:role/anything" --role-session-name cli-session --duration-seconds 900 --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
TAK="$(printf '%s' "$CREDS" | cut -f1)"; TSK="$(printf '%s' "$CREDS" | cut -f2)"; TTOK="$(printf '%s' "$CREDS" | cut -f3)"
CASE_CMD="sts assume-role" ok assume-role -- test -n "$TAK" -a -n "$TSK" -a -n "$TTOK"
eq assume-role-arn "arn:aws:sts::$ACCOUNT:assumed-role/root/cli-session" -- aws sts assume-role --role-arn "arn:aws:iam::$ACCOUNT:role/anything" --role-session-name cli-session --query AssumedRoleUser.Arn --output text
ok assume-role-s3-ls -- env AWS_ACCESS_KEY_ID="$TAK" AWS_SECRET_ACCESS_KEY="$TSK" AWS_SESSION_TOKEN="$TTOK" aws s3 ls
ok assume-role-put-object -- env AWS_ACCESS_KEY_ID="$TAK" AWS_SECRET_ACCESS_KEY="$TSK" AWS_SESSION_TOKEN="$TTOK" aws s3api put-object --bucket "$B" --key sts.txt --body hello.txt
ok assume-role-caller-identity -- env AWS_ACCESS_KEY_ID="$TAK" AWS_SECRET_ACCESS_KEY="$TSK" AWS_SESSION_TOKEN="$TTOK" aws sts get-caller-identity
fails assume-role-no-token InvalidToken -- env AWS_ACCESS_KEY_ID="$TAK" AWS_SECRET_ACCESS_KEY="$TSK" aws s3 ls
CREDS2="$(capture assume2 -- aws sts assume-role --role-arn "arn:aws:iam::$ACCOUNT:role/anything" --role-session-name narrowed --policy file://readers.json --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
TAK2="$(printf '%s' "$CREDS2" | cut -f1)"; TSK2="$(printf '%s' "$CREDS2" | cut -f2)"; TTOK2="$(printf '%s' "$CREDS2" | cut -f3)"
CASE_CMD="sts assume-role" ok assume-role-policy -- test -n "$TAK2" -a -n "$TTOK2"
ok assume-role-policy-ls -- env AWS_ACCESS_KEY_ID="$TAK2" AWS_SECRET_ACCESS_KEY="$TSK2" AWS_SESSION_TOKEN="$TTOK2" aws s3 ls
fails assume-role-policy-put-denied AccessDenied -- env AWS_ACCESS_KEY_ID="$TAK2" AWS_SECRET_ACCESS_KEY="$TSK2" AWS_SESSION_TOKEN="$TTOK2" aws s3api put-object --bucket "$B" --key sts2.txt --body hello.txt
fails assume-role-bad-policy MalformedPolicyDocument -- aws sts assume-role --role-arn "arn:aws:iam::$ACCOUNT:role/anything" --role-session-name sess --policy '{"Statement":[{"Effect":"Allow"}]}'
ok user-assume-role -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws sts assume-role --role-arn "arn:aws:iam::$ACCOUNT:role/anything" --role-session-name usess
unsupported get-session-token InvalidAction -- aws sts get-session-token
unsupported get-federation-token InvalidAction -- aws sts get-federation-token --name fed
unsupported get-access-key-info InvalidAction -- aws sts get-access-key-info --access-key-id "$AK"
unsupported decode-authorization-message InvalidAction -- aws sts decode-authorization-message --encoded-message xyz
unsupported assume-root InvalidAction -- aws sts assume-root --target-principal 000000000000 --task-policy-arn arn=arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials
unsupported assume-role-with-web-identity InvalidAction -- aws sts assume-role-with-web-identity --role-arn "arn:aws:iam::$ACCOUNT:role/r" --role-session-name wsess --web-identity-token abcd
unsupported assume-role-with-saml InvalidAction -- aws sts assume-role-with-saml --role-arn "arn:aws:iam::$ACCOUNT:role/r" --principal-arn "arn:aws:iam::$ACCOUNT:saml-provider/p" --saml-assertion abcd
unsupported get-delegated-access-token InvalidAction -- aws sts get-delegated-access-token --trade-in-token abcd
unsupported get-web-identity-token InvalidAction -- aws sts get-web-identity-token --audience x --signing-algorithm RS256

echo "== cleanup"
ok delete-access-key -- aws iam delete-access-key --user-name "$USER" --access-key-id "$AK"
fails user-key-deleted InvalidAccessKeyId -- env AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" aws sts get-caller-identity
ok delete-user -- aws iam delete-user --user-name "$USER"
fails delete-user-missing NoSuchEntity -- aws iam delete-user --user-name "$USER"
purge_versions "$VB"
purge_versions "$LB" --bypass-governance-retention
ok delete-bucket-versioned -- aws s3api delete-bucket --bucket "$VB"
ok delete-bucket-lock -- aws s3api delete-bucket --bucket "$LB"
ok delete-bucket-conf -- aws s3api delete-bucket --bucket "$CB"
aws s3 rm "s3://$B" --recursive >/dev/null 2>&1
ok delete-bucket -- aws s3api delete-bucket --bucket "$B"
fails delete-bucket-missing NoSuchBucket -- aws s3api delete-bucket --bucket "$B"
eq list-buckets-clean 0 -- aws s3api list-buckets --query "length(Buckets[?starts_with(Name, 'awscli-')])" --output text

echo
echo "awscli suite: $NPASS passed, $NFAIL failed ($RESULTS_TSV)"
[ "$NFAIL" -eq 0 ]
