#!/usr/bin/env python3
"""Acquire native full-module source inputs from reviewed integrity receipts.

This step downloads inputs only. Their checksums come from the trusted recipe,
never from the acquired archives. It does not build or accept product artifacts.
"""
import argparse
import hashlib
import json
from pathlib import Path
import platform
import time
from urllib.parse import urlsplit
from urllib.request import urlopen

from build_full_bundle import validate_recipe


def acquire(recipe_path, output):
    arch = {"x86_64":"amd64", "aarch64":"arm64", "arm64":"arm64"}.get(platform.machine())
    if platform.system() != "Linux" or arch is None:
        raise ValueError("full module inputs require their exact native Linux host")
    target = {"os":"linux", "arch":arch, "mode":"full"}
    recipe = validate_recipe(json.loads(recipe_path.read_text(encoding="utf-8")), target, payload_only=True)
    output.mkdir(parents=True, exist_ok=False)
    for index, asset in enumerate(recipe.get("inputs", [])):
        suffix = "".join(Path(urlsplit(asset["url"]).path).suffixes)
        if suffix not in (".tar.gz", ".tar.xz", ".zip", ".tgz"):
            raise ValueError("native input requires a supported archive format")
        name = f"input-{index}"+suffix
        partial, destination = output / (name+".part"), output / name
        size, checksum = 0, hashlib.sha256()
        started = reported = time.monotonic()
        print(f"Acquiring {recipe['module']} input {index+1} ({asset['version']})...", flush=True)
        try:
            with urlopen(asset["url"], timeout=30) as response, partial.open("xb") as sink:
                redirect = urlsplit(response.url)
                if redirect.scheme != "https" or redirect.username or redirect.fragment:
                    raise ValueError("native input redirect weakened its acquisition contract")
                length = response.headers.get("Content-Length")
                if length is not None and int(length) != asset["bytes"]:
                    raise ValueError("native input response differs from trusted byte length")
                while chunk := response.read(1024*1024):
                    size += len(chunk)
                    if size > asset["bytes"]:
                        raise ValueError("native input exceeded trusted byte length")
                    sink.write(chunk)
                    checksum.update(chunk)
                    now = time.monotonic()
                    if now-reported >= 10:
                        print(f"Still acquiring input {index+1}: {size}/{asset['bytes']} bytes ({int(now-started)}s elapsed)...", flush=True)
                        reported = now
            if size != asset["bytes"] or checksum.hexdigest() != asset["sha256"]:
                raise ValueError("native input differs from its independently trusted receipt")
            partial.rename(destination)
            asset["archive"] = name
        finally:
            partial.unlink(missing_ok=True)
    (output / "full-inputs.json").write_text(json.dumps(recipe, indent=2, sort_keys=True)+"\n", encoding="utf-8")
    print(output / "full-inputs.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--trust", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    acquire(args.trust.resolve(), args.output.resolve())
