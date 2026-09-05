#!/usr/bin/env python3
from __future__ import annotations

import asyncio
from pathlib import Path
import json
import time

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client


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


async def verify() -> None:
    await wait_for_server()
    token = TOKEN_PATH.read_text(encoding="utf-8").strip()
    async with httpx2.AsyncClient(headers={"Authorization": f"Bearer {token}"}) as http_client:
        async with streamable_http_client(MCP_URL, http_client=http_client) as streams:
            async with ClientSession(*streams) as session:
                initialized = await session.initialize()
                instructions = initialized.instructions or ""
                assert "user-visible preamble before the first tool call" in instructions
                assert "after each meaningful batch" in instructions
                assert "raw commands and verbose output in the tool-call cards" in instructions
                assert "use command_start" in instructions
                assert "useful independent work" in instructions
                assert "Do not mutate files that the running validation reads" in instructions
                catalog = await session.list_tools()
                names = {tool.name for tool in catalog.tools}
                assert names == {
                    "system_inspect", "runtime_stop", "preview_publish",
                    "shared_resources", "revoke_share", "browser_session",
                    "browser_observe", "browser_interact", "browser_screenshot",
                    "browser_save_screenshot", "browser_share_screenshot",
                    "workspace_read", "read_image", "share_image", "artifact_publish",
                    "write_image", "workspace_edit", "project", "restore_workspace_file",
                    "remove_tracked_file", "agent_context", "skill_read", "skill_write",
                    "git_inspect", "git_stage", "developer_view", "secret_inspect",
                    "secret_write", "secret_delete", "bootstrap_project",
                    "action", "command_run", "command_start", "process_inspect",
                    "task_inspect", "task_write", "task_delete",
                }
                for tool in catalog.tools:
                    metadata = tool.model_dump(by_alias=True).get("_meta", {})
                    assert metadata.get("openai/toolInvocation/invoking"), tool.name
                    assert metadata.get("openai/toolInvocation/invoked"), tool.name
                print(json.dumps({
                    "progress_guidance": "ok",
                    "tool_count": len(catalog.tools),
                    "all_tools_have_invocation_status": True,
                }))


asyncio.run(verify())
