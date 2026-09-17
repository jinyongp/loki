"""Typed Taskwarrior operations; callers hold the repository-wide task lock."""
from __future__ import annotations

from typing import Any, Callable
from uuid import UUID
from datetime import datetime, timezone
import json
import re

from .policy import PolicyError


def task_uuid(value: Any) -> str:
    if not isinstance(value, str):
        raise PolicyError("a full task UUID is required")
    try:
        normalized = str(UUID(value))
    except ValueError as error:
        raise PolicyError("a full task UUID is required") from error
    if normalized != value:
        raise PolicyError("a canonical full task UUID is required")
    return normalized


def attributes(fields: dict[str, Any]) -> list[str]:
    allowed = {"description", "priority", "due", "wait", "scheduled", "tags", "depends"}
    if not isinstance(fields, dict) or set(fields) - allowed:
        raise PolicyError("unsupported task fields")
    result = []
    for name, value in fields.items():
        if name in {"tags", "depends"}:
            if not isinstance(value, list) or len(value) > 64:
                raise PolicyError("task tags and dependencies must be bounded arrays")
            for item in value:
                if name == "depends":
                    task_uuid(item)
                elif not isinstance(item, str) or re.fullmatch(r"[A-Za-z][A-Za-z0-9_-]{0,63}", item) is None:
                    raise PolicyError("invalid task tag")
            result.append(f"{name}:" + ",".join(value))
        elif name == "description":
            if not isinstance(value, str) or not value.strip() or len(value.encode()) > 8192 or "\x00" in value:
                raise PolicyError("task description is empty or invalid")
            result.append("description:" + value)
        elif name == "priority":
            if value is not None and (not isinstance(value, str) or value not in {"", "H", "M", "L"}):
                raise PolicyError("priority must be H, M, L, or null")
            result.append("priority:" + (value or ""))
        else:
            if value is not None and (not isinstance(value, str) or re.fullmatch(r"\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}Z)?", value) is None):
                raise PolicyError("task dates must be ISO dates or null")
            result.append(f"{name}:" + (value or ""))
    return result


def operate(run: Callable[[list[str]], dict[str, Any]], request: dict[str, Any], slug: str) -> dict[str, Any]:
    action = request.get("action")
    if action not in {"list", "next", "get", "count", "add", "modify", "annotate", "start", "stop", "done", "delete"}:
        raise PolicyError("unsupported task action")

    def execute(args: list[str]) -> str:
        result = run(["rc.confirmation=off", "rc.verbose=nothing", "rc.json.array=on", "rc.hooks=off", *args])
        if result["exit_code"] != 0:
            raise PolicyError("TASK_COMMAND_FAILED: " + str(result["output"])[-2048:])
        return str(result["output"])

    def read(identifier: str | None = None) -> list[dict[str, Any]]:
        args = ["project.is:" + slug]
        if identifier:
            args.append(task_uuid(identifier))
        try:
            raw = json.loads(execute([*args, "export"]))
        except ValueError as error:
            raise PolicyError("invalid Taskwarrior JSON response") from error
        if not isinstance(raw, list) or not all(isinstance(item, dict) for item in raw):
            raise PolicyError("invalid Taskwarrior JSON response")
        return raw

    identifier = request.get("uuid")
    if action in {"list", "next", "count"}:
        status = request.get("status", "pending")
        if status not in {"pending", "waiting", "completed", "deleted", "all"}:
            raise PolicyError("invalid task status")
        all_items = read()
        items = [item for item in all_items if status == "all" or item.get("status") == status]
        if action == "next":
            now = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
            pending = {item["uuid"] for item in all_items if item.get("status") in {"pending", "waiting"}}
            items = [item for item in items if item.get("status") == "pending"
                     and (not item.get("wait") or item["wait"] <= now)
                     and (not item.get("scheduled") or item["scheduled"] <= now)
                     and not (set(item.get("depends", [])) & pending)]
            items.sort(key=lambda item: -float(item.get("urgency", 0)))
        limit = request.get("limit", 50)
        if isinstance(limit, bool) or not isinstance(limit, int) or not 1 <= limit <= 200:
            raise PolicyError("task limit must be between 1 and 200")
        offset = request.get("offset", 0)
        if isinstance(offset, bool) or not isinstance(offset, int) or offset < 0:
            raise PolicyError("task offset must be a nonnegative integer")
        return {"count": len(items), "tasks": items[offset:offset + limit] if action != "count" else [],
                "has_more": action != "count" and len(items) > offset + limit, "offset": offset}
    if action != "add":
        identifier = task_uuid(identifier)
        before = read(identifier)
        if len(before) != 1:
            raise PolicyError("TASK_NOT_FOUND: UUID does not belong to this workstream")
        if action == "get":
            return {"task": before[0]}
    if action in {"add", "modify"}:
        fields = request.get("fields") or {}
        args = attributes(fields)
        if not args or (action == "add" and "description" not in fields):
            raise PolicyError("task fields are required")
        for dependency in fields.get("depends", []):
            if dependency == identifier or len(read(dependency)) != 1:
                raise PolicyError("dependency must belong to this workstream and differ from the task")
        if action == "modify" and "depends" in fields:
            graph = {item["uuid"]: item.get("depends", []) for item in read()}
            pending = list(fields["depends"])
            visited = set()
            while pending:
                current = pending.pop()
                if current == identifier:
                    raise PolicyError("dependency cycle is not allowed")
                if current not in visited:
                    visited.add(current)
                    pending.extend(graph.get(current, []))
        if action == "add":
            existing = {item["uuid"] for item in read()}
            execute(["add", "project:" + slug, *args])
            created = [item for item in read() if item["uuid"] not in existing]
            if len(created) != 1:
                raise PolicyError("task creation readback is ambiguous; inspect before retrying")
            identifier = created[0]["uuid"]
        else:
            execute([identifier, "modify", *args])
    elif action == "annotate":
        note = request.get("annotation")
        attributes({"description": note})
        # End option parsing before passing arbitrary annotation text.
        execute([identifier, "annotate", "--", note])
    else:
        execute([identifier, action])
    after = read(identifier)
    if len(after) != 1:
        raise PolicyError("task mutation readback failed")
    return {"task": after[0], "action": action}
