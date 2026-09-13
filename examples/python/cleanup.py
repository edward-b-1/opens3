#!/usr/bin/env python3
"""Delete every object (and any in-progress multipart uploads) in the test
buckets, then delete the buckets themselves.

Only buckets whose name starts with OPENS3_BUCKET_PREFIX are touched.
"""

from __future__ import annotations

import argparse

from botocore.exceptions import ClientError

import s3conf


def empty_bucket(s3, bucket: str) -> int:
    deleted = 0
    paginator = s3.get_paginator("list_objects_v2")
    for page in paginator.paginate(Bucket=bucket):
        keys = [{"Key": o["Key"]} for o in page.get("Contents", [])]
        if keys:
            s3.delete_objects(Bucket=bucket, Delete={"Objects": keys, "Quiet": True})
            deleted += len(keys)
    # Abort any multipart uploads left behind by interrupted runs.
    try:
        for u in s3.list_multipart_uploads(Bucket=bucket).get("Uploads", []):
            s3.abort_multipart_upload(Bucket=bucket, Key=u["Key"], UploadId=u["UploadId"])
    except ClientError as e:
        print(f"  (list_multipart_uploads not supported or failed: {e.response['Error']['Code']})")
    return deleted


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--keep-buckets", action="store_true", help="empty the buckets but do not delete them")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    s3 = s3conf.client()
    prefix = s3conf.BUCKET_PREFIX + "-"
    targets = [b["Name"] for b in s3.list_buckets()["Buckets"] if b["Name"].startswith(prefix)]
    if not targets:
        print(f"no buckets with prefix {prefix!r}; nothing to do")
        return
    for b in targets:
        if args.dry_run:
            n = sum(len(p.get("Contents", [])) for p in s3.get_paginator("list_objects_v2").paginate(Bucket=b))
            print(f"would empty {b} ({n} objects)" + ("" if args.keep_buckets else " and delete it"))
            continue
        n = empty_bucket(s3, b)
        print(f"emptied {b}: {n} objects removed")
        if not args.keep_buckets:
            s3.delete_bucket(Bucket=b)
            print(f"deleted bucket {b}")


if __name__ == "__main__":
    main()
