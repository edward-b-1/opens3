#!/usr/bin/env bash
# Transport scenarios: the real AWS CLI against a TLS server and a plain
# server with matching and mismatching endpoint schemes, plus the browser
# side of the TLS port. Appends to the results of suite.sh.
#
# Environment (set by run.sh): AWS_BIN, AWSCLI_OUT, AWSCLI_SCRATCH,
# PLAIN_PORT, TLS_PORT, TLS_CA (path to the server certificate), plus the
# AWS_* credentials. AWS_ENDPOINT_URL is set per case here.
set -uo pipefail
OUT="${AWSCLI_OUT:?}"
SCRATCH="${AWSCLI_SCRATCH:-$(mktemp -d)}"
RESULTS_TSV="$OUT/results.tsv"
mkdir -p "$OUT/cases" "$SCRATCH"
cd "$SCRATCH"
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
unset AWS_ENDPOINT_URL

HTTPS="https://127.0.0.1:$TLS_PORT"
HTTP_ON_TLS="http://127.0.0.1:$TLS_PORT"
HTTPS_ON_PLAIN="https://127.0.0.1:$PLAIN_PORT"
B="transport-$RANDOM"
echo "== transport: TLS server on :$TLS_PORT, plain server on :$PLAIN_PORT"

# 1. TLS server, https client trusting the certificate: everything works.
CASE_CMD="transport tls-client-with-ca" ok tls-mb -- env AWS_ENDPOINT_URL="$HTTPS" AWS_CA_BUNDLE="$TLS_CA" "$AWS_BIN" s3 mb "s3://$B"
echo hello >"$SCRATCH/t.txt"
CASE_CMD="transport tls-client-with-ca" ok tls-cp -- env AWS_ENDPOINT_URL="$HTTPS" AWS_CA_BUNDLE="$TLS_CA" "$AWS_BIN" s3 cp "$SCRATCH/t.txt" "s3://$B/t.txt"
CASE_CMD="transport tls-client-with-ca" eq tls-ls t.txt -- env AWS_ENDPOINT_URL="$HTTPS" AWS_CA_BUNDLE="$TLS_CA" "$AWS_BIN" s3api list-objects-v2 --bucket "$B" --query 'Contents[0].Key' --output text
CASE_CMD="transport tls-client-with-ca" ok tls-iam -- env AWS_ENDPOINT_URL="$HTTPS" AWS_CA_BUNDLE="$TLS_CA" "$AWS_BIN" sts get-caller-identity

# 2. TLS server, https client that does not trust the certificate: refused
#    by the client with a verification error, never a hang or a plain 4xx.
CASE_CMD="transport tls-client-without-ca" fails_with tls-no-ca 'CERTIFICATE_VERIFY_FAILED|SSL validation failed|certificate verify failed' -- env -u AWS_CA_BUNDLE AWS_ENDPOINT_URL="$HTTPS" "$AWS_BIN" s3 ls "s3://$B"

# 3. TLS server, client still using http://: a parsed S3 error that names
#    the https URL (botocore used to loop on a redirect here).
CASE_CMD="transport http-client-to-tls-server" fails tls-http-ls InvalidRequest -- env AWS_ENDPOINT_URL="$HTTP_ON_TLS" "$AWS_BIN" s3 ls "s3://$B/"
CASE_CMD="transport http-client-to-tls-server" fails_with tls-http-message 'requires HTTPS\. Use https://127\.0\.0\.1' -- env AWS_ENDPOINT_URL="$HTTP_ON_TLS" "$AWS_BIN" s3api list-objects-v2 --bucket "$B"
CASE_CMD="transport http-client-to-tls-server" fails tls-http-put InvalidRequest -- env AWS_ENDPOINT_URL="$HTTP_ON_TLS" "$AWS_BIN" s3 cp "$SCRATCH/t.txt" "s3://$B/u.txt"

# 4. Plain server, https client: a client-side TLS error, not a hang.
CASE_CMD="transport https-client-to-plain-server" fails_with plain-https 'SSL|wrong version number|Could not connect|EOF' -- env AWS_ENDPOINT_URL="$HTTPS_ON_PLAIN" AWS_CA_BUNDLE="$TLS_CA" "$AWS_BIN" s3 ls

# 5. Browser side of the TLS port: http console is redirected, https console
#    serves the page with HSTS.
CASE_CMD="transport browser-http-redirect" eq console-redirect 307 -- curl -s -o /dev/null -w '%{http_code}' -H 'Accept: text/html' "$HTTP_ON_TLS/console/"
CASE_CMD="transport browser-http-redirect" eq console-redirect-location "$HTTPS/console/" -- curl -s -o /dev/null -w '%{redirect_url}' -H 'Accept: text/html' "$HTTP_ON_TLS/console/"
CASE_CMD="transport browser-https" eq console-https 200 -- curl -s -o /dev/null -w '%{http_code}' --cacert "$TLS_CA" "$HTTPS/console/"
CASE_CMD="transport browser-https" eq console-hsts 'max-age=63072000' -- bash -c "curl -sI --cacert '$TLS_CA' '$HTTPS/console/' | tr -d '\r' | sed -n 's/^[Ss]trict-[Tt]ransport-[Ss]ecurity: //p'"
CASE_CMD="transport browser-https" eq health-https 200 -- curl -s -o /dev/null -w '%{http_code}' --cacert "$TLS_CA" "$HTTPS/opens3/health/ready"

CASE_CMD="transport tls-client-with-ca" ok tls-rb -- env AWS_ENDPOINT_URL="$HTTPS" AWS_CA_BUNDLE="$TLS_CA" "$AWS_BIN" s3 rb "s3://$B" --force
echo "transport: $NPASS passed, $NFAIL failed"
[ "$NFAIL" -eq 0 ]
