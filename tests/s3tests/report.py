#!/usr/bin/env python3
"""Summarise a Ceph s3-tests junit XML run for OpenS3.

Usage: report.py --junit results.xml --known known-failures.txt
                 [--markdown docs/CONFORMANCE.md] [--ref <s3-tests commit>]
                 [--duration <seconds>]

Exit status is 1 when a test that is not in the known-failures list failed
(a regression) or when the results file is unreadable; otherwise 0. Tests in
the known list that now pass are printed so the list can shrink.

Stdlib only.
"""
import argparse
import collections
import datetime
import re
import sys
import xml.etree.ElementTree as ET

# Feature buckets: the first matching regexp on the bare test name wins.
FEATURES = [
    ("Authentication (SigV2/SigV4, presigned)", r"auth|_sigv|presign|anon|bad_authorization|_expired|_date|_no_key|_no_date|_no_secret|_unreadable"),
    ("Headers & request validation", r"_md5|content_length|_contentlength|_expect|_header|user_metadata|_metadata|_transfer|_chunk|content_type|content_disposition|content_encoding|content_language|cache_control|expires"),
    ("Bucket create / delete / naming", r"^bucket_(create|delete|head|recreate|configure|notexist|list_?buckets)|bucket_?name|bucket_delete|bucket_head|bucket_create|create_bucket|delete_bucket|bucket_notexist|bucket_recreate|buckets_create|buckets_list|list_buckets|bucket_location"),
    ("Bucket listing (ListObjects v1/v2, versions, delimiter, marker)", r"^bucket_list|list_objects|listv2|list_versions|bucket_listv2"),
    ("Object ACLs", r"^object_acl|object_.*acl|acl.*object"),
    ("Bucket ACLs & grants", r"acl|grant|_canned|object_ownership|ownership"),
    ("Bucket policy & public access", r"policy|public_access|block_public|principal"),
    ("Object lock, retention, legal hold", r"object_lock|retention|legal_hold|governance|compliance"),
    ("Versioning & delete markers", r"version|delete_marker"),
    ("Multipart upload", r"multipart|upload_part|list_parts|abort_"),
    ("Copy", r"copy"),
    ("Encryption (SSE-C, SSE-S3, SSE-KMS)", r"sse|encrypt|kms"),
    ("Lifecycle configuration", r"lifecycle"),
    ("CORS", r"cors"),
    ("Tagging", r"tag"),
    ("Website / logging / accelerate / requester pays", r"website|logging|accelerate|request_?pay|torrent"),
    ("Conditional & ranged reads/writes", r"ifmatch|ifnonematch|if_match|if_none|ifmodified|ifunmodified|precondition|range|conditional|_etag|_read_not_exist"),
    ("POST object (browser form upload)", r"^post_|post_object|post_upload"),
    ("Checksums & GetObjectAttributes", r"checksum|crc|sha|attributes"),
    ("Object put / get / delete / head", r"object|^put_|^get_|^delete|^head|^list_|_delete|_put|_get|_head"),
    ("Bucket configuration (encryption, notifications, misc)", r"bucket|notification"),
]


def feature_of(name):
    bare = re.sub(r"^test_", "", name.split("[", 1)[0])
    for feat, rx in FEATURES:
        if re.search(rx, bare):
            return feat
    return "Other"


def load_known(path):
    known = {}
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.split("#", 1)[0].strip() if not line.lstrip().startswith("#") else ""
                if line:
                    known[line] = True
    except FileNotFoundError:
        pass
    return known


def parse_junit(path):
    tree = ET.parse(path)
    cases = []
    for tc in tree.iter("testcase"):
        classname = tc.get("classname", "")
        name = tc.get("name", "")
        module = classname.split(".")[-1] if classname else "?"
        module_path = classname.replace(".", "/") + ".py" if classname else "?"
        test_id = f"{module_path}::{name}"
        status, detail = "passed", ""
        for child in tc:
            if child.tag in ("failure", "error"):
                status = "failed" if child.tag == "failure" else "error"
                msg = (child.get("message") or child.text or "").strip()
                detail = first_line(msg)
            elif child.tag == "skipped":
                status = "skipped"
                detail = first_line(child.get("message") or child.text or "")
        cases.append({"id": test_id, "module": module, "name": name, "status": status, "detail": detail, "time": float(tc.get("time") or 0)})
    return cases


def first_line(msg):
    msg = msg.strip()
    # pytest puts the assertion / exception on the first line; keep it short.
    lines = [l.strip() for l in msg.splitlines() if l.strip()]
    if not lines:
        return ""
    line = lines[0]
    # botocore errors are long; keep the useful part.
    line = re.sub(r"^botocore\.exceptions\.", "", line)
    line = re.sub(r"^(AssertionError|Failed): ?", "", line)
    return line[:160]


