#!/usr/bin/env python3
"""Read everything back from the server and check it against the manifest.

Exercises:
  * ListObjectsV2 with pagination (small MaxKeys) and with Prefix/Delimiter
  * GetObject full downloads with SHA-256 comparison
  * Range GETs (first bytes, middle, suffix)
  * conditional GET with If-None-Match (expects 304)
  * HeadObject on a missing key (expects 404)
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path

from botocore.exceptions import ClientError

import s3conf
from s3conf import DATA_DIR, DOWNLOAD_DIR

MB = 1024 * 1024


def list_all(s3, bucket: str, prefix: str = "") -> list[dict]:
    out: list[dict] = []
    paginator = s3.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket, Prefix=prefix, PaginationConfig={"PageSize": 7}):
        out.extend(page.get("Contents", []))
    return out


def stream_sha256(body) -> tuple[str, int]:
    h = hashlib.sha256()
    n = 0
    for chunk in body.iter_chunks(chunk_size=1 * MB):
        h.update(chunk)
        n += len(chunk)
    return h.hexdigest(), n


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--data", type=Path, default=DATA_DIR)
    ap.add_argument("--download-dir", type=Path, default=DOWNLOAD_DIR, help="where to save full downloads (default ./downloads)")
    ap.add_argument("--no-save", action="store_true", help="hash in memory, do not write downloads to disk")
    args = ap.parse_args()

    manifest = json.loads((args.data / "manifest.json").read_text())
    expected = {e["path"]: e for e in manifest["files"]}
    s3 = s3conf.client()
    bucket = s3conf.bucket_name("data")
    print(f"config: {s3conf.describe()}\nbucket: {bucket}")

    failures = 0

    # 1. Paginated listing covers every manifest key.
    try:
        listed = {o["Key"]: o for o in list_all(s3, bucket)}
    except ClientError as ex:
        if ex.response["Error"]["Code"] == "NoSuchBucket":
            raise SystemExit(f"bucket {bucket} does not exist on the server; run upload_data.py first")
        raise
    missing = set(expected) - set(listed)
    if missing:
        failures += 1
        print(f"FAIL listing is missing {len(missing)} keys: {sorted(missing)[:5]}...")
    else:
        print(f"ok   ListObjectsV2 (paginated) returned all {len(expected)} keys ({len(listed)} total)")
    for key, e in expected.items():
        if key in listed and listed[key]["Size"] != e["size"]:
            failures += 1
            print(f"FAIL listed size for {key}: {listed[key]['Size']} != {e['size']}")

    # 2. Prefix + delimiter listing behaves like directories.
    r = s3.list_objects_v2(Bucket=bucket, Prefix="tree/", Delimiter="/")
    prefixes = sorted(p["Prefix"] for p in r.get("CommonPrefixes", []))
    want = ["tree/year=2024/", "tree/year=2025/"]
    if prefixes != want:
        failures += 1
        print(f"FAIL CommonPrefixes {prefixes} != {want}")
    else:
        print(f"ok   delimiter listing: {prefixes}")

    # 3. Full download + hash of every object.
    if not args.no_save:
        args.download_dir.mkdir(parents=True, exist_ok=True)
    for key, e in expected.items():
        try:
            r = s3.get_object(Bucket=bucket, Key=key)
        except ClientError as ex:
            failures += 1
            print(f"FAIL GET {key}: {ex}")
            continue
        if args.no_save:
            digest, n = stream_sha256(r["Body"])
        else:
            dest = args.download_dir / key
            dest.parent.mkdir(parents=True, exist_ok=True)
            h = hashlib.sha256()
            n = 0
            with dest.open("wb") as f:
                for chunk in r["Body"].iter_chunks(chunk_size=1 * MB):
                    f.write(chunk)
                    h.update(chunk)
                    n += len(chunk)
            digest = h.hexdigest()
        if n != e["size"] or digest != e["sha256"]:
            failures += 1
            print(f"FAIL GET {key}: size={n}/{e['size']} sha_ok={digest == e['sha256']}")
        else:
            print(f"ok   GET {key} ({n} B, sha256 match)")

    # 4. Range requests against a medium blob.
    key = "bin/blob-1m.bin"
    local = (args.data / key).read_bytes()
    for rng, want in [("bytes=0-9", local[:10]), ("bytes=1000-1999", local[1000:2000]), ("bytes=-100", local[-100:])]:
        r = s3.get_object(Bucket=bucket, Key=key, Range=rng)
        got = r["Body"].read()
        status = r["ResponseMetadata"]["HTTPStatusCode"]
        if got != want or status != 206:
            failures += 1
            print(f"FAIL range {rng}: status={status} len={len(got)}")
        else:
            print(f"ok   range {rng} -> {len(got)} bytes, 206, Content-Range={r.get('ContentRange')}")

    # 5. Conditional GET: If-None-Match with the current ETag must yield 304.
    head = s3.head_object(Bucket=bucket, Key="tiny.txt")
    try:
        s3.get_object(Bucket=bucket, Key="tiny.txt", IfNoneMatch=head["ETag"])
        failures += 1
        print("FAIL If-None-Match: expected 304 Not Modified, got 200")
    except ClientError as ex:
        if ex.response["ResponseMetadata"]["HTTPStatusCode"] == 304:
            print("ok   If-None-Match -> 304 Not Modified")
        else:
            failures += 1
            print(f"FAIL If-None-Match: {ex}")

    # 6. Missing key must be a clean 404.
    try:
        s3.head_object(Bucket=bucket, Key="does/not/exist.bin")
        failures += 1
        print("FAIL HEAD missing key returned 200")
    except ClientError as ex:
        code = ex.response["ResponseMetadata"]["HTTPStatusCode"]
        print("ok   HEAD missing key -> 404" if code == 404 else f"FAIL HEAD missing key -> {code}")
        failures += code != 404

    print()
    if failures:
        print(f"FAILED: {failures} check(s) failed")
        sys.exit(1)
    print("PASSED: all checks succeeded")


if __name__ == "__main__":
    main()
