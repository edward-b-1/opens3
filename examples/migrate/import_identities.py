#!/usr/bin/env python3
"""Import MinIO users, groups and policies into OpenS3.

MinIO does not export secret keys, so every imported user gets a new
access key pair, written to a CSV you hand to each user. Built-in policy
names (readonly, readwrite, writeonly, diagnostics, consoleAdmin) map to
OpenS3's built-ins of the same name; every other policy document is
created as is, since both use the AWS policy language.

Export from MinIO first (see examples/migrate/README.md):

    mc admin user list  minio --json  > export/users.json
    mc admin group list minio --json  > export/groups.json
    for p in $(mc admin policy list minio); do
        mc admin policy info minio "$p" > "export/policies/$p.json"; done
    for g in $(mc admin group list minio); do
        mc admin group info minio "$g" --json > "export/groups/$g.json"; done

Then, against OpenS3 with root (or an administrator's) credentials:

    ./import_identities.py --endpoint http://opens3:9000 \\
        --access-key ROOT --secret-key SECRET --export export --out new-keys.csv

Idempotent: existing users, groups and policies are left in place and
their attachments updated; new keys are only issued to users that have
no key yet, unless --rotate-keys is given.
"""

from __future__ import annotations

import argparse
import csv
import json
import sys
from pathlib import Path

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

BUILTIN = {"readonly", "readwrite", "writeonly", "diagnostics", "consoleAdmin"}


def read_json_lines(path: Path) -> list[dict]:
    """mc --json prints one JSON object per line (or one object)."""
    if not path.exists():
        return []
    text = path.read_text().strip()
    if not text:
        return []
    try:
        one = json.loads(text)
        return one if isinstance(one, list) else [one]
    except json.JSONDecodeError:
        return [json.loads(line) for line in text.splitlines() if line.strip()]


def policy_document(raw: dict) -> dict | None:
    """`mc admin policy info` prints the document itself; some versions wrap it."""
    if "Statement" in raw:
        return raw
    for key in ("policy", "Policy", "policyInfo", "PolicyInfo"):
        v = raw.get(key)
        if isinstance(v, dict) and "Statement" in v:
            return v
        if isinstance(v, str):
            try:
                doc = json.loads(v)
                if "Statement" in doc:
                    return doc
            except json.JSONDecodeError:
                pass
    return None


def split_policies(v) -> list[str]:
    if not v:
        return []
    if isinstance(v, list):
        return [p for p in v if p]
    return [p.strip() for p in str(v).split(",") if p.strip()]


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--endpoint", required=True, help="OpenS3 endpoint, e.g. http://localhost:9000")
    ap.add_argument("--access-key", required=True)
    ap.add_argument("--secret-key", required=True)
    ap.add_argument("--region", default="us-east-1")
    ap.add_argument("--ca-bundle", default=None, help="certificate file for a self-signed OpenS3")
    ap.add_argument("--export", type=Path, required=True, help="directory with users.json, groups.json, policies/, groups/")
    ap.add_argument("--out", type=Path, default=Path("new-keys.csv"), help="CSV of the access keys issued")
    ap.add_argument("--rotate-keys", action="store_true", help="issue a new key even to users that already have one")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    iam = boto3.client("iam", endpoint_url=args.endpoint, region_name=args.region, verify=args.ca_bundle or True,
                       aws_access_key_id=args.access_key, aws_secret_access_key=args.secret_key,
                       config=Config(retries={"max_attempts": 3}))
    account = iam.get_user()["User"]["Arn"].split(":")[4]

    def arn(policy: str) -> str:
        if policy in BUILTIN:
            return f"arn:aws:iam::aws:policy/{policy}"
        return f"arn:aws:iam::{account}:policy/{policy}"

    # Policies.
    created_policies = 0
    for f in sorted((args.export / "policies").glob("*.json")) if (args.export / "policies").is_dir() else []:
        name = f.stem
        if name in BUILTIN:
            print(f"policy {name}: built-in, mapped to OpenS3's {name}")
            continue
        doc = policy_document(json.loads(f.read_text()))
        if doc is None:
            print(f"policy {name}: no policy document found in {f}, skipped", file=sys.stderr)
            continue
        if args.dry_run:
            print(f"policy {name}: would create")
            continue
        try:
            iam.create_policy(PolicyName=name, PolicyDocument=json.dumps(doc))
            created_policies += 1
            print(f"policy {name}: created")
        except ClientError as e:
            if e.response["Error"]["Code"] == "EntityAlreadyExists":
                print(f"policy {name}: exists")
            else:
                raise

    # Groups (created before users so memberships can be set).
    groups: dict[str, dict] = {}
    for f in sorted((args.export / "groups").glob("*.json")) if (args.export / "groups").is_dir() else []:
        for g in read_json_lines(f):
            name = g.get("groupName") or g.get("name") or f.stem
            groups[name] = g
    for name, g in groups.items():
        if args.dry_run:
            print(f"group {name}: would create with {len(g.get('members') or [])} members")
            continue
        try:
            iam.create_group(GroupName=name)
            print(f"group {name}: created")
        except ClientError as e:
            if e.response["Error"]["Code"] != "EntityAlreadyExists":
                raise
            print(f"group {name}: exists")
        for p in split_policies(g.get("groupPolicy") or g.get("policy")):
            iam.attach_group_policy(GroupName=name, PolicyArn=arn(p))

    # Users.
    rows = []
    for u in read_json_lines(args.export / "users.json"):
        name = u.get("accessKey") or u.get("name")
        if not name:
            continue
        enabled = (u.get("userStatus") or "enabled") == "enabled"
        policies = split_policies(u.get("policyName"))
        if args.dry_run:
            print(f"user {name}: would create ({'enabled' if enabled else 'disabled'}), policies {policies}")
            continue
        try:
            iam.create_user(UserName=name)
            print(f"user {name}: created")
        except ClientError as e:
            if e.response["Error"]["Code"] != "EntityAlreadyExists":
                raise
            print(f"user {name}: exists")
        for p in policies:
            iam.attach_user_policy(UserName=name, PolicyArn=arn(p))
        for gname, g in groups.items():
            if name in (g.get("members") or []):
                iam.add_user_to_group(GroupName=gname, UserName=name)
        if not enabled:
            print(f"user {name}: was disabled in MinIO; created without an access key. Enable it in OpenS3 when ready.")
            continue
        existing = iam.list_access_keys(UserName=name)["AccessKeyMetadata"]
        if existing and not args.rotate_keys:
            print(f"user {name}: already has {len(existing)} key(s); not issuing another (use --rotate-keys)")
            continue
        k = iam.create_access_key(UserName=name)["AccessKey"]
        rows.append((name, k["AccessKeyId"], k["SecretAccessKey"]))
        print(f"user {name}: new access key {k['AccessKeyId']}")

    if rows and not args.dry_run:
        with args.out.open("w", newline="") as f:
            w = csv.writer(f)
            w.writerow(["user", "access_key", "secret_key"])
            w.writerows(rows)
        args.out.chmod(0o600)
        print(f"\n{len(rows)} new key pair(s) written to {args.out}; hand each user their line and delete the file.")
    print(f"done: {created_policies} policies created, {len(groups)} groups, {len(rows)} keys issued")


if __name__ == "__main__":
    try:
        main()
    except ClientError as e:
        print(f"error: {e}", file=sys.stderr)
        sys.exit(1)
