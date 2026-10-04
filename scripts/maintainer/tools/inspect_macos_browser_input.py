#!/usr/bin/env python3
"""Inspect pinned macOS browser input signatures without launching a browser."""
import argparse
import json
from pathlib import Path
import platform
import subprocess
import tempfile

from build_browser_bundle import contained, digest, relative, unpack


def inspect(inputs):
    if platform.system() != "Darwin":
        raise ValueError("signature input inspection requires native macOS")
    arch = {"x86_64":"amd64", "arm64":"arm64"}[platform.machine().lower()]
    recipe = inputs / ("darwin-"+arch) / "browser-inputs.json"
    data = json.loads(recipe.read_text(encoding="utf-8"))
    asset = data["assets"]["chrome"]
    archive = recipe.parent / asset["archive"]
    if archive.stat().st_size != asset["bytes"] or digest(archive) != asset["sha256"]:
        raise ValueError("browser input differs from reviewed receipt")
    with tempfile.TemporaryDirectory(prefix="loki-macos-input-") as temporary:
        root = Path(temporary) / "unpacked"
        unpack(archive, root)
        source = contained(root, root / relative(asset["root"]))
        binary = contained(source, source / relative(asset["executable"]))
        app = next(p for p in binary.parents if p.suffix == ".app")
        observations = []
        for target in (app, binary):
            for flags in (("--display", "--verbose=4"), ("--verify", "--strict", "--ignore-resources")):
                command = ["/usr/bin/codesign", *flags, str(target)]
                result = subprocess.run(command, capture_output=True, text=True, encoding="utf-8", timeout=30)
                observations.append({"target":target.relative_to(source).as_posix(), "flags":flags, "exit_code":result.returncode, "diagnostics":result.stdout+result.stderr})
        print(json.dumps({"arch":arch, "upstream_sha256":asset["sha256"], "browser_executed":False, "observations":observations}, indent=2), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    args = parser.parse_args()
    inspect(args.inputs.resolve())
