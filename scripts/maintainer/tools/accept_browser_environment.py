#!/usr/bin/env python3
"""Real browser lifecycle on minimal disposable Linux execution hosts.

Requires Docker, a native Linux manager, and its trusted project-host catalog
and digest-named archives. Only disposable containers install system packages.
The container permits user namespaces; Chrome's own sandbox remains enabled.
This does not certify Windows/WSL transport or desktop application rendering.
"""
import argparse
import asyncio
from concurrent.futures import ThreadPoolExecutor
import errno
import hashlib
import json
import os
from pathlib import Path
import pty
import select
import shutil
import subprocess
import tempfile
import time
import tomllib
import uuid
import zipfile

from accept_browser_candidate import protocol


def accept(manager, catalog, archives, image, output):
    output.mkdir(parents=True, exist_ok=False)
    inputs = output / "inputs"
    inputs.mkdir(mode=0o755)
    shutil.copyfile(manager, inputs / "loki")
    (inputs / "loki").chmod(0o755)
    document = json.loads(catalog.read_text())
    artifacts = [a for a in document["artifacts"] if a["module"] == "browser"]
    if len(artifacts) != 1 or artifacts[0]["target"]["os"] != "linux" or artifacts[0]["target"]["mode"] != "project-host":
        raise ValueError("requires one Linux project-host browser artifact")
    artifact = artifacts[0]
    archive = archives / (artifact["sha256"] + ".zip")
    if archive.stat().st_size != artifact["bytes"] or hashlib.file_digest(archive.open("rb"), "sha256").hexdigest() != artifact["sha256"]:
        raise ValueError("browser archive differs from trusted catalog")
    shutil.copyfile(catalog, inputs / "catalog.json")
    shutil.copyfile(archive, inputs / archive.name)
    container = "loki-browser-accept-" + uuid.uuid4().hex[:12]
    log = (output / "commands.log").open("w")
    created = False

    def command(argv, *, data=None, check=True, timeout=900):
        completed = subprocess.run(argv, input=data, capture_output=True, text=True, timeout=timeout)
        log.write("$ " + " ".join(argv) + "\n" + completed.stdout + completed.stderr + "\n")
        log.flush()
        if check and completed.returncode:
            raise ValueError("command failed: " + " ".join(argv) + "\n" + completed.stdout[-8192:] + completed.stderr[-8192:])
        return completed

    prefix = ["docker", "exec", "--user", "lokitest", container]
    root = "/home/lokitest/management"
    binary = "/home/lokitest/bin/loki"
    cli = [binary, "--host", "local", "--root", root]
    install = ["tools", "install", "browser", "--catalog", "/inputs/catalog.json", "--archives", "/inputs"]

    def invoke(*args):
        return command(prefix + cli + list(args)).stdout

    def state():
        return json.loads(invoke("status", "--json"))

    def read(path):
        return command(prefix + ["cat", path]).stdout

    try:
        command(["docker", "run", "-d", "--init", "--name", container, "--label", "loki.test=browser-environment",
                 "--security-opt", "seccomp=unconfined", "-v", str(inputs.resolve()) + ":/inputs:ro", image, "sleep", "infinity"])
        created = True
        image_id = command(["docker", "inspect", "--format", "{{.Image}}", container]).stdout.strip()
        print(image + ": preparing minimal host", flush=True)
        command(["docker", "exec", container, "sh", "-c", "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends sudo && useradd -m -s /bin/bash -G sudo lokitest"])
        command(["docker", "exec", "-i", container, "chpasswd"], data="lokitest:loki-disposable-fixture\n")
        command(prefix + ["/inputs/loki", "--host", "local", "--root", root, "install", "--bin-dir", "/home/lokitest/bin"])
        assert state()["tools"] == {}, "CLI bootstrap installed tools"
        report = json.loads(invoke("_browser-environment"))
        assert report["administrator_required"] and "libatomic1" in report["missing_packages"]
        (output / "before.json").write_text(json.dumps(report, indent=2))

        # Exercise the public terminal prompt and real sudo, not a fake runner.
        master, slave = pty.openpty()
        process = subprocess.Popen(["docker", "exec", "-it", "--user", "lokitest", container] + cli + install,
                                   stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
        os.close(slave)
        terminal = bytearray()
        sent = False
        deadline = time.monotonic() + 900
        try:
            while time.monotonic() < deadline:
                if select.select([master], [], [], 0.2)[0]:
                    try:
                        chunk = os.read(master, 65536)
                    except OSError as error:
                        if error.errno == errno.EIO:
                            break
                        raise
                    if not chunk:
                        break
                    terminal.extend(chunk)
                    if not sent and b"(hidden):" in terminal:
                        time.sleep(0.1)
                        os.write(master, b"loki-disposable-fixture\n")
                        sent = True
                if process.poll() is not None:
                    break
            else:
                raise TimeoutError("public browser installation timed out")
            assert process.wait(timeout=10) == 0, "public install failed; see public-install.log"
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            os.close(master)
            (output / "public-install.log").write_bytes(terminal)
        assert sent and b"loki-disposable-fixture" not in terminal, "administrator prompt missing or echoed password"
        assert b"Browser runtime verified." in terminal, "installation did not observe browser startup"
        assert json.loads(invoke("_browser-environment"))["missing_packages"] == []
        assert not state()["tools"]["browser"]["enabled"], "install activated browser"
        # An installed receipt cannot mask a damaged executable. The same
        # public install must fail until the runtime is repaired, retaining data.
        snapshot = json.loads(read(root + "/control/state.json"))
        digest = snapshot["installed"]["browser"]["artifact"]["sha256"]
        chrome = root + "/tools/browser/generations/" + digest + "/chrome/chrome"
        command(prefix + ["chmod", "0644", chrome])
        broken = command(prefix + cli + install, check=False)
        assert broken.returncode and "runtime verification failed" in broken.stderr and "activation is separate" not in broken.stdout, "damaged browser was reported installed successfully"
        assert not state()["tools"]["browser"]["enabled"], "failed verification enabled browser"
        command(prefix + ["chmod", "0755", chrome])
        apt_before = read("/var/log/apt/history.log")
        invoke(*install)
        assert read("/var/log/apt/history.log") == apt_before, "repeat install changed system packages"

        config = "/home/lokitest/.codex/config.toml"
        original = '# User configuration\nmodel = "fixture"\n[mcp_servers.other]\ncommand = "other"\n'
        command(prefix + ["mkdir", "-p", "/home/lokitest/.codex"])
        command(["docker", "exec", "-i", "--user", "lokitest", container, "sh", "-c", 'cat > "$1"', "fixture", config], data=original)
        rejected = command(prefix + cli + ["connections", "setup", "codex"], check=False)
        assert rejected.returncode and read(config) == original, "disabled tools altered connection configuration"
        invoke("tools", "enable", "browser")
        invoke("doctor")
        invoke("connections", "setup", "codex")
        configured = read(config)
        assert tomllib.loads(configured)["mcp_servers"]["other"]["command"] == "other"
        invoke("connections", "setup", "codex")
        assert read(config) == configured, "repeat connection setup changed configuration"
        connection = tomllib.loads(configured)["mcp_servers"]["loki"]
        assert "--workspace" not in connection["args"], "default connection granted project files"
        workspace = output / "workspace"
        workspace.mkdir()
        launch = ["docker", "exec", "-i", "--user", "lokitest", container, connection["command"], *connection["args"]]
        for _ in range(2):
            invoke("tools", "enable", "browser")
            asyncio.run(protocol(Path(binary), Path(root), workspace, invoke, launch=launch))
            assert not state()["tools"]["browser"]["enabled"]
        processes = command(["docker", "top", container, "-eo", "pid,stat,args"]).stdout
        assert not any("chrome/chrome " in line and len(line.split()) > 1 and not line.split()[1].startswith("Z") for line in processes.splitlines()), "Chrome outlived its MCP session"
        invoke("tools", "update", "--catalog", "/inputs/catalog.json", "--archives", "/inputs")
        assert not state()["tools"]["browser"]["enabled"], "update changed activation"
        invoke("connections", "remove", "codex")
        remaining = tomllib.loads(read(config))
        assert remaining == tomllib.loads(original), "disconnect changed unrelated configuration"
        marker = root + "/retained-user-data"
        command(["docker", "exec", "-i", "--user", "lokitest", container, "sh", "-c", 'cat > "$1"', "fixture", marker], data="keep\n")
        invoke("uninstall", "browser")
        assert "browser" not in state()["tools"] and read(marker) == "keep\n"
        invoke(*install)
        assert not state()["tools"]["browser"]["enabled"]
        invoke("tools", "enable", "browser")
        invoke("doctor")
        assert read(marker) == "keep\n"
        invoke("uninstall", "--all")
        assert state()["tools"] == {} and read(marker) == "keep\n"
        result = {"result": "pass", "image": image, "image_id": image_id,
                  "manager_sha256": hashlib.file_digest(manager.open("rb"), "sha256").hexdigest(),
                  "browser_sha256": artifact["sha256"], "container_user_namespace_policy": "seccomp=unconfined; Chrome sandbox enabled",
                  "checks": ["CLI-only-install", "real-sudo-public-install", "runtime-startup-screenshot-video", "damaged-runtime-rejected", "repeat-install-no-apt", "install-enable-separation", "disabled-connect-rejected", "preserve-client-config", "idempotent-connect", "both-engines-generated-connection", "screenshot-file-transfer", "disable-revocation", "re-enable", "process-cleanup", "update-keeps-activation", "disconnect", "uninstall-keeps-data", "reinstall", "uninstall-all"]}
        (output / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result), flush=True)
    finally:
        if created:
            command(["docker", "rm", "-f", container], check=False, timeout=30)
        log.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--manager", type=Path)
    source.add_argument("--candidate", type=Path)
    parser.add_argument("--catalog", type=Path)
    parser.add_argument("--archives", type=Path)
    parser.add_argument("--image", action="append", help="repeat to check isolated distributions concurrently")
    parser.add_argument("--output", required=True, type=Path)
    options = parser.parse_args()
    if options.candidate and (options.catalog or options.archives):
        parser.error("use --candidate or --manager with --catalog and --archives")
    if options.manager and (not options.catalog or not options.archives):
        parser.error("--manager requires --catalog and --archives")
    with tempfile.TemporaryDirectory(prefix="loki-browser-host-manager-") as scratch:
        if options.candidate:
            candidate = options.candidate.resolve()
            receipt = json.loads((candidate / "manager/manager-receipt.json").read_text())
            if receipt["os"] != "linux" or receipt["binary"] != "loki":
                parser.error("minimal host acceptance requires a Linux manager candidate")
            archive = candidate / "manager" / receipt["archive"]
            if hashlib.file_digest(archive.open("rb"), "sha256").hexdigest() != receipt["archive_sha256"]:
                raise ValueError("candidate manager archive differs from its receipt")
            options.manager = Path(scratch) / "loki"
            with zipfile.ZipFile(archive) as packed:
                options.manager.write_bytes(packed.read("loki"))
            if hashlib.file_digest(options.manager.open("rb"), "sha256").hexdigest() != receipt["binary_sha256"]:
                raise ValueError("candidate manager differs from its receipt")
            options.catalog = candidate / "release/catalog.json"
            options.archives = candidate / "release/archives"
        images = options.image or ["ubuntu:24.04"]
        if len(set(images)) != len(images):
            parser.error("duplicate image")
        output = options.output.resolve()
        if len(images) > 1:
            output.mkdir(parents=True, exist_ok=False)
        with ThreadPoolExecutor(max_workers=4) as pool:
            futures = [pool.submit(accept, options.manager.resolve(), options.catalog.resolve(), options.archives.resolve(), image,
                                   output / str(index) if len(images) > 1 else output) for index, image in enumerate(images)]
            # Inspect every result; independent hosts finish and clean up even
            # when a sibling fails. The command fails if any host failed.
            failures = []
            for image, future in zip(images, futures):
                try:
                    future.result()
                except Exception as error:
                    failures.append(image + ": " + str(error))
            if failures:
                raise ValueError("\n".join(failures))


if __name__ == "__main__":
    main()
