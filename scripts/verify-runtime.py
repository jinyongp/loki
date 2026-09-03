#!/usr/bin/python3
from __future__ import annotations

from pathlib import Path
import asyncio
import json
import secrets
import shutil
import subprocess
import time

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

from loki_mcp.runtime import INBOX_DIRECTORY, request_runtime


TOKEN_PATH = Path("/etc/loki/token")
MCP_URL = "http://127.0.0.1:8765/mcp"


async def wait_for_server() -> None:
    deadline = time.monotonic() + 15
    while True:
        try:
            reader, writer = await asyncio.open_connection("127.0.0.1", 8765)
            writer.close()
            await writer.wait_closed()
            return
        except OSError:
            if time.monotonic() >= deadline:
                raise RuntimeError("Loki MCP did not become ready within 15 seconds")
            await asyncio.sleep(0.1)


async def verify_mcp(
    profile: str, managed_profile: str, import_id: str, imported_secret: str,
    project_cwd: str,
) -> tuple[dict[str, object], dict[str, object]]:
    await wait_for_server()
    token = TOKEN_PATH.read_text(encoding="utf-8").strip()
    async with httpx2.AsyncClient(headers={"Authorization": f"Bearer {token}"}) as http_client:
        async with streamable_http_client(MCP_URL, http_client=http_client) as streams:
            async with ClientSession(*streams) as session:
                await session.initialize()
                catalog = await session.list_tools()
                names = {tool.name for tool in catalog.tools}
                expected = {"secret_inspect", "secret_write", "secret_delete", "project", "action"}
                assert expected <= names, sorted(expected - names)
                imports_result = await session.call_tool("secret_inspect", {"action": "imports"})
                assert not imports_result.is_error, imports_result.content
                imports = imports_result.structured_content
                assert isinstance(imports, dict)
                assert import_id in {item["import_id"] for item in imports["imports"]}
                create_result = await session.call_tool(
                    "secret_write", {"action": "create_profile", "profile": managed_profile},
                )
                assert not create_result.is_error, create_result.content
                import_result = await session.call_tool("secret_write", {
                    "action": "import_env", "profile": managed_profile, "import_id": import_id,
                })
                assert not import_result.is_error, import_result.content
                imported = import_result.structured_content
                assert isinstance(imported, dict)
                assert imported["imported"] == ["EMPTY", "MCP_IMPORTED_SECRET"]
                assert imported_secret not in json.dumps(imported)
                metadata_result = await session.call_tool(
                    "secret_inspect", {"action": "profile", "profile": managed_profile},
                )
                metadata = metadata_result.structured_content
                assert isinstance(metadata, dict)
                assert metadata["secret_names"] == ["EMPTY", "MCP_IMPORTED_SECRET"]
                assert metadata["configured_secret_count"] == 1
                assert metadata["empty_secret_names"] == ["EMPTY"]
                assert imported_secret not in json.dumps(metadata)
                generated_result = await session.call_tool("secret_write", {
                    "action": "generate", "profile": managed_profile, "secret": "EMPTY", "bytes": 32,
                })
                assert not generated_result.is_error, generated_result.content
                generated = generated_result.structured_content
                assert isinstance(generated, dict) and generated["generated"] is True
                project_result = await session.call_tool("project", {
                    "action": "register", "cwd": project_cwd, "name": "runtime-e2e",
                })
                assert not project_result.is_error, project_result.content
                action_result = await session.call_tool("action", {
                    "operation": "set", "profile": managed_profile,
                    "action_name": "mcp-admin-check", "cwd": project_cwd,
                    "command": ["printenv", "EMPTY"], "all_secrets": True,
                    "timeout_seconds": 30, "max_output_bytes": 65536,
                })
                assert not action_result.is_error, action_result.content
                workflow_result = await session.call_tool("project", {
                    "action": "set_workflow", "cwd": project_cwd,
                    "workflow": "development",
                    "steps": [f"{managed_profile}/mcp-admin-check"],
                    "required_secrets": [f"{managed_profile}/EMPTY"],
                    "timeout_seconds": 60,
                })
                assert not workflow_result.is_error, workflow_result.content
                registration_result = await session.call_tool("project", {
                    "action": "registration", "cwd": project_cwd,
                })
                registration = registration_result.structured_content
                assert isinstance(registration, dict)
                assert registration["registered"] is True
                assert registration["workflows"] == ["development"]
                list_result = await session.call_tool("action", {
                    "operation": "list", "profile": managed_profile,
                })
                listed = list_result.structured_content
                assert isinstance(listed, dict)
                assert "mcp-admin-check" in listed["actions"]
                for payload in (
                    {"action": "remove_workflow", "cwd": project_cwd, "workflow": "development"},
                    {"action": "unregister", "cwd": project_cwd},
                ):
                    cleanup_result = await session.call_tool("project", payload)
                    assert not cleanup_result.is_error, cleanup_result.content
                action_remove_result = await session.call_tool("action", {
                    "operation": "remove", "profile": managed_profile,
                    "action_name": "mcp-admin-check",
                })
                assert not action_remove_result.is_error, action_remove_result.content
                remove_result = await session.call_tool("secret_delete", {
                    "action": "secret", "profile": managed_profile, "secret": "EMPTY",
                })
                assert not remove_result.is_error, remove_result.content
                remove_profile_result = await session.call_tool(
                    "secret_delete", {"action": "profile", "profile": managed_profile},
                )
                assert not remove_profile_result.is_error, remove_profile_result.content
                started_result = await session.call_tool("action", {
                    "operation": "run", "profile": profile, "action_name": "redaction-check",
                })
                assert not started_result.is_error, started_result.content
                started = started_result.structured_content
                assert isinstance(started, dict)
                session_id = str(started["session_id"])
                deadline = time.monotonic() + 15
                while True:
                    read_result = await session.call_tool("action", {
                        "operation": "process", "session_id": session_id, "limit": 65536,
                    })
                    assert not read_result.is_error, read_result.content
                    result = read_result.structured_content
                    assert isinstance(result, dict)
                    if result["status"] == "exited":
                        redaction_result = result
                        break
                    assert time.monotonic() < deadline
                    await asyncio.sleep(0.1)
                stop_started_result = await session.call_tool(
                    "action", {"operation": "run", "profile": profile, "action_name": "stop-check"},
                )
                assert not stop_started_result.is_error, stop_started_result.content
                stop_started = stop_started_result.structured_content
                assert isinstance(stop_started, dict)
                stop_result = await session.call_tool(
                    "action", {"operation": "stop", "session_id": str(stop_started["session_id"])},
                )
                assert not stop_result.is_error, stop_result.content
                stopped = stop_result.structured_content
                assert isinstance(stopped, dict)
                assert stopped["status"] == "exited", stopped
                assert stopped["exit_code"] is not None, stopped
                return redaction_result, stopped


