#!/usr/bin/env python3
"""Create buckets on the OpenS3 server and upload the generated data.

Exercises:
  * CreateBucket / HeadBucket / ListBuckets
  * PutObject for small files (with Content-Type and user metadata)
  * transfer-manager uploads (automatic multipart for large files)
  * an explicit, hand-driven multipart upload
  * CopyObject and a DeleteObjects batch
  * HeadObject validation of size + metadata after every upload

Run generate_data.py first (or pass --generate).
"""

from __future__ import annotations

import argparse
import json
import mimetypes
import sys
import time
from pathlib import Path

from boto3.s3.transfer import TransferConfig
from botocore.exceptions import ClientError

import s3conf
from s3conf import DATA_DIR

MB = 1024 * 1024


def ensure_bucket(s3, name: str) -> None:
    try:
        s3.head_bucket(Bucket=name)
        print(f"bucket {name}: exists")
        return
    except ClientError as e:
        if e.response["Error"]["Code"] not in ("404", "NoSuchBucket", "NotFound"):
            raise
    s3.create_bucket(Bucket=name)
    print(f"bucket {name}: created")


def upload_small(s3, bucket: str, root: Path, entry: dict) -> None:
    rel = entry["path"]
    path = root / rel
    ctype = mimetypes.guess_type(path.name)[0] or "application/octet-stream"
    with path.open("rb") as f:
        resp = s3.put_object(
            Bucket=bucket,
            Key=rel,
            Body=f,
            ContentType=ctype,
            Metadata={"sha256": entry["sha256"], "source": "pyclient"},
        )
    etag = resp.get("ETag", "")
    print(f"  put   {rel} ({entry['size']} B, {ctype}) etag={etag}")


def upload_managed(resource, bucket: str, root: Path, entry: dict) -> None:
    """Use boto3's transfer manager, which switches to multipart above 8 MiB."""
    rel = entry["path"]
    path = root / rel
    cfg = TransferConfig(multipart_threshold=8 * MB, multipart_chunksize=8 * MB, max_concurrency=4)
    start = time.monotonic()
    resource.Bucket(bucket).upload_file(
        str(path),
        rel,
        ExtraArgs={"Metadata": {"sha256": entry["sha256"], "source": "pyclient-transfer"}},
        Config=cfg,
    )
    secs = time.monotonic() - start
    print(f"  xfer  {rel} ({entry['size'] / MB:.1f} MiB) in {secs:.2f}s")


def upload_manual_multipart(s3, bucket: str, root: Path, entry: dict, part_size: int = 5 * MB) -> None:
    """Drive CreateMultipartUpload / UploadPart / CompleteMultipartUpload by hand."""
    rel = entry["path"]
    key = "manual-multipart/" + rel
    path = root / rel
    mpu = s3.create_multipart_upload(Bucket=bucket, Key=key, Metadata={"sha256": entry["sha256"]})
    upload_id = mpu["UploadId"]
    parts = []
    try:
        with path.open("rb") as f:
            n = 1
            while True:
                chunk = f.read(part_size)
                if not chunk:
                    break
                r = s3.upload_part(Bucket=bucket, Key=key, UploadId=upload_id, PartNumber=n, Body=chunk)
                parts.append({"PartNumber": n, "ETag": r["ETag"]})
                n += 1
        listed = s3.list_parts(Bucket=bucket, Key=key, UploadId=upload_id)
        assert len(listed.get("Parts", [])) == len(parts), "ListParts count mismatch"
        s3.complete_multipart_upload(
            Bucket=bucket, Key=key, UploadId=upload_id, MultipartUpload={"Parts": parts}
        )
    except Exception:
        s3.abort_multipart_upload(Bucket=bucket, Key=key, UploadId=upload_id)
        raise
    print(f"  mpu   {key} in {len(parts)} parts of {part_size / MB:.0f} MiB")


