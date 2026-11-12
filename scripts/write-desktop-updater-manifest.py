#!/usr/bin/env python3
"""Write the Tauri updater manifest for one signed macOS desktop archive.

The same universal archive is published for Apple silicon and Intel.
Usage: scripts/write-desktop-updater-manifest.py VERSION SIG_FILE URL DEST
"""

import json
import pathlib
import sys
from datetime import datetime, timezone


def main() -> None:
    if len(sys.argv) != 5:
        sys.exit(
            "usage: scripts/write-desktop-updater-manifest.py VERSION SIG_FILE URL DEST"
        )
    version, sig_path, url, dest = sys.argv[1:]
    signature = pathlib.Path(sig_path).read_text(encoding="utf-8").strip()
    if not signature:
        sys.exit(f"signature file is empty: {sig_path}")
    platform = {"signature": signature, "url": url}
    document = {
        "version": version,
        "notes": "Meridian desktop update.",
        "pub_date": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "platforms": {
            "darwin-aarch64": platform,
            "darwin-x86_64": platform,
        },
    }
    pathlib.Path(dest).write_text(
        json.dumps(document, indent=2) + "\n", encoding="utf-8"
    )


if __name__ == "__main__":
    main()