profile = "verify-" + secrets.token_hex(4)
managed_profile = "managed-" + secrets.token_hex(4)
secret_name = "LOKI_VERIFY_SECRET"
secret_value = "loki-verify-" + secrets.token_urlsafe(24)
second_secret_name = "LOKI_VERIFY_SECOND_SECRET"
second_secret_value = "loki-verify-" + secrets.token_urlsafe(24)
session_id = ""
staged_path: Path | None = None
project_directory = Path("/srv/workspace/loki/.loki") / f"runtime-admin-e2e-{secrets.token_hex(4)}"
try:
    initialized = request_runtime({"operation": "init"})
    assert initialized["initialized"] is True
    request_runtime({"operation": "profile_create", "profile": profile})
    request_runtime({
        "operation": "secret_set", "profile": profile,
        "secret": secret_name, "value": secret_value,
    })
    request_runtime({
        "operation": "secret_set", "profile": profile,
        "secret": second_secret_name, "value": second_secret_value,
    })
    request_runtime({
        "operation": "action_set", "profile": profile, "action_name": "redaction-check",
        "action": {
            "cwd": ".", "command": ["printenv", secret_name, second_secret_name],
            "secrets": [], "all_secrets": True,
            "timeout_seconds": 30, "max_output_bytes": 65_536,
        },
    })
    request_runtime({
        "operation": "action_set", "profile": profile, "action_name": "stop-check",
        "action": {
            "cwd": ".", "command": ["tail", "-f", "/dev/null"],
            "secrets": [], "all_secrets": True,
            "timeout_seconds": 30, "max_output_bytes": 65_536,
        },
    })
    metadata = request_runtime({"operation": "get_profile", "profile": profile})
    assert metadata["secret_names"] == sorted([secret_name, second_secret_name])
    assert metadata["action_policies"]["redaction-check"]["all_secrets"] is True
    assert secret_value not in json.dumps(metadata)
    assert second_secret_value not in json.dumps(metadata)
    encrypted = Path("/var/lib/loki/runtime/store.json").read_bytes()
    assert secret_value.encode() not in encrypted
    assert second_secret_value.encode() not in encrypted
    import_source = Path("/tmp") / f"loki-runtime-import-{secrets.token_hex(8)}.env"
    imported_secret = "loki-imported-" + secrets.token_urlsafe(24)
    import_source.write_text(
        f"MCP_IMPORTED_SECRET={imported_secret}\nEMPTY=\n", encoding="utf-8",
    )
    staged_result = subprocess.run(
        ["/usr/local/bin/loki", "secret", "stage-env", str(import_source), "--delete-source"],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=True, timeout=10,
    )
    staged = json.loads(staged_result.stdout)
    import_id = staged["import_id"]
    staged_path = INBOX_DIRECTORY / f"{import_id}.env"
    assert staged_path.is_file() and not import_source.exists()
    assert imported_secret not in staged_result.stdout
    subprocess.run(
        ["/usr/sbin/runuser", "-u", "runner", "--", "/usr/bin/git", "init", str(project_directory)],
        check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    result, stopped = asyncio.run(verify_mcp(
        profile, managed_profile, import_id, imported_secret,
        project_directory.relative_to("/srv/workspace/loki").as_posix(),
    ))
    assert staged_path is not None and not staged_path.exists()
    session_id = str(result["session_id"])
    assert result["exit_code"] == 0, result
    assert "[REDACTED]" in result["output"]
    assert secret_value not in result["output"]
    assert second_secret_value not in result["output"]
    assert stopped["exit_code"] != 0
    denied = subprocess.run(
        [
            "/usr/sbin/runuser", "-u", "runner", "--", "/usr/local/bin/loki",
            "action", "remove", profile, "forbidden-action",
        ],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=10,
    )
    assert denied.returncode != 0
    assert "require root" in denied.stdout.lower()
    audit = request_runtime({"operation": "audit", "limit": 20})
    assert any(record["profile"] == profile for record in audit["records"])
finally:
    shutil.rmtree(project_directory, ignore_errors=True)
    if staged_path is not None:
        staged_path.unlink(missing_ok=True)
    if session_id:
        try:
            request_runtime({"operation": "stop_process", "session_id": session_id})
        except Exception:
            pass
    try:
        request_runtime({"operation": "profile_remove", "profile": profile})
    except Exception:
        pass
    try:
        request_runtime({"operation": "profile_remove", "profile": managed_profile})
    except Exception:
        pass

print(json.dumps({"runtime": "ok", "profile": profile, "redaction": True, "stop": True}))
