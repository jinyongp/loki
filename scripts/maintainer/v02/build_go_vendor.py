#!/usr/bin/env python3
"""Prepare a native module-owned gh/devtools archive from exact source inputs.

This producer builds receipt-bound programs and retains source/dependency
notices. It neither runs their product API checks nor publishes the archive.
"""
import argparse
import gzip
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
from urllib.parse import urlsplit

from build_browser_bundle import contained, digest, relative, unpack
from build_manager_bundle import go_notices

OWNERS = {
    "github":{"program":"gh", "version":"2.102.0", "commit":"fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd", "module":"github.com/cli/cli/v2", "source":"https://codeload.github.com/cli/cli/tar.gz/fc4b137cdef0a6bd28fd461b7cf9c84a5812a8cd", "package":"./cmd/gh", "flags":"-X github.com/cli/cli/v2/internal/build.Version=2.102.0 -X github.com/cli/cli/v2/internal/build.Date=2026-09-30"},
    "coordination":{"program":"devtools", "version":"0.23.0", "commit":"b66fcfc024c5b4f1b8ce49a21e7a4e0e1fd75b11", "module":"github.com/jinyongp/devtools", "source":"https://codeload.github.com/jinyongp/devtools/tar.gz/b66fcfc024c5b4f1b8ce49a21e7a4e0e1fd75b11", "package":"./cmd/devtools", "flags":"-X main.version=0.23.0 -X main.commit=b66fcfc024c5b4f1b8ce49a21e7a4e0e1fd75b11"},
}


def prepare(recipe_path, output):
    recipe = json.loads(recipe_path.read_text(encoding="utf-8"))
    arch = {"x86_64":"amd64", "aarch64":"arm64", "arm64":"arm64"}.get(platform.machine())
    target = {"os":"linux", "arch":arch, "mode":"full"}
    owner = recipe.get("owner")
    if platform.system() != "Linux" or arch is None or recipe.get("schema") != 1 or owner not in OWNERS or recipe.get("target") != target:
        raise ValueError("vendor preparation requires its exact native full module target")
    expected = OWNERS[owner]
    source = recipe.get("source", {})
    archive = (recipe_path.parent / source.get("archive", "")).resolve()
    if source.get("url") != expected["source"] or source.get("version") != expected["version"] or source.get("commit") != expected["commit"] or archive.is_symlink() or not archive.is_file() or archive.stat().st_size != source.get("bytes") or digest(archive) != source.get("sha256") or not source.get("notices"):
        raise ValueError("vendor source differs from its independently reviewed exact commit receipt")
    publication = urlsplit(recipe.get("url", ""))
    if publication.scheme != "https" or not publication.hostname or publication.username or publication.fragment:
        raise ValueError("prepared vendor archive requires a public provenance/publication URL")
    version = subprocess.check_output(["go", "version"], text=True).split()
    if len(version) < 3 or version[2] != "go1.27.1":
        raise ValueError("vendor preparation requires pinned Go 1.27.1")
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="loki-go-vendor-") as temporary:
        stage = Path(temporary)
        unpack(archive, stage / "source")
        root = contained(stage / "source", stage / "source" / relative(source["root"]))
        module = (root / "go.mod").read_text(encoding="utf-8").splitlines()
        if "module "+expected["module"] not in module:
            raise ValueError("vendor source has a different Go module owner")
        bundle = stage / "bundle"
        (bundle / "bin").mkdir(parents=True)
        environment = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOTOOLCHAIN="local", GOWORK="off", GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org", GOPRIVATE="", GONOSUMDB="", GONOPROXY="")
        environment.pop("GOFLAGS", None)
        binary = bundle / "bin" / expected["program"]
        print("Preparing native "+expected["program"]+" from its pinned source commit...", flush=True)
        subprocess.run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid= "+expected["flags"], "-o", str(binary), expected["package"]], cwd=root, env=environment, check=True)
        dependencies = go_notices(root, bundle, environment, (expected["package"],))
        source_notices = bundle / "notices" / "source"
        source_notices.mkdir()
        for index, notice in enumerate(source["notices"]):
            path = contained(root, root / relative(notice))
            if not path.is_file():
                raise ValueError("vendor source notice is missing")
            shutil.copyfile(path, source_notices / f"NOTICE-{index}")
        source_receipt = {key:source[key] for key in ("url", "version", "commit", "bytes", "sha256", "notices")}
        if source.get("digest_provenance"):
            source_receipt["digest_provenance"] = source["digest_provenance"]
        (bundle / "source-receipt.json").write_text(json.dumps({"schema":1, "owner":owner, "target":target, "source":source_receipt, "go":"1.27.1", "dependencies":dependencies}, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        name = f"{expected['program']}-{expected['version']}-linux-{arch}.tar.gz"
        archive = output / name
        with archive.open("xb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed, tarfile.open(fileobj=compressed, mode="w|") as packed:
            for path in sorted(bundle.rglob("*")):
                if not path.is_file():
                    continue
                info = tarfile.TarInfo("vendor/"+path.relative_to(bundle).as_posix())
                info.size, info.mode, info.mtime = path.stat().st_size, 0o755 if path.stat().st_mode & 0o111 else 0o644, 0
                with path.open("rb") as source:
                    packed.addfile(info, source)
        receipt = {"target":target, "archive":name, "url":recipe["url"], "version":expected["version"], "root":"vendor", "bytes":archive.stat().st_size, "sha256":digest(archive), "notices":[path.relative_to(bundle).as_posix() for path in sorted((bundle / "notices").rglob("*")) if path.is_file()], "programs":{expected["program"]:"bin/"+expected["program"]}, "source":source_receipt}
        (output / "vendor-receipt.json").write_text(json.dumps(receipt, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        print(output / "vendor-receipt.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    prepare(args.inputs.resolve(), args.output.resolve())
