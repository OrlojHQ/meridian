#!/usr/bin/env python3
"""Check the updater manifest writer with and without a signing key."""

import json
import subprocess
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "write-desktop-updater-manifest.py"
BASE = "https://github.com/OrlojHQ/meridian/releases/download/v1.2.3"
MAC_URL = f"{BASE}/meridian-desktop_1.2.3_macOS_universal.app.tar.gz"
DEB_URL = f"{BASE}/meridian-desktop_1.2.3_linux_amd64.deb"


def write(directory: Path, name: str, document: dict) -> Path:
    path = directory / name
    path.write_text(json.dumps(document), encoding="utf-8")
    return path


def run(directory: Path, mac_sig: str, deb_sig: str, mac_url: str = MAC_URL):
    mac = write(
        directory,
        "updater-macos.json",
        {"url": mac_url, "signature": mac_sig, "platforms": ["darwin-aarch64", "darwin-x86_64"]},
    )
    linux = write(
        directory,
        "updater-linux.json",
        {"url": DEB_URL, "signature": deb_sig, "platforms": ["linux-x86_64"]},
    )
    dest = directory / "latest.json"
    result = subprocess.run(
        ["python3", str(SCRIPT), "1.2.3", str(dest), str(mac), str(linux)],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        return None
    return json.loads(dest.read_text(encoding="utf-8"))


def main() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        directory = Path(tmp)
        signed = run(directory, "bWFj", "ZGVi")
        if signed is None or signed["version"] != "1.2.3":
            raise SystemExit(signed)
        for name in ("darwin-aarch64", "darwin-x86_64"):
            if signed["platforms"][name] != {"signature": "bWFj", "url": MAC_URL}:
                raise SystemExit(signed["platforms"][name])
        if signed["platforms"]["linux-x86_64"] != {"signature": "ZGVi", "url": DEB_URL}:
            raise SystemExit(signed["platforms"]["linux-x86_64"])

        unsigned = run(directory, "", "")
        if unsigned is None or set(unsigned["platforms"]) != {
            "darwin-aarch64",
            "darwin-x86_64",
            "linux-x86_64",
        }:
            raise SystemExit(unsigned)

        if run(directory, "bWFj", "") is not None:
            raise SystemExit("a half-signed manifest was written")
        if run(directory, "", "", mac_url="https://example.test/app.tar.gz") is not None:
            raise SystemExit("a foreign url was accepted")


if __name__ == "__main__":
    main()
