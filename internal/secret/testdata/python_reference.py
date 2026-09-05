"""Synthetic-only reference: no server startup, process execution, or live state."""
import json
from pathlib import Path
import sys
import tempfile

from loki_mcp import __version__, runtime
from loki_mcp.project_state import ProjectStateStore
from loki_mcp.policy import PolicyError
from loki_mcp.tools import WorkspaceTools


def main():
    if __version__ != "0.47.1":
        raise SystemExit("reference requires Loki Python 0.47.1")
    workspace, root = (Path(value).resolve() for value in sys.argv[1:3])
    temporary = Path(tempfile.gettempdir()).resolve()
    if not workspace.is_relative_to(temporary) or not root.is_relative_to(temporary):
        raise SystemExit("reference requires temporary paths")
    if not root.name.startswith("secret-reference-") or any(root.iterdir()):
        raise SystemExit("reference requires a new empty directory")
    runtime.WORKSPACE_ROOT = workspace
    runtime.INBOX_DIRECTORY = root / "inbox"
    controller = runtime.RuntimeController.__new__(runtime.RuntimeController)
    controller.store = runtime.EncryptedStateStore(root / "master.key", root / "store.json")
    controller.project_state = ProjectStateStore(workspace, root / "project-state")
    controller.store.initialize()
    allowed = {
        "init", "list_profiles", "get_profile", "profile_create", "profile_remove",
        "import_env", "secret_set", "public_value_set", "secret_generate", "secret_remove",
        "action_set", "action_remove", "project_register", "project_unregister",
        "project_status", "project_set_workflow", "project_remove_workflow", "project_workflow",
    }
    results = []
    for case in json.load(sys.stdin):
        try:
            kind = case["kind"]
            if kind == "action":
                runtime._validate_action(case["value"], set(case["available"]))
                value = True
            elif kind == "document":
                runtime.EncryptedStateStore._validate(case["value"])
                value = True
            elif kind == "dotenv":
                value = runtime.parse_dotenv(case["value"])
            elif kind == "exec":
                WorkspaceTools._validate_exec_policy(case["executable"], case["arguments"])
                value = True
            elif kind == "action_cwd":
                cwd = runtime._action_cwd({"cwd": case["registered"]}, case["override"])
                value = (Path("/workspace") / cwd.relative_to(workspace)).as_posix()
            elif kind == "preview_bindings":
                value = runtime._preview_bindings(case["policy"], case["values"])
            elif kind == "public_environment":
                action = case["policy"]
                allowed_names = set(action.get("public_environment", []))
                origin = (action.get("dynamic_port") or {}).get("origin_environment")
                if origin:
                    allowed_names.add(origin)
                value = runtime._validated_public_environment(case["values"], allowed_names)
            elif kind == "operation":
                request = case["request"]
                operation = request["operation"]
                if operation not in allowed:
                    raise SystemExit("operation outside synthetic reference scope")
                value = getattr(controller, "op_" + operation)(request)
            else:
                raise SystemExit("unknown reference kind")
            results.append({"result": value})
        except (runtime.LokiRuntimeError, PolicyError) as error:
            results.append({"error": str(error)})
    json.dump(results, sys.stdout, ensure_ascii=False)


if __name__ == "__main__":
    main()
