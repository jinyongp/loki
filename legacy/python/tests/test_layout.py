"""Offline layout regressions; never execute an installer or contact a server."""
import ast
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import tomllib
import unittest

PYTHON_ROOT = Path(__file__).resolve().parents[1]
REPO_ROOT = PYTHON_ROOT.parents[1]
INSTALLERS = ("install-loki-mcp.sh", "install-cloudflared.sh", "verify-and-deploy-loki.sh")


class LegacyLayoutTests(unittest.TestCase):
    def test_source_roots_from_any_directory(self):
        with tempfile.TemporaryDirectory(prefix="loki-layout-") as temporary:
            base = Path(temporary)
            checkout = base / "checkout with spaces"
            scripts = checkout / "legacy/python/scripts"
            scripts.mkdir(parents=True)
            for name in INSTALLERS:
                script = scripts / name
                shutil.copy2(PYTHON_ROOT / "scripts" / name, script)
                content = script.read_text()
                # Execute only the three path assignments, not the installer.
                setup = re.search(r"(?m)^REPO_DIR=.*\nREPO_DIR=.*\nSOURCE_DIR=.*\n", content)
                self.assertIsNotNone(setup, name)
                probe = setup.group(0) + 'printf "%s\\n%s\\n" "$REPO_DIR" "$SOURCE_DIR"\n'
                for arguments in ([], [str(checkout)], [checkout.name]):
                    with self.subTest(installer=name, arguments=arguments):
                        result = subprocess.run(["/bin/sh", "-eu", "-c", probe, str(script), *arguments], cwd=base, capture_output=True, text=True, check=True)
                        self.assertEqual(result.stdout.splitlines(), [str(checkout), str(checkout / "legacy/python")])
                invalid = subprocess.run(["/bin/sh", "-eu", "-c", probe, str(script), str(base / "missing")], cwd=base, capture_output=True)
                self.assertNotEqual(invalid.returncode, 0)

    def test_all_installer_source_assets_exist(self):
        for name in INSTALLERS:
            text = (PYTHON_ROOT / "scripts" / name).read_text()
            for variable, suffix in re.findall(r"\$(SOURCE_DIR|REPO_DIR)/([A-Za-z0-9_./-]+)", text):
                root = PYTHON_ROOT if variable == "SOURCE_DIR" else REPO_ROOT
                with self.subTest(installer=name, path=suffix):
                    self.assertTrue((root / suffix).exists(), suffix)
        deploy = (PYTHON_ROOT / "scripts/verify-and-deploy-loki.sh").read_text()
        self.assertIn('/bin/sh "$SOURCE_DIR/scripts/install-loki-mcp.sh" "$REPO_DIR"', deploy)

    def test_shared_assets_are_not_duplicated(self):
        self.assertTrue((REPO_ROOT / "bundled_skills/devtools/SKILL.md").is_file())
        self.assertTrue((REPO_ROOT / "config/gitconfig").is_file())
        self.assertFalse((PYTHON_ROOT / "bundled_skills").exists())
        self.assertFalse((PYTHON_ROOT / "config/gitconfig").exists())
        installer = (PYTHON_ROOT / "scripts/install-loki-mcp.sh").read_text()
        for required in ('test -d "$REPO_DIR/bundled_skills"', 'test -f "$REPO_DIR/config/gitconfig"'):
            self.assertLess(installer.index(required), installer.index("systemctl stop"))

    def test_legacy_template_and_go_compatibility_fixture(self):
        original = tomllib.loads((PYTHON_ROOT / "config/loki-mcp.toml").read_text())
        fixture = tomllib.loads((REPO_ROOT / "internal/config/testdata/python-v047.toml").read_text())
        self.assertEqual(original, fixture)
        sys.path.insert(0, str(PYTHON_ROOT / "src"))
        try:
            from loki_mcp.config import load_config
            config = load_config(PYTHON_ROOT / "config/loki-mcp.toml")
            self.assertEqual(config.port, 8765)
            self.assertEqual(config.root, Path("/workspace"))
        finally:
            sys.path.pop(0)

    def test_package_entrypoints_resolve(self):
        for root in (PYTHON_ROOT, PYTHON_ROOT / "browser_sidecar"):
            metadata = tomllib.loads((root / "pyproject.toml").read_text())
            self.assertEqual(metadata["tool"]["setuptools"]["packages"]["find"]["where"], ["src"])
            for entrypoint in metadata["project"]["scripts"].values():
                module, _ = entrypoint.split(":", 1)
                self.assertTrue((root / "src" / (module.replace(".", "/") + ".py")).is_file())

    def test_python_and_shell_syntax(self):
        checked = 0
        for directory in ("src", "browser_sidecar/src", "scripts", "tests"):
            for path in sorted((PYTHON_ROOT / directory).rglob("*")):
                if not path.is_file() or "__pycache__" in path.parts:
                    continue
                content = path.read_text()
                first = content.splitlines()[0] if content else ""
                if path.suffix == ".py" or first.startswith("#!") and "python" in first:
                    ast.parse(content, filename=str(path))
                    checked += 1
                elif first.startswith("#!") and ("/sh" in first or "bash" in first):
                    shell = "/bin/bash" if "bash" in first else "/bin/sh"
                    subprocess.run([shell, "-n", str(path)], check=True, capture_output=True)
                    checked += 1
        self.assertGreater(checked, 60)


if __name__ == "__main__":
    unittest.main()
