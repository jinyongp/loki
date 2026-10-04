#!/usr/bin/env python3
"""Acquire Ubuntu package closures in isolated APT state without installation.

APT authenticates repository indices with the system Ubuntu archive keyring.
Dependency resolution uses an empty package status, including dependencies
already present on the development host. Product programs are never executed.
"""
import argparse
from input_cache import restore, save, size_digest
import hashlib
import json
import os
from pathlib import Path
import platform
import pwd
import re
import shlex
import subprocess
import tempfile
from urllib.request import urlopen


def fields(text):
    result = {}
    for line in text.splitlines():
        if line and not line.startswith(" ") and ": " in line:
            key, value = line.split(": ", 1)
            result[key] = value
    return result


def acquire(recipe_path, output, resolve_only=False):
    recipe = json.loads(recipe_path.read_text(encoding="utf-8"))
    arch = {"amd64":"amd64", "x86_64":"amd64", "aarch64":"arm64", "arm64":"arm64"}.get(platform.machine().lower())
    if resolve_only:
        arch = recipe.get("target", {}).get("arch")
    target = {"os":"linux", "arch":arch, "mode":"full"}
    packages = recipe.get("packages", [])
    if packages and isinstance(packages[0], dict):
        if resolve_only:
            recipe = dict(recipe, packages=recipe["requested_packages"])
            packages = recipe["packages"]
        else:
            return acquire_frozen(recipe, output, target)
    if platform.system() != "Linux" or arch is None or recipe.get("schema") != 1 or recipe.get("target") != target or recipe.get("owner") not in ("git", "browser", "trust-store") or not packages or any(not isinstance(name, str) or not re.fullmatch(r"[a-z0-9][a-z0-9+.-]*(?:=[A-Za-z0-9+:~._-]+)?", name) for name in packages):
        raise ValueError("package acquisition requires its finite owner and exact native target")
    keyring = Path("/usr/share/keyrings/ubuntu-archive-keyring.gpg")
    key = keyring.stat()
    if not keyring.is_file() or keyring.is_symlink() or key.st_uid != 0 or key.st_mode & 0o022:
        raise ValueError(f"Ubuntu archive verification requires an administrator-owned keyring: uid={key.st_uid}, mode={oct(key.st_mode & 0o777)}, symlink={keyring.is_symlink()}")
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="loki-apt-inputs-") as temporary:
        root = Path(temporary)
        for name in ("etc", "lists/partial", "archives/partial", "empty"):
            (root / name).mkdir(parents=True, exist_ok=True)
        (root / "status").touch()
        mirror = "https://archive.ubuntu.com/ubuntu" if arch == "amd64" else "https://ports.ubuntu.com/ubuntu-ports"
        sources = "".join(f"deb [arch={arch} signed-by={keyring}] {mirror} {suite} main universe\n" for suite in ("noble", "noble-updates", "noble-security"))
        (root / "etc/sources.list").write_text(sources, encoding="utf-8")
        options = {
            "Dir::Etc::sourcelist":str(root / "etc/sources.list"),
            "Dir::Etc::sourceparts":str(root / "empty"),
            "Dir::Etc::main":str(root / "etc/absent.conf"),
            "Dir::Etc::parts":str(root / "empty"),
            "Dir::Etc::preferences":str(root / "etc/absent.preferences"),
            "Dir::Etc::preferencesparts":str(root / "empty"),
            "Dir::State::lists":str(root / "lists"),
            "Dir::State::status":str(root / "status"),
            "Dir::Cache::archives":str(root / "archives"),
            "Dir::Cache::pkgcache":"", "Dir::Cache::srcpkgcache":"",
            "APT::Architecture":arch, "APT::Architectures":arch,
            "APT::Sandbox::User":pwd.getpwuid(os.getuid()).pw_name,
            "Acquire::AllowInsecureRepositories":"false",
            "Acquire::AllowDowngradeToInsecureRepositories":"false",
            "APT::Get::AllowUnauthenticated":"false",
            "Acquire::Languages":"none", "Acquire::Retries":"1",
        }
        config = root / "apt.conf"
        config.write_text("".join(key+" "+json.dumps(value)+";\n" for key, value in options.items()), encoding="utf-8")
        environment = dict(os.environ, APT_CONFIG=str(config), LC_ALL="C")
        print("Authenticating isolated Ubuntu package indices...", flush=True)
        subprocess.run(["apt-get", "update", "--error-on=any"], env=environment, check=True, timeout=300)
        plan = subprocess.check_output(["apt-get", "--print-uris", "--yes", "--download-only", "--no-install-recommends", "install", *packages], env=environment, text=True, timeout=60)
        indices = [{"name":path.name, "bytes":path.stat().st_size, "sha256":hashlib.file_digest(path.open("rb"), "sha256").hexdigest()} for path in sorted((root / "lists").glob("*InRelease"))]
        if not indices:
            raise ValueError("authenticated package index receipts are absent")
        receipts = []
        for line in plan.splitlines():
            if not line.startswith("'https://"):
                continue
            url, name, length, *_ = shlex.split(line)
            if not url.startswith(mirror+"/pool/") or Path(name).name != name or not name.endswith(".deb"):
                raise ValueError("resolved package leaves its reviewed repository")
            package_name, version, package_arch = name[:-4].rsplit("_", 2)
            # APT filenames encode epochs; show accepts the decoded version.
            from urllib.parse import unquote
            metadata = fields(subprocess.check_output(["apt-cache", "show", package_name+"="+unquote(version)], env=environment, text=True, timeout=30))
            checksum = metadata.get("SHA256", "")
            if not re.fullmatch(r"[a-f0-9]{64}", checksum) or metadata.get("Size") != length or metadata.get("Architecture") not in (arch, "all") or unquote(url) != mirror+"/"+metadata.get("Filename", ""):
                raise ValueError("resolved package differs from its authenticated index")
            print("Acquiring "+package_name+" "+metadata["Version"]+"...", flush=True)
            destination = output / name
            observed, digest = 0, hashlib.sha256()
            if resolve_only:
                observed = int(length)
            elif not restore(destination, {"bytes":int(length), "sha256":checksum}):
                with urlopen(url, timeout=30) as response, destination.open("xb") as sink:
                    if not response.url.startswith(mirror+"/pool/"):
                        raise ValueError("package redirect leaves its reviewed repository")
                    while chunk := response.read(1024*1024):
                        observed += len(chunk)
                        if observed > int(length):
                            raise ValueError("package exceeded its authenticated byte length")
                        digest.update(chunk)
                        sink.write(chunk)
            if not resolve_only:
                observed, digest = size_digest(destination)
                if observed != int(length) or digest.hexdigest() != checksum:
                    raise ValueError("package differs from its authenticated content digest")
                save(destination, {"bytes":int(length), "sha256":checksum})
            receipts.append({"archive":name, "name":metadata["Package"], "version":metadata["Version"], "url":url, "bytes":observed, "sha256":checksum, "signed_index":{"verification":"APT authenticated InRelease and package index", "keyring_sha256":hashlib.file_digest(keyring.open("rb"), "sha256").hexdigest(), "indices":indices}})
        if not receipts:
            raise ValueError("empty native dependency closure")
        for path in (root / "lists").glob("*InRelease"):
            (output / path.name).write_bytes(path.read_bytes())
        result = dict(recipe, distribution="ubuntu-24.04", requested_packages=packages, packages=receipts)
        (output / "closure-inputs.json").write_text(json.dumps(result, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        print(output / "closure-inputs.json", flush=True)


def acquire_frozen(recipe, output, target):
    if platform.system() != "Linux" or recipe.get("schema") != 1 or recipe.get("target") != target or recipe.get("owner") not in {"git", "browser", "trust-store"}:
        raise ValueError("frozen package closure differs from the native owner/target")
    mirror = "https://archive.ubuntu.com/ubuntu" if target["arch"] == "amd64" else "https://ports.ubuntu.com/ubuntu-ports"
    packages = [dict(package, signed_index=package.get("signed_index", recipe.get("signed_index"))) for package in recipe["packages"]]
    for package in packages:
        if not isinstance(package,dict) or Path(package.get("archive", "")).name != package.get("archive") or not package["archive"].endswith(".deb") or not package.get("url", "").startswith(mirror+"/pool/") or not isinstance(package.get("bytes"),int) or package["bytes"] <= 0 or not re.fullmatch(r"[a-f0-9]{64}",package.get("sha256", "")) or not package.get("version") or not package.get("signed_index", {}).get("indices"):
            raise ValueError("frozen package lacks its authenticated exact byte receipt")
    output.mkdir(parents=True,exist_ok=False)
    for package in packages:
        destination = output/package["archive"]
        if not restore(destination, package):
            observed = 0
            with urlopen(package["url"],timeout=30) as response,destination.open("xb") as sink:
                if not response.url.startswith(mirror+"/pool/"):
                    raise ValueError("package redirect leaves its frozen repository")
                while chunk := response.read(1024*1024):
                    observed += len(chunk)
                    if observed > package["bytes"]:
                        raise ValueError("package exceeds its frozen length")
                    sink.write(chunk)
        observed, digest = size_digest(destination)
        if observed != package["bytes"] or digest.hexdigest() != package["sha256"]:
            raise ValueError("package differs from its frozen authenticated digest")
        save(destination,package)
    (output/"closure-inputs.json").write_text(json.dumps(dict(recipe,packages=packages),indent=2,sort_keys=True)+"\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--trust", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--resolve-only", action="store_true", help="freeze latest authenticated versions without downloading packages; permits cross-architecture resolution")
    args = parser.parse_args()
    acquire(args.trust.resolve(), args.output.resolve(), args.resolve_only)
