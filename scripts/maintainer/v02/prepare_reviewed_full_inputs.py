#!/usr/bin/env python3
"""Retain reviewed full input recipes for acquisition on each native runner."""
import argparse
import json
from pathlib import Path
import shutil


def prepare(output):
    output.mkdir(parents=True, exist_ok=False)
    source = Path(__file__).resolve().parents[3] / "packaging/v02/inputs"
    included = set()
    for arch in ("amd64", "arm64"):
        name = "candidate-linux-"+arch+"-full.json"
        candidate = json.loads((source / name).read_text(encoding="utf-8"))
        included.add(name)
        included.add(candidate["images"])
        included.add(candidate["browser"]["inputs"])
        included.update(module["inputs"] for module in candidate["modules"].values())
        included.update(specification["inputs"] for specification in candidate["native_sources"].values())
    for name in sorted(included):
        if Path(name).name != name or not (source / name).is_file():
            raise ValueError("full recipe reference must be an owned source input")
        shutil.copyfile(source / name, output / name)
    (output / "input-state.json").write_text(json.dumps({"schema":1, "release":"0.2.0", "scope":"reviewed source receipts/package selections", "package_verification":"Native runner authenticates Ubuntu InRelease before acquiring each package closure", "product_executed":False, "accepted":False, "published":False})+"\n", encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    prepare(args.output.resolve())
