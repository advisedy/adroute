#!/usr/bin/env python3
"""Build standalone release assets and their checksums from the current checkout."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

root = Path(__file__).resolve().parent.parent
version = json.loads((root / "package.json").read_text())["version"]
destination = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else root / "dist" / f"v{version}"
destination.mkdir(parents=True, exist_ok=True)
assets = []
for system in ("linux", "windows"):
    for architecture in ("amd64", "arm64"):
        suffix = ".exe" if system == "windows" else ""
        target = destination / f"adroute_{version}_{system}_{architecture}{suffix}"
        subprocess.run(
            ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(target), "./cmd/adroute"],
            cwd=root,
            env={**os.environ, "GOOS": system, "GOARCH": architecture, "CGO_ENABLED": "0"},
            check=True,
        )
        assets.append(target)
for name in ("install.sh", "install.ps1", "install-release.sh", "install-release.ps1", "README.md"):
    target = destination / name
    shutil.copyfile(root / name, target)
    assets.append(target)
with (destination / "SHA256SUMS").open("w", newline="\n") as checksums:
    for asset in sorted(assets):
        checksums.write(f"{hashlib.sha256(asset.read_bytes()).hexdigest()}  {asset.name}\n")
print(f"Release v{version}: {destination}")
