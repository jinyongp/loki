"""Bind successful native checks to exact selected units before publication."""
import argparse
import json
from pathlib import Path
import os

from release_pipeline import read, write, digest


def evidence(plan, mode, system, arch, lock_sha256):
    if mode not in {"manager", "project-host", "full"}:
        raise ValueError("unsupported acceptance mode")
    return {"schema": 1, "source_commit": plan["source_commit"], "lock_sha256":lock_sha256,
            "release": plan["release"], "os": system, "arch": arch, "mode": mode,
            "run_id": os.environ.get("GITHUB_RUN_ID", "local"),
            "run_attempt": os.environ.get("GITHUB_RUN_ATTEMPT", "1"), "passed": True}


def gate(plan, publication, checks, results):
    required = {"plan", "source-checks", "assemble"}
    counts = {"manager-build": "manager", "browser-build": "browser", "full-build": "full",
              "manager-checks": "manager-checks", "browser-checks": "browser-checks", "full-checks": "full-checks"}
    for name, matrix in counts.items():
        if plan["matrices"][matrix]:
            required.add(name)
        elif results.get(name) not in {"success", "skipped"}:
            raise ValueError("unselected job failed or was cancelled: "+name)
    if any(results.get(name) != "success" for name in required):
        raise ValueError("selected release jobs did not all succeed")
    lock_path = publication/"assets/loki-release-lock.json"
    lock = read(lock_path)
    if plan.get("publication_recovery"):
        # Production and native proofs are the already sealed public cohort.
        # Preserve their bytes exactly for Releaseway's immutable verification.
        for name in required:
            if results.get(name) != "success":
                raise ValueError("publication recovery checks failed")
        return lock
    if lock["release"] != plan["release"] or lock["source_commit"] != plan["source_commit"] or set(lock["units"]) != set(plan["units"]):
        raise ValueError("assembled lock differs from selected plan")
    proofs = {}
    expected = []
    for matrix, mode in (("manager-checks", "manager"), ("browser-checks", "project-host"), ("full-checks", "full")):
        expected += [(t["os"], t["arch"], mode) for t in plan["matrices"][matrix]]
    for path in checks.rglob("acceptance.json"):
        proof = read(path)
        key = (proof["os"], proof["arch"], proof["mode"])
        attempt = proof.get("run_attempt", "")
        if key in proofs or key not in expected or not proof.get("passed") or proof.get("lock_sha256") != digest(lock_path) or proof.get("source_commit") != plan["source_commit"] or proof.get("release") != plan["release"] or proof.get("run_id") != os.environ.get("GITHUB_RUN_ID", "local") or not str(attempt).isdigit() or not 1 <= int(attempt) <= int(os.environ.get("GITHUB_RUN_ATTEMPT", "1")):
            raise ValueError("acceptance evidence is duplicated or belongs to another cohort")
        proofs[key] = proof
    if set(proofs) != set(expected):
        raise ValueError("selected native acceptance evidence is missing")
    for key, unit in lock["units"].items():
        chosen = plan["units"][key]
        if any(unit.get(field) != chosen.get(field) for field in ("fingerprint", "version", "target", "kind")):
            raise ValueError("unit input identity changed after assembly")
        target = unit["target"]
        proof_key = (target["os"], target["arch"], "manager" if unit["kind"] == "manager" else target["mode"])
        if proof_key in proofs:
            unit["acceptance"] = dict(proofs[proof_key], fingerprint=unit["fingerprint"], validation=chosen["validation"])
            unit["validation"] = chosen["validation"]
        elif chosen["action"] == "build" or not unit.get("acceptance"):
            raise ValueError("unit has no successful native acceptance")
    write(lock_path, lock)
    assets = publication/"assets"
    (assets/"SHA256SUMS").write_text("".join(digest(path)+"  "+path.name+"\n" for path in sorted(assets.iterdir()) if path.is_file() and path.name != "SHA256SUMS"))
    return lock


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["evidence", "gate"])
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--checks", type=Path)
    parser.add_argument("--results", type=Path)
    parser.add_argument("--mode")
    parser.add_argument("--publication", type=Path)
    args = parser.parse_args()
    plan = read(args.plan)
    if args.action == "evidence":
        from release_pipeline import native
        system, arch = native()
        write(args.output, evidence(plan, args.mode, system, arch, digest(args.publication/"assets/loki-release-lock.json")))
    else:
        gate(plan, args.output, args.checks, read(args.results))
