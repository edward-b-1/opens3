#!/usr/bin/env python3
"""Run the whole cycle with uv: generate -> upload -> verify -> cleanup."""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
STEPS = ["generate_data.py", "upload_data.py", "verify_data.py", "cleanup.py"]


def main() -> None:
    steps = STEPS[:-1] if "--keep" in sys.argv else STEPS
    for script in steps:
        print(f"\n===== {script} =====")
        rc = subprocess.call(["uv", "run", str(HERE / script)], cwd=HERE)
        if rc != 0:
            print(f"{script} failed with exit code {rc}")
            sys.exit(rc)
    print("\nall steps passed")


if __name__ == "__main__":
    main()
