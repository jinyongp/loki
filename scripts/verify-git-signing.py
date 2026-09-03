#!/usr/bin/env python3
from __future__ import annotations

import asyncio
import json
from pathlib import Path
import shutil
import time

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client


TOKEN_PATH = Path("/etc/loki/token")
MCP_URL = "http://127.0.0.1:8765/mcp"
WORKSPACE = Path("/srv/workspace/loki")
TEST_DIRECTORY = WORKSPACE / ".loki" / "git-signing-e2e"


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


async def call(session: ClientSession, name: str, arguments: dict[str, object]) -> dict[str, object]:
    result = await session.call_tool(name, arguments)
    if result.is_error:
        raise RuntimeError(f"{name} failed: {result.content}")
    value = result.structured_content
    if not isinstance(value, dict):
        raise RuntimeError(f"{name} returned no structured result")
    return value


async def verify() -> None:
    await wait_for_server()
    resolved = TEST_DIRECTORY.resolve()
    if resolved != WORKSPACE / ".loki" / "git-signing-e2e":
        raise RuntimeError("unsafe Git signing verification directory")
    if TEST_DIRECTORY.exists():
        shutil.rmtree(TEST_DIRECTORY)
    try:
        token = TOKEN_PATH.read_text(encoding="utf-8").strip()
        async with httpx2.AsyncClient(headers={"Authorization": f"Bearer {token}"}) as http_client:
            async with streamable_http_client(MCP_URL, http_client=http_client) as streams:
                async with ClientSession(*streams) as session:
                    await session.initialize()
                    await call(session, "agent_context", {"cwd": "."})
                    diagnostics = await call(session, "system_inspect", {"action": "diagnostics"})
                    signing = diagnostics["git_signing"]
                    assert signing == {
                        "identity_configured": True,
                        "format": "ssh",
                        "commit_signing_required": True,
                        "public_key_available": True,
                        "agent_socket_available": True,
                    }

                    fixture = ".loki/git-signing-e2e"
                    await call(session, "workspace_edit", {
                        "action": "create",
                        "path": f"{fixture}/signed.txt",
                        "content": "signed by Loki\n",
                    })
                    for arguments in (
                        ["init", "-q"],
                        ["add", "signed.txt"],
                        ["commit", "-m", "test: verify Loki commit signing"],
                    ):
                        result = await call(session, "command_run", {
                            "action": "exec", "executable": "git", "arguments": arguments, "cwd": fixture,
                        })
                        assert result["exit_code"] == 0, result

                    commit = await call(session, "command_run", {
                        "action": "exec", "executable": "git",
                        "arguments": ["cat-file", "commit", "HEAD"],
                        "cwd": fixture,
                    })
                    assert "gpgsig -----BEGIN SSH SIGNATURE-----" in str(commit["output"])
                    verified = await call(session, "command_run", {
                        "action": "exec", "executable": "git", "arguments": ["verify-commit", "HEAD"], "cwd": fixture,
                    })
                    assert verified["exit_code"] == 0, verified

                    bypass = await session.call_tool("command_run", {
                        "action": "exec", "executable": "git",
                        "arguments": ["commit", "--allow-empty", "--no-gpg-sign", "-m", "unsigned"],
                        "cwd": fixture,
                    })
                    assert bypass.is_error
                    assert "unsigned Git commits" in str(bypass.content)

                    print(json.dumps({
                        "identity": "configured",
                        "format": "ssh",
                        "signature": "verified",
                        "unsigned_bypass": "blocked",
                    }, separators=(",", ":")))
    finally:
        if TEST_DIRECTORY.exists():
            shutil.rmtree(TEST_DIRECTORY)


if __name__ == "__main__":
    asyncio.run(verify())
