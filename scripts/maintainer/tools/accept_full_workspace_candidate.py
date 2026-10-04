#!/usr/bin/env python3
"""Accept actual native full workspace transport and revocation, without publishing.

Loads only the receipt-bound core service image into the local Docker cache.
Runs an isolated owned deployment and preserves its state if acceptance fails.
This does not certify execution, Git, browser, sharing or endpoint workflows.
"""
import argparse
from release_config import RELEASE
import asyncio
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import zipfile

from accept_manager_bundle import accept as accept_manager
from build_browser_bundle import digest, relative
from build_full_images import oci_manifest
from mcp_probe import Client, result


async def protocol(binary, root, workspace, invoke):
    client = Client([str(binary), "--root", str(root), "tools", "serve"], workspace)
    await client.start()
    try:
        await client.initialize()
        definitions = result(await client.request("tools/list"))["tools"]
        names = {entry["name"] for entry in definitions}
        expected = {"developer_view", "workspace_read", "workspace_edit", "agent_guidance", "read_image", "write_image", "restore_workspace_file"}
        if names != expected or len(definitions) != len(expected):
            raise ValueError("full workspace discovery exposed unexpected selected authority")
        result(await client.request("tools/call", {"name":"workspace_edit", "arguments":{"action":"create", "path":"full-fixture.txt", "content":"Owned full workspace fixture\n"}}))
        content = result(await client.request("tools/call", {"name":"workspace_read", "arguments":{"action":"file", "path":"full-fixture.txt"}}))
        if "Owned full workspace fixture" not in json.dumps(content):
            raise ValueError("full workspace transport changed its created file")
        invoke("tools", "disable", "workspace")
        denied = await client.request("tools/call", {"name":"workspace_read", "arguments":{"action":"file", "path":"full-fixture.txt"}})
        if "error" not in denied and not denied.get("result", {}).get("isError"):
            raise ValueError("cached full invocation survived disabled selection")
    finally:
        await client.close()


def accept(candidate):
    document = json.loads((candidate / "candidate.json").read_text(encoding="utf-8"))
    target = document.get("target", {})
    native_arch = {"x86_64":"amd64", "aarch64":"arm64", "amd64":"amd64", "arm64":"arm64"}.get(platform.machine().lower())
    if platform.system() != "Linux" or document.get("schema") != 1 or document.get("release") != RELEASE or target != {"os":"linux", "arch":native_arch, "mode":"full"} or document.get("manager") != "manager" or document.get("catalog") != "release/catalog.json" or document.get("images") != "images/images.json":
        raise ValueError("acceptance requires an exact native Linux full candidate")
    manager = candidate / "manager"
    accept_manager(manager)
    receipt = json.loads((manager / "manager-receipt.json").read_text(encoding="utf-8"))
    images = json.loads((candidate / "images" / "images.json").read_text(encoding="utf-8"))
    image = images["images"]["service"]
    if images.get("schema") != 1 or images.get("release") != RELEASE or images.get("target") != target or image.get("owner") != "runtime-core" or image.get("target") != target:
        raise ValueError("core service image differs from its native receipt")
    docker = shutil.which("docker")
    if docker is None:
        raise ValueError("full native acceptance requires local Docker")
    environment = {key:value for key, value in os.environ.items() if key not in {"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_API_VERSION"}}
    environment["DOCKER_API_VERSION"] = "1.47"
    if "archive" in image:
        archive = candidate / "images" / relative(image["archive"])
        if archive.is_symlink() or archive.stat().st_size != image["bytes"] or digest(archive) != image["sha256"] or oci_manifest(archive, target) != image["reference"].split("@", 1)[1]:
            raise ValueError("core service image archive differs")
        subprocess.run([docker, "--host", "unix:///var/run/docker.sock", "image", "load", "--input", str(archive)], env=environment, check=True, timeout=180)
    else:
        from registry_images import verify
        for entry in images["images"].values():
            verify(entry["reference"])
        subprocess.run([docker, "--host", "unix:///var/run/docker.sock", "pull", image["reference"]], env=environment, check=True, timeout=180)
    scratch = Path(tempfile.mkdtemp(prefix="loki-full-workspace-accept-"))
    passed, started = False, False
    binary = scratch / "loki"
    root = scratch / "management"
    try:
        packed_archive = manager / receipt["archive"]
        if digest(packed_archive) != receipt["archive_sha256"]:
            raise ValueError("manager changed after native acceptance")
        with zipfile.ZipFile(packed_archive) as packed:
            binary.write_bytes(packed.read(receipt["binary"]))
        if digest(binary) != receipt["binary_sha256"]:
            raise ValueError("full acceptance command differs from its receipt")
        binary.chmod(0o755)
        def invoke(*arguments):
            completed = subprocess.run([str(binary), "--root", str(root), *arguments], capture_output=True, text=True, timeout=180)
            if completed.returncode:
                raise ValueError("Full candidate command failed: " + " ".join(arguments) + "\n" + completed.stdout + completed.stderr)
            return completed.stdout
        invoke("tools", "configure", "--mode", "full")
        invoke("tools", "install", "workspace", "--catalog", str(candidate / "release" / "catalog.json"), "--archives", str(candidate / "release" / "archives"))
        invoke("tools", "enable", "workspace")
        started = True
        observation = json.loads(invoke("tools", "start"))["observation"]
        if not observation["ready"]:
            raise ValueError("full workspace did not become ready")
        if not json.loads(invoke("doctor"))["healthy"]:
            raise ValueError("full candidate doctor is unhealthy")
        asyncio.run(protocol(binary, root, scratch, invoke))
        passed = True
    finally:
        # A stop failure must preserve the owned record needed for recovery.
        if started:
            invoke("tools", "stop")
        if passed:
            shutil.rmtree(scratch)
        else:
            print(json.dumps({"retained_private_acceptance_root":str(scratch)}), flush=True)
    print(json.dumps({"full_workspace_acceptance":"pass", "target":target, "checks":["native candidate receipts", "private stdio", "selected discovery", "create/read", "cached revocation", "owned stop"], "publication":False}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path)
    accept(parser.parse_args().candidate.resolve())
