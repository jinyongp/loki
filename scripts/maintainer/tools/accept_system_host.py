#!/usr/bin/env python3
"""Native Linux acceptance for the owned administrator socket and cleanup.

This runs only on disposable GitHub Actions Linux runners. It never adopts an
existing service, manager, socket or data root and retains the shared Docker engine.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import zipfile


def accept(candidate):
    if platform.system() != "Linux" or os.geteuid() == 0 or os.environ.get("GITHUB_ACTIONS") != "true":
        raise ValueError("system host acceptance requires a disposable non-root Linux Actions runner")
    uid = os.getuid()
    root = Path(f"/var/lib/loki-tools/{uid}")
    command = Path(f"/usr/local/lib/loki-tools/{uid}/bin/loki")
    socket = Path(f"/run/loki-tools-manager-{uid}.sock")
    units = [Path(f"/etc/systemd/system/loki-tools-manager-{uid}.{suffix}") for suffix in ("service", "socket")]
    for path in [root, command, socket, *units]:
        if subprocess.run(["sudo", "test", "-e", str(path)]).returncode == 0 or subprocess.run(["sudo", "test", "-L", str(path)]).returncode == 0:
            raise ValueError(f"existing administrator namespace is preserved: {path}")
    receipt = json.loads((candidate / "manager-receipt.json").read_text())
    archive = candidate / receipt["archive"]
    if archive.stat().st_size != receipt["archive_bytes"] or hashlib.sha256(archive.read_bytes()).hexdigest() != receipt["archive_sha256"]:
        raise ValueError("system host candidate differs from its verified receipt")
    # accept_manager_bundle.py already verifies the archive's receipt before
    # this step. Extract exactly the receipt-declared native command again.
    with tempfile.TemporaryDirectory(prefix="loki-system-host-accept-") as temporary:
        scratch = Path(temporary)
        binary = scratch / "loki"
        with zipfile.ZipFile(candidate / receipt["archive"]) as packed:
            binary.write_bytes(packed.read(receipt["binary"]))
        if binary.stat().st_size != receipt["binary_bytes"] or hashlib.sha256(binary.read_bytes()).hexdigest() != receipt["binary_sha256"]:
            raise ValueError("system host command differs from its verified receipt")
        binary.chmod(0o755)
        frontend = scratch / "frontend"
        owned = False

        def run(*args, expected=0):
            result = subprocess.run([str(binary), "--root", str(frontend), *args], capture_output=True, text=True, timeout=240)
            if result.returncode != expected:
                raise ValueError(f"system host {args}: {result.returncode}\n{result.stdout}\n{result.stderr}")
            return result.stdout

        try:
            run("install")
            run("hosts", "prepare", "local", "--json")
            owned = True
            status = json.loads(run("status", "--json"))
            if status["target"]["mode"] != "full" or status["tools"] != {}:
                raise ValueError("ordinary commands did not use the remembered administrator host")
            json.loads(run("tools", "plan", "--json"))
            json.loads(run("doctor", "--json"))
            backup = json.loads(run("backup", "--json"))
            json.loads(run("restore", backup["id"], "--json"))
            forbidden = subprocess.run([str(binary), "_system-relay", "--socket", str(socket), "--", "tools", "install", "git", "-catalog=/tmp/untrusted.json"], capture_output=True, text=True, timeout=20)
            if forbidden.returncode == 0 or "outside system-host authority" not in forbidden.stderr:
                raise ValueError("administrator socket admitted a caller-controlled catalog")
            run("uninstall", "--purge-data", "--json")
            run("hosts", "remove", "--purge", "--json")
            owned = False
            for path in [root, command, socket, *units]:
                if subprocess.run(["sudo", "test", "-e", str(path)]).returncode == 0:
                    raise ValueError(f"owned system host resource remains: {path}")
            if json.loads(run("status", "--json"))["target"]["mode"] != "project-host":
                raise ValueError("frontend remained bound to its removed administrator host")
        finally:
            if owned:
                subprocess.run([str(binary), "--root", str(frontend), "hosts", "remove", "--purge"], timeout=240, check=False)
        print(json.dumps({"native_system_host_acceptance": "pass", "uid": uid, "namespace": "owned disposable runner", "docker_retained": True}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", type=Path, required=True)
    accept(parser.parse_args().candidate.resolve())
