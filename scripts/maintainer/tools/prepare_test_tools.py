"""Acquire the exact release-owned native ripgrep prerequisite for source tests."""
import argparse
import json
import os
from pathlib import Path

from release_pipeline import native
from release_config import ROOT
from acquire_full_inputs import acquire
from build_browser_bundle import unpack, contained


def prepare(output):
    system,arch=native()
    if system != "linux":
        raise ValueError("source/full search acceptance uses its native Linux runner")
    recipe=ROOT/f"packaging/tools/inputs/workspace-linux-{arch}.json"
    acquire(recipe,output/"inputs")
    document=json.loads((output/"inputs/full-inputs.json").read_text())
    asset=document["inputs"][0]
    unpack(output/"inputs"/asset["archive"],output/"unpacked")
    binary=contained(output/"unpacked",output/"unpacked"/asset["root"]/"rg")
    if not binary.is_file() or binary.is_symlink():
        raise ValueError("reviewed ripgrep input does not contain its owned executable")
    binary.chmod(0o755)
    if os.environ.get("GITHUB_ENV"):
        with open(os.environ["GITHUB_ENV"],"a",encoding="utf-8") as out:
            out.write("LOKI_TEST_RG="+str(binary.resolve())+"\n")
    print("Native source test prerequisite: "+str(binary),flush=True)
    return binary


if __name__=="__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output",required=True,type=Path)
    prepare(parser.parse_args().output.resolve())
