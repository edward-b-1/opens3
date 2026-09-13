"""Shared configuration for the OpenS3 Python test scripts.

Reads connection settings from a `.env` file next to this module (or from
the environment, which takes precedence) and builds boto3 clients that talk
to the OpenS3 endpoint with path-style addressing.
"""

from __future__ import annotations

import os
from pathlib import Path

import boto3
from botocore.config import Config

HERE = Path(__file__).resolve().parent
ENV_FILE = HERE / ".env"
DATA_DIR = HERE / "generated"
DOWNLOAD_DIR = HERE / "downloads"


def load_env(path: Path = ENV_FILE) -> None:
    """Minimal .env loader: KEY=VALUE lines, '#' comments, no interpolation.

    Values already present in the process environment are not overridden so
    that `OPENS3_ENDPOINT=... python upload_data.py` works as expected.
    """
    if not path.exists():
        return
    for raw in path.read_text().splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip().strip('"').strip("'")
        os.environ.setdefault(key, value)


load_env()

ENDPOINT = os.environ.get("OPENS3_ENDPOINT", "http://localhost:9000")
ACCESS_KEY = os.environ.get("OPENS3_ACCESS_KEY", "root")
SECRET_KEY = os.environ.get("OPENS3_SECRET_KEY", "password")
REGION = os.environ.get("OPENS3_REGION", "us-east-1")
BUCKET_PREFIX = os.environ.get("OPENS3_BUCKET_PREFIX", "pyclient-test")


def _tls_verify(value: str):
    """'true' -> verify with system CAs, 'false' -> no verification (self-signed
    dev certs), anything else -> path to a CA bundle / server certificate."""
    v = value.strip()
    if v.lower() in ("", "1", "true", "yes"):
        return True
    if v.lower() in ("0", "false", "no"):
        return False
    v = os.path.expanduser(v)
    return v if os.path.isabs(v) else str((HERE / v).resolve())


TLS_VERIFY = _tls_verify(os.environ.get("OPENS3_TLS_VERIFY", "true"))

if TLS_VERIFY is False:
    import urllib3

    urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)


def session() -> boto3.session.Session:
    return boto3.session.Session(
        aws_access_key_id=ACCESS_KEY,
        aws_secret_access_key=SECRET_KEY,
        region_name=REGION,
    )


def client():
    """Low-level S3 client (used for bucket ops, listing, multipart)."""
    return session().client(
        "s3",
        endpoint_url=ENDPOINT,
        verify=TLS_VERIFY,
        config=Config(
            s3={"addressing_style": "path"},
            retries={"max_attempts": 3, "mode": "standard"},
        ),
    )


def resource():
    """High-level S3 resource (used for transfer-manager uploads)."""
    return session().resource(
        "s3",
        endpoint_url=ENDPOINT,
        verify=TLS_VERIFY,
        config=Config(s3={"addressing_style": "path"}),
    )


def bucket_name(suffix: str) -> str:
    return f"{BUCKET_PREFIX}-{suffix}"


def describe() -> str:
    return (
        f"endpoint={ENDPOINT} access_key={ACCESS_KEY} region={REGION} "
        f"bucket_prefix={BUCKET_PREFIX} tls_verify={TLS_VERIFY}"
    )
