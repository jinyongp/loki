#!/usr/bin/env python3
"""Prepare one native management/tool candidate from reviewed local inputs.

Run after source implementation is delivered and before final product checks.
This pipeline never uploads artifacts, publishes images or changes an installed
Loki deployment. Separate native runners prepare their own execution targets.
"""
import argparse
import json
from pathlib import Path
import platform
import subprocess
import sys

from build_browser_bundle import digest
from build_full_bundle import REQUIRED_PROGRAMS
from build_full_images import oci_manifest


def prepare(recipe_path, output):
    recipe = json.loads(recipe_path.read_text(encoding="utf-8"))
    arch = {"amd64":"amd64", "x86_64":"amd64", "aarch64":"arm64", "arm64":"arm64"}.get(platform.machine().lower())
    host = {"Linux":"linux", "Windows":"windows", "Darwin":"darwin"}.get(platform.system())
    target = recipe.get("target", {})
    if recipe.get("schema") != 1 or recipe.get("release") != "0.2.1" or target.get("os") != host or target.get("arch") != arch or target.get("mode") not in ("project-host", "full") or target["mode"] == "full" and host != "linux":
        raise ValueError("candidate preparation requires its exact native 0.2.1 execution target")
    output.mkdir(parents=True, exist_ok=False)
    scripts = Path(__file__).resolve().parent
    catalogs = []
    def run(name, *arguments):
        print("Preparing "+name+"...", flush=True)
        subprocess.run([sys.executable, str(scripts / name), *map(str, arguments)], check=True)
    native_sources = recipe.get("native_sources", {})
    if native_sources and target["mode"] != "full":
        raise ValueError("native source closures belong to Linux full candidates")
    if set(native_sources) - {"git", "github", "coordination", "browser_libraries", "trust_store"}:
        raise ValueError("unknown native source closure owner")
    native_receipts = {}
    for owner, specification in sorted(native_sources.items()):
        source = (recipe_path.parent / specification["inputs"]).resolve()
        inputs = json.loads(source.read_text(encoding="utf-8"))
        expected = "browser" if owner == "browser_libraries" else "trust-store" if owner == "trust_store" else owner
        if inputs.get("target") != target or inputs.get("owner") != expected:
            raise ValueError("native source recipe differs from candidate owner/target")
        acquired = output / "native-inputs" / owner
        vendor = output / "native-closures" / owner
        if owner in ("github", "coordination"):
            run("acquire_go_vendor_inputs.py", "--trust", source, "--output", acquired)
            run("build_go_vendor.py", "--inputs", acquired / "vendor-inputs.json", "--output", vendor)
            receipt_file = vendor / "vendor-receipt.json"
        else:
            run("acquire_debian_inputs.py", "--trust", source, "--output", acquired)
            run("build_debian_closure.py", "--inputs", acquired / "closure-inputs.json", "--output", vendor)
            receipt_file = vendor / "closure-receipt.json"
        receipt = json.loads(receipt_file.read_text(encoding="utf-8"))
        receipt["archive"] = str((vendor / receipt["archive"]).resolve())
        native_receipts[owner] = receipt
    run("build_manager_bundle.py", "--output", output / "manager")
    modules = recipe.get("modules", {})
    if target["mode"] == "project-host" and modules:
        raise ValueError("project-host candidate contains only its standalone browser module")
    if set(modules) - set(REQUIRED_PROGRAMS):
        raise ValueError("full candidate has an unknown module owner")
    if (set(native_receipts) & {"git", "github", "coordination"}) - set(modules):
        raise ValueError("native source programs require their selected candidate module")
    prepared = {}
    for owner, module in sorted(modules.items()):
        source = (recipe_path.parent / module["inputs"]).resolve()
        inputs = json.loads(source.read_text(encoding="utf-8"))
        if owner in native_receipts:
            asset = dict(native_receipts[owner], destination="native")
            programs = asset.pop("programs")
            inputs = {"schema":1, "module":owner, "target":target, "images":{}, "assets":{}, "programs":{name:"native/"+path for name, path in programs.items()}, "inputs":[asset]}
        else:
            for asset in inputs.get("inputs", []):
                if "archive" not in asset:
                    acquired = output / "native-inputs" / owner
                    run("acquire_full_inputs.py", "--trust", source, "--output", acquired)
                    inputs = json.loads((acquired / "full-inputs.json").read_text(encoding="utf-8"))
                    for asset in inputs.get("inputs", []):
                        asset["archive"] = str((acquired / asset["archive"]).resolve())
                    break
            else:
                for asset in inputs.get("inputs", []):
                    asset["archive"] = str((source.parent / asset["archive"]).resolve())
        if inputs.get("module") != owner or inputs.get("target") != target:
            raise ValueError("module recipe owner/target differs from candidate")
        directory = output / "prepared" / owner
        bound_inputs = output / (owner+"-inputs.json")
        bound_inputs.write_text(json.dumps(inputs, indent=2)+"\n", encoding="utf-8")
        run("build_full_bundle.py", "--inputs", bound_inputs, "--output", directory, "--release-url", module["url"], "--payload-only")
        prepared[owner] = directory
    images_path = None
    if target["mode"] == "full" and "images" in recipe:
        source = (recipe_path.parent / recipe["images"]).resolve()
        images = json.loads(source.read_text(encoding="utf-8"))
        if images.get("target") != target:
            raise ValueError("image recipe differs from candidate target")
        images["payloads"] = {owner:str(directory.resolve()) for owner, directory in prepared.items()}
        for key in ("trust_store", "browser_libraries"):
            if key in native_receipts:
                images[key] = native_receipts[key]
        trust = images.get("trust_store")
        if trust:
            trust["archive"] = str((source.parent / trust["archive"]).resolve())
        libraries = images.get("browser_libraries")
        if libraries:
            libraries["archive"] = str((source.parent / libraries["archive"]).resolve())
        bound_recipe = output / "image-inputs.json"
        bound_recipe.write_text(json.dumps(images, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        run("build_full_images.py", "--inputs", bound_recipe, "--output", output / "images")
        images_path = output / "images" / "images.json"
    for owner, directory in sorted(prepared.items()):
        destination = output / "modules" / owner
        arguments = ["--prepared", directory, "--output", destination, "--release-url", modules[owner]["url"]]
        if images_path:
            arguments += ["--images", images_path]
        run("finalize_full_bundle.py", *arguments)
        catalogs.append(destination / "catalog.json")
    browser = recipe.get("browser")
    if browser:
        source = (recipe_path.parent / browser["inputs"]).resolve()
        inputs = json.loads(source.read_text(encoding="utf-8"))
        if inputs.get("target") != target:
            raise ValueError("browser recipe differs from candidate target")
        if any("archive" not in asset for asset in inputs["assets"].values()):
            acquired = output / "native-inputs" / "browser"
            run("acquire_browser_inputs.py", "--trust", source, "--output", acquired, "--mode", target["mode"])
            source = acquired / "browser-inputs.json"
            inputs = json.loads(source.read_text(encoding="utf-8"))
        for asset in inputs["assets"].values():
            asset["archive"] = str((source.parent / asset["archive"]).resolve())
        if target["mode"] == "full":
            if images_path is None:
                raise ValueError("full browser requires its owned image candidate")
            images = json.loads(images_path.read_text(encoding="utf-8"))
            image = images["images"].get("browser", {})
            name = image.get("archive", "")
            if Path(name).name != name or not name or image.get("owner") != "browser" or image.get("target") != target:
                raise ValueError("full browser image ownership differs")
            archive = images_path.parent / name
            if archive.stat().st_size != image.get("bytes") or digest(archive) != image.get("sha256") or image["reference"].split("@", 1)[1] != oci_manifest(archive, target):
                raise ValueError("full browser OCI archive differs from its owned image receipt")
            inputs["full_image"] = image
        bound_recipe = output / "browser-inputs.json"
        bound_recipe.write_text(json.dumps(inputs, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        destination = output / "modules" / "browser"
        run("build_browser_bundle.py", "--inputs", bound_recipe, "--output", destination, "--release-url", browser["url"])
        catalogs.append(destination / "browser-catalog.json")
    if catalogs:
        arguments = []
        for catalog in catalogs:
            arguments += ["--catalog", catalog]
        run("merge_catalogs.py", *arguments, "--output", output / "release")
    (output / "candidate.json").write_text(json.dumps({"schema":1, "release":"0.2.1", "target":target, "manager":"manager", "catalog":"release/catalog.json" if catalogs else None, "images":"images/images.json" if images_path else None, "accepted":False, "published":False}, indent=2, sort_keys=True)+"\n", encoding="utf-8")
    print(output / "candidate.json", flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inputs", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    prepare(args.inputs.resolve(), args.output.resolve())
