#!/opt/loki-mcp/venv/bin/python
from __future__ import annotations

import signal
import sys
import time

from loki_mcp.runtime import request_runtime


current_session: str | None = None


def stop_current(*_: object) -> None:
    if current_session is not None:
        try:
            request_runtime({"operation": "stop_process", "session_id": current_session})
        except Exception:
            pass
    raise SystemExit(143)


def run_step(profile: str, action: str, cwd: str) -> None:
    global current_session
    print(f"bootstrap: starting {profile}/{action}", flush=True)
    result = request_runtime({
        "operation": "run_action", "profile": profile, "action_name": action,
        "cwd": cwd,
    })
    current_session = str(result["session_id"])
    offset = 0
    while True:
        snapshot = request_runtime({
            "operation": "read_process", "session_id": current_session,
            "offset": offset, "limit": 65_536,
        })
        output = str(snapshot.get("output", ""))
        if output:
            print(output, end="", flush=True)
        offset = int(snapshot.get("next_offset", offset))
        if snapshot.get("status") == "exited":
            exit_code = int(snapshot.get("exit_code") or 0)
            current_session = None
            if exit_code != 0:
                raise SystemExit(exit_code)
            print(f"bootstrap: completed {profile}/{action}", flush=True)
            return
        time.sleep(1)


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: loki-project-bootstrap CWD WORKFLOW")
    cwd, workflow_name = sys.argv[1:]
    workflow = request_runtime({
        "operation": "project_workflow", "cwd": cwd, "workflow": workflow_name,
    })
    signal.signal(signal.SIGTERM, stop_current)
    signal.signal(signal.SIGINT, stop_current)
    for profile, action in workflow["steps"]:
        run_step(profile, action, cwd)


if __name__ == "__main__":
    main()
