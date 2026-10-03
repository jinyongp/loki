#!/usr/bin/env python3
"""Choose a reviewed CI recipe by the runner's actual native target."""
import argparse
import os
from pathlib import Path
import platform

from prepare_candidate import prepare


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    target_os = {"Linux":"linux", "Windows":"windows", "Darwin":"darwin"}.get(platform.system())
    arch = {"x86_64":"amd64", "AMD64":"amd64", "aarch64":"arm64", "arm64":"arm64"}.get(platform.machine())
    mode = os.environ.get("CANDIDATE_MODE", "")
    if target_os is None or arch is None or mode not in ("project-host", "full"):
        raise ValueError("CI candidate requires an explicit supported native target/mode")
    name = f"candidate-{target_os}-{arch}-{mode}.json"
    recipe = args.inputs.resolve() / name
    if recipe.is_symlink() or not recipe.is_file():
        raise ValueError("reviewed native recipe is missing: "+name)
    prepare(recipe, args.output.resolve())