def counts(cases):
    c = collections.Counter(x["status"] for x in cases)
    return c["passed"], c["failed"] + c["error"], c["skipped"]


def write_markdown(path, cases, known, ref, duration):
    total_p, total_f, total_s = counts(cases)
    lines = []
    lines.append("# S3 conformance report (Ceph s3-tests)")
    lines.append("")
    lines.append("Generated by `tests/s3tests/run.sh` — do not edit by hand.")
    lines.append("")
    lines.append(f"- s3-tests commit: `{ref or 'unknown'}`")
    lines.append(f"- run date: {datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%d %H:%M UTC')}")
    if duration:
        lines.append(f"- wall time: {int(duration)//60}m{int(duration)%60:02d}s")
    lines.append(f"- selection: `s3tests/functional/test_s3.py`, `s3tests/functional/test_headers.py` (markers excluded: see run.sh)")
    lines.append("")
    run = total_p + total_f
    pct = (100.0 * total_p / run) if run else 0.0
    lines.append(f"**{total_p} passed, {total_f} failed, {total_s} skipped — {pct:.1f}% of executed tests pass.**")
    lines.append("")
    lines.append("## Matrix by feature")
    lines.append("")
    lines.append("| Feature | Pass | Fail | Skip | Pass rate |")
    lines.append("|---|---:|---:|---:|---:|")
    by_feat = collections.OrderedDict()
    for feat, _ in FEATURES + [("Other", None)]:
        by_feat[feat] = []
    for c in cases:
        by_feat[feature_of(c["name"])].append(c)
    for feat, cs in by_feat.items():
        if not cs:
            continue
        p, f, s = counts(cs)
        r = p + f
        lines.append(f"| {feat} | {p} | {f} | {s} | {(100.0*p/r if r else 0):.0f}% |")
    lines.append("")
    lines.append("## Matrix by module")
    lines.append("")
    lines.append("| Module | Pass | Fail | Skip |")
    lines.append("|---|---:|---:|---:|")
    by_mod = collections.defaultdict(list)
    for c in cases:
        by_mod[c["module"]].append(c)
    for mod in sorted(by_mod):
        p, f, s = counts(by_mod[mod])
        lines.append(f"| {mod} | {p} | {f} | {s} |")
    lines.append("")
    lines.append("## Failing tests")
    lines.append("")
    lines.append("Tests listed in `tests/s3tests/known-failures.txt` are marked *known*; the comment there gives the reason.")
    lines.append("")
    for feat, cs in by_feat.items():
        fails = [c for c in cs if c["status"] in ("failed", "error")]
        if not fails:
            continue
        lines.append(f"### {feat}")
        lines.append("")
        for c in sorted(fails, key=lambda x: x["id"]):
            tag = "known" if c["id"] in known else "**NEW**"
            detail = c["detail"].replace("|", "\\|")
            lines.append(f"- `{c['name']}` ({tag}) — {detail}")
        lines.append("")
    skips = [c for c in cases if c["status"] == "skipped"]
    if skips:
        lines.append("## Skipped tests")
        lines.append("")
        reasons = collections.Counter(c["detail"] for c in skips)
        for reason, n in reasons.most_common():
            lines.append(f"- {n} × {reason or '(no reason)'}")
        lines.append("")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--junit", required=True)
    ap.add_argument("--known", required=True)
    ap.add_argument("--markdown")
    ap.add_argument("--ref", default="")
    ap.add_argument("--duration", type=float, default=0)
    args = ap.parse_args()

    try:
        cases = parse_junit(args.junit)
    except (ET.ParseError, OSError) as e:
        print(f"report: cannot read {args.junit}: {e}", file=sys.stderr)
        return 1
    known = load_known(args.known)
    p, f, s = counts(cases)
    print(f"s3-tests: {p} passed, {f} failed, {s} skipped ({len(cases)} collected)")

    failed = {c["id"] for c in cases if c["status"] in ("failed", "error")}
    passed = {c["id"] for c in cases if c["status"] == "passed"}
    new_failures = sorted(failed - set(known))
    fixed = sorted(k for k in known if k in passed)

    if args.markdown:
        write_markdown(args.markdown, cases, known, args.ref, args.duration)
        print(f"wrote {args.markdown}")
    if fixed:
        print(f"\n{len(fixed)} known failure(s) now pass — remove them from {args.known}:")
        for t in fixed:
            print("  " + t)
    if new_failures:
        print(f"\n{len(new_failures)} REGRESSION(S) (failed and not in {args.known}):")
        by_id = {c["id"]: c for c in cases}
        for t in new_failures:
            print(f"  {t}\n      {by_id[t]['detail']}")
        return 1
    print("\nno regressions")
    return 0


if __name__ == "__main__":
    sys.exit(main())
