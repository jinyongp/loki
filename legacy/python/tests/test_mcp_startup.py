"""Isolated systemd regression tests; run as root in the Ubuntu test distro.

python3 -m unittest discover -s tests -p test_mcp_startup.py -v
Only temporary test units and sockets are used; Loki services are not touched.
"""

import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
import uuid


SOURCE = Path(__file__).resolve().parents[1] / "systemd/loki-mcp.service"
AVAILABLE = os.geteuid() == 0 and Path("/run/systemd/system").is_dir() if os.name == "posix" else False


@unittest.skipUnless(AVAILABLE, "requires root and systemd in an isolated test distro")
class MCPStartupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="loki-startup-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.name = f"loki-startup-test-{uuid.uuid4().hex}.service"
        self.signing = self.root / "signing.sock"
        self.runtime = self.root / "runtime.sock"
        sections = {"Unit": [], "Service": []}
        section = None
        for line in SOURCE.read_text().splitlines():
            if line.startswith("["):
                section = line.strip("[]")
            if section == "Unit" and (
                line.startswith("StartLimit") or line.startswith("ConditionPathExists=/run/")
            ):
                sections[section].append(line)
            if section == "Service" and line.startswith(("ExecStartPre=", "TimeoutStartSec=", "Restart=", "RestartSec=")):
                sections[section].append(line)
        unit = "[Unit]\n" + "\n".join(sections["Unit"])
        # A single start per minute would block recovery without the source's
        # explicit rate-limit policy. Shorten timeouts to keep tests fast.
        unit += "\nStartLimitBurst=1\n[Service]\n" + "\n".join(sections["Service"])
        unit += "\nType=simple\nExecStart=/bin/sleep infinity\n"
        unit = unit.replace("/run/loki/signing/agent.sock", str(self.signing))
        unit = unit.replace("/run/loki/runtime/control.sock", str(self.runtime))
        unit = unit.replace("TimeoutStartSec=30", "TimeoutStartSec=1")
        unit = unit.replace("RestartSec=3", "RestartSec=0.2")
        self.path = self.root / self.name
        self.path.write_text(unit)
        self.addCleanup(self.stop_unit)
        self.ctl("link", "--runtime", str(self.path))
        self.ctl("daemon-reload")

    def ctl(self, *args, check=True):
        return subprocess.run(["systemctl", *args], check=check, capture_output=True, text=True, timeout=15)

    def stop_unit(self):
        self.ctl("stop", self.name, check=False)
        self.ctl("disable", "--runtime", self.name, check=False)
        self.ctl("reset-failed", self.name, check=False)
        self.ctl("daemon-reload")

    def bind(self, path):
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.addCleanup(sock.close)
        sock.bind(str(path))
        sock.listen()

    def value(self, key):
        return self.ctl("show", self.name, "--property", key, "--value").stdout.strip()

    def wait_for(self, predicate):
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(0.05)
        self.fail(self.ctl("status", self.name, check=False).stdout)

    def test_ready_sockets_start_immediately(self):
        self.bind(self.signing)
        self.bind(self.runtime)
        self.ctl("start", self.name)
        self.assertEqual(self.value("ActiveState"), "active")
        self.assertEqual(self.value("NRestarts"), "0")

    def test_delayed_sockets_recover_after_timeout(self):
        self.ctl("start", "--no-block", self.name)
        self.wait_for(lambda: int(self.value("NRestarts")) >= 1)
        self.assertNotEqual(self.value("ActiveState"), "active")
        self.bind(self.runtime)
        self.signing.touch()  # An ordinary file must not pass socket readiness.
        restarts = int(self.value("NRestarts"))
        self.wait_for(lambda: int(self.value("NRestarts")) > restarts)
        self.assertNotEqual(self.value("ActiveState"), "active")
        self.signing.unlink()
        self.bind(self.signing)
        self.wait_for(lambda: self.value("ActiveState") == "active")


if __name__ == "__main__":
    unittest.main()