def check_head(s3, bucket: str, key: str, expect_size: int, expect_sha: str | None) -> None:
    h = s3.head_object(Bucket=bucket, Key=key)
    if h["ContentLength"] != expect_size:
        raise SystemExit(f"HEAD {key}: size {h['ContentLength']} != expected {expect_size}")
    if expect_sha and h.get("Metadata", {}).get("sha256") != expect_sha:
        raise SystemExit(f"HEAD {key}: metadata sha256 missing or wrong: {h.get('Metadata')}")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--data", type=Path, default=DATA_DIR, help="directory produced by generate_data.py")
    ap.add_argument("--generate", action="store_true", help="run generate_data.py first")
    ap.add_argument("--skip-large", action="store_true", help="skip the large multipart blobs")
    args = ap.parse_args()

    if args.generate or not (args.data / "manifest.json").exists():
        import generate_data

        generate_data.generate(args.data, seed=42, large_mb=20)

    manifest = json.loads((args.data / "manifest.json").read_text())
    entries = manifest["files"]
    print(f"config: {s3conf.describe()}")

    s3 = s3conf.client()
    res = s3conf.resource()

    main_bucket = s3conf.bucket_name("data")
    copy_bucket = s3conf.bucket_name("copies")
    ensure_bucket(s3, main_bucket)
    ensure_bucket(s3, copy_bucket)

    names = {b["Name"] for b in s3.list_buckets()["Buckets"]}
    missing = {main_bucket, copy_bucket} - names
    if missing:
        raise SystemExit(f"ListBuckets did not return {missing}")

    small = [e for e in entries if not e["path"].startswith("large/")]
    large = [e for e in entries if e["path"].startswith("large/")]

    print(f"\nuploading {len(small)} small/medium objects to {main_bucket}")
    for e in small:
        upload_small(s3, main_bucket, args.data, e)
        check_head(s3, main_bucket, e["path"], e["size"], e["sha256"])

    if not args.skip_large:
        print(f"\nuploading {len(large)} large objects via transfer manager")
        for e in large:
            upload_managed(res, main_bucket, args.data, e)
            check_head(s3, main_bucket, e["path"], e["size"], e["sha256"])

        print("\nmanual multipart upload")
        upload_manual_multipart(s3, main_bucket, args.data, large[0])
        check_head(s3, main_bucket, "manual-multipart/" + large[0]["path"], large[0]["size"], large[0]["sha256"])

    print(f"\nserver-side copies into {copy_bucket}")
    for e in small[:5]:
        s3.copy_object(
            Bucket=copy_bucket,
            Key="copied/" + e["path"],
            CopySource={"Bucket": main_bucket, "Key": e["path"]},
        )
        check_head(s3, copy_bucket, "copied/" + e["path"], e["size"], e["sha256"])
        print(f"  copy  {e['path']} -> {copy_bucket}/copied/{e['path']}")

    print("\nbatch delete of temporary objects")
    tmp_keys = [f"tmp/scratch-{i}.txt" for i in range(10)]
    for k in tmp_keys:
        s3.put_object(Bucket=main_bucket, Key=k, Body=b"scratch")
    resp = s3.delete_objects(
        Bucket=main_bucket, Delete={"Objects": [{"Key": k} for k in tmp_keys], "Quiet": False}
    )
    deleted = {d["Key"] for d in resp.get("Deleted", [])}
    if deleted != set(tmp_keys) or resp.get("Errors"):
        raise SystemExit(f"DeleteObjects mismatch: {resp}")
    print(f"  deleted {len(deleted)} objects in one request")

    total = sum(e["size"] for e in entries)
    print(f"\nOK: {len(entries)} objects ({total / MB:.1f} MiB) uploaded to {main_bucket}")


if __name__ == "__main__":
    try:
        main()
    except ClientError as e:
        print(f"S3 error: {e}", file=sys.stderr)
        sys.exit(1)
