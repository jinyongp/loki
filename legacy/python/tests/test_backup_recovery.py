"""Exercise backup recovery without touching services or deployment data."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts/backup-python-deployment.sh"
SERVICES = ["loki-runtime.service", "loki-mcp.service", "loki-cloudflared.service"]
MOCK = """#!/usr/bin/python3
import json, os, signal, sys
from pathlib import Path
name = Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["CALL_LOG"], "a") as log:
    log.write(json.dumps([name, *args]) + "\\n")
mode = os.environ["CASE"]
if name == "id":
    print("0")
elif name == "systemctl":
    if args[0] == "is-active":
        sys.exit(0 if args[-1] in os.environ["ACTIVE"].split() else 3)
    if args[0] == "stop" and mode == "stop_failure":
        sys.exit(5)
    if args[0] == "start" and mode == "restore_failure" and args[1] == "loki-runtime.service":
        sys.exit(6)
elif name == "tar":
    if mode == "backup_failure":
        sys.exit(7)
    if mode == "term":
        os.kill(os.getppid(), signal.SIGTERM)
"""


class BackupRecoveryTest(unittest.TestCase):
    def run_backup(self, mode="success", active=SERVICES):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dispatcher = root / "mock"
            dispatcher.write_text(MOCK)
            dispatcher.chmod(0o755)
            for command in ("id", "mkdir", "systemctl", "tar", "sha256sum"):
                (root / command).symlink_to(dispatcher)
            log = root / "calls.jsonl"
            result = subprocess.run(
                ["/bin/sh", str(SCRIPT), "/var/backups/loki/python-99999999-regression"],
                env={**os.environ, "PATH": str(root), "CALL_LOG": str(log),
                     "CASE": mode, "ACTIVE": " ".join(active)},
                capture_output=True, text=True, timeout=10,
            )
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            starts = [call[2] for call in calls if call[:2] == ["systemctl", "start"]]
            return result, starts

    def test_active_services_restored_on_every_exit_path(self):
        for mode, code in (("success", 0), ("backup_failure", 7),
                           ("stop_failure", 5), ("term", 143)):
            with self.subTest(mode=mode):
                result, starts = self.run_backup(mode)
                self.assertEqual(result.returncode, code, result.stderr)
                self.assertEqual(starts, SERVICES)

    def test_inactive_tunnel_stays_inactive(self):
        result, starts = self.run_backup(active=SERVICES[:2])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(starts, SERVICES[:2])

    def test_inactive_services_stay_inactive(self):
        result, starts = self.run_backup(active=[])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(starts, [])

    def test_restore_failure_is_reported_and_remaining_services_attempted(self):
        result, starts = self.run_backup("restore_failure")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Failed to restore loki-runtime.service", result.stderr)
        self.assertEqual(starts, SERVICES)


if __name__ == "__main__":
    unittest.main()
