"""Test-only Node resolution and token consumption; never starts a service."""
import json
import runpy
from pathlib import Path
import sys
import tempfile
import time

from loki_mcp import __version__, runtime


def main():
    if __version__ != "0.47.1":
        raise SystemExit("reference requires Loki Python 0.47.1")
    root = Path(sys.argv[1]).resolve()
    if not root.is_relative_to(Path(tempfile.gettempdir()).resolve()) or not root.name.startswith("action-reference-") or any(root.iterdir()):
        raise SystemExit("reference requires a fresh temporary directory")
    results = []
    dotenv_value = runpy.run_path(str(Path(runtime.__file__).resolve().parents[2] / "scripts/loki-action-runner.py"))["_dotenv_value"]
    for index, case in enumerate(json.load(sys.stdin)):
        if "dotenv" in case:
            results.append({"result": dotenv_value(case["dotenv"])})
            continue
        if case.get("token_mode"):
            mode = case["token_mode"]
            token = "0" * 32
            controller = runtime.RuntimeController.__new__(runtime.RuntimeController)
            controller._action_launches = {token: {"profile": "fixture", "action": "web", "cwd": "/synthetic/repo", "port": 43210, "expires_at": time.monotonic() + 30}}
            if mode == "expired":
                controller._action_launches[token]["expires_at"] = 0
            if mode == "unknown":
                controller._action_launches.clear()
            if mode == "malformed":
                token = "invalid"
            if mode == "uppercase":
                token = "A" * 32
            values = []
            for _ in range(2):
                try:
                    values.append({"result": controller._consume_action_launch(token, "other" if mode == "profile" else "fixture", "other" if mode == "action" else "web", Path("/synthetic/feature" if mode == "cwd" else "/synthetic/repo"))})
                except ValueError as error:
                    values.append({"error": str(error)})
            results.append({"result": values})
            continue
        workspace = root / str(index)
        cwd = workspace / "repo"
        cwd.mkdir(parents=True)
        runtime.WORKSPACE_ROOT = workspace
        for field, filename in (("hint", ".node-version"), ("nvm", ".nvmrc")):
            if case.get(field) is not None:
                (cwd / filename).write_text(case[field], encoding="utf-8")
        for version in case.get("installed", []):
            (workspace / ".loki/fnm/node-versions" / version).mkdir(parents=True)
        try:
            results.append({"result": runtime._resolved_command(case["command"], cwd)})
        except ValueError as error:
            results.append({"error": str(error)})
    json.dump(results, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
