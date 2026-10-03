#!/usr/bin/env python3
"""Prepare one exact-target full module archive from reviewed local inputs.

Preparation emits artifacts and receipts. Runtime/native acceptance is a later
phase. Vendor programs remain module-owned; the core worker contains no vendor
Git, OpenSSH, gh, devtools or browser runtime installation.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import subprocess
import tempfile
from urllib.parse import urlsplit
import zipfile

from build_browser_bundle import contained, digest, materialize, relative, unpack
from build_manager_bundle import go_notices

REQUIRED_PROGRAMS = {
    "runtime-core": {"loki"}, "execution": {"launcher", "executor"},
    "git": {"git", "ssh-agent", "ssh-add", "ssh-keygen"},
    "github": {"gh"}, "coordination": {"devtools"}, "workspace": {"rg"},
    "secrets": set(), "sharing": set(),
}
OWNED_IMAGES = {"runtime-core": {"service", "gateway"}, "execution": {"workload"}, "git": {"git-workload"}}
GO_PROGRAMS = {"runtime-core": {"loki": "./cmd/loki"}, "execution": {"launcher": "./cmd/launcher", "executor": "./cmd/executor"}}
SOURCE_ASSETS = {
    "runtime-core": {
        "execution-contract": "packaging/v02/config/full-execution-contract.json",
        "egress-policy": "packaging/v02/config/full-egress-policy.json",
        "browser-seccomp": "packaging/v02/config/browser-seccomp.json",
    },
    "git": {"gitconfig": "modules/git/assets/gitconfig", "templates": "modules/git/assets/templates"},
    "execution": {"toolchain-catalog": "packaging/native/toolchain-catalog.json"},
    "workspace": {"skills": "bundled_skills"},
}
IMAGE = re.compile(r"[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}")
NAME = re.compile(r"[a-z][a-z0-9-]{0,63}")


def validate_recipe(recipe, target, payload_only=False):
    module = recipe.get("module")
    if recipe.get("schema") != 1 or module not in REQUIRED_PROGRAMS or recipe.get("target") != target:
        raise ValueError("full input recipe must identify one exact native Linux module target")
    images = recipe.get("images", {})
    if set(images) != (set() if payload_only else OWNED_IMAGES.get(module, set())):
        raise ValueError("module input must declare exactly its owned service/workload images")
    for image in images.values():
        if not IMAGE.fullmatch(image.get("reference", "")) or image.get("target") != target or not image.get("notices"):
            raise ValueError("image requires an exact-target digest and retained notice receipt")
    programs = recipe.get("programs", {})
    if set(programs) | set(GO_PROGRAMS.get(module, {})) != REQUIRED_PROGRAMS[module] or set(programs) & set(GO_PROGRAMS.get(module, {})):
        raise ValueError("module programs differ from the required vendor and Go program contract")
    for collection in (programs, recipe.get("assets", {})):
        for name, path in collection.items():
            if not NAME.fullmatch(name):
                raise ValueError("invalid payload resource name")
            relative(path)
    prefixes = set()
    for asset in recipe.get("inputs", []):
        source = urlsplit(asset.get("url", ""))
        if source.scheme != "https" or not source.hostname or source.username or source.fragment:
            raise ValueError("native input requires a reviewed primary HTTPS provenance URL")
        if not isinstance(asset.get("bytes"), int) or not 0 < asset["bytes"] <= 8 << 30 or not re.fullmatch(r"[a-f0-9]{64}", asset.get("sha256", "")) or not asset.get("version") or not asset.get("notices"):
            raise ValueError("native input requires independent integrity, version and retained notices")
        prefix = relative(asset["destination"]).as_posix()
        if prefix in prefixes or any(prefix.startswith(old + "/") or old.startswith(prefix + "/") for old in prefixes):
            raise ValueError("native input destinations overlap")
        if prefix.split("/")[0] in ("bin", "notices", "source-assets"):
            raise ValueError("vendor input cannot replace build-owned paths")
        prefixes.add(prefix)
    return recipe


def assemble(recipe_path, output, release_url, payload_only=False):
    arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or arch is None:
        raise ValueError("full module preparation requires its native Linux host")
    target = {"os": "linux", "arch": arch, "mode": "full"}
    recipe = validate_recipe(json.loads(recipe_path.read_text(encoding="utf-8")), target, payload_only)
    module = recipe["module"]
    url = urlsplit(release_url)
    if url.scheme != "https" or not url.hostname or url.username or url.fragment:
        raise ValueError("candidate acquisition URL must be reviewed HTTPS")
    repo = Path(__file__).resolve().parents[3]
    manifest_path = repo / ("packaging/v02/module.full.json" if module == "runtime-core" else f"modules/{module}/module.full.json")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    if manifest["release"] != "0.2.0" or manifest["id"] != module or target not in manifest["targets"]:
        raise ValueError("module source manifest differs from the preparation contract")
    manifest["targets"] = [target]
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="loki-full-build-") as temporary:
        stage = Path(temporary)
        bundle = stage / "bundle"
        bundle.mkdir()
        receipts = []
        for index, asset in enumerate(recipe.get("inputs", [])):
            archive = (recipe_path.parent / asset["archive"]).resolve()
            if archive.stat().st_size != asset["bytes"] or digest(archive) != asset["sha256"]:
                raise ValueError("native archive differs from independently trusted integrity")
            extracted = stage / f"input-{index}"
            unpack(archive, extracted)
            source = contained(extracted, extracted / relative(asset["root"]))
            destination = bundle / relative(asset["destination"])
            materialize(source, destination)
            for notice in asset["notices"]:
                if not contained(destination, destination / relative(notice)).is_file():
                    raise ValueError("vendor notice missing from module payload")
            receipts.append({key: asset[key] for key in ("url", "version", "bytes", "sha256", "destination", "notices")})
        programs = dict(recipe.get("programs", {}))
        go_dependencies = []
        packages = GO_PROGRAMS.get(module, {})
        if packages:
            version = subprocess.check_output(["go", "version"], text=True).split()
            if len(version) < 3 or version[2] != "go1.27.1":
                raise ValueError("core module preparation requires pinned Go 1.27.1")
            env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOTOOLCHAIN="local")
            env.pop("GOFLAGS", None)
            (bundle / "bin").mkdir()
            for name, package in packages.items():
                path = f"bin/{name}"
                subprocess.run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid= -X loki/internal/buildinfo.Version=0.2.0", "-o", str(bundle / path), package], cwd=repo, env=env, check=True)
                programs[name] = path
            go_dependencies = go_notices(repo, bundle, env, tuple(packages.values()))
        assets = dict(recipe.get("assets", {}))
        for name, source_path in SOURCE_ASSETS.get(module, {}).items():
            if name in assets:
                raise ValueError("input recipe cannot replace release-owned runtime assets")
            source = contained(repo, repo / relative(source_path))
            destination = bundle / "source-assets" / source.name
            destination.parent.mkdir(parents=True, exist_ok=True)
            if source.is_dir():
                materialize(source, destination)
                files = [{"path": item.relative_to(source).as_posix(), "bytes": item.stat().st_size, "sha256": digest(item)} for item in sorted(source.rglob("*")) if item.is_file()]
                receipts.append({"source": source_path, "files": files})
            else:
                shutil.copyfile(source, destination)
                receipts.append({"source": source_path, "bytes": source.stat().st_size, "sha256": digest(source)})
            assets[name] = destination.relative_to(bundle).as_posix()
        if module == "runtime-core":
            receipt_path = repo / "packaging/v02/config/browser-seccomp.receipt.json"
            receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
            profile = bundle / assets["browser-seccomp"]
            if profile.stat().st_size != receipt["bytes"] or digest(profile) != receipt["sha256"]:
                raise ValueError("sandbox profile differs from its immutable upstream receipt")
            receipt["notice"] = "notices/Playwright.LICENSE"
            notices = bundle / "notices"
            notices.mkdir(exist_ok=True)
            shutil.copyfile(repo / "packaging/v02/notices/Playwright.LICENSE", notices / "Playwright.LICENSE")
            receipts.append(receipt)
        for path in programs.values():
            owned = contained(bundle, bundle / relative(path))
            if not owned.is_file() or not owned.stat().st_mode & 0o111:
                raise ValueError("module executable is absent or not executable")
        for path in assets.values():
            contained(bundle, bundle / relative(path))
        payload = {"schema": 1, "module": module, "release": "0.2.0", "target": target, "images": {role: image["reference"] for role, image in recipe.get("images", {}).items()}, "programs": programs, "assets": assets}
        metadata = {"module.json": manifest, "full-runtime.json": payload, "upstream-receipts.json": {"schema": 1, "inputs": receipts, "images": recipe.get("images", {}), "go_dependencies": go_dependencies}}
        for name, value in metadata.items():
            (bundle / name).write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        shutil.copyfile(repo / "LICENSE", bundle / "LICENSE")
        if payload_only:
            files = {path.relative_to(bundle).as_posix(): {"bytes": path.stat().st_size, "sha256": digest(path), "executable": bool(path.stat().st_mode & 0o111)} for path in sorted(bundle.rglob("*")) if path.is_file()}
            shutil.copytree(bundle, output / "payload")
            (output / "payload-receipt.json").write_text(json.dumps({"schema": 1, "release": "0.2.0", "module": module, "target": target, "files": files}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            print(output / "payload-receipt.json", flush=True)
            return
        archive_name = f"loki-{module}-0.2.0-linux-{arch}-full.zip"
        archive = stage / archive_name
        with zipfile.ZipFile(archive, "x", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as packed:
            for path in sorted(bundle.rglob("*")):
                if not path.is_file():
                    continue
                if path.is_symlink():
                    raise ValueError("full Linux payload must materialize native links")
                entry = zipfile.ZipInfo(path.relative_to(bundle).as_posix(), (1980, 1, 1, 0, 0, 0))
                entry.create_system = 3
                entry.external_attr = (stat.S_IFREG | (0o755 if path.stat().st_mode & 0o111 else 0o644)) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                with path.open("rb") as source, packed.open(entry, "w", force_zip64=True) as sink:
                    shutil.copyfileobj(source, sink)
        artifact = {"module": module, "release": "0.2.0", "target": target, "url": release_url, "sha256": digest(archive), "bytes": archive.stat().st_size, "format": "zip"}
        shutil.copyfile(archive, output / archive_name)
        (output / "catalog.json").write_text(json.dumps({"schema": 1, "release": "0.2.0", "modules": [manifest], "artifacts": [artifact]}, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(output / archive_name, flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--release-url", required=True)
    parser.add_argument("--payload-only", action="store_true", help="prepare immutable native files before owned image assembly; emits no install catalog")
    args = parser.parse_args()
    assemble(args.inputs.resolve(), args.output.resolve(), args.release_url, args.payload_only)
