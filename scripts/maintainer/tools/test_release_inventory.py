"""Publication inventory tests use independent fixture bytes and exact file sets."""
import copy
import hashlib
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from release_pipeline import write, read, digest
from release_inventory import create, validate, verify, record
from release_gate import evidence, seal_publication


class InventoryTests(unittest.TestCase):
    def test_inventory_json_rejects_duplicate_names(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/"inventory.json"
            path.write_text('{"assets":{"manager.zip":{},"manager.zip":{}}}')
            with self.assertRaisesRegex(ValueError, "duplicate"): read(path)

    def fixture(self, root):
        target = {"os": "linux", "arch": "amd64", "mode": "project-host"}
        unit = dict(kind="manager", target=target, fingerprint="a"*64, validation="b"*64, version="0.2.5", action="build")
        plan = dict(source_commit="c"*40, release="0.2.5", units={"manager:linux:amd64": unit},
                    matrices={name: ([target] if name in ("manager", "manager-checks") else []) for name in ("manager", "browser", "full", "manager-checks", "browser-checks", "full-checks")})
        payload = b"independent fixture archive"
        assets = root/"assets"; assets.mkdir()
        (assets/"manager.zip").write_bytes(payload)
        artifact = dict(url="https://github.com/jinyongp/loki/releases/download/v0.2.5/manager.zip", bytes=len(payload), sha256=hashlib.sha256(payload).hexdigest())
        lock = dict(source_commit=plan["source_commit"], release=plan["release"], units={"manager:linux:amd64": dict(unit, artifact=artifact)})
        write(assets/"loki-release-lock.json", lock)
        inventory = create(plan, root)
        write(root/"checks/acceptance.json", evidence(plan, "manager", "linux", "amd64", digest(assets/"loki-release-lock.json")))
        results = {"plan": "success", "source-checks": "success", "assemble": "success", "manager-build": "success", "manager-checks": "success", "browser-build": "skipped", "full-build": "skipped", "browser-checks": "skipped", "full-checks": "skipped"}
        return plan, inventory, results, payload

    def test_validation_only_checks_actual_bytes_and_final_checksums(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); plan, inventory, results, payload = self.fixture(root)
            final = seal_publication(plan, root, root/"checks", results, True)
            self.assertEqual(final["phase"], "sealed")
            self.assertEqual(final["assets"]["manager.zip"], dict(bytes=len(payload), sha256=hashlib.sha256(payload).hexdigest()))
            sums = (root/"assets/SHA256SUMS").read_text()
            self.assertIn(hashlib.sha256(payload).hexdigest()+"  manager.zip\n", sums)
            self.assertNotIn("  SHA256SUMS", sums)
            self.assertEqual(final["assets"]["SHA256SUMS"], record(root/"assets/SHA256SUMS"))

    def test_read_only_gate_seals_without_archive_then_publisher_verifies(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); plan, inventory, results, payload = self.fixture(root)
            (root/"assets/manager.zip").unlink()
            final = seal_publication(plan, root, root/"checks", results, False)
            with self.assertRaisesRegex(ValueError, "file set"):
                verify(root, final, plan["source_commit"], plan["release"])
            (root/"assets/manager.zip").write_bytes(payload)
            verify(root, final, plan["source_commit"], plan["release"])

    def test_verifier_rejects_extra_missing_changed_and_nonregular_files(self):
        for fault in ("extra", "missing", "digest", "length", "directory", "symlink", "checksum"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temp:
                root = Path(temp); plan, _, results, payload = self.fixture(root)
                final = seal_publication(plan, root, root/"checks", results, True)
                path = root/"assets/manager.zip"
                if fault == "extra": (root/"assets/extra.zip").write_bytes(b"unexpected")
                elif fault == "missing": path.unlink()
                elif fault == "digest": path.write_bytes(b"x"*len(payload))
                elif fault == "length": path.write_bytes(payload+b"x")
                elif fault == "directory": path.unlink(); path.mkdir()
                elif fault == "symlink": path.unlink(); path.symlink_to(root/"assets/SHA256SUMS")
                else: (root/"assets/SHA256SUMS").write_text("wrong")
                with self.assertRaises(ValueError): verify(root, final, plan["source_commit"], plan["release"])

    def test_inventory_rejects_foreign_and_future_cohorts(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); plan, inventory, _, _ = self.fixture(root)
            for field, value in (("source_commit", "d"*40), ("release", "0.2.6"), ("run_id", "other"), ("run_attempt", "2"), ("assembled_lock_sha256", "e"*64)):
                bad = dict(inventory, **{field: value})
                with self.subTest(field=field), self.assertRaises(ValueError): validate(plan, root, bad)
            with patch.dict(os.environ, {"GITHUB_RUN_ATTEMPT": "2"}): validate(plan, root, inventory)

    def test_missing_built_archive_and_tampered_unit_binding_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); plan, inventory, _, _ = self.fixture(root)
            bad = copy.deepcopy(inventory); bad["units"]["manager:linux:amd64"]["sha256"] = "e"*64
            with self.assertRaises(ValueError): validate(plan, root, bad)
            (root/"assets/manager.zip").unlink()
            with self.assertRaisesRegex(ValueError, "missing"): create(plan, root)

    def test_recovery_preserves_original_lock_and_checksum_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); plan, _, results, _ = self.fixture(root)
            seal_publication(plan, root, root/"checks", results, True)
            original = {p.name: p.read_bytes() for p in (root/"assets").iterdir()}
            plan["publication_recovery"] = [dict(url="https://github.com/jinyongp/loki/releases/download/v0.2.5/"+name, bytes=len(data), sha256=hashlib.sha256(data).hexdigest()) for name, data in original.items()]
            create(plan, root)
            final = seal_publication(plan, root, root/"checks", results, True)
            self.assertEqual(original, {p.name: p.read_bytes() for p in (root/"assets").iterdir()})
            plan["publication_recovery"][0]["sha256"] = "e"*64
            with self.assertRaises(ValueError): create(plan, root)


if __name__ == "__main__":
    unittest.main()
