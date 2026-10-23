"""Bind assembled publication metadata to exact files, then verify sealed bytes."""
import argparse
import copy
import os
from pathlib import Path
import re

from release_pipeline import read, write, digest

LOCK = "loki-release-lock.json"
SUMS = "SHA256SUMS"
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,199}\Z")


def record(path):
    if path.is_symlink() or not path.is_file() or not NAME.fullmatch(path.name):
        raise ValueError("publication must contain named regular files")
    return {"bytes": path.stat().st_size, "sha256": digest(path)}


def bindings(lock, assets):
    result = {}
    for key, unit in lock["units"].items():
        if unit["kind"] == "image":
            continue
        artifact = unit["artifact"]
        name = Path(artifact["url"]).name
        result[key] = dict(url=artifact["url"], bytes=artifact["bytes"], sha256=artifact["sha256"], present=name in assets)
        if name in assets and assets[name] != {"bytes": artifact["bytes"], "sha256": artifact["sha256"]}:
            raise ValueError("publication asset differs from unit receipt")
    return result


def create(plan, publication):
    assets = {p.name: record(p) for p in (publication/"assets").iterdir()}
    lock = read(publication/"assets"/LOCK)
    inventory = {"schema": 1, "phase": "assembled", "source_commit": plan["source_commit"],
                 "release": plan["release"], "run_id": os.environ.get("GITHUB_RUN_ID", "local"),
                 "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT", "1"),
                 "assembled_lock_sha256": assets[LOCK]["sha256"], "assets": assets,
                 "units": bindings(lock, assets)}
    validate(plan, publication, inventory)
    write(publication/"release-inventory.json", inventory)
    return inventory


def cohort(inventory, source, release, phase):
    attempt = str(inventory.get("run_attempt", ""))
    if (inventory.get("schema") != 1 or inventory.get("phase") != phase or
        inventory.get("source_commit") != source or inventory.get("release") != release or
        inventory.get("run_id") != os.environ.get("GITHUB_RUN_ID", "local") or
        not attempt.isdigit() or not 1 <= int(attempt) <= int(os.environ.get("GITHUB_RUN_ATTEMPT", "1"))):
        raise ValueError("publication inventory belongs to another cohort")
    assets = inventory.get("assets")
    if not isinstance(assets, dict) or not 1 <= len(assets) <= 1000 or LOCK not in assets:
        raise ValueError("invalid publication inventory file set")
    for name, item in assets.items():
        if (not NAME.fullmatch(name) or not isinstance(item, dict) or set(item) != {"bytes", "sha256"} or
            type(item["bytes"]) is not int or not 0 <= item["bytes"] <= 8*1024**3 or
            not isinstance(item["sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", item["sha256"])):
            raise ValueError("invalid publication inventory entry")


def validate(plan, publication, inventory):
    cohort(inventory, plan["source_commit"], plan["release"], "assembled")
    lock_path = publication/"assets"/LOCK
    lock = read(lock_path)
    if (lock["source_commit"] != plan["source_commit"] or lock["release"] != plan["release"] or
        record(lock_path) != inventory["assets"][LOCK] or
        digest(lock_path) != inventory["assembled_lock_sha256"] or
        bindings(lock, inventory["assets"]) != inventory["units"]):
        raise ValueError("assembled inventory and lock differ")
    if plan.get("publication_recovery"):
        expected = {Path(a["url"]).name: {"bytes": a["bytes"], "sha256": a["sha256"]} for a in plan["publication_recovery"]}
        if expected != inventory["assets"] or SUMS not in expected:
            raise ValueError("recovery differs from immutable public asset inventory")
    else:
        if SUMS in inventory["assets"]:
            raise ValueError("unsealed publication already has checksums")
        for key, chosen in plan["units"].items():
            if chosen["kind"] != "image" and (chosen["action"] == "build" or chosen["kind"] == "manager"):
                if not inventory["units"][key]["present"]:
                    raise ValueError("selected local publication archive is missing")


def seal(inventory, publication):
    final = copy.deepcopy(inventory)
    final["phase"] = "sealed"
    for name in (LOCK, SUMS):
        final["assets"][name] = record(publication/"assets"/name)
    write(publication/"release-inventory.json", final)
    return final


def verify(publication, inventory, source, release):
    cohort(inventory, source, release, "sealed")
    if SUMS not in inventory["assets"]:
        raise ValueError("sealed inventory lacks checksums")
    assets = publication/"assets"
    if assets.is_symlink() or not assets.is_dir():
        raise ValueError("publication assets must be a regular directory")
    paths = list(assets.iterdir())
    if {p.name for p in paths} != set(inventory["assets"]):
        raise ValueError("publication file set differs from sealed inventory")
    for path in paths:
        if record(path) != inventory["assets"][path.name]:
            raise ValueError("publication bytes differ from sealed inventory: "+path.name)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--publication", type=Path, required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--release", required=True)
    args = parser.parse_args()
    verify(args.publication, read(args.publication/"release-inventory.json"), args.source, args.release)
