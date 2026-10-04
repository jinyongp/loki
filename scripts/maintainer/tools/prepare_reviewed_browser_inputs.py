#!/usr/bin/env python3
"""Acquire all declared project-browser inputs without native execution."""
import argparse
from release_config import RELEASE, asset_url
import json
from pathlib import Path

from acquire_browser_inputs import PLATFORMS, acquire


def prepare(output):
    output.mkdir(parents=True, exist_ok=False)
    repo = Path(__file__).resolve().parents[3]
    for system, arch in PLATFORMS:
        target = {"os":system, "arch":arch, "mode":"project-host"}
        name = system+"-"+arch
        trust = repo / "packaging/tools/inputs" / ("browser-"+name+"-project-host.json")
        acquire(trust, output / name, "project-host", target)
        recipe = {"schema":1, "release":RELEASE, "target":target, "browser":{"inputs":name+"/browser-inputs.json", "url":asset_url("browser", target)}}
        (output / ("candidate-"+name+"-project-host.json")).write_text(json.dumps(recipe, indent=2)+"\n", encoding="utf-8")
    (output / "input-state.json").write_text(json.dumps({"schema":1, "release":RELEASE, "scope":"reviewed source inputs", "product_executed":False, "accepted":False, "published":False})+"\n", encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    prepare(args.output.resolve())
