#!/usr/bin/env python3
"""Bind prepared immutable module files to their owned OCI candidates.

The same prepared files are embedded in images and module archives. Finalizing
does not rebuild programs, publish images or establish runtime acceptance.
"""
import argparse
import json
from pathlib import Path
import shutil
import stat
import tempfile
from urllib.parse import urlsplit
import zipfile

from build_browser_bundle import digest
from build_full_bundle import IMAGE, OWNED_IMAGES
from build_full_images import ROLE_OWNER, oci_manifest, verify_payload


def finalize(prepared, image_path, output, release_url):
    receipt = json.loads((prepared / "payload-receipt.json").read_text(encoding="utf-8"))
    target = receipt.get("target")
    if not isinstance(target, dict) or target.get("os") != "linux" or target.get("arch") not in ("amd64", "arm64") or target.get("mode") != "full":
        raise ValueError("full candidate must have an exact Linux/full target")
    receipt, root = verify_payload(prepared, target)
    owner = receipt["module"]
    roles = OWNED_IMAGES.get(owner, set())
    images = {}
    image_receipts = {}
    if roles:
        if image_path is None:
            raise ValueError("this module requires its owned native image receipt")
        document = json.loads(image_path.read_text(encoding="utf-8"))
        if document.get("schema") != 1 or document.get("release") != "0.2.2" or document.get("target") != target:
            raise ValueError("image receipt differs from the prepared native release")
        for role in sorted(roles):
            image = document.get("images", {}).get(role, {})
            if image.get("owner") != owner or ROLE_OWNER.get(role) != owner or image.get("target") != target or not IMAGE.fullmatch(image.get("reference", "")) or not image.get("notices") or image.get("inputs", {}).get(owner) != receipt:
                raise ValueError("owned image contains different module files or provenance")
            archive_name = image.get("archive", "")
            if Path(archive_name).name != archive_name or not archive_name:
                raise ValueError("OCI archive must be beside its local receipt")
            archive = image_path.parent / archive_name
            if archive.is_symlink() or not archive.is_file() or archive.stat().st_size != image.get("bytes") or digest(archive) != image.get("sha256") or image["reference"].split("@", 1)[1] != oci_manifest(archive, target):
                raise ValueError("owned image differs from the receipt-bound OCI archive")
            images[role] = image["reference"]
            image_receipts[role] = image
    parsed = urlsplit(release_url)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.fragment:
        raise ValueError("candidate acquisition URL must be public HTTPS without credentials")
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="loki-full-finalize-") as temporary:
        stage = Path(temporary)
        bundle = stage / "bundle"
        shutil.copytree(root, bundle)
        payload = json.loads((bundle / "full-runtime.json").read_text(encoding="utf-8"))
        payload["images"] = images
        (bundle / "full-runtime.json").write_text(json.dumps(payload, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        (bundle / "prepared-payload-receipt.json").write_text(json.dumps(receipt, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        (bundle / "image-receipts.json").write_text(json.dumps({"schema":1, "images":image_receipts}, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        name = f"loki-{owner}-0.2.2-linux-{target['arch']}-full.zip"
        archive = output / name
        with zipfile.ZipFile(archive, "x", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as packed:
            for path in sorted(bundle.rglob("*")):
                if not path.is_file():
                    continue
                entry = zipfile.ZipInfo(path.relative_to(bundle).as_posix(), (1980,1,1,0,0,0))
                entry.create_system = 3
                entry.external_attr = (stat.S_IFREG | (0o755 if path.stat().st_mode & 0o111 else 0o644)) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                with path.open("rb") as source, packed.open(entry, "w", force_zip64=True) as sink:
                    shutil.copyfileobj(source, sink)
        manifest = json.loads((bundle / "module.json").read_text(encoding="utf-8"))
        artifact = {"module":owner, "release":"0.2.2", "target":target, "url":release_url, "sha256":digest(archive), "bytes":archive.stat().st_size, "format":"zip"}
        (output / "catalog.json").write_text(json.dumps({"schema":1, "release":"0.2.2", "modules":[manifest], "artifacts":[artifact]}, indent=2, sort_keys=True)+"\n", encoding="utf-8")
        print(archive, flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepared", required=True, type=Path)
    parser.add_argument("--images", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--release-url", required=True)
    args = parser.parse_args()
    finalize(args.prepared.resolve(), args.images.resolve() if args.images else None, args.output.resolve(), args.release_url)
