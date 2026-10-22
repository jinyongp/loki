#!/usr/bin/env python3
"""Boot and reboot a disposable same-architecture VM without external network."""
import argparse
import hashlib
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
# Ubuntu's immutable release build and its published SHA256SUMS.
BASE = "https://cloud-images.ubuntu.com/minimal/releases/noble/release-20261017/"
IMAGES = {
    "amd64": "b3296484e362ba2e713489a5de6225c719d9500962bde25d15f2f21e512c7658",
    "arm64": "11378ad399741da3884b4343a9b06c1a1bb3d8cb71e309a4a11cadc2b516d0f9",
}


def run(*args):
    subprocess.run([str(a) for a in args], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--devtools", required=True, type=Path)
    args = parser.parse_args()
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if arch not in IMAGES or platform.system() != "Linux":
        raise ValueError("a native supported Linux host is required")
    for program in ["go", "qemu-img", "xorriso", "cloud-localds", "qemu-system-x86_64" if arch == "amd64" else "qemu-system-aarch64"]:
        if not shutil.which(program):
            raise ValueError("missing required VM prerequisite: " + program)
    with tempfile.TemporaryDirectory(prefix="loki-secret-reboot-") as temporary:
        work = Path(temporary)
        payload = work / "payload"
        payload.mkdir()
        env = dict(os.environ, CGO_ENABLED="0")
        for package, name in [("./cmd/loki", "loki"), ("./cmd/secret-broker", "loki-secret-broker")]:
            subprocess.run(["go", "build", "-trimpath", "-o", str(payload / name), package], cwd=ROOT, env=env, check=True)
        shutil.copyfile(args.devtools, payload / "devtools")
        for name in ["secrets.service", "secrets-prepare.service"]:
            shutil.copyfile(ROOT / "packaging/native/systemd" / name, payload / name)
        shutil.copyfile(ROOT / "packaging/native/execution-contract.json", payload / "execution-contract.json")
        shutil.copyfile(ROOT / "config/runtime.toml", payload / "config.toml")
        run("xorriso", "-as", "mkisofs", "-quiet", "-r", "-J", "-V", "LOKIPAYLOAD", "-o", work / "payload.iso", payload)
        image = work / "base.img"
        name = f"ubuntu-24.04-minimal-cloudimg-{arch}.img"
        print(f"Downloading pinned Ubuntu {arch} VM image", flush=True)
        with urllib.request.urlopen(BASE + name, timeout=90) as source, image.open("wb") as output:
            shutil.copyfileobj(source, output)
        with image.open("rb") as source:
            digest = hashlib.file_digest(source, "sha256").hexdigest()
        if digest != IMAGES[arch]:
            raise ValueError("VM image digest mismatch")
        run("qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", image, work / "guest.qcow2", "6G")
        guest = (ROOT / "scripts/verify/secret-reboot-guest.sh").read_text()
        # cloud-init writes one owned test fixture and runs it on both boots.
        # There are no guest passwords, SSH keys, or unlock credentials.
        user = "#cloud-config\nusers: []\nssh_genkeytypes: []\nssh_pwauth: false\npackage_update: false\npackage_upgrade: false\nwrite_files:\n  - path: /var/lib/cloud/scripts/per-boot/loki-secret-reboot\n    permissions: '0700'\n    content: |\n" + "".join("      " + line + "\n" for line in guest.splitlines())
        (work / "user-data").write_text(user)
        (work / "meta-data").write_text("instance-id: loki-secret-reboot\nlocal-hostname: loki-secret-reboot\n")
        # One unconnected QEMU hub gives networkd a configured local link,
        # without a host bridge, socket, user-mode forwarding, DNS or gateway.
        # It avoids wait-online's absent-interface timeout on the arm64 image.
        (work / "network-config").write_text('version: 2\nethernets:\n  fixture:\n    match:\n      macaddress: "52:54:00:12:34:56"\n    dhcp4: false\n    dhcp6: false\n    addresses: [10.77.0.2/24]\n')
        run("cloud-localds", "--network-config=" + str(work / "network-config"), work / "seed.iso", work / "user-data", work / "meta-data")
        kvm = os.access("/dev/kvm", os.R_OK | os.W_OK)
        command = ["qemu-system-x86_64" if arch == "amd64" else "qemu-system-aarch64", "-machine", "q35" if arch == "amd64" else "virt", "-accel", "kvm" if kvm else "tcg,thread=multi", "-cpu", "host" if kvm else ("max" if arch == "amd64" else "cortex-a72"), "-m", "2048", "-smp", "2", "-nographic", "-monitor", "none", "-netdev", "hubport,id=fixture,hubid=0", "-device", "virtio-net-pci,netdev=fixture,mac=52:54:00:12:34:56,romfile=", "-object", "rng-random,filename=/dev/urandom,id=rng0", "-device", "virtio-rng-pci,rng=rng0"]
        if arch == "arm64":
            firmware = Path("/usr/share/AAVMF/AAVMF_CODE.fd")
            variables = Path("/usr/share/AAVMF/AAVMF_VARS.fd")
            if not firmware.is_file() or not variables.is_file():
                raise ValueError("native arm64 UEFI firmware is required")
            shutil.copyfile(variables, work / "uefi-vars.fd")
            command += ["-drive", f"if=pflash,format=raw,readonly=on,file={firmware}", "-drive", f"if=pflash,format=raw,file={work / 'uefi-vars.fd'}"]
        for path, device, fmt in [(work / "guest.qcow2", "virtio", "qcow2"), (work / "seed.iso", "virtio", "raw"), (work / "payload.iso", "virtio", "raw")]:
            command += ["-drive", f"file={path},if={device},format={fmt}" + (",readonly=on" if fmt == "raw" else "")]
        print(f"Running native {arch} VM with {'KVM' if kvm else 'same-architecture TCG'}", flush=True)
        log = work / "serial.log"
        started = time.monotonic()
        with log.open("wb") as output:
            process = subprocess.Popen(command, stdout=output, stderr=subprocess.STDOUT)
            while True:
                try:
                    result = process.wait(timeout=30)
                    break
                except subprocess.TimeoutExpired:
                    elapsed = time.monotonic() - started
                    print(f"VM {arch}: {elapsed:.0f}s elapsed", flush=True)
                    if elapsed >= 900:
                        process.kill()
                        process.wait()
                        result = -1
                        break
        text = log.read_text(errors="replace")
        if result != 0 or "LOKI_SECRET_REBOOT_PASS" not in text or "LOKI_SECRET_REBOOT_FIRST_BOOT_PASS" not in text or "LOKI_SECRET_REBOOT_FAIL" in text:
            print(text[-24000:])
            raise RuntimeError("actual VM reboot verification failed")
        print(f"PASS: new kernel boot ID, fresh broker instance, locked vault, erased tmpfs credential; {arch}, {time.monotonic()-started:.1f}s", flush=True)


if __name__ == "__main__":
    main()
