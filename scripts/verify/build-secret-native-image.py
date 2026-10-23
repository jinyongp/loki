#!/usr/bin/env python3
"""Build the shipping Dockerfile locally for native secret acceptance.

Inputs come from build-release-inputs.sh on this same native runner. This does
not publish an image or assert that the other architecture was executed.
"""
import argparse
import hashlib
import json
import platform
from pathlib import Path
import shutil
import subprocess
import tempfile


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def build(inputs, tag, loki_version="0.49.0-dev"):
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or arch is None or output("go", "env", "GOARCH") != arch:
        raise ValueError("native Linux runner is required")
    if not tag.startswith("loki-secret-verification:") or len(tag.split(":")) != 2:
        raise ValueError("use the disposable local verification image namespace")
    root = Path(__file__).resolve().parents[2]
    paths = {"devtools": inputs / "devtools" / arch / "devtools",
             "ripgrep": inputs / "ripgrep" / arch / "rg",
             "gh": inputs / "gh" / arch / "gh"}
    version = json.loads(output(str(paths["devtools"]), "version"))
    # The public version response is an envelope, not the buildinfo object.
    provider = version.get("data", {})
    if not version.get("ok") or provider.get("protocol_version") != 12 or provider.get("version") != "0.33.0" or provider.get("commit") != "276a78e91479321ba751c5555769a39e6ad05e07":
        raise ValueError("provider identity does not match the reviewed source")
    if not output(str(paths["ripgrep"]), "--version").startswith("ripgrep 15.2.0 ") or not output(str(paths["gh"]), "version").startswith("gh version 2.102.0 "):
        raise ValueError("search or GitHub input differs from the pinned release contract")
    revision = output("git", "-C", str(root), "rev-parse", "HEAD")
    date = output("git", "-C", str(root), "show", "-s", "--format=%cI", revision)
    with tempfile.TemporaryDirectory(prefix="loki-native-image-") as temporary:
        artifacts = Path(temporary)
        digests = {}
        for name, source in paths.items():
            destination = artifacts / name / arch / source.name
            destination.parent.mkdir(parents=True)
            shutil.copyfile(source, destination)
            destination.chmod(0o755)
            digests[name] = hashlib.sha256(source.read_bytes()).hexdigest()
        metadata = artifacts / "metadata"
        metadata.mkdir()
        subprocess.run(["go", "run", "./modules/coordination/cmd/gencatalog", "-binary", str(paths["devtools"]), "-output", str(metadata / "devtools-catalog.json")], cwd=root, check=True)
        provenance = {"version": 1, "loki": {"version": loki_version, "revision": revision, "date": date},
                      "devtools": version, "binaries": {name: {arch: {"sha256": digest}} for name, digest in digests.items()}}
        (metadata / "provenance.json").write_text(json.dumps(provenance) + "\n", encoding="utf-8")
        args = ["docker", "buildx", "build", str(root), "--file", str(root / "packaging/images/Dockerfile"),
                "--platform", "linux/" + arch, "--build-context", "artifacts=" + str(artifacts),
                "--tag", tag, "--load", "--provenance=false"]
        values = {"LOKI_VERSION": loki_version, "LOKI_REVISION": revision, "LOKI_DATE": date,
                  "DEVTOOLS_VERSION": "0.33.0", "RIPGREP_VERSION": "15.2.0", "GH_VERSION": "2.102.0"}
        for name, digest in digests.items():
            values[name.upper() + "_" + arch.upper() + "_SHA256"] = digest
        for name, value in values.items():
            args.extend(["--build-arg", name + "=" + value])
        subprocess.run(args, check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", type=Path, required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--version", choices=["0.49.0-dev", "0.49.1-dev"], default="0.49.0-dev")
    args = parser.parse_args()
    build(args.inputs.resolve(), args.tag, args.version)
