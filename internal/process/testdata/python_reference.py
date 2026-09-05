"""Synthetic process snapshots only; no subprocesses, server, or live state."""
import base64
import json
import sys
from types import SimpleNamespace

from loki_mcp import __version__
from loki_mcp.processes import ManagedProcess


def main():
    if __version__ != "0.47.1":
        raise SystemExit("reference requires Loki Python 0.47.1")
    results = []
    for case in json.load(sys.stdin):
        process = SimpleNamespace(
            poll=lambda: case["exit_code"], returncode=case["exit_code"],
        )
        item = ManagedProcess(
            session_id="fixture", name="reference", process=process,
            started_at=1234.125, max_output_bytes=6, timeout_seconds=60,
            metadata={"profile": "synthetic", "port": 32180},
        )
        item.timed_out = case["exit_code"] == -9
        for chunk in case["chunks"]:
            item.append(base64.b64decode(chunk))
        results.append(item.snapshot(case["offset"], case["limit"]))
    json.dump(results, sys.stdout)


if __name__ == "__main__":
    main()
