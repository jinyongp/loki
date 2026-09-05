from pathlib import Path
import json
import os
import shutil
import subprocess

import pytest

from loki_mcp.policy import PolicyError
from loki_mcp.tasks import attributes, operate, task_uuid

UUID = "11111111-1111-4111-8111-111111111111"


@pytest.mark.parametrize("value", ["1", "rc.hooks=on", "../task", UUID.upper().replace("1", "A"), None])
def test_task_uuid_rejects_noncanonical(value):
    with pytest.raises(PolicyError):
        task_uuid(value)


@pytest.mark.parametrize("fields", [
    {"project": "other"}, {"priority": []}, {"tags": ["rc.hooks=on"]},
    {"depends": ["1"]}, {"due": "tomorrow"}, {"description": ""},
])
def test_task_fields_are_typed(fields):
    with pytest.raises(PolicyError):
        attributes(fields)


def test_next_ignores_blocked_and_scheduled():
    rows = [
        {"uuid": UUID, "status": "pending", "description": "ready"},
        {"uuid": "22222222-2222-4222-8222-222222222222", "status": "pending", "depends": [UUID]},
        {"uuid": "33333333-3333-4333-8333-333333333333", "status": "pending", "scheduled": "29990101T000000Z"},
    ]
    result = operate(lambda args: {"exit_code": 0, "output": json.dumps(rows)}, {"action": "next"}, "test")
    assert result["tasks"] == rows[:1]


def test_task_unknown_scope_is_not_mutated():
    calls = []
    def run(args):
        calls.append(args)
        return {"exit_code": 0, "output": "[]"}
    with pytest.raises(PolicyError, match="TASK_NOT_FOUND"):
        operate(run, {"action": "delete", "uuid": UUID}, "other")
    assert len(calls) == 1 and calls[0][-1] == "export"


def test_real_task_lifecycle(tmp_path):
    binary = shutil.which("task") or "/home/linuxbrew/.linuxbrew/bin/task"
    if not Path(binary).is_file():
        pytest.skip("Taskwarrior binary unavailable")
    taskrc = tmp_path / "taskrc"
    taskrc.write_text("confirmation=off\n", encoding="utf-8")
    def run(args):
        result = subprocess.run([binary, *args], env={
            **os.environ, "TASKRC": str(taskrc), "TASKDATA": str(tmp_path / "data"),
        }, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=10)
        return {"exit_code": result.returncode, "output": result.stdout}
    def op(action, **kwargs):
        return operate(run, {"action": action, **kwargs}, "fixture")
    first = op("add", fields={"description": "rc.hooks=on harmless literal", "tags": ["validation"]})["task"]
    uuid = first["uuid"]
    assert first["description"] == "rc.hooks=on harmless literal"
    assert first["project"] == "fixture"
    note = op("annotate", uuid=uuid, annotation="rc.hooks=on literal annotation")["task"]
    assert note["annotations"][-1]["description"] == "rc.hooks=on literal annotation"
    second = op("add", fields={"description": "blocked", "depends": [uuid]})["task"]
    with pytest.raises(PolicyError, match="cycle"):
        op("modify", uuid=uuid, fields={"depends": [second["uuid"]]})
    page = op("list", limit=1)
    assert page["count"] == 2 and page["has_more"]
    assert len(op("list", limit=1, offset=1)["tasks"]) == 1
    assert op("next")["count"] == 1
    assert op("start", uuid=uuid)["task"]["start"]
    assert "start" not in op("stop", uuid=uuid)["task"]
    assert op("modify", uuid=uuid, fields={"priority": "H"})["task"]["priority"] == "H"
    assert op("done", uuid=uuid)["task"]["status"] == "completed"
    assert op("next")["count"] == 1
    assert op("delete", uuid=second["uuid"])["task"]["status"] == "deleted"
    assert op("count", status="all")["count"] == 2
