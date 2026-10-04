#!/usr/bin/env python3
"""Prepare owned native OCI image candidates without publishing them.

Payload receipts come from build_full_bundle.py --payload-only. Exact native
base images and browser system libraries are independently reviewed inputs.
Image manifests are content digests, never Docker configuration/image IDs.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shlex
import shutil
import ssl
import subprocess
import tarfile
import tempfile
import uuid
from urllib.parse import urlsplit

from build_browser_bundle import contained, digest, materialize, relative, unpack
from build_full_bundle import REQUIRED_PROGRAMS

IMAGE = re.compile(r"[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}")
REPOSITORY = re.compile(r"[a-z0-9][a-z0-9._:/-]*")
ROLE_MODULES = {"service": ("runtime-core",), "gateway": ("runtime-core",), "workload": ("runtime-core", "execution"), "git-workload": ("runtime-core", "execution", "git"), "browser": ("runtime-core",)}
ROLE_OWNER = {"service":"runtime-core", "gateway":"runtime-core", "workload":"execution", "git-workload":"git", "browser":"browser"}
ROLE_IMAGE = {"service":"runtime-core-service", "gateway":"runtime-core-gateway", "workload":"execution-workload", "git-workload":"git-workload", "browser":"browser"}


def verify_payload(directory, target):
    receipt = json.loads((directory / "payload-receipt.json").read_text(encoding="utf-8"))
    if receipt.get("schema") != 1 or receipt.get("release") != "0.2.3" or receipt.get("target") != target or receipt.get("module") not in REQUIRED_PROGRAMS:
        raise ValueError("image input must be an exact-release native payload receipt")
    root = directory / "payload"
    if root.is_symlink() or not root.is_dir():
        raise ValueError("prepared module root must be a real directory")
    files = {path.relative_to(root).as_posix() for path in root.rglob("*") if path.is_file()}
    if any(path.is_symlink() or not path.is_file() and not path.is_dir() for path in root.rglob("*")):
        raise ValueError("prepared image payload must contain only real files and directories")
    if files != set(receipt["files"]):
        raise ValueError("prepared module contains missing or unrecorded files")
    for name, expected in receipt["files"].items():
        path = root / relative(name)
        if path.is_symlink() or contained(root, path) != path.resolve() or path.stat().st_size != expected["bytes"] or digest(path) != expected["sha256"] or bool(path.stat().st_mode & 0o111) != expected["executable"]:
            raise ValueError("prepared module changed after its receipt")
    payload = json.loads((root / "full-runtime.json").read_text(encoding="utf-8"))
    manifest = json.loads((root / "module.json").read_text(encoding="utf-8"))
    if payload.get("schema") != 1 or payload.get("module") != receipt["module"] or payload.get("release") != "0.2.3" or payload.get("target") != target or payload.get("images") != {} or set(payload.get("programs", {})) != REQUIRED_PROGRAMS[receipt["module"]] or manifest.get("id") != receipt["module"] or manifest.get("release") != "0.2.3" or manifest.get("targets") != [target]:
        raise ValueError("prepared payload differs from its exact module program contract")
    for path in payload["programs"].values():
        executable = root / relative(path)
        if not contained(root, executable).is_file() or not executable.stat().st_mode & 0o111:
            raise ValueError("declared image program is not an executable regular file")
    return receipt, root


def oci_manifest(archive, target):
    with tarfile.open(archive, "r:") as source:
        names = source.getnames()
        if len(names) != len(set(names)):
            raise ValueError("duplicate OCI archive members")
        members = {item.name: item for item in source.getmembers()}

        def read(name, maximum=4 << 20):
            member = members.get(name)
            if member is None or not member.isfile() or member.size > maximum:
                raise ValueError("invalid bounded OCI metadata member")
            return source.extractfile(member).read()

        index = json.loads(read("index.json"))
        candidates = [item for item in index.get("manifests", []) if item.get("platform", {}).get("os") == target["os"] and item.get("platform", {}).get("architecture") == target["arch"]]
        # Native exports may put platform metadata on the config rather than
        # the index descriptor. Attestations are disabled for this producer.
        if not candidates and len(index.get("manifests", [])) == 1:
            candidates = index["manifests"]
        if len(candidates) != 1:
            raise ValueError("OCI export has no unique native manifest")
        descriptor = candidates[0]
        manifest_digest = descriptor.get("digest", "")
        if not re.fullmatch(r"sha256:[a-f0-9]{64}", manifest_digest):
            raise ValueError("OCI descriptor is not a SHA-256 content digest")
        raw = read("blobs/sha256/"+manifest_digest[7:])
        if len(raw) != descriptor["size"] or hashlib.sha256(raw).hexdigest() != manifest_digest[7:]:
            raise ValueError("OCI manifest differs from its descriptor")
        manifest = json.loads(raw)
        config_digest = manifest["config"]["digest"]
        if not re.fullmatch(r"sha256:[a-f0-9]{64}", config_digest):
            raise ValueError("invalid OCI config digest")
        config_raw = read("blobs/sha256/"+config_digest[7:])
        if len(config_raw) != manifest["config"]["size"] or hashlib.sha256(config_raw).hexdigest() != config_digest[7:]:
            raise ValueError("OCI configuration integrity mismatch")
        configuration = json.loads(config_raw)
        if configuration.get("os") != target["os"] or configuration.get("architecture") != target["arch"]:
            raise ValueError("exported image is not native to its recipe")
        if manifest.get("schemaVersion") != 2:
            raise ValueError("unsupported OCI manifest schema")
        for layer in manifest.get("layers", []):
            value = layer.get("digest", "")
            if not re.fullmatch(r"sha256:[a-f0-9]{64}", value) or not isinstance(layer.get("size"), int) or not 0 <= layer["size"] <= 32 << 30:
                raise ValueError("invalid bounded OCI layer descriptor")
            member = members.get("blobs/sha256/"+value[7:])
            if member is None or not member.isfile() or member.size != layer["size"]:
                raise ValueError("OCI layer is missing or has changed size")
            checksum = hashlib.sha256()
            with source.extractfile(member) as stream:
                for chunk in iter(lambda: stream.read(1024*1024), b""):
                    checksum.update(chunk)
            if checksum.hexdigest() != value[7:]:
                raise ValueError("OCI layer content differs from its manifest")
        return manifest_digest


def assemble(recipe_path, output):
    arch = {"amd64":"amd64", "x86_64":"amd64", "aarch64":"arm64", "arm64":"arm64"}.get(platform.machine().lower())
    target = {"os":"linux", "arch":arch, "mode":"full"}
    recipe = json.loads(recipe_path.read_text(encoding="utf-8"))
    if platform.system() != "Linux" or arch is None or recipe.get("schema") != 1 or recipe.get("target") != target or not IMAGE.fullmatch(recipe.get("base", "")) or not recipe.get("base_notices") or not IMAGE.fullmatch(recipe.get("buildkit", "")) or not IMAGE.fullmatch(recipe.get("frontend", "")):
        raise ValueError("owned image preparation requires a pinned native Linux base and its notice receipt")
    repository = recipe.get("repository", "")
    if not REPOSITORY.fullmatch(repository) or ".." in repository or "@" in repository:
        raise ValueError("image repository must be a public publication destination without credentials")
    payloads = {}
    for module, path in recipe.get("payloads", {}).items():
        receipt, root = verify_payload((recipe_path.parent / path).resolve(), target)
        if receipt["module"] != module:
            raise ValueError("prepared module owner differs from the image recipe")
        payloads[module] = (receipt, root)
    roles = recipe.get("roles", [])
    if not roles or len(roles) != len(set(roles)) or any(role not in ROLE_MODULES for role in roles):
        raise ValueError("image roles must be a finite unique owned set")
    output.mkdir(parents=True, exist_ok=False)
    receipts = {}
    with tempfile.TemporaryDirectory(prefix="loki-full-images-") as temporary:
        scratch = Path(temporary)
        trust = recipe.get("trust_store", {})
        archive = (recipe_path.parent / trust.get("archive", "")).resolve()
        provenance = urlsplit(trust.get("url", ""))
        if trust.get("target") != target or not trust.get("notices") or not trust.get("version") or provenance.scheme != "https" or not provenance.hostname or provenance.username or provenance.fragment or archive.is_symlink() or not archive.is_file() or archive.stat().st_size != trust.get("bytes") or digest(archive) != trust.get("sha256"):
            raise ValueError("full images require an independently reviewed public certificate trust-store archive")
        trust_root = scratch / "trust-store"
        unpack(archive, trust_root)
        trust_source = contained(trust_root, trust_root / relative(trust["root"]))
        certificate_file = contained(trust_source, trust_source / relative(trust["certificates"]))
        if not certificate_file.is_file() or certificate_file.stat().st_size > 4 << 20:
            raise ValueError("public certificate bundle exceeds its supported bound")
        certificates = certificate_file.read_text(encoding="ascii")
        blocks = re.findall(r"-----BEGIN CERTIFICATE-----\s+[A-Za-z0-9+/=\s]+-----END CERTIFICATE-----", certificates)
        if not blocks or re.sub(r"\s", "", certificates) != re.sub(r"\s", "", "".join(blocks)):
            raise ValueError("trust-store payload must contain only public PEM certificates")
        for block in blocks:
            ssl.PEM_cert_to_DER_cert(block)
        for notice in trust["notices"]:
            if not contained(trust_source, trust_source / relative(notice)).is_file():
                raise ValueError("public trust-store notice is missing")
        environment = dict(os.environ)
        for name in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_API_VERSION", "DOCKER_CONFIG", "BUILDX_BUILDER", "BUILDX_CONFIG", "BUILDKIT_HOST"):
            environment.pop(name, None)
        configuration = scratch / "docker-configuration"
        configuration.mkdir(mode=0o700)
        environment["DOCKER_CONFIG"] = str(configuration)
        builder = "loki-tools-candidate-"+uuid.uuid4().hex
        docker = ["docker", "--host", "unix:///var/run/docker.sock"]
        try:
            subprocess.run([*docker, "buildx", "create", "--name", builder, "--driver", "docker-container", "--driver-opt", "image="+recipe["buildkit"], "unix:///var/run/docker.sock"], env=environment, check=True, timeout=60)
            for role in roles:
                context = scratch / role
                context.mkdir()
                lines = ["# syntax="+recipe["frontend"], "FROM "+recipe["base"], "LABEL org.opencontainers.image.source=https://github.com/jinyongp/loki io.loki.release=0.2.3 io.loki.image.owner="+ROLE_OWNER[role]+" io.loki.image.role="+role]
                (context / "public-trust").mkdir()
                shutil.copyfile(certificate_file, context / "public-trust" / "ca-certificates.crt")
                for index, notice in enumerate(trust["notices"]):
                    shutil.copyfile(contained(trust_source, trust_source / relative(notice)), context / "public-trust" / f"NOTICE-{index}")
                lines.extend(["COPY public-trust/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt", "COPY public-trust/ /usr/share/doc/loki-public-trust/"])
                inputs = {"trust_store":trust}
                for module in ROLE_MODULES[role]:
                    if module not in payloads:
                        raise ValueError("image prerequisite payload was not prepared")
                    receipt, root = payloads[module]
                    materialize(root, context / module)
                    lines.append("COPY "+module+" /opt/loki/modules/"+module+"/")
                    inputs[module] = receipt
                if role == "browser":
                    libraries = recipe.get("browser_libraries", {})
                    archive = (recipe_path.parent / libraries.get("archive", "")).resolve()
                    provenance = urlsplit(libraries.get("url", ""))
                    if libraries.get("target") != target or not libraries.get("notices") or provenance.scheme != "https" or not provenance.hostname or provenance.username or provenance.fragment or not libraries.get("version") or archive.stat().st_size != libraries.get("bytes") or digest(archive) != libraries.get("sha256"):
                        raise ValueError("browser libraries require independent native integrity and notices")
                    extracted = scratch / "browser-libraries"
                    unpack(archive, extracted)
                    root = contained(extracted, extracted / relative(libraries["root"]))
                    for notice in libraries["notices"]:
                        if not contained(root, root / relative(notice)).is_file():
                            raise ValueError("browser system library notice is missing")
                    # This dedicated image receives only the reviewed runtime
                    # library closure, fonts and notices. No host state is copied.
                    if set(item.name for item in root.iterdir()) - {"usr", "lib", "lib64", "etc"}:
                        raise ValueError("browser native closure has an unexpected root")
                    materialize(root, context / "browser-libraries")
                    allowed = ("usr/lib/", "usr/share/fonts/", "usr/share/fontconfig/", "usr/share/glib-2.0/", "usr/share/doc/", "usr/share/licenses/", "usr/share/common-licenses/", "etc/fonts/", "etc/ssl/certs/", "lib/", "lib64/")
                    if any(path.is_file() and not path.relative_to(root).as_posix().startswith(allowed) for path in root.rglob("*")):
                        raise ValueError("browser system closure contains non-library, font, trust or notice files")
                    # Base package notice directories may be aliases. Keep
                    # module notices under their own owner instead of writing
                    # materialized directories over those base aliases.
                    notice_context = context / "browser-notices"
                    notice_context.mkdir()
                    for directory in ("doc", "licenses", "common-licenses"):
                        path = context / "browser-libraries/usr/share" / directory
                        if path.is_dir():
                            shutil.move(str(path), str(notice_context / directory))
                    lines.append("COPY browser-notices/ /usr/share/doc/loki-browser-native/")
                    inputs["browser_libraries_notice_root"] = "/usr/share/doc/loki-browser-native"
                    # Ubuntu's merged /usr layout has root /lib aliases.
                    # Copy closure directories to their real destinations.
                    for directory, destination in (("usr", "/usr/"), ("etc", "/etc/"), ("lib", "/usr/lib/"), ("lib64", "/usr/lib64/")):
                        if (root / directory).is_dir():
                            lines.append("COPY browser-libraries/"+directory+"/ "+destination)
                    inputs["browser_libraries"] = libraries
                if role == "git-workload":
                    git_payload = json.loads((payloads["git"][1] / "full-runtime.json").read_text(encoding="utf-8"))
                    for program, path in git_payload["programs"].items():
                        # Native wrappers locate their closure through $0.
                        # Invoke their actual owned path instead of an alias.
                        wrapper = '#!/bin/sh\\nexec '+shlex.quote("/opt/loki/modules/git/"+relative(path).as_posix())+' "$@"\\n'
                        lines.append("RUN --network=none mkdir -p /opt/loki/bin && printf '%b' "+shlex.quote(wrapper)+" > /opt/loki/bin/"+program+" && chmod 755 /opt/loki/bin/"+program)
                    configuration = relative(git_payload["assets"]["gitconfig"]).as_posix()
                    lines.append("RUN --network=none mkdir -p /etc/loki && cp "+shlex.quote("/opt/loki/modules/git/"+configuration)+" /etc/loki/gitconfig")
                core_payload = json.loads((payloads["runtime-core"][1] / "full-runtime.json").read_text(encoding="utf-8"))
                worker = "/opt/loki/modules/runtime-core/"+relative(core_payload["programs"]["loki"]).as_posix()
                lines.append("RUN --network=none mkdir -p /opt/loki/bin && ln -s "+shlex.quote(worker)+" /opt/loki/bin/loki")
                if role in ("workload", "git-workload"):
                    for program in ("node", "npm", "npx", "pnpm", "python", "python3", "uv", "uvx", "rustc", "cargo", "rustfmt", "cargo-fmt", "clippy-driver", "cargo-clippy", "rust-analyzer", "go", "gofmt"):
                        lines.append("RUN --network=none ln -s "+shlex.quote(worker)+" /opt/loki/bin/"+program)
                identities = "runner:x:10000:10000::/home/runner:/bin/sh\\nloki:x:10001:10001::/nonexistent:/usr/sbin/nologin\\negress:x:10002:10002::/nonexistent:/usr/sbin/nologin\\nbrowser:x:10003:10003::/nonexistent:/usr/sbin/nologin\\nexecutor:x:10004:10004::/nonexistent:/usr/sbin/nologin\\nbrowser-proxy:x:10005:10005::/nonexistent:/usr/sbin/nologin\\n"
                lines.extend(["RUN --network=none mkdir -p /workspace /var/tmp/loki /home/runner && chmod 1777 /tmp /var/tmp && chown 10000:10000 /home/runner && printf '"+identities+"' >> /etc/passwd && printf 'runner:x:10000:\\nloki:x:10001:\\n' >> /etc/group", "ENV PATH=/opt/loki/bin:/usr/bin:/bin", "WORKDIR /workspace", "USER 10000:10000"])
                (context / "Dockerfile").write_text("\n".join(lines)+"\n", encoding="utf-8")
                name = f"loki-{ROLE_IMAGE[role]}-0.2.3-linux-{arch}.oci.tar"
                archive = output / name
                if any(c in str(archive) for c in ",\r\n\x00"):
                    raise ValueError("OCI output path cannot contain exporter separators")
                print("Preparing owned native "+role+" image...", flush=True)
                image_name = repository+"/"+ROLE_IMAGE[role]+":0.2.3-linux-"+arch
                subprocess.run([*docker, "buildx", "build", "--builder", builder, "--platform", "linux/"+arch, "--network", "none", "--provenance=false", "--sbom=false", "--output", "type=oci,name="+image_name+",dest="+str(archive), str(context)], env=environment, check=True, timeout=600)
                manifest = oci_manifest(archive, target)
                receipts[role] = {"owner":ROLE_OWNER[role], "target":target, "reference":repository+"/"+ROLE_IMAGE[role]+"@"+manifest, "archive":name, "bytes":archive.stat().st_size, "sha256":digest(archive), "notices":recipe["base_notices"], "base":recipe["base"], "buildkit":recipe["buildkit"], "frontend":recipe["frontend"], "inputs":inputs, "published":False, "accepted":False}
        finally:
            # Preserve the preparation failure if creation/building failed.
            # A successful preparation still requires successful cleanup.
            import sys
            failing = sys.exc_info()[0] is not None
            cleanup = subprocess.run([*docker, "buildx", "rm", "--force", builder], env=environment, check=False, timeout=60)
            if cleanup.returncode and not failing:
                raise RuntimeError("owned candidate builder cleanup failed")
    (output / "images.json").write_text(json.dumps({"schema":1, "release":"0.2.3", "target":target, "images":receipts}, indent=2, sort_keys=True)+"\n", encoding="utf-8")
    print(output / "images.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    assemble(args.inputs.resolve(), args.output.resolve())
