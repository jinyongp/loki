"""Isolated 0.47.1 oracle for Go differential tests; never uses runtime defaults."""
import json
from pathlib import Path
import sys
import tempfile
from datetime import datetime, timezone
from unittest.mock import patch

from loki_mcp import __version__
from loki_mcp.project_state import ProjectStateStore
from loki_mcp.tasks import operate, attributes, task_uuid
from loki_mcp.policy import PolicyError


def main():
    if __version__ != "0.47.1":
        raise SystemExit("reference requires Loki Python 0.47.1")
    workspace, state_root = (Path(value).resolve() for value in sys.argv[1:3])
    temporary_root = Path(tempfile.gettempdir()).resolve()
    if not workspace.is_relative_to(temporary_root) or not state_root.is_relative_to(temporary_root):
        raise SystemExit("reference requires disposable temporary paths")
    if not state_root.name.startswith("python-state-") or any(state_root.iterdir()):
        raise SystemExit("reference requires a new empty python-state directory")
    store = ProjectStateStore(workspace, state_root)
    outputs = []
    for request in json.load(sys.stdin):
        request = dict(request)
        action = request.pop("action")
        cwd = workspace / request.pop("cwd", "project")
        if not cwd.resolve().is_relative_to(workspace):
            raise SystemExit("reference cwd outside temporary workspace")
        try:
            if action == "status":
                value = store.status(cwd)
            elif action == "list":
                value = store.list_workstreams(cwd)
            elif action == "init":
                value = store.initialize_workstream(cwd, **request)
            elif action == "bind":
                value = store.bind_workstream(cwd, request["slug"])
            elif action == "read":
                value = store.read_artifact(cwd, request.get("slug"), request["filename"])
            elif action == "write":
                value = store.write_artifact(cwd, request.get("slug"), request["filename"], request["content"], request.get("expected_sha256"))
            elif action == "attributes":
                value = sorted(attributes(request["fields"]))
            elif action == "uuid":
                value = task_uuid(request.get("uuid"))
            elif action == "task":
                class FixedClock:
                    @staticmethod
                    def now(tz):
                        return datetime(2026, 9, 4, 0, 0, 0, tzinfo=timezone.utc)
                with patch("loki_mcp.tasks.datetime", FixedClock):
                    value = operate(lambda args: {"exit_code": 0, "output": json.dumps(request["rows"])}, request["request"], "test-workstream-state")
            else:
                raise SystemExit("unknown reference action")
            outputs.append({"result": value})
        except PolicyError as error:
            outputs.append({"error": str(error)})
    json.dump(outputs, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
