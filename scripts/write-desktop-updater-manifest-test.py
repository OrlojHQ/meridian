#!/usr/bin/env python3
"""Check the updater manifest writer without a signing key."""

import json
import subprocess
import tempfile
from pathlib import Path


def main() -> None:
    root = Path(__file__).resolve().parents[1]
    script = root / "scripts" / "write-desktop-updater-manifest.py"
    with tempfile.TemporaryDirectory() as tmp:
        directory = Path(tmp)
        signature = directory / "app.sig"
        dest = directory / "latest.json"
        signature.write_text("c2lnbmF0dXJl\n", encoding="utf-8")
        subprocess.check_call(
            [
                "python3",
                str(script),
                "1.2.3",
                str(signature),
                "https://example.test/app.tar.gz",
                str(dest),
            ]
        )
        document = json.loads(dest.read_text(encoding="utf-8"))
    if document["version"] != "1.2.3":
        raise SystemExit(document)
    for name in ("darwin-aarch64", "darwin-x86_64"):
        platform = document["platforms"][name]
        if platform["signature"] != "c2lnbmF0dXJl":
            raise SystemExit(platform)
        if platform["url"] != "https://example.test/app.tar.gz":
            raise SystemExit(platform)


if __name__ == "__main__":
    main()
