#!/usr/bin/env python3
"""Assemble exact native candidate bytes for a Loki release.

The publisher separately requires successful, same-source native acceptance
runs. This assembler never rebuilds, mutates archives or publishes anything.
"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
from urllib.parse import urlsplit

from build_browser_bundle import digest, relative
from build_full_images import oci_manifest
from render_public_installers import render

MANAGERS = {("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64"), ("windows", "amd64"), ("windows", "arm64")}
BROWSERS = MANAGERS - {("windows", "arm64")}
FULL = {("linux", "amd64"), ("linux", "arm64")}
BASE = "https://github.com/jinyongp/loki/releases/download/v0.2.1/"


def read(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 8 << 20:
        raise ValueError("release receipt must be a bounded regular file")
    return json.loads(path.read_text(encoding="utf-8"))


def check(path, expected):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != expected["bytes"] or digest(path) != expected["sha256"]:
        raise ValueError("release bytes differ from their candidate receipt: " + str(path))


def prepare(managers, browsers, full, output, source, acceptance=()):
    if not re.fullmatch(r"[a-f0-9]{40}", source):
        raise ValueError("release requires its exact source commit")
    output.mkdir(parents=True, exist_ok=False)
    assets = output / "assets"
    assets.mkdir()
    images, accepted = [], []
    def copy(path, name):
        if relative(name).name != name:
            raise ValueError("public release asset must have a flat filename")
        target = assets / name
        if target.exists():
            if digest(target) != digest(path):
                raise ValueError("release asset collision: " + name)
        else:
            shutil.copyfile(path, target)
    seen = set()
    for directory in sorted(managers.iterdir()):
        receipt = read(directory / "manager-receipt.json")
        target = (receipt.get("os"), receipt.get("arch"))
        name = "loki-manager-0.2.1-" + "-".join(target) + ".zip"
        if receipt.get("schema") != 1 or receipt.get("release") != "0.2.1" or receipt.get("included_tools") != [] or receipt.get("archive") != name or target not in MANAGERS or target in seen:
            raise ValueError("manager candidate target/identity differs")
        seen.add(target)
        archive = directory / name
        check(archive, {"bytes":receipt["archive_bytes"], "sha256":receipt["archive_sha256"]})
        copy(archive, name)
        copy(directory / "manager-receipt.json", name.removesuffix(".zip") + "-receipt.json")
    if seen != MANAGERS:
        raise ValueError("release lacks one of its six accepted native managers")
    for mode, directories, targets in [("project-host", browsers, BROWSERS), ("full", full, FULL)]:
        seen = set()
        for directory in sorted(directories.iterdir()):
            candidate = read(directory / "candidate.json")
            target = candidate.get("target", {})
            native = (target.get("os"), target.get("arch"))
            if candidate.get("schema") != 1 or candidate.get("release") != "0.2.1" or target.get("mode") != mode or native not in targets or native in seen or candidate.get("catalog") != "release/catalog.json":
                raise ValueError("tool candidate native target/identity differs")
            seen.add(native)
            catalog = read(directory / "release" / "catalog.json")
            if catalog.get("schema") != 1 or catalog.get("release") != "0.2.1" or not catalog.get("artifacts"):
                raise ValueError("release requires a nonempty exact catalog")
            # Modes keep separate manifests and prerequisite contracts.
            # Publish each trusted native catalog unchanged.
            for artifact in catalog["artifacts"]:
                if artifact.get("target") != target or artifact.get("release") != "0.2.1" or artifact.get("format") != "zip" or not artifact.get("url", "").startswith(BASE):
                    raise ValueError("artifact does not belong to this release and target")
                name = Path(urlsplit(artifact["url"]).path).name
                if artifact["url"] != BASE + name:
                    raise ValueError("release artifact URL contains an unexpected path or query")
                archive = directory / "release" / "archives" / (artifact["sha256"] + ".zip")
                check(archive, artifact)
                copy(archive, name)
            copy(directory / "release" / "catalog.json", "loki-catalog-" + "-".join(native) + "-" + mode + ".json")
            accepted.append({"target":target, "catalog_sha256":digest(directory / "release" / "catalog.json")})
            if mode == "full":
                receipt = read(directory / "images" / "images.json")
                if receipt.get("schema") != 1 or receipt.get("release") != "0.2.1" or receipt.get("target") != target:
                    raise ValueError("full image receipt identity differs")
                for role, image in sorted(receipt["images"].items()):
                    archive = directory / "images" / relative(image["archive"])
                    check(archive, image)
                    reference = image["reference"]
                    if image.get("target") != target or not re.fullmatch(r"ghcr\.io/jinyongp/loki/[a-z0-9-]+@sha256:[a-f0-9]{64}", reference) or oci_manifest(archive, target) != reference.split("@", 1)[1]:
                        raise ValueError("full OCI manifest differs from its pinned destination")
                    images.append({"archive":str(archive.resolve()), "reference":reference, "tag":reference.split("@", 1)[0] + ":0.2.1-" + target["os"] + "-" + target["arch"]})
        if seen != targets:
            raise ValueError("release lacks accepted native tool candidates")
    notes = """Install the native Loki CLI with one command:

