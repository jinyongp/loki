"""Digest-keyed public input cache; restoration never supplies trust metadata."""
import hashlib
import os
from pathlib import Path
import re
import shutil


def valid(path, expected):
    if not path.is_file() or path.is_symlink() or path.stat().st_size != int(expected["bytes"]):
        return False
    return size_digest(path)[1].hexdigest() == expected["sha256"]


def size_digest(path):
    with path.open("rb") as stream:
        return path.stat().st_size, hashlib.file_digest(stream, "sha256")


def location(expected):
    directory = os.environ.get("LOKI_INPUT_CACHE")
    if not directory:
        return None
    value = expected.get("sha256", "")
    if not re.fullmatch(r"[a-f0-9]{64}", value):
        raise ValueError("input cache requires an independently trusted digest")
    root = Path(directory)
    root.mkdir(parents=True, exist_ok=True)
    return root / value


def _restore(destination, expected):
    source = location(expected)
    if source is None or not source.exists():
        return False
    if not valid(source, expected):
        source.unlink()
        print("Discarded corrupt public input cache: "+expected["sha256"], flush=True)
        return False
    temporary = destination.with_name(destination.name+f".{os.getpid()}.restore")
    try:
        shutil.copyfile(source, temporary)
        if not valid(temporary, expected):
            raise OSError("cache restoration changed bytes")
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)
    print("Verified public input cache hit: "+expected["sha256"], flush=True)
    return True


def _save(source, expected):
    if not valid(source, expected):
        raise ValueError("cache insertion differs from trusted length/digest")
    destination = location(expected)
    if destination is None:
        return
    temporary = destination.with_name(destination.name+f".{os.getpid()}.part")
    try:
        shutil.copyfile(source, temporary)
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)


def restore(destination, expected):
    try:
        return _restore(destination, expected)
    except OSError as error:
        print("Public input cache unavailable; downloading verified bytes: "+str(error), flush=True)
        return False


def save(source, expected):
    try:
        _save(source, expected)
    except OSError as error:
        print("Public input cache write skipped: "+str(error), flush=True)
