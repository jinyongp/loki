"""Final-phase fixtures for vendor tree handling; no upstream process runs."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location("bundle", Path(__file__).with_name("build_browser_bundle.py"))
bundle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bundle)

acquisition_spec = importlib.util.spec_from_file_location("acquisition", Path(__file__).with_name("acquire_browser_inputs.py"))
acquisition = importlib.util.module_from_spec(acquisition_spec)
acquisition_spec.loader.exec_module(acquisition)


class VendorInputs(unittest.TestCase):
    def test_chrome_signature_scope_resolves_root_alias(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            actual = root / "actual"
            executable = "Chrome.app/Contents/MacOS/Chrome"
            binary = actual / executable
            binary.parent.mkdir(parents=True)
            binary.write_bytes(b"fixture")
            alias = root / "alias"
            alias.symlink_to(actual, target_is_directory=True)
            with patch.object(bundle.platform, "machine", return_value="x86_64"), patch.object(bundle, "verify_chrome_signature_kind") as verify, patch.object(bundle.subprocess, "run") as run:
                bundle.verify_chrome_signature(alias, executable, "darwin")
                verify.assert_called_once_with(binary.resolve(), "amd64")
                run.assert_not_called()

    def test_native_receipts_keep_architecture_specific_ffmpeg(self):
        target = {"os": "darwin", "arch": "arm64", "mode": "project-host"}
        self.assertTrue(acquisition.urls(target)["ffmpeg"].endswith("ffmpeg-mac-arm64.zip"))
        target = {"os": "linux", "arch": "arm64", "mode": "project-host"}
        self.assertTrue(acquisition.urls(target)["chrome"].endswith("linux-arm64/chrome-linux-arm64.zip"))
        self.assertTrue(acquisition.urls(target)["ffmpeg"].endswith("ffmpeg-linux-arm64.zip"))
        with self.assertRaises(KeyError):
            acquisition.urls({"os": "windows", "arch": "arm64", "mode": "project-host"})

    def test_acquisition_requires_independent_exact_receipts(self):
        target = {"os": "linux", "arch": "amd64", "mode": "project-host"}
        assets = {name: {"version": version, "url": acquisition.urls(target)[name], "bytes": 1, "sha256": "a" * 64, "root": "vendor", "notices": ["LICENSE"]} for name, version in acquisition.VERSIONS.items()}
        recipe = {"schema": 1, "target": target, "native_requirements": [], "assets": assets}
        acquisition.trusted_inputs(recipe, target)
        assets["chrome"]["url"] = "https://example.com/chrome.zip"
        with self.assertRaises(ValueError):
            acquisition.trusted_inputs(recipe, target)
    def test_signed_chrome_framework_aliases_are_preserved_within_app(self):
        with tempfile.TemporaryDirectory() as scratch:
            root = Path(scratch)
            source = root / "vendor"
            framework = source / "Chrome.app" / "Contents" / "Frameworks" / "X.framework"
            (framework / "Versions" / "A").mkdir(parents=True)
            (framework / "Versions" / "A" / "X").write_text("signed payload")
            (framework / "Versions" / "Current").symlink_to("A")
            (framework / "X").symlink_to("Versions/Current/X")
            bundle.preserve_chrome_tree(source, root / "owned")
            alias = root / "owned" / framework.relative_to(source) / "X"
            self.assertTrue(alias.is_symlink())
            self.assertEqual(alias.readlink().as_posix(), "Versions/Current/X")
            self.assertEqual(alias.read_text(), "signed payload")
            (source / "outside-app").write_text("unrelated payload")
            (framework / "escape").symlink_to("../../../../outside-app")
            with self.assertRaises(ValueError):
                bundle.preserve_chrome_tree(source, root / "rejected")

    def test_internal_links_are_materialized_and_escaping_links_rejected(self):
        with tempfile.TemporaryDirectory() as scratch:
            root = Path(scratch)
            source = root / "vendor"
            source.mkdir()
            (source / "license").write_text("notice", encoding="utf-8")
            (source / "alias").symlink_to("license")
            bundle.materialize(source, root / "owned")
            self.assertFalse((root / "owned" / "alias").is_symlink())
            self.assertEqual((root / "owned" / "alias").read_text(), "notice")
            (root / "outside").write_text("user data", encoding="utf-8")
            (source / "escape").symlink_to("../outside")
            with self.assertRaises(ValueError):
                bundle.materialize(source, root / "rejected")
            self.assertEqual((root / "outside").read_text(), "user data")

    def test_zip_parent_traversal_cannot_extract(self):
        with tempfile.TemporaryDirectory() as scratch:
            root = Path(scratch)
            archive = root / "vendor.zip"
            with zipfile.ZipFile(archive, "w") as packed:
                packed.writestr("../outside", "invalid")
            with self.assertRaises(ValueError):
                bundle.unpack(archive, root / "extracted")
            self.assertFalse((root / "outside").exists())


if __name__ == "__main__":
    unittest.main()
