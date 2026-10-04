"""Verify/publish exact native OCI manifests, separate from build caches."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

REFERENCE = re.compile(r"ghcr\.io/jinyongp/loki/[a-z0-9-]+@sha256:[a-f0-9]{64}")


def verify(reference, auth=None):
    if not REFERENCE.fullmatch(reference):
        raise ValueError("image reference leaves the owned immutable namespace")
    command = ["skopeo", "inspect", "--raw"]
    if auth:
        command += ["--authfile", str(auth)]
    raw = subprocess.check_output([*command, "docker://"+reference], timeout=60)
    if "sha256:"+hashlib.sha256(raw).hexdigest() != reference.split("@",1)[1]:
        raise ValueError("registry manifest differs from the native accepted digest")
    return reference


def publish(directory):
    path = directory / "images.json"
    document = json.loads(path.read_text())
    token = os.environ.get("LOKI_REGISTRY_TOKEN")
    if not token:
        raise ValueError("native candidate registry transfer requires the workflow token")
    with tempfile.TemporaryDirectory(prefix="loki-registry-auth-") as temporary:
        auth = Path(temporary)/"auth.json"
        subprocess.run(["skopeo","login","--authfile",str(auth),"--username",os.environ["GITHUB_ACTOR"],"--password-stdin","ghcr.io"],input=token,text=True,check=True,timeout=60)
        for image in document["images"].values():
            reference = image["reference"]
            if not REFERENCE.fullmatch(reference):
                raise ValueError("candidate image destination differs")
            if "archive" not in image:
                verify(reference, auth)
                continue
            archive = directory / image["archive"]
            if archive.parent != directory or archive.is_symlink() or archive.stat().st_size != image["bytes"]:
                raise ValueError("candidate OCI archive differs from receipt")
            with archive.open("rb") as stream:
                if hashlib.file_digest(stream,"sha256").hexdigest() != image["sha256"]:
                    raise ValueError("candidate OCI checksum differs")
            tag = reference.split("@",1)[0]+":candidate-"+os.environ["GITHUB_RUN_ID"]+"-"+os.environ.get("GITHUB_RUN_ATTEMPT","1")+"-"+document["target"]["arch"]
            subprocess.run(["skopeo","copy","--preserve-digests","--authfile",str(auth),"oci-archive:"+str(archive),"docker://"+tag],check=True,timeout=300)
            verify(reference, auth)
            image["candidate_tag"] = tag
    return document
