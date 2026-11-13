#!/usr/bin/env python3
"""Write the Tauri updater manifest from the per-platform release fragments.

Each fragment is the JSON scripts/release-desktop.sh writes:
{"url": ..., "signature": ..., "platforms": [...]}. The signature is empty
when the release had no Tauri updater key. The manifest still announces the
version so Homebrew and Linux installs can show their upgrade command, and an
app with the public key refuses to install an unsigned archive.

Usage: scripts/write-desktop-updater-manifest.py VERSION DEST FRAGMENT...
"""

import json
import pathlib
import re
import sys
from datetime import datetime, timezone

EXPECTED = {"darwin-aarch64", "darwin-x86_64", "linux-x86_64"}
URL_PREFIX = "https://github.com/OrlojHQ/meridian/releases/download/v"


def main() -> None:
    if len(sys.argv) < 4:
        sys.exit(
            "usage: scripts/write-desktop-updater-manifest.py VERSION DEST FRAGMENT..."
        )
    version, dest, *fragments = sys.argv[1:]
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        sys.exit(f"VERSION must be MAJOR.MINOR.PATCH without a leading v: {version}")
    platforms: dict[str, dict[str, str]] = {}
    for path in fragments:
        fragment = json.loads(pathlib.Path(path).read_text(encoding="utf-8"))
        url = fragment["url"]
        if not url.startswith(f"{URL_PREFIX}{version}/"):
            sys.exit(f"{path}: url is not an asset of v{version}: {url}")
        for name in fragment["platforms"]:
            if name in platforms:
                sys.exit(f"{path}: platform {name} appears twice")
            platforms[name] = {"signature": fragment["signature"], "url": url}
    if set(platforms) != EXPECTED:
        sys.exit(f"manifest platforms {sorted(platforms)} != {sorted(EXPECTED)}")
    signed = {bool(entry["signature"]) for entry in platforms.values()}
    if len(signed) != 1:
        sys.exit("either every platform is signed or none is")
    document = {
        "version": version,
        "notes": "Meridian desktop update.",
        "pub_date": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "platforms": dict(sorted(platforms.items())),
    }
    pathlib.Path(dest).write_text(
        json.dumps(document, indent=2) + "\n", encoding="utf-8"
    )


if __name__ == "__main__":
    main()
