#!/usr/bin/env python3
"""Create every kind of record the master key protects, then verify it.

Meant for testing a master key rotation (`opens3 master rotate`): run it
once to create the data, rotate the key with the server stopped, then run
it again with --verify to prove everything still reads back.

Creates (in bucket <prefix>-encrypted):
  * objects with SSE-S3 (AES256): data keys wrapped by the master key
  * objects with SSE-KMS (aws:kms, default key): wrapped by a named key,
    which is itself wrapped by the master key
  * an object with SSE-C: wrapped by a key the server never stores
  * an object written under a bucket default encryption rule (SSE-S3)
  * a multipart upload with SSE-S3 left in progress (its data key is
    wrapped in the upload record)
  * a plaintext object, as a control
Also creates an IAM user with an access key: stored secrets are wrapped by
the master key too, and --verify signs a request with that key.

State needed to verify (hashes, the SSE-C key, the IAM key pair, the
upload id) is saved to generated/encrypted-manifest.json. Treat that file
as a secret while it exists; --cleanup removes it with everything else.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import sys

from botocore.config import Config
from botocore.exceptions import ClientError

import s3conf
from s3conf import DATA_DIR

MANIFEST = DATA_DIR / "encrypted-manifest.json"
MB = 1024 * 1024


def iam_client():
    return s3conf.session().client("iam", endpoint_url=s3conf.ENDPOINT, verify=s3conf.TLS_VERIFY)


def s3_client_as(access_key: str, secret_key: str):
    """An S3 client signing with a specific key pair (the created IAM user)."""
    return s3conf.session().client(
        "s3",
        endpoint_url=s3conf.ENDPOINT,
        verify=s3conf.TLS_VERIFY,
        aws_access_key_id=access_key,
        aws_secret_access_key=secret_key,
        config=Config(s3={"addressing_style": "path"}),
    )


def body(seed: str, size: int) -> bytes:
    """Deterministic pseudo-random bytes so the content is reproducible."""
    out = bytearray()
    counter = 0
    while len(out) < size:
        out += hashlib.sha256(f"{seed}:{counter}".encode()).digest()
        counter += 1
    return bytes(out[:size])


def sha256(b: bytes) -> str:
    return hashlib.sha256(b).hexdigest()


def ssec_args(key: bytes) -> dict:
    return {
        "SSECustomerAlgorithm": "AES256",
        "SSECustomerKey": base64.b64encode(key).decode(),
        "SSECustomerKeyMD5": base64.b64encode(hashlib.md5(key).digest()).decode(),
    }


# --------------------------------------------------------------------- create


def create(args) -> None:
    s3 = s3conf.client()
    iam = iam_client()
    bucket = s3conf.bucket_name("encrypted")
    user = f"{s3conf.BUCKET_PREFIX}-rotation-user"
    print(f"config: {s3conf.describe()}\nbucket: {bucket}\nuser:   {user}")

    try:
        s3.create_bucket(Bucket=bucket)
        print("created bucket")
    except ClientError as e:
        if e.response["Error"]["Code"] != "BucketAlreadyOwnedByYou":
            raise
        print("bucket exists")

    manifest: dict = {"bucket": bucket, "objects": [], "user": user}

    def put(key: str, data: bytes, expect_sse: str, **extra) -> None:
        r = s3.put_object(Bucket=bucket, Key=key, Body=data, **extra)
        got = r.get("ServerSideEncryption") or ("SSE-C" if r.get("SSECustomerAlgorithm") else "none")
        if got != expect_sse:
            raise SystemExit(f"PUT {key}: server reported encryption {got!r}, expected {expect_sse!r}")
        manifest["objects"].append({"key": key, "size": len(data), "sha256": sha256(data), "sse": expect_sse})
        print(f"  put {key:<28} {len(data):>9} B  {expect_sse}")

    print("\nobjects")
    for i in range(3):
        put(f"sse-s3/object-{i}.bin", body(f"s3-{i}", 64 * 1024 * (i + 1)), "AES256", ServerSideEncryption="AES256")
    for i in range(2):
        put(f"sse-kms/object-{i}.bin", body(f"kms-{i}", 100 * 1024), "aws:kms", ServerSideEncryption="aws:kms")
    ssec_key = os.urandom(32)
    put("sse-c/object.bin", body("ssec", 50 * 1024), "SSE-C", **ssec_args(ssec_key))
    manifest["ssec_key"] = base64.b64encode(ssec_key).decode()
    put("plain/object.bin", body("plain", 40 * 1024), "none")

    # Bucket default encryption: an object uploaded with no encryption
    # headers is stored SSE-S3.
    s3.put_bucket_encryption(
        Bucket=bucket,
        ServerSideEncryptionConfiguration={"Rules": [{"ApplyServerSideEncryptionByDefault": {"SSEAlgorithm": "AES256"}}]},
    )
    put("default-rule/object.bin", body("default", 30 * 1024), "AES256")
    s3.delete_bucket_encryption(Bucket=bucket)

    # A multipart upload left in progress with SSE-S3; --verify finishes it.
    print("\nmultipart upload (left in progress)")
    part1 = body("mpu-1", 5 * MB)
    mpu = s3.create_multipart_upload(Bucket=bucket, Key="sse-s3/multipart.bin", ServerSideEncryption="AES256")
    if mpu.get("ServerSideEncryption") != "AES256":
        raise SystemExit(f"CreateMultipartUpload did not report AES256: {mpu}")
    r = s3.upload_part(Bucket=bucket, Key="sse-s3/multipart.bin", UploadId=mpu["UploadId"], PartNumber=1, Body=part1)
    manifest["upload"] = {"key": "sse-s3/multipart.bin", "id": mpu["UploadId"], "part1_etag": r["ETag"], "completed": False}
    print(f"  upload {mpu['UploadId']} with part 1 ({len(part1) / MB:.0f} MiB)")

    # An IAM user with an access key: the secret is stored wrapped.
    print("\nIAM user and access key")
    try:
        iam.create_user(UserName=user)
    except ClientError as e:
        if e.response["Error"]["Code"] != "EntityAlreadyExists":
            raise
    for k in iam.list_access_keys(UserName=user)["AccessKeyMetadata"]:
        iam.delete_access_key(UserName=user, AccessKeyId=k["AccessKeyId"])
    # Attach the built-in read-only policy (inline user policies are not
    # part of the IAM API OpenS3 serves).
    iam.attach_user_policy(UserName=user, PolicyArn="arn:aws:iam::aws:policy/readonly")
    ck = iam.create_access_key(UserName=user)["AccessKey"]
    manifest["access_key"] = {"id": ck["AccessKeyId"], "secret": ck["SecretAccessKey"]}
    print(f"  {user}: access key {ck['AccessKeyId']}")

    DATA_DIR.mkdir(parents=True, exist_ok=True)
    MANIFEST.write_text(json.dumps(manifest, indent=2) + "\n")
    os.chmod(MANIFEST, 0o600)
    print(f"\nwrote {MANIFEST}")
    print("Now: stop the server, `opens3 master rotate --root DIR`, start it, and run this script with --verify.")


# --------------------------------------------------------------------- verify


def verify(args) -> None:
    if not MANIFEST.exists():
        raise SystemExit(f"{MANIFEST} not found; run without --verify first")
    m = json.loads(MANIFEST.read_text())
    s3 = s3conf.client()
    bucket = m["bucket"]
    print(f"config: {s3conf.describe()}\nbucket: {bucket}")
    failures = 0

    def check(ok: bool, what: str) -> None:
        nonlocal failures
        print(("ok   " if ok else "FAIL ") + what)
        failures += not ok

    print("\nobjects")
    ssec_key = base64.b64decode(m["ssec_key"])
    for o in m["objects"]:
        extra = ssec_args(ssec_key) if o["sse"] == "SSE-C" else {}
        try:
            r = s3.get_object(Bucket=bucket, Key=o["key"], **extra)
            data = r["Body"].read()
        except ClientError as e:
            check(False, f"GET {o['key']}: {e.response['Error']['Code']} {e.response['Error'].get('Message', '')}")
            continue
        got = r.get("ServerSideEncryption") or ("SSE-C" if r.get("SSECustomerAlgorithm") else "none")
        check(len(data) == o["size"] and sha256(data) == o["sha256"] and got == o["sse"],
              f"GET {o['key']:<28} {len(data):>9} B  {got}  sha256 {'match' if sha256(data) == o['sha256'] else 'MISMATCH'}")

    # The SSE-C object must still be refused without its key.
    try:
        s3.get_object(Bucket=bucket, Key="sse-c/object.bin")
        check(False, "SSE-C object readable without the customer key")
    except ClientError as e:
        check(e.response["ResponseMetadata"]["HTTPStatusCode"] == 400, "SSE-C object refused without the customer key (400)")

    print("\nmultipart upload")
    up = m["upload"]
    if up["completed"]:
        r = s3.get_object(Bucket=bucket, Key=up["key"])
        data = r["Body"].read()
        check(sha256(data) == up["sha256"] and r.get("ServerSideEncryption") == "AES256", f"GET {up['key']} (completed on an earlier verify)")
    else:
        try:
            parts = s3.list_parts(Bucket=bucket, Key=up["key"], UploadId=up["id"]).get("Parts", [])
            check(len(parts) == 1 and parts[0]["ETag"] == up["part1_etag"], f"ListParts on upload {up['id']}: {len(parts)} part(s)")
            part2 = body("mpu-2", 1 * MB)
            r2 = s3.upload_part(Bucket=bucket, Key=up["key"], UploadId=up["id"], PartNumber=2, Body=part2)
            s3.complete_multipart_upload(
                Bucket=bucket, Key=up["key"], UploadId=up["id"],
                MultipartUpload={"Parts": [{"PartNumber": 1, "ETag": up["part1_etag"]}, {"PartNumber": 2, "ETag": r2["ETag"]}]},
            )
            expect = sha256(body("mpu-1", 5 * MB) + part2)
            r = s3.get_object(Bucket=bucket, Key=up["key"])
            data = r["Body"].read()
            ok = sha256(data) == expect and r.get("ServerSideEncryption") == "AES256"
            check(ok, f"completed upload {up['id']} after rotation; GET {len(data)} B, {r.get('ServerSideEncryption')}")
            if ok:
                up.update({"completed": True, "sha256": expect})
                MANIFEST.write_text(json.dumps(m, indent=2) + "\n")
        except ClientError as e:
            check(False, f"multipart upload {up['id']}: {e.response['Error']['Code']} {e.response['Error'].get('Message', '')}")

    print("\nIAM user's access key")
    ak = m["access_key"]
    try:
        r = s3_client_as(ak["id"], ak["secret"]).list_objects_v2(Bucket=bucket, MaxKeys=1)
        check(r["ResponseMetadata"]["HTTPStatusCode"] == 200, f"{ak['id']} signed a request as {m['user']}")
    except ClientError as e:
        check(False, f"{ak['id']}: {e.response['Error']['Code']} {e.response['Error'].get('Message', '')}")

    print()
    if failures:
        print(f"FAILED: {failures} check(s) failed")
        sys.exit(1)
    print("PASSED: every encrypted record reads back")


# -------------------------------------------------------------------- cleanup


def cleanup(args) -> None:
    s3 = s3conf.client()
    iam = iam_client()
    bucket = s3conf.bucket_name("encrypted")
    user = f"{s3conf.BUCKET_PREFIX}-rotation-user"
    try:
        for page in s3.get_paginator("list_multipart_uploads").paginate(Bucket=bucket):
            for u in page.get("Uploads", []):
                s3.abort_multipart_upload(Bucket=bucket, Key=u["Key"], UploadId=u["UploadId"])
        for page in s3.get_paginator("list_object_versions").paginate(Bucket=bucket):
            objs = [{"Key": v["Key"], "VersionId": v["VersionId"]} for v in page.get("Versions", []) + page.get("DeleteMarkers", [])]
            if objs:
                s3.delete_objects(Bucket=bucket, Delete={"Objects": objs, "Quiet": True})
        s3.delete_bucket(Bucket=bucket)
        print(f"deleted bucket {bucket}")
    except ClientError as e:
        if e.response["Error"]["Code"] != "NoSuchBucket":
            raise
        print(f"bucket {bucket} does not exist")
    try:
        for k in iam.list_access_keys(UserName=user)["AccessKeyMetadata"]:
            iam.delete_access_key(UserName=user, AccessKeyId=k["AccessKeyId"])
        for p in iam.list_attached_user_policies(UserName=user)["AttachedPolicies"]:
            iam.detach_user_policy(UserName=user, PolicyArn=p["PolicyArn"])
        iam.delete_user(UserName=user)
        print(f"deleted user {user}")
    except ClientError as e:
        if e.response["Error"]["Code"] != "NoSuchEntity":
            raise
        print(f"user {user} does not exist")
    if MANIFEST.exists():
        MANIFEST.unlink()
        print(f"removed {MANIFEST}")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    g = ap.add_mutually_exclusive_group()
    g.add_argument("--verify", action="store_true", help="read everything back and check it (after a rotation)")
    g.add_argument("--cleanup", action="store_true", help="delete the bucket, the user and the manifest")
    args = ap.parse_args()
    if args.verify:
        verify(args)
    elif args.cleanup:
        cleanup(args)
    else:
        create(args)


if __name__ == "__main__":
    try:
        main()
    except ClientError as e:
        print(f"S3 error: {e}", file=sys.stderr)
        sys.exit(1)
