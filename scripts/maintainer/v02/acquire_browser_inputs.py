#!/usr/bin/env python3
"""Acquire pinned native vendor inputs from an independently trusted recipe.

This step downloads source inputs only. It neither builds a product candidate
nor runs product checks. Review the recipe's checksums/provenance independently;
the downloaded bytes never supply their own trust metadata.
"""

import argparse
import hashlib
import json
from pathlib import Path
import platform
import re
import time
from urllib.parse import urlsplit
from urllib.request import urlopen

VERSIONS = {"node": "22.22.2", "chrome": "154.0.8037.92", "ffmpeg": "1011"}
PLATFORMS = {
    ("linux", "amd64"): ("linux-x64", "tar.xz", "linux64", "linux"),
    ("linux", "arm64"): ("linux-arm64", "tar.xz", "linux-arm64", "linux-arm64"),
    ("darwin", "amd64"): ("darwin-x64", "tar.gz", "mac-x64", "mac"),
    ("darwin", "arm64"): ("darwin-arm64", "tar.gz", "mac-arm64", "mac-arm64"),
    ("windows", "amd64"): ("win-x64", "zip", "win64", "win64"),
}


def native_target(mode):
    target_os = {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}.get(platform.system())
    arch = {"amd64": "amd64", "x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower())
    if (target_os, arch) not in PLATFORMS or mode not in ("project-host", "full") or mode == "full" and target_os != "linux":
        raise ValueError("no pinned browser acquisition contract for this native target/runtime mode")
    return {"os": target_os, "arch": arch, "mode": mode}


def urls(target):
    node_platform, node_format, chrome_platform, ffmpeg_platform = PLATFORMS[(target["os"], target["arch"])]
    return {
        "node": f"https://nodejs.org/dist/v22.22.2/node-v22.22.2-{node_platform}.{node_format}",
        "chrome": f"https://storage.googleapis.com/chrome-for-testing-public/154.0.8037.92/{chrome_platform}/chrome-{chrome_platform}.zip",
        "ffmpeg": f"https://cdn.playwright.dev/dbazure/download/playwright/builds/ffmpeg/1011/ffmpeg-{ffmpeg_platform}.zip",
    }


def trusted_inputs(recipe, target):
    if recipe.get("schema") != 1 or recipe.get("target") != target or not isinstance(recipe.get("native_requirements"), list):
        raise ValueError("trusted recipe must identify this exact native target and reviewed host requirements")
    expected = urls(target)
    if set(recipe.get("assets", {})) != set(VERSIONS):
        raise ValueError("trusted recipe requires exactly Node, Chrome and FFmpeg")
    for name, version in VERSIONS.items():
        asset = recipe["assets"][name]
        if asset.get("version") != version or asset.get("url") != expected[name]:
            raise ValueError(f"{name} receipt differs from the pinned primary acquisition location")
        if not isinstance(asset.get("bytes"), int) or not 0 < asset["bytes"] <= 8 << 30 or not re.fullmatch(r"[a-f0-9]{64}", asset.get("sha256", "")):
            raise ValueError(f"{name} requires independently trusted length and SHA-256")
        if not asset.get("root") or not asset.get("notices"):
            raise ValueError(f"{name} receipt requires its archive root and retained notices")
    return recipe


def acquire(recipe_path, output, mode, selected_target=None):
    target = native_target(mode) if selected_target is None else selected_target
    if (target.get("os"), target.get("arch")) not in PLATFORMS or target.get("mode") != mode or mode == "full" and target["os"] != "linux":
        raise ValueError("no pinned browser input contract for the selected target/runtime mode")
    recipe = trusted_inputs(json.loads(recipe_path.read_text(encoding="utf-8")), target)
    output.mkdir(parents=True, exist_ok=False)
    for name in VERSIONS:
        asset = recipe["assets"][name]
        filename = Path(urlsplit(asset["url"]).path).name
        temporary = output / (filename + ".part")
        destination = output / filename
        size, digest = 0, hashlib.sha256()
        started = reported = time.monotonic()
        print(f"Acquiring pinned {name} {asset['version']}...", flush=True)
        try:
            with urlopen(asset["url"], timeout=30) as response, temporary.open("xb") as sink:
                if urlsplit(response.url).scheme != "https":
                    raise ValueError("vendor redirect weakened HTTPS transport")
                if response.headers.get("Content-Length") is not None and int(response.headers["Content-Length"]) != asset["bytes"]:
                    raise ValueError(f"{name} response length differs from trusted metadata")
                while chunk := response.read(1024 * 1024):
                    size += len(chunk)
                    if size > asset["bytes"]:
                        raise ValueError(f"{name} download exceeded trusted length")
                    sink.write(chunk)
                    digest.update(chunk)
                    now = time.monotonic()
                    if now - reported >= 10:
                        print(f"Still acquiring {name}: {size}/{asset['bytes']} bytes ({int(now-started)}s elapsed)...", flush=True)
                        reported = now
            if size != asset["bytes"] or digest.hexdigest() != asset["sha256"]:
                raise ValueError(f"{name} archive differs from trusted length/checksum")
            temporary.rename(destination)
            asset["archive"] = filename
        finally:
            temporary.unlink(missing_ok=True)
    (output / "browser-inputs.json").write_text(json.dumps(recipe, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(output / "browser-inputs.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--trust", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--mode", choices=("project-host", "full"), default="project-host")
    parser.add_argument("--target", choices=[system+"/"+arch for system, arch in PLATFORMS], help="acquire another target's bytes only; assembly remains native")
    args = parser.parse_args()
    selected = None
    if args.target:
        system, arch = args.target.split("/")
        selected = {"os":system, "arch":arch, "mode":args.mode}
    acquire(args.trust.resolve(), args.output.resolve(), args.mode, selected)
