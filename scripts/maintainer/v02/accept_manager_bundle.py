#!/usr/bin/env python3
"""Final-phase native manager acceptance in an isolated temporary namespace."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import platform
import shutil
import stat
import subprocess
import tempfile
import zipfile


def run(binary, root, *args):
    return subprocess.check_output([str(binary), "--root", str(root), *args], text=True)


def accept(candidate):
    receipt = json.loads((candidate / "manager-receipt.json").read_text(encoding="utf-8"))
    archive = candidate / receipt["archive"]
    if archive.stat().st_size != receipt["archive_bytes"] or hashlib.sha256(archive.read_bytes()).hexdigest() != receipt["archive_sha256"]:
        raise ValueError("candidate archive differs from its receipt")
    if receipt["schema"] != 1 or receipt["release"] != "0.2.0" or receipt["included_tools"] != []:
        raise ValueError("candidate is not a management-only 0.2.0 artifact")
    native_os = {"Windows": "windows", "Darwin": "darwin", "Linux": "linux"}.get(platform.system())
    native_arch = {"AMD64": "amd64", "x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine())
    if receipt["os"] != native_os or receipt["arch"] != native_arch:
        raise ValueError("candidate acceptance requires its actual native execution host")
    if receipt["binary"] != ("loki.exe" if native_os == "windows" else "loki") or receipt["archive"] != f"loki-manager-0.2.0-{native_os}-{native_arch}.zip":
        raise ValueError("candidate receipt declares an unexpected native command or archive path")
    with tempfile.TemporaryDirectory(prefix="loki-manager-accept-") as temporary:
        scratch = Path(temporary)
        bundle = scratch / "bundle"
        bundle.mkdir()
        with zipfile.ZipFile(archive) as packed:
            total = 0
            for entry in packed.infolist():
                relative = PurePosixPath(entry.filename)
                mode = entry.external_attr >> 16
                if relative.is_absolute() or ".." in relative.parts or "\\" in entry.filename or ":" in entry.filename or not stat.S_ISREG(mode):
                    raise ValueError("candidate contains invalid paths or special files")
                total += entry.file_size
                if total > 512 * 1024 * 1024:
                    raise ValueError("manager candidate exceeds extraction limit")
                target = bundle / Path(*relative.parts)
                target.parent.mkdir(parents=True, exist_ok=True)
                with packed.open(entry) as source, target.open("xb") as sink:
                    shutil.copyfileobj(source, sink)
                target.chmod(0o755 if mode & 0o111 else 0o644)
        binary = bundle / receipt["binary"]
        if binary.stat().st_size != receipt["binary_bytes"] or hashlib.sha256(binary.read_bytes()).hexdigest() != receipt["binary_sha256"]:
            raise ValueError("native command differs from the candidate receipt")
        root = scratch / "management state"
        bin_directory = scratch / "command bin"
        if native_os == "windows":
            subprocess.run(["powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", str(bundle / "install.ps1"), "-BinDirectory", str(bin_directory), "-ManagementRoot", str(root)], check=True)
        else:
            subprocess.run(["sh", str(bundle / "install.sh"), str(bin_directory), str(root)], check=True)
        installed = bin_directory / receipt["binary"]
        if run(installed, root, "version").strip() != "loki 0.2.0":
            raise ValueError("installed manager reports an unexpected version")
        status = json.loads(run(installed, root, "status"))
        if not status["installed"] or status["tools"] != {}:
            raise ValueError("management-only installation initialized product tools")
        doctor = json.loads(run(installed, root, "doctor"))
        if not doctor["healthy"] or doctor["issues"]:
            raise ValueError("native installed manager is not healthy")
        run(binary, root, "install", "--bin-dir", str(bin_directory))
        run(binary, root, "tools", "recover")
        unowned_bin = scratch / "unowned bin"
        unowned_bin.mkdir()
        user_command = unowned_bin / receipt["binary"]
        user_command.write_bytes(b"existing user command")
        attempt = subprocess.run([str(binary), "--root", str(root), "install", "--bin-dir", str(unowned_bin)], capture_output=True, text=True)
        if attempt.returncode == 0 or user_command.read_bytes() != b"existing user command":
            raise ValueError("installer replaced an unowned command")
        print(json.dumps({"native_manager_acceptance": "pass", "os": native_os, "arch": native_arch, "archive_sha256": receipt["archive_sha256"], "user_namespace": "isolated temporary directories"}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path)
    args = parser.parse_args()
    accept(args.candidate.resolve())
