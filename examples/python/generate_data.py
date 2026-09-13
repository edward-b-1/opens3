#!/usr/bin/env python3
"""Generate a deterministic set of test files under ./generated.

Produces a mix of shapes that exercise different server paths:
  * tiny and empty objects
  * text, JSON and CSV documents
  * medium binary blobs
  * a couple of large blobs that trigger multipart upload
  * a nested "tree" of small files to test prefixes / delimiters

Re-running with the same seed produces identical content, so uploads and
verification are reproducible. A manifest.json with sizes and SHA-256
digests is written alongside the files.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import random
import shutil
from pathlib import Path

from s3conf import DATA_DIR

KB = 1024
MB = 1024 * KB


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 * MB), b""):
            h.update(chunk)
    return h.hexdigest()


def write_random(path: Path, size: int, rng: random.Random) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("wb") as f:
        remaining = size
        while remaining > 0:
            n = min(remaining, 1 * MB)
            f.write(rng.randbytes(n))
            remaining -= n


def write_text(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def generate(out: Path, seed: int, large_mb: int) -> dict:
    rng = random.Random(seed)
    if out.exists():
        shutil.rmtree(out)
    out.mkdir(parents=True)

    files: list[Path] = []

    # Edge cases.
    write_text(out / "empty.txt", "")
    write_text(out / "tiny.txt", "hello opens3\n")
    write_text(out / "unicode-ключ-鍵.txt", "unicode filename and content: ключ 鍵 ✓\n")
    write_text(out / "with spaces and +plus.txt", "keys with spaces and plus signs\n")
    files += [
        out / "empty.txt",
        out / "tiny.txt",
        out / "unicode-ключ-鍵.txt",
        out / "with spaces and +plus.txt",
    ]

    # Structured documents.
    records = [
        {"id": i, "name": f"item-{i}", "score": round(rng.uniform(0, 100), 2), "tags": rng.sample(["a", "b", "c", "d"], 2)}
        for i in range(500)
    ]
    write_text(out / "docs" / "records.json", json.dumps(records, indent=2))
    csv_lines = ["id,name,score"] + [f"{r['id']},{r['name']},{r['score']}" for r in records]
    write_text(out / "docs" / "records.csv", "\n".join(csv_lines) + "\n")
    write_text(out / "docs" / "lorem.txt", ("lorem ipsum dolor sit amet " * 4000) + "\n")
    files += [out / "docs" / "records.json", out / "docs" / "records.csv", out / "docs" / "lorem.txt"]

    # Binary blobs of assorted sizes.
    for name, size in [
        ("blob-1k.bin", 1 * KB),
        ("blob-64k.bin", 64 * KB),
        ("blob-1m.bin", 1 * MB),
        ("blob-5m.bin", 5 * MB),
    ]:
        write_random(out / "bin" / name, size, rng)
        files.append(out / "bin" / name)

    # Large blobs (multipart territory: boto3 default threshold is 8 MiB).
    for i in range(2):
        p = out / "large" / f"large-{i}-{large_mb}m.bin"
        write_random(p, large_mb * MB, rng)
        files.append(p)

    # Nested tree for prefix / delimiter listing tests.
    for year in (2024, 2025):
        for month in (1, 2, 3):
            for day in (1, 15):
                p = out / "tree" / f"year={year}" / f"month={month:02d}" / f"day={day:02d}" / "part-0000.txt"
                write_text(p, f"{year}-{month:02d}-{day:02d} sample row {rng.randint(0, 1_000_000)}\n")
                files.append(p)

    manifest = {
        "seed": seed,
        "files": [
            {
                "path": str(p.relative_to(out)),
                "size": p.stat().st_size,
                "sha256": sha256_of(p),
            }
            for p in files
        ],
    }
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2, ensure_ascii=False))
    return manifest


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", type=Path, default=DATA_DIR, help="output directory (default: ./generated)")
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--large-mb", type=int, default=20, help="size of each large blob in MiB (default 20)")
    args = ap.parse_args()

    manifest = generate(args.out, args.seed, args.large_mb)
    total = sum(f["size"] for f in manifest["files"])
    print(f"generated {len(manifest['files'])} files, {total / MB:.1f} MiB total, in {args.out}")
    for f in manifest["files"]:
        print(f"  {f['size']:>12}  {f['path']}")


if __name__ == "__main__":
    main()
