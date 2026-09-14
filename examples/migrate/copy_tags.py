#!/usr/bin/env python3
"""Copy object tags from one S3 server to another.

`mc mirror` and `rclone sync` copy object bytes, content type and user
metadata, but not object tags. Run this after the copy: it lists every
object in each bucket on the source, reads its tags, and writes them to
the same key on the destination. Objects without tags are skipped.

    ./copy_tags.py --src http://minio:9000 --src-key K --src-secret S \\
                   --dst http://opens3:9000 --dst-key K --dst-secret S [--bucket B]
"""

from __future__ import annotations

import argparse
import sys

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


def client(endpoint, key, secret, region, ca):
    return boto3.client("s3", endpoint_url=endpoint, region_name=region, verify=ca or True,
                        aws_access_key_id=key, aws_secret_access_key=secret,
                        config=Config(s3={"addressing_style": "path"}, retries={"max_attempts": 3}))


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--src", required=True)
    ap.add_argument("--src-key", required=True)
    ap.add_argument("--src-secret", required=True)
    ap.add_argument("--dst", required=True)
    ap.add_argument("--dst-key", required=True)
    ap.add_argument("--dst-secret", required=True)
    ap.add_argument("--region", default="us-east-1")
    ap.add_argument("--dst-ca-bundle", default=None)
    ap.add_argument("--bucket", default=None, help="only this bucket (default: every bucket on the source)")
    args = ap.parse_args()
    src = client(args.src, args.src_key, args.src_secret, args.region, None)
    dst = client(args.dst, args.dst_key, args.dst_secret, args.region, args.dst_ca_bundle)
    buckets = [args.bucket] if args.bucket else [b["Name"] for b in src.list_buckets()["Buckets"]]
    copied = skipped = failed = 0
    for b in buckets:
        for page in src.get_paginator("list_objects_v2").paginate(Bucket=b):
            for o in page.get("Contents", []):
                key = o["Key"]
                tags = src.get_object_tagging(Bucket=b, Key=key)["TagSet"]
                if not tags:
                    skipped += 1
                    continue
                try:
                    dst.put_object_tagging(Bucket=b, Key=key, Tagging={"TagSet": tags})
                    copied += 1
                except ClientError as e:
                    failed += 1
                    print(f"{b}/{key}: {e.response['Error']['Code']}", file=sys.stderr)
    print(f"tags copied for {copied} objects, {skipped} had none, {failed} failed")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