Windows PowerShell: `irm https://jinyongp.dev/loki/install.ps1 | iex`

Linux/macOS: `curl -fsSL https://jinyongp.dev/loki/install.sh | sh`

The installer verifies the release-bound native manager archive and installs
only the CLI. Configure the execution host, install and enable individual tool
groups, and connect your MCP client afterward using `loki tools`.
Full tools require an accessible Docker Engine on Linux/WSL.
Each catalog binds archive lengths and SHA-256; module ZIPs retain upstream
notices and licenses.

Management: Linux, macOS and Windows amd64/arm64. Independent browser:
Linux/macOS amd64/arm64 and Windows amd64. Full mode: Linux amd64/arm64.
Example: `loki tools configure --mode project-host`,
`loki tools install browser --catalog /absolute/path/to/loki-catalog-linux-amd64-project-host.json`,
`loki tools enable browser`, then
`loki tools serve browser --workspace /absolute/path/to/project --engine both`.

Independent Playwright/Chrome DevTools navigation, owned image transfer and
explicit optional capabilities have native acceptance evidence. Full workspace
private stdio, create/read, cached selection revocation and owned stop have
native acceptance evidence. Other full job/network/signing/endpoint/sharing
combinations and experimental browser workflows retain final acceptance gates.
Actual Windows desktop SSH image rendering remains a separate user-side check.

Install the native management command and select the tools needed for your host.
"""
    (assets / "loki-release-notes.md").write_text(notes, encoding="utf-8")
    evidence = {"schema":1, "release":"0.2.1", "channel":"stable", "source_commit":source, "tool_candidates":accepted, "publication_images":images}
    (output / "publication-images.json").write_text(json.dumps(images, indent=2) + "\n", encoding="utf-8")
    # Public evidence includes immutable refs, never local preparation paths.
    evidence["publication_images"] = [{"reference":image["reference"]} for image in images]
    (assets / "loki-release-evidence.json").write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    for path in acceptance:
        record = read(path)
        if record.get("head_sha") != source or record.get("conclusion") != "success":
            raise ValueError("public acceptance evidence differs from the release source")
        copy(path, path.name)
    render(assets, output / "pages")
    sums = "".join(digest(path) + "  " + path.name + "\n" for path in sorted(assets.iterdir()))
    (assets / "SHA256SUMS").write_text(sums, encoding="utf-8")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("managers", "browsers", "full", "output"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--source", required=True)
    parser.add_argument("--acceptance", action="append", type=Path, default=[])
    args = parser.parse_args()
    prepare(args.managers.resolve(), args.browsers.resolve(), args.full.resolve(), args.output.resolve(), args.source, args.acceptance)
