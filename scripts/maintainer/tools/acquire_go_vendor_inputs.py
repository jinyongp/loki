#!/usr/bin/env python3
"""Acquire receipt-bound exact-commit Go vendor sources; execute no product."""
import argparse
from input_cache import restore, save, size_digest
import hashlib
import json
from pathlib import Path
from urllib.request import urlopen

from build_go_vendor import OWNERS


def acquire(recipe_path, output):
    recipe = json.loads(recipe_path.read_text(encoding="utf-8"))
    source = recipe.get("source", {})
    owner = OWNERS.get(recipe.get("owner"))
    if recipe.get("schema") != 1 or owner is None or any(source.get(key) != owner[key] for key in ("version", "commit")) or source.get("url") != owner["source"] or not isinstance(source.get("bytes"), int) or not 0 < source["bytes"] <= 128 << 20:
        raise ValueError("source acquisition requires its finite exact-commit receipt")
    output.mkdir(parents=True, exist_ok=False)
    archive = output / "source.tar.gz"
    size, checksum = 0, hashlib.sha256()
    print("Acquiring pinned "+recipe["owner"]+" source...", flush=True)
    if not restore(archive, source):
        with urlopen(source["url"], timeout=30) as response, archive.open("xb") as sink:
            if not response.url.startswith("https://codeload.github.com/"):
                raise ValueError("source redirect leaves its reviewed primary host")
            while chunk := response.read(1024*1024):
                size += len(chunk)
                if size > source["bytes"]:
                    raise ValueError("source archive exceeded its trusted length")
                sink.write(chunk)
                checksum.update(chunk)
    size, checksum = size_digest(archive)
    if size != source["bytes"] or checksum.hexdigest() != source.get("sha256"):
        raise ValueError("source archive differs from its independent commit receipt")
    save(archive, source)
    source["archive"] = archive.name
    (output / "vendor-inputs.json").write_text(json.dumps(recipe, indent=2)+"\n", encoding="utf-8")
    print(output / "vendor-inputs.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--trust", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    acquire(args.trust.resolve(), args.output.resolve())
