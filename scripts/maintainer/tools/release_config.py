"""Canonical version selection, separate from artifact compatibility."""
import json
import os
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[3]
CONFIG = json.loads((ROOT / "packaging/tools/release.json").read_text())
CONTRACT = CONFIG["composition_contract"]
RELEASE_PATTERN = re.compile(r"0\.2\.(?:0|[1-9][0-9]*)")


def version(value):
    if not isinstance(value, str) or not RELEASE_PATTERN.fullmatch(value):
        raise ValueError("release requires an exact stable 0.2.x version")
    return value


RELEASE = version(os.environ.get("LOKI_RELEASE", CONFIG["version"]))
VERSIONS = json.loads(os.environ.get("LOKI_ARTIFACT_VERSIONS", "{}"))
if not isinstance(VERSIONS, dict) or set(VERSIONS) - set(CONFIG["modules"]):
    raise ValueError("unknown module artifact version override")
for value in VERSIONS.values():
    version(value)


def artifact_release(owner):
    if owner not in CONFIG["modules"]:
        raise ValueError("unknown module owner")
    return version(VERSIONS.get(owner, RELEASE))


def asset_url(owner, target):
    release = artifact_release(owner)
    return f"https://github.com/{CONFIG['repository']}/releases/download/v{release}/loki-{owner}-{release}-{target['os']}-{target['arch']}-{target['mode']}.zip"
