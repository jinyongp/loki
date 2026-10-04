"""Publication trust checks: bytes, paths and complete native manager coverage."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from prepare_release import check, prepare, read


class ReleaseTrustTests(unittest.TestCase):
    def test_changed_asset_and_receipt_alias_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "fixture.zip"
            archive.write_bytes(b"accepted bytes")
            expected = {"bytes":archive.stat().st_size, "sha256":hashlib.sha256(archive.read_bytes()).hexdigest()}
            check(archive, expected)
            archive.write_bytes(b"different data")
            with self.assertRaises(ValueError):
                check(archive, expected)
            receipt = root / "receipt.json"
            receipt.write_text(json.dumps({"schema":1}))
            alias = root / "alias.json"
            alias.symlink_to(receipt)
            with self.assertRaises(ValueError):
                read(alias)

    def test_missing_native_candidates_cannot_be_published(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in ("managers", "browsers", "full"):
                (root / name).mkdir()
            with self.assertRaisesRegex(ValueError, "six accepted native managers"):
                prepare(root / "managers", root / "browsers", root / "full", root / "publication", "a" * 40)


if __name__ == "__main__":
    unittest.main()
