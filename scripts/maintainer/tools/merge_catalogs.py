#!/usr/bin/env python3
"""Assemble a receipt-bound release catalog from local module candidates.

Candidate assembly checks archive identity and prerequisite completeness; it
does not run product acceptance, upload archives or publish container images.
"""
import argparse
import json
from pathlib import Path
import re
import shutil
from urllib.parse import urlsplit
import zipfile

from build_browser_bundle import digest

LIMIT = 1 << 20
ID = re.compile(r"[a-z][a-z0-9]*(?:-[a-z0-9]+)*")


def document(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > LIMIT:
        raise ValueError("catalog input must be a bounded regular file")
    return json.loads(path.read_text(encoding="utf-8"))


def target_key(target):
    if set(target) != {"os", "arch", "mode"} or target["os"] not in ("linux", "windows", "darwin") or target["arch"] not in ("amd64", "arm64") or target["mode"] not in ("project-host", "full"):
        raise ValueError("invalid artifact target")
    return tuple(target[key] for key in ("os", "arch", "mode"))


def merge(inputs, output):
    manifests, artifacts, receipts = {}, {}, []
    for path in inputs:
        catalog = document(path)
        if set(catalog) != {"schema", "release", "modules", "artifacts"} or catalog["schema"] != 1 or catalog["release"] != "0.2.2":
            raise ValueError("candidate catalog must identify the exact 0.2.2 release")
        local_manifests = {}
        for manifest in catalog["modules"]:
            owner = manifest.get("id", "")
            if not ID.fullmatch(owner) or manifest.get("schema") != 1 or manifest.get("release") != "0.2.2" or set(manifest) - {"schema", "id", "release", "targets", "requires", "tools", "capabilities"}:
                raise ValueError("candidate has an invalid module manifest")
            if owner in local_manifests:
                raise ValueError("input catalog repeats a module owner")
            targets = {target_key(target):target for target in manifest["targets"]}
            if len(targets) != len(manifest["targets"]) or not targets:
                raise ValueError("module targets are empty or repeated")
            base = {key:value for key, value in manifest.items() if key != "targets"}
            if owner in manifests:
                previous, all_targets = manifests[owner]
                if previous != base:
                    raise ValueError("native catalogs disagree on their module contract")
                all_targets.update(targets)
            else:
                manifests[owner] = (base, dict(targets))
            local_manifests[owner] = manifest
        for artifact in catalog["artifacts"]:
            if set(artifact) != {"module", "release", "target", "url", "sha256", "bytes", "format"} or artifact["release"] != "0.2.2" or artifact["format"] != "zip":
                raise ValueError("candidate artifact contract differs")
            owner = artifact["module"]
            target = target_key(artifact["target"])
            key = (owner, *target)
            if owner not in local_manifests or artifact["target"] not in local_manifests[owner]["targets"] or key in artifacts:
                raise ValueError("artifact has no matching manifest or duplicates a native candidate")
            url = urlsplit(artifact["url"])
            if url.scheme != "https" or not url.hostname or url.username or url.fragment:
                raise ValueError("artifact acquisition must use public HTTPS without credentials")
            name = Path(url.path).name
            archive = path.parent / name
            if not name or archive.is_symlink() or not archive.is_file() or not isinstance(artifact["bytes"], int) or not 0 < artifact["bytes"] <= 16 << 30 or archive.stat().st_size != artifact["bytes"] or not re.fullmatch(r"[a-f0-9]{64}", artifact["sha256"]) or digest(archive) != artifact["sha256"]:
                raise ValueError("local candidate differs from its trusted artifact identity")
            with zipfile.ZipFile(archive) as packed:
                names = packed.namelist()
                if len(names) != len(set(names)) or names.count("module.json") != 1 or packed.getinfo("module.json").file_size > LIMIT:
                    raise ValueError("candidate archive has ambiguous module ownership")
                if json.loads(packed.read("module.json")) != local_manifests[owner]:
                    raise ValueError("archive manifest differs from its catalog")
            artifacts[key] = artifact
            receipts.append({"module":owner, "target":artifact["target"], "archive":str(archive.resolve()), "sha256":artifact["sha256"], "bytes":artifact["bytes"]})
    visiting, visited = set(), set()
    def visit(owner):
        if owner in visiting or owner not in manifests:
            raise ValueError("candidate prerequisite is missing or cyclic")
        if owner in visited:
            return
        visiting.add(owner)
        for dependency in manifests[owner][0].get("requires", []):
            visit(dependency)
        visiting.remove(owner)
        visited.add(owner)
    for owner in manifests:
        visit(owner)
    # Every supported native target must have a complete installable closure.
    # A target is not admitted merely because its manifest came from source.
    for owner, (manifest, targets) in manifests.items():
        for target in targets:
            if (owner, *target) not in artifacts:
                raise ValueError("declared native target lacks a prepared artifact")
            for dependency in manifest.get("requires", []):
                if target not in manifests[dependency][1]:
                    raise ValueError("native candidate lacks its exact-target prerequisite")
    if not artifacts:
        raise ValueError("candidate catalog requires at least one module artifact")
    merged = {"schema":1, "release":"0.2.2", "modules":[dict(manifests[owner][0], targets=[manifests[owner][1][target] for target in sorted(manifests[owner][1])]) for owner in sorted(manifests)], "artifacts":[artifacts[key] for key in sorted(artifacts)]}
    encoded = json.dumps(merged, indent=2, sort_keys=True)+"\n"
    if len(encoded.encode()) > LIMIT:
        raise ValueError("assembled catalog exceeds the management input bound")
    output.mkdir(parents=True, exist_ok=False)
    (output / "archives").mkdir()
    for receipt in receipts:
        destination = output / "archives" / (receipt["sha256"]+".zip")
        if not destination.exists():
            shutil.copyfile(receipt["archive"], destination)
        if destination.stat().st_size != receipt["bytes"] or digest(destination) != receipt["sha256"]:
            raise ValueError("candidate archive changed while preparing offline inputs")
    (output / "catalog.json").write_text(encoded, encoding="utf-8")
    (output / "candidate-receipts.json").write_text(json.dumps({"schema":1, "release":"0.2.2", "accepted":False, "published":False, "artifacts":receipts}, indent=2, sort_keys=True)+"\n", encoding="utf-8")
    print(output / "catalog.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", action="append", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    merge([path.resolve() for path in args.catalog], args.output.resolve())
