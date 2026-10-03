#!/usr/bin/env python3
"""Produce a management-only native candidate; product acceptance runs later."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import stat
import subprocess
import tempfile
import zipfile


def go_notices(repo, bundle, env, packages=("./cmd/loki-manager",)):
    raw = subprocess.check_output(["go", "list", "-mod=readonly", "-buildvcs=false", "-deps", "-json", *packages], cwd=repo, env=env, text=True)
    decoder = json.JSONDecoder()
    modules = {}
    while raw.strip():
        package, offset = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[offset:]
        module = package.get("Module")
        if module and not module.get("Main"):
            if module.get("Replace") or not module.get("Version") or not module.get("Dir"):
                raise ValueError("manager dependencies require exact unmodified Go modules")
            modules[(module["Path"], module["Version"])] = module
    notices = bundle / "notices"
    notices.mkdir()
    goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], env=env, text=True).strip())
    license_path = goroot / "LICENSE"
    if not license_path.is_file() and goroot.name == "libexec":
        # Homebrew retains the unmodified upstream license beside libexec.
        license_path = goroot.parent / "LICENSE"
    if not license_path.is_file():
        raise ValueError("pinned Go distribution license is missing")
    shutil.copyfile(license_path, notices / "go-LICENSE")
    sums = {}
    for line in (repo / "go.sum").read_text(encoding="utf-8").splitlines():
        path, version, checksum = line.split()
        sums[(path, version)] = checksum
    inventory = []
    for index, key in enumerate(sorted(modules)):
        module = modules[key]
        directory = Path(module["Dir"]).resolve()
        copied = []
        for source in sorted(directory.rglob("*")):
            name = source.name.upper()
            if not source.is_file() or not (name.startswith(("LICENSE", "LICENCE", "NOTICE", "COPYING")) or name in ("COPYRIGHT", "THIRD_PARTY_NOTICES")):
                continue
            if not source.resolve().is_relative_to(directory):
                raise ValueError("Go dependency notice escapes its module")
            relative = source.relative_to(directory)
            target = notices / str(index) / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
            copied.append(target.relative_to(bundle).as_posix())
        if not copied or key not in sums:
            raise ValueError(f"missing dependency notices or go.sum integrity for {key[0]} {key[1]}")
        inventory.append({"module": key[0], "version": key[1], "integrity": sums[key], "notices": copied})
    return inventory


def produce(output):
    target_os = {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}.get(platform.system())
    target_arch = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    if not target_os or not target_arch:
        raise ValueError("native manager target is not supported")
    repo = Path(__file__).resolve().parents[3]
    version = subprocess.check_output(["go", "version"], text=True).split()
    if len(version) < 3 or version[2] != "go1.27.1":
        raise ValueError("candidate preparation requires pinned Go 1.27.1")
    name = f"loki-manager-0.2.0-{target_os}-{target_arch}.zip"
    output.mkdir(parents=True, exist_ok=True)
    if (output / name).exists() or (output / "manager-receipt.json").exists():
        raise ValueError("use a fresh native candidate output directory")
    with tempfile.TemporaryDirectory(prefix="loki-manager-build-") as temporary:
        stage = Path(temporary)
        bundle = stage / "bundle"
        bundle.mkdir()
        binary_name = "loki.exe" if target_os == "windows" else "loki"
        env = dict(os.environ, CGO_ENABLED="0", GOOS=target_os, GOARCH=target_arch, GOTOOLCHAIN="local")
        env.pop("GOFLAGS", None)
        subprocess.run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", str(bundle / binary_name), "./cmd/loki-manager"], cwd=repo, env=env, check=True)
        installer = "install.ps1" if target_os == "windows" else "install.sh"
        shutil.copyfile(Path(__file__).with_name(installer), bundle / installer)
        shutil.copyfile(repo / "LICENSE", bundle / "LICENSE")
        dependencies = go_notices(repo, bundle, env)
        receipt = {"schema": 1, "release": "0.2.0", "os": target_os, "arch": target_arch, "go": "1.27.1", "binary": binary_name, "binary_sha256": hashlib.sha256((bundle / binary_name).read_bytes()).hexdigest(), "binary_bytes": (bundle / binary_name).stat().st_size, "included_tools": [], "go_dependencies": dependencies}
        (bundle / "manager.json").write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        archive = stage / name
        with zipfile.ZipFile(archive, "x", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as packed:
            for path in sorted(bundle.rglob("*")):
                if path.is_dir():
                    continue
                entry = zipfile.ZipInfo(path.relative_to(bundle).as_posix(), (1980, 1, 1, 0, 0, 0))
                entry.create_system = 3
                mode = 0o755 if path.name in ("loki", "install.sh") else 0o644
                entry.external_attr = (stat.S_IFREG | mode) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                packed.writestr(entry, path.read_bytes())
        receipt["archive"] = name
        receipt["archive_sha256"] = hashlib.sha256(archive.read_bytes()).hexdigest()
        receipt["archive_bytes"] = archive.stat().st_size
        with archive.open("rb") as source, (output / name).open("xb") as sink:
            shutil.copyfileobj(source, sink)
        (output / "manager-receipt.json").write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(output / name)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    produce(args.output.resolve())
