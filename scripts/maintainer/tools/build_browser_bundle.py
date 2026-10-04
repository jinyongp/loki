#!/usr/bin/env python3
"""Assemble a browser artifact on its native release runner.

Inputs are a trusted local recipe and already acquired vendor archives. The
recipe binds upstream URLs, byte lengths and checksums independently of the
archive files. Acquisition and platform acceptance are separate release steps.
"""

import argparse
from release_config import RELEASE, CONTRACT, artifact_release
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import stat
import struct
import subprocess
import tarfile
import tempfile
from urllib.parse import urlsplit
import zipfile

VERSIONS = {"node": "26.10.0", "chrome": "154.0.8037.92", "ffmpeg": "1011"}
CAPABILITIES = ["unsafe-code", "vision", "pdf", "devtools", "network", "storage", "testing", "tracing", "config", "extensions", "pwa", "webmcp", "third-party", "memory"]


def digest(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def relative(value):
    path = PurePosixPath(value)
    if not value or path.is_absolute() or ".." in path.parts or "\\" in value or ":" in value or any(ord(c) < 32 or ord(c) == 127 for c in value):
        raise ValueError("asset path must be a local POSIX path")
    return Path(*path.parts)


def contained(root, path):
    result = path.resolve(strict=True)
    if not result.is_relative_to(root.resolve()):
        raise ValueError("asset or symbolic link escapes its vendor root")
    return result


def unpack(archive, destination):
    destination.mkdir()
    if zipfile.is_zipfile(archive):
        links = []
        with zipfile.ZipFile(archive) as zipped:
            for entry in zipped.infolist():
                path = destination / relative(entry.filename.rstrip("/"))
                mode = entry.external_attr >> 16
                if stat.S_ISLNK(mode):
                    links.append((path, zipped.read(entry).decode("utf-8")))
                elif entry.is_dir():
                    path.mkdir(parents=True, exist_ok=True)
                else:
                    if mode and stat.S_IFMT(mode) not in (0, stat.S_IFREG):
                        raise ValueError("special file in vendor zip")
                    path.parent.mkdir(parents=True, exist_ok=True)
                    with zipped.open(entry) as source, path.open("xb") as target:
                        shutil.copyfileobj(source, target)
                    path.chmod(0o755 if mode & 0o111 else 0o644)
        # Delay link creation so file extraction cannot traverse a prior link.
        for path, target in links:
            if os.path.isabs(target):
                raise ValueError("absolute vendor symbolic link")
            path.parent.mkdir(parents=True, exist_ok=True)
            path.symlink_to(target)
        for path, _ in links:
            contained(destination, path)
    else:
        with tarfile.open(archive) as packed:
            packed.extractall(destination, filter="data")


def materialize(source, destination, root=None, ancestors=()):
    root = root or source.resolve()
    actual = contained(root, source)
    if actual in ancestors:
        raise ValueError("symbolic link cycle in vendor tree")
    if actual.is_dir():
        destination.mkdir(parents=True, exist_ok=False)
        for child in sorted(actual.iterdir()):
            materialize(child, destination / child.name, root, (*ancestors, actual))
    elif actual.is_file():
        shutil.copyfile(actual, destination)
        destination.chmod(0o755 if actual.stat().st_mode & 0o111 else 0o644)
    else:
        raise ValueError("vendor asset is not a regular file or directory")


def preserve_chrome_tree(source, destination):
    # macOS app layouts include framework aliases. Validate links before copying,
    # preserve their exact relative spelling and keep all aliases in one app.
    for directory, directories, files in os.walk(source, followlinks=False):
        for name in (*directories, *files):
            entry = Path(directory) / name
            if not entry.is_symlink():
                continue
            target = os.readlink(entry)
            if os.path.isabs(target) or "\\" in target or ":" in target or "\x00" in target:
                raise ValueError("Chrome framework link must be relative")
            relative_entry = entry.relative_to(source).as_posix()
            marker = relative_entry.find(".app/Contents/")
            if marker < 0:
                raise ValueError("Chrome vendor link must belong to its app")
            scope = source / relative(relative_entry[:marker + len(".app")])
            if not contained(source, entry).is_relative_to(scope.resolve()):
                raise ValueError("Chrome framework link escapes its app")
    shutil.copytree(source, destination, symlinks=True)


def json_file(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def verify_chrome_signature(root, executable, native_os):
    if native_os != "darwin":
        return
    # macOS /var is an alias of /private/var. Compare canonical paths on both
    # sides after containment has resolved the executable.
    root = root.resolve(strict=True)
    binary = contained(root, root / relative(executable))
    apps = [parent for parent in binary.parents if parent.suffix == ".app" and parent.is_relative_to(root)]
    if not apps:
        raise ValueError("macOS Chrome executable must belong to its vendor app")
    arch = {"x86_64": "amd64", "arm64": "arm64"}.get(platform.machine().lower())
    verify_chrome_signature_kind(binary, arch)
    if arch == "arm64":
        subprocess.run(["/usr/bin/codesign", "--verify", "--strict", "--ignore-resources", str(binary)], check=True)


def verify_chrome_signature_kind(binary, arch):
    # Exact pinned upstream forms: Intel is unsigned; arm64 has a linker
    # ad-hoc signature with zero special slots (no app resource seal).
    # Preserve these bytes; source receipts and generation integrity bind resources.
    expected_cpu = {"amd64": 0x1000007, "arm64": 0x100000c}.get(arch)
    with binary.open("rb") as stream:
        header = stream.read(32)
        if len(header) != 32:
            raise ValueError("truncated Chrome Mach-O header")
        magic, cpu, _, _, count, size, _, _ = struct.unpack("<8I", header)
        if magic != 0xfeedfacf or cpu != expected_cpu or size > 1024 * 1024:
            raise ValueError("Chrome Mach-O architecture/header mismatch")
        commands = stream.read(size)
        if len(commands) != size or count > size // 8:
            raise ValueError("invalid Chrome load commands")
        position, signature = 0, None
        for _ in range(count):
            if position + 8 > size:
                raise ValueError("truncated Chrome load command")
            cmd, length = struct.unpack_from("<2I", commands, position)
            if length < 8 or position + length > size:
                raise ValueError("invalid Chrome load command size")
            if cmd == 0x1d:
                if signature is not None or length != 16:
                    raise ValueError("invalid Chrome signature command")
                offset, sigsize = struct.unpack_from("<2I", commands, position + 8)
                if sigsize < 12 or sigsize > 1024 * 1024:
                    raise ValueError("invalid Chrome signature size")
                stream.seek(offset)
                signature = stream.read(sigsize)
                if len(signature) != sigsize:
                    raise ValueError("truncated Chrome signature")
            position += length
        if position != size:
            raise ValueError("unaccounted Chrome load command bytes")
    if arch == "amd64":
        if signature is not None:
            raise ValueError("pinned Intel Chrome must retain its unsigned input")
        return
    if signature is None:
        raise ValueError("missing Chrome ad-hoc signature")
    magic, size, count = struct.unpack_from(">3I", signature)
    if magic != 0xfade0cc0 or size < 12 or size > len(signature) or count > (size - 12) // 8:
        raise ValueError("invalid Chrome signature container")
    found = False
    for i in range(count):
        slot, offset = struct.unpack_from(">2I", signature, 12 + i * 8)
        if slot != 0:
            continue
        if found or offset < 12 + count * 8 or offset > size - 28:
            raise ValueError("invalid Chrome code directory")
        cd = struct.unpack_from(">7I", signature, offset)
        if cd[0] != 0xfade0c02 or cd[1] < 28 or cd[1] > size - offset or cd[3] != 0x20002 or cd[6] != 0:
            raise ValueError("Chrome must retain its pinned linker ad-hoc signature without a resource seal")
        found = True
    if not found:
        raise ValueError("missing Chrome ad-hoc code directory")


def assemble(recipe_path, output, release_url):
    release = artifact_release("browser")
    recipe = json.loads(recipe_path.read_text(encoding="utf-8"))
    if recipe.get("schema") != 1 or set(recipe.get("assets", {})) != set(VERSIONS):
        raise ValueError("recipe requires schema 1 and exact Node, Chrome and FFmpeg assets")
    target = recipe["target"]
    native_os = {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}.get(platform.system())
    native_arch = {"amd64": "amd64", "x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower())
    mode = target.get("mode")
    if mode not in ("project-host", "full") or target != {"os": native_os, "arch": native_arch, "mode": mode}:
        raise ValueError("browser artifacts must be assembled on their exact native target")
    if mode == "full" and native_os != "linux":
        raise ValueError("protected full-mode browser artifacts require Linux")
    if native_os == "windows" and native_arch == "arm64":
        raise ValueError("native Windows arm64 Chrome is not in the browser support contract")
    url = urlsplit(release_url)
    if url.scheme != "https" or not url.hostname or url.username or url.fragment:
        raise ValueError("release acquisition URL must be HTTPS without credentials or fragment")
    output.mkdir(parents=True, exist_ok=True)
    repo = Path(__file__).resolve().parents[3]
    with tempfile.TemporaryDirectory(prefix="loki-browser-build-") as temporary:
        scratch = Path(temporary)
        bundle = scratch / "bundle"
        bundle.mkdir()
        receipts = {}
        for name, version in VERSIONS.items():
            asset = recipe["assets"][name]
            if asset["version"] != version:
                raise ValueError(f"{name} version differs from pinned {version}")
            upstream = urlsplit(asset["url"])
            if upstream.scheme != "https" or not upstream.hostname or upstream.username or upstream.fragment:
                raise ValueError("vendor receipt requires an HTTPS provenance URL")
            archive = (recipe_path.parent / asset["archive"]).resolve()
            if archive.stat().st_size != asset["bytes"] or digest(archive) != asset["sha256"]:
                raise ValueError(f"{name} vendor archive integrity mismatch")
            unpacked = scratch / (name + "-unpacked")
            unpack(archive, unpacked)
            source = contained(unpacked, unpacked / relative(asset["root"]))
            if name == "chrome":
                verify_chrome_signature(source, asset["executable"], native_os)
            if name == "chrome" and native_os == "darwin":
                preserve_chrome_tree(source, bundle / name)
            else:
                materialize(source, bundle / name)
            if name == "chrome":
                verify_chrome_signature(bundle / name, asset["executable"], native_os)
            if not asset.get("notices"):
                raise ValueError(f"{name} receipt must list bundled notices")
            for notice in asset["notices"]:
                if not contained(bundle / name, bundle / name / relative(notice)).is_file():
                    raise ValueError("vendor notice must be a bundled regular file")
            receipts[name] = {key: asset[key] for key in ("version", "url", "bytes", "sha256")}
            receipts[name]["digest_provenance"] = asset.get("digest_provenance", "Maintainer-reviewed input receipt; independent provenance acceptance is required.")
            receipts[name]["notices"] = asset["notices"]
        node_recipe = recipe["assets"]["node"]
        node = contained(bundle / "node", bundle / "node" / relative(node_recipe["executable"]))
        npm = contained(bundle / "node", bundle / "node" / relative(node_recipe["npm"]))
        chrome_relative = relative(recipe["assets"]["chrome"]["executable"])
        contained(bundle / "chrome", bundle / "chrome" / chrome_relative)
        # Use the committed workspace lock and bundled npm; no runtime npx or
        # machine Node participates in the shipped closure.
        npm_workspace = scratch / "npm-workspace"
        (npm_workspace / "modules" / "browser").mkdir(parents=True)
        for filename in ("package.json", "package-lock.json"):
            shutil.copyfile(repo / filename, npm_workspace / filename)
        shutil.copyfile(repo / "modules" / "browser" / "package.json", npm_workspace / "modules" / "browser" / "package.json")
        env = dict(os.environ)
        for key in list(env):
            normalized = key.upper()
            if normalized in ("NODE_OPTIONS", "NODE_PATH", "PLAYWRIGHT_BROWSERS_PATH") or normalized.startswith(("NPM_CONFIG_", "PLAYWRIGHT_MCP_")):
                del env[key]
        subprocess.run([str(node), str(npm), "ci", "--ignore-scripts", "--omit=dev", "--no-audit", "--no-fund"], cwd=npm_workspace, env=env, check=True)
        # Workspace links and npm command shims are build inputs, not runtime
        # dependencies. All actual dependency trees and notices are preserved.
        dependencies = npm_workspace / "node_modules"
        shutil.rmtree(dependencies / ".bin", ignore_errors=True)
        workspace_link = dependencies / "@loki" / "browser"
        if workspace_link.is_symlink():
            workspace_link.unlink()
        elif workspace_link.is_junction():
            # npm uses a directory junction for the owned Windows workspace.
            # Remove the link itself before validating dependency containment.
            if workspace_link.resolve() != (npm_workspace / "modules" / "browser").resolve():
                raise ValueError("npm workspace junction differs from its owned source")
            workspace_link.rmdir()
        materialize(dependencies, bundle / "node_modules")
        shutil.copyfile(repo / "package-lock.json", bundle / "package-lock.json")
        browsers = bundle / "browsers"
        browsers.mkdir()
        (bundle / "ffmpeg").rename(browsers / "ffmpeg-1011")
        module = {"schema": 1, "contract": CONTRACT, "id": "browser", "release": release, "targets": [target], "tools": ["loki_browser_files"], "capabilities": CAPABILITIES}
        if mode == "full":
            module = json.loads((repo / "modules" / "browser" / "module.full.json").read_text(encoding="utf-8"))
            if target not in module["targets"] or module["capabilities"] != CAPABILITIES:
                raise ValueError("full browser manifest differs from its native bundle contract")
            module["targets"] = [target]
            module["release"] = release
            module["contract"] = CONTRACT
            image = recipe.get("full_image", {})
            if image.get("target") != target or not re.fullmatch(r"[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}", image.get("reference", "")) or not image.get("notices"):
                raise ValueError("full browser requires its exact-target digest-pinned service image and notice receipt")
            json_file(bundle / "full-runtime.json", {"schema": 1, "module": "browser", "release": release, "target": target, "images": {"browser": image["reference"]}, "programs": {}, "assets": {"runtime": "runtime.json"}})
            json_file(bundle / "image-receipt.json", image)
        json_file(bundle / "module.json", module)
        json_file(bundle / "runtime.json", {"schema": 1, "node": "node/" + node_recipe["executable"], "chrome": "chrome/" + str(chrome_relative).replace(os.sep, "/"), "browsers": "browsers"})
        json_file(bundle / "upstream-receipts.json", {"schema": 1, "assets": receipts, "npm_lock_sha256": digest(repo / "package-lock.json"), "native_requirements": recipe["native_requirements"]})
        # Normalized timestamps, modes and ordering give a stable archive for
        # identical native inputs. Only signed native macOS Chrome app links
        # remain; the installer scopes that exception to the same app tree.
        artifact_name = f"loki-browser-{release}-{native_os}-{native_arch}-{mode}.zip"
        published_artifact = output / artifact_name
        if published_artifact.exists() or (output / "browser-catalog.json").exists():
            raise ValueError("candidate artifact already exists; use a fresh output directory")
        artifact = scratch / artifact_name
        with zipfile.ZipFile(artifact, "x", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as packed:
            for path in sorted(bundle.rglob("*")):
                if path.is_symlink():
                    entry = zipfile.ZipInfo(path.relative_to(bundle).as_posix(), (1980, 1, 1, 0, 0, 0))
                    entry.create_system = 3
                    entry.external_attr = (stat.S_IFLNK | 0o777) << 16
                    entry.compress_type = zipfile.ZIP_DEFLATED
                    packed.writestr(entry, os.readlink(path).encode("utf-8"))
                    continue
                if path.is_dir():
                    continue
                entry = zipfile.ZipInfo(path.relative_to(bundle).as_posix(), (1980, 1, 1, 0, 0, 0))
                entry.create_system = 3
                entry.external_attr = (stat.S_IFREG | (0o755 if path.stat().st_mode & 0o111 else 0o644)) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                with path.open("rb") as source, packed.open(entry, "w", force_zip64=True) as sink:
                    shutil.copyfileobj(source, sink)
        catalog = {"schema": 1, "contract": CONTRACT, "release": RELEASE, "modules": [module], "artifacts": [{"module": "browser", "release": release, "target": target, "url": release_url, "sha256": digest(artifact), "bytes": artifact.stat().st_size, "format": "zip"}]}
        with artifact.open("rb") as source, published_artifact.open("xb") as target_stream:
            shutil.copyfileobj(source, target_stream)
        json_file(output / "browser-catalog.json", catalog)
        print(published_artifact)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--release-url", required=True)
    args = parser.parse_args()
    assemble(args.inputs.resolve(), args.output.resolve(), args.release_url)
