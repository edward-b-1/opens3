#!/usr/bin/env python3
"""Compare a migrated OpenS3 with its MinIO source: every bucket and
current object (size, content hash, content type, user metadata, tags),
and the imported identities (users, groups, attached policies, and that
a migrated user's new key is bound by its policy)."""

from __future__ import annotations

import argparse
import csv
import hashlib
import sys

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError


def s3(endpoint, key, secret):
    return boto3.client("s3", endpoint_url=endpoint, region_name="us-east-1", aws_access_key_id=key, aws_secret_access_key=secret,
                        config=Config(s3={"addressing_style": "path"}))


def iam(endpoint, key, secret):
    return boto3.client("iam", endpoint_url=endpoint, region_name="us-east-1", aws_access_key_id=key, aws_secret_access_key=secret)


def sha256_of(client, bucket, key):
    h = hashlib.sha256()
    body = client.get_object(Bucket=bucket, Key=key)["Body"]
    for chunk in body.iter_chunks(1 << 20):
        h.update(chunk)
    return h.hexdigest()


def main() -> None:
    ap = argparse.ArgumentParser()
    for a in ("--src", "--src-key", "--src-secret", "--dst", "--dst-key", "--dst-secret", "--keys"):
        ap.add_argument(a, required=True)
    args = ap.parse_args()
    src, dst = s3(args.src, args.src_key, args.src_secret), s3(args.dst, args.dst_key, args.dst_secret)
    failures = 0

    def check(ok, what):
        nonlocal failures
        print(("ok   " if ok else "FAIL ") + what)
        failures += not ok

    # Buckets and objects.
    sb = sorted(b["Name"] for b in src.list_buckets()["Buckets"])
    db = sorted(b["Name"] for b in dst.list_buckets()["Buckets"])
    check(set(sb) <= set(db), f"buckets {sb} present on the destination")
    objects = 0
    for b in sb:
        for page in src.get_paginator("list_objects_v2").paginate(Bucket=b):
            for o in page.get("Contents", []):
                objects += 1
                key = o["Key"]
                try:
                    sh, dh = src.head_object(Bucket=b, Key=key), dst.head_object(Bucket=b, Key=key)
                except ClientError as e:
                    check(False, f"{b}/{key}: {e.response['Error']['Code']}")
                    continue
                same = sh["ContentLength"] == dh["ContentLength"] and sha256_of(src, b, key) == sha256_of(dst, b, key)
                check(same, f"{b}/{key}: {sh['ContentLength']} bytes, content identical")
                check(sh.get("ContentType") == dh.get("ContentType"), f"{b}/{key}: content type {dh.get('ContentType')}")
                # rclone records the source's timestamps as mtime/btime metadata
                # (and mc mirror --preserve as mc-attrs); compare the rest.
                added = ("mtime", "btime", "mc-attrs")
                sm = {k: v for k, v in sh.get("Metadata", {}).items() if k not in added}
                dm = {k: v for k, v in dh.get("Metadata", {}).items() if k not in added}
                check(sm == dm, f"{b}/{key}: user metadata {dm}")
                check("mtime" in dh.get("Metadata", {}), f"{b}/{key}: original modification time kept as mtime metadata")
                st = sorted((t["Key"], t["Value"]) for t in src.get_object_tagging(Bucket=b, Key=key)["TagSet"])
                dt = sorted((t["Key"], t["Value"]) for t in dst.get_object_tagging(Bucket=b, Key=key)["TagSet"])
                check(st == dt, f"{b}/{key}: tags {dt}")
    check(objects >= 4, f"{objects} objects compared")
    # Only the current version of a versioned key is migrated (documented).
    sv = src.list_object_versions(Bucket="archive", Prefix="report.txt").get("Versions", [])
    dv = dst.list_object_versions(Bucket="archive", Prefix="report.txt").get("Versions", [])
    check(len(sv) == 2 and len(dv) == 1, f"archive/report.txt: {len(sv)} versions at the source, {len(dv)} at the destination (current only)")
    body = dst.get_object(Bucket="archive", Key="report.txt")["Body"].read()
    check(b"v2" in body, "archive/report.txt: the current version was migrated")

    # Identities.
    di = iam(args.dst, args.dst_key, args.dst_secret)
    users = {u["UserName"] for u in di.list_users()["Users"]}
    check({"alice", "bob"} <= users, f"users {sorted(users)}")
    pol = lambda u: sorted(p["PolicyName"] for p in di.list_attached_user_policies(UserName=u)["AttachedPolicies"])
    check(pol("alice") == ["photos-ro"], f"alice policies {pol('alice')}")
    check(pol("bob") == ["readwrite"], f"bob policies {pol('bob')}")
    groups = {g["GroupName"] for g in di.list_groups()["Groups"]}
    check("analysts" in groups, f"groups {sorted(groups)}")
    members = [u["UserName"] for u in di.get_group(GroupName="analysts")["Users"]]
    check(members == ["alice"], f"analysts members {members}")
    gp = sorted(p["PolicyName"] for p in di.list_attached_group_policies(GroupName="analysts")["AttachedPolicies"])
    check(gp == ["readonly"], f"analysts policies {gp}")
    # Alice's new key: photos readable (own policy), docs readable (group readonly), nothing writable.
    with open(args.keys) as f:
        keys = {r["user"]: r for r in csv.DictReader(f)}
    check({"alice", "bob"} <= set(keys), f"new keys issued for {sorted(keys)}")
    alice = s3(args.dst, keys["alice"]["access_key"], keys["alice"]["secret_key"])
    try:
        alice.head_object(Bucket="photos", Key="2026/photo.bin")
        check(True, "alice reads photos with her new key")
    except ClientError as e:
        check(False, f"alice reads photos: {e.response['Error']['Code']}")
    try:
        alice.put_object(Bucket="photos", Key="x", Body=b"x")
        check(False, "alice could write to photos")
    except ClientError as e:
        check(e.response["Error"]["Code"] == "AccessDenied", "alice cannot write to photos")

    print()
    if failures:
        print(f"FAILED: {failures} check(s)")
        sys.exit(1)
    print(f"VERIFIED: {objects} objects and the identities match")


if __name__ == "__main__":
    main()
