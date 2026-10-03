#!/usr/bin/env python3
"""Prepare a module-owned Ubuntu native closure from reviewed package inputs.

Package installation and maintainer scripts never run. The host dpkg-deb only
reads the receipt-bound archives; native product execution belongs to final
acceptance. Package trust is supplied independently through the reviewed recipe.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import shutil
import subprocess
import tarfile
import tempfile
import threading
from urllib.parse import urlsplit

from build_browser_bundle import contained, digest, materialize, relative

LIBRARY_PREFIXES = ("usr/lib/", "lib/", "lib64/", "usr/share/fonts/", "usr/share/doc/", "usr/share/licenses/", "usr/share/common-licenses/", "etc/fonts/", "etc/ssl/certs/", "usr/share/ca-certificates/")
GIT_PREFIXES = LIBRARY_PREFIXES + ("usr/bin/", "usr/libexec/", "usr/share/git-core/", "usr/share/perl/", "usr/share/perl5/", "etc/ssh/")
PROGRAMS = {"git":"usr/bin/git", "ssh-agent":"usr/bin/ssh-agent", "ssh-add":"usr/bin/ssh-add", "ssh-keygen":"usr/bin/ssh-keygen"}
TRUST_PREFIXES = ("usr/share/ca-certificates/", "usr/share/doc/", "usr/share/licenses/", "usr/share/common-licenses/")


def package_files(archive, root, prefixes):
    process = subprocess.Popen(["dpkg-deb", "--fsys-tarfile", str(archive)], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    deadline = threading.Timer(180, process.kill)
    deadline.start()
    links = []
    hardlinks = []
    total = 0
    try:
        with tarfile.open(fileobj=process.stdout, mode="r|") as contents:
            for member in contents:
                name = member.name.removeprefix("./").rstrip("/")
                if not name:
                    continue
                if member.isdir():
                    continue
                if not any(name.startswith(prefix) for prefix in prefixes):
                    continue
                path = relative(name)
                if name.startswith("usr/share/doc/"):
                    # Retain package copyright/license notices and aliases
                    # between package notice directories, without changelogs
                    # or service documentation dependencies.
                    leaf = path.name.lower()
                    notice = any(token in leaf for token in ("copyright", "license", "notice", "copying"))
                    directory_alias = member.issym() and len(path.parts) == 4
                    if not notice and not directory_alias:
                        continue
                destination = root / path
                # Package aliases are resolved after extraction. A later
                # package must never write through an earlier package alias.
                for parent in destination.parents:
                    if parent == root:
                        break
                    if parent.is_symlink():
                        raise ValueError("native package writes through an alias")
                destination.parent.mkdir(parents=True, exist_ok=True)
                if member.issym():
                    links.append((destination, member.linkname))
                    continue
                if member.islnk():
                    target = relative(member.linkname.removeprefix("./"))
                    if not any(target.as_posix().startswith(prefix) for prefix in prefixes):
                        raise ValueError("native hard link leaves its selected closure")
                    hardlinks.append((destination, root / target))
                    continue
                if not member.isfile():
                    raise ValueError("selected native package contains a special file")
                total += member.size
                if total > 4 << 30:
                    raise ValueError("native package exceeds its extraction bound")
                with contents.extractfile(member) as source:
                    if destination.exists():
                        checksum = hashlib.sha256()
                        length = 0
                        while chunk := source.read(1024 * 1024):
                            checksum.update(chunk)
                            length += len(chunk)
                        if not destination.is_file() or destination.is_symlink() or destination.stat().st_size != length or digest(destination) != checksum.hexdigest():
                            raise ValueError("native packages disagree on an owned file")
                    else:
                        with destination.open("xb") as sink:
                            shutil.copyfileobj(source, sink)
                        destination.chmod(0o755 if member.mode & 0o111 else 0o644)
        if process.wait(timeout=30) != 0:
            raise ValueError("native package filesystem could not be read")
    finally:
        deadline.cancel()
        if process.poll() is None:
            process.kill()
        process.wait()
        process.stdout.close()
    for destination, target in hardlinks:
        target = contained(root, target)
        if not target.is_file() or destination.exists() or destination.is_symlink():
            raise ValueError("native hard link target or destination is invalid")
        shutil.copyfile(target, destination)
        destination.chmod(target.stat().st_mode & 0o777)
    for destination, target in links:
        if "\x00" in target or "\\" in target:
            raise ValueError("invalid native package alias")
        # Debian's absolute aliases refer to the package root. Rewrite them
        # within this prepared closure before resolving; host paths are never
        # used to satisfy a package dependency.
        if PurePosixPath(target).is_absolute():
            relative_target = PurePosixPath(target).relative_to("/")
            target = os.path.relpath(root / Path(*relative_target.parts), destination.parent)
        if destination.is_symlink():
            if str(destination.readlink()) != target:
                raise ValueError("native packages disagree on an alias")
        elif destination.exists():
            raise ValueError("native alias conflicts with a regular file")
        else:
            destination.symlink_to(target)


def prepare(recipe_path, output):
    recipe = json.loads(recipe_path.read_text(encoding="utf-8"))
    arch = {"x86_64":"amd64", "aarch64":"arm64", "arm64":"arm64"}.get(platform.machine())
    target = {"os":"linux", "arch":arch, "mode":"full"}
    owner = recipe.get("owner")
    if platform.system() != "Linux" or arch is None or recipe.get("schema") != 1 or recipe.get("target") != target or recipe.get("distribution") != "ubuntu-24.04" or owner not in ("git", "browser", "trust-store") or not recipe.get("packages"):
        raise ValueError("package closure requires its exact native Ubuntu target and finite owner")
    url = urlsplit(recipe.get("url", ""))
    if url.scheme != "https" or not url.hostname or url.username or url.fragment:
        raise ValueError("prepared closure requires its public provenance/publication URL")
    output.mkdir(parents=True, exist_ok=False)
    receipts = []
    with tempfile.TemporaryDirectory(prefix="loki-native-packages-") as temporary:
        stage = Path(temporary)
        package_root = stage / "packages"
        package_root.mkdir()
        prefixes = GIT_PREFIXES if owner == "git" else TRUST_PREFIXES if owner == "trust-store" else LIBRARY_PREFIXES
        if owner == "browser":
            gnu = "x86_64-linux-gnu" if arch == "amd64" else "aarch64-linux-gnu"
            # Native browser images own libraries/fonts, not init services
            # dragged into APT's package-level dependency closure.
            prefixes = tuple(prefix for prefix in prefixes if prefix != "usr/lib/") + ("usr/lib/"+gnu+"/", "usr/share/fontconfig/", "usr/share/glib-2.0/")
        for package in recipe["packages"]:
            archive = (recipe_path.parent / package["archive"]).resolve()
            origin = urlsplit(package.get("url", ""))
            if archive.is_symlink() or not archive.is_file() or origin.scheme != "https" or not origin.hostname or origin.username or origin.fragment or archive.stat().st_size != package.get("bytes") or digest(archive) != package.get("sha256") or not package.get("signed_index"):
                raise ValueError("package differs from independently reviewed signed-index receipt")
            actual = subprocess.check_output(["dpkg-deb", "--field", str(archive), "Package", "Version", "Architecture"], text=True, timeout=15)
            metadata = dict(line.split(": ", 1) for line in actual.splitlines())
            if metadata.get("Package") != package.get("name") or metadata.get("Version") != package.get("version") or metadata.get("Architecture") not in (arch, "all"):
                raise ValueError("package identity differs from the native closure recipe")
            package_files(archive, package_root, prefixes)
            receipts.append({key:package[key] for key in ("name", "version", "url", "bytes", "sha256", "signed_index")})
        # Resolve only after all package files exist. Every native alias must
        # remain inside the selected closure; missing dependencies fail here.
        for path in package_root.rglob("*"):
            if path.is_symlink():
                contained(package_root, path)
        bundle = stage / "bundle"
        programs = {}
        if owner == "git":
            bundle.mkdir()
            materialize(package_root, bundle / "vendor")
            (bundle / "bin").mkdir()
            gnu = "x86_64-linux-gnu" if arch == "amd64" else "aarch64-linux-gnu"
            for name, path in PROGRAMS.items():
                program = contained(bundle, bundle / "vendor" / relative(path))
                if not program.is_file() or not program.stat().st_mode & 0o111:
                    raise ValueError("Git/OpenSSH native program is missing from the reviewed closure")
                script = '#!/bin/sh\nset -eu\nroot=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)\n'
                script += 'export LD_LIBRARY_PATH="$root/vendor/usr/lib/'+gnu+':$root/vendor/lib/'+gnu+'"\n'
                script += 'export PATH="$root/vendor/usr/bin:/usr/bin:/bin"\n'
                if name == "git":
                    script += 'export GIT_EXEC_PATH="$root/vendor/usr/lib/git-core"\n'
                script += 'exec "$root/vendor/'+path+'" "$@"\n'
                destination = bundle / "bin" / name
                destination.write_text(script, encoding="utf-8")
                destination.chmod(0o755)
                programs[name] = "bin/"+name
        else:
            materialize(package_root, bundle)
            if owner == "browser":
                # The standard trust-store image input owns these public CA
                # files. A browser library closure retains library/font/docs.
                shutil.rmtree(bundle / "usr/share/ca-certificates", ignore_errors=True)
            if owner == "trust-store":
                roots = sorted((bundle / "usr/share/ca-certificates").rglob("*.crt"))
                if not roots:
                    raise ValueError("reviewed trust-store package has no public root certificates")
                destination = bundle / "etc/ssl/certs/ca-certificates.crt"
                destination.parent.mkdir(parents=True, exist_ok=True)
                destination.write_bytes(b"\n".join(path.read_bytes().rstrip() for path in roots)+b"\n")
        notices = [path.relative_to(bundle).as_posix() for path in sorted(bundle.rglob("*")) if path.is_file() and (path.name == "copyright" or "common-licenses" in path.parts or "licenses" in path.parts)]
        if not notices:
            raise ValueError("native closure contains no retained package copyright notices")
        receipt_directory = bundle / "usr/share/doc/loki-native-closure"
        receipt_directory.mkdir(parents=True, exist_ok=True)
        (receipt_directory / "package-receipts.json").write_text(json.dumps({"schema":1, "owner":owner, "target":target, "packages":receipts}, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        archive = output / f"{owner}-ubuntu-24.04-{arch}.tar.gz"
        with archive.open("xb") as raw:
            import gzip
            with gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed, tarfile.open(fileobj=compressed, mode="w|") as packed:
                for path in sorted(bundle.rglob("*")):
                    if not path.is_file():
                        continue
                    info = tarfile.TarInfo("closure/"+path.relative_to(bundle).as_posix())
                    info.size, info.mode, info.mtime = path.stat().st_size, 0o755 if path.stat().st_mode & 0o111 else 0o644, 0
                    with path.open("rb") as source:
                        packed.addfile(info, source)
        receipt = {"target":target, "archive":archive.name, "url":recipe["url"], "version":"ubuntu-24.04", "root":"closure", "bytes":archive.stat().st_size, "sha256":digest(archive), "notices":notices, "programs":programs, "package_inputs":receipts}
        if owner == "trust-store":
            receipt["certificates"] = "etc/ssl/certs/ca-certificates.crt"
        (output / "closure-receipt.json").write_text(json.dumps(receipt, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        print(output / "closure-receipt.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    prepare(args.inputs.resolve(), args.output.resolve())
