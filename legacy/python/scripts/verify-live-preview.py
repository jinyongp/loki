#!/usr/bin/env python3
from __future__ import annotations

import asyncio
import argparse
import grp
import json
import os
from pathlib import Path
import pwd
import shutil
from urllib.parse import urlsplit

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client
from websockets.asyncio.client import connect as websocket_connect


TOKEN_PATH = Path("/etc/loki/token")
MCP_URL = "http://127.0.0.1:8765/mcp"
WORKSPACE = Path("/srv/workspace/loki")
TEST_CWD = ".loki/live-preview-e2e"
TEST_DIRECTORY = WORKSPACE / TEST_CWD
TARGET_PORT = 50874


async def structured(session: ClientSession, name: str, arguments: dict) -> dict:
    result = await session.call_tool(name, arguments)
    if result.is_error:
        raise RuntimeError(f"{name} failed: {result.content}")
    value = result.structured_content
    if not isinstance(value, dict):
        raise RuntimeError(f"{name} returned no structured content")
    return value


async def wait_for_port(session: ClientSession, port: int) -> None:
    for _ in range(30):
        state = await structured(session, "system_inspect", {"action": "port", "port": port})
        if state.get("in_use") is True:
            return
        await asyncio.sleep(0.5)
    raise RuntimeError(f"port {port} did not become ready")


async def verify(hold_seconds: int = 0) -> None:
    token = TOKEN_PATH.read_text(encoding="utf-8").strip()
    headers = {"Authorization": f"Bearer {token}"}
    session_id: str | None = None
    share_id: str | None = None
    if TEST_DIRECTORY.exists():
        shutil.rmtree(TEST_DIRECTORY)
    TEST_DIRECTORY.mkdir(parents=True)
    shutil.copy2(Path(__file__).with_name("verify-preview-server.mjs"), TEST_DIRECTORY / "server.mjs")
    runner = pwd.getpwnam("runner")
    workspace_group = grp.getgrnam("workspace")
    os.chown(TEST_DIRECTORY, runner.pw_uid, workspace_group.gr_gid)
    os.chown(TEST_DIRECTORY / "server.mjs", runner.pw_uid, workspace_group.gr_gid)
    async with httpx2.AsyncClient(headers=headers) as http_client:
        async with streamable_http_client(MCP_URL, http_client=http_client) as streams:
            async with ClientSession(*streams) as session:
                await session.initialize()
                try:
                    current = await structured(session, "system_inspect", {"action": "port", "port": TARGET_PORT})
                    if current.get("in_use") is True:
                        raise RuntimeError(f"verification port {TARGET_PORT} is already in use")
                    process = await structured(
                        session,
                        "command_start",
                        {
                            "action": "exec", "executable": "node",
                            "arguments": ["server.mjs", str(TARGET_PORT)], "cwd": TEST_CWD,
                        },
                    )
                    session_id = str(process["session_id"])
                    await wait_for_port(session, TARGET_PORT)
                    shared = await structured(
                        session,
                        "preview_publish",
                        {"action": "server", "port": TARGET_PORT, "ttl_seconds": 300},
                    )
                    share_id = str(shared["share_id"])
                    hostname = urlsplit(str(shared["url"])).hostname
                    assert hostname is not None
                    async with httpx2.AsyncClient(follow_redirects=True) as preview_client:
                        response = await preview_client.get(
                            "http://127.0.0.1:8765/",
                            headers={"Host": hostname},
                        )
                        response.raise_for_status()
                        assert "loki browser verifier" in response.text.lower()
                        vite_client = await preview_client.get(
                            "http://127.0.0.1:8765/asset.js",
                            headers={"Host": hostname},
                        )
                        vite_client.raise_for_status()
                        parallel = await asyncio.gather(*(
                            preview_client.get(
                                "http://127.0.0.1:8765/asset.js",
                                headers={"Host": hostname},
                            )
                            for _ in range(32)
                        ))
                        assert all(item.status_code == 200 for item in parallel)
                    websocket_uri = f"ws://{hostname}/socket"
                    async with websocket_connect(
                        websocket_uri,
                        host="127.0.0.1",
                        port=8765,
                        origin=f"https://{hostname}",
                        subprotocols=["vite-hmr"],
                        open_timeout=10,
                    ) as websocket:
                        assert websocket.subprotocol == "vite-hmr"
                    print(json.dumps({
                        "url": shared["url"],
                        "share_id": share_id,
                        "status_code": response.status_code,
                        "bytes": len(response.content),
                        "parallel_requests": len(parallel),
                        "websocket": "vite-hmr",
                    }, separators=(",", ":")), flush=True)
                    if hold_seconds > 0:
                        await asyncio.sleep(hold_seconds)
                finally:
                    if share_id is not None:
                        await session.call_tool("revoke_share", {"kind": "preview", "share_id": share_id})
                    if session_id is not None:
                        await session.call_tool("runtime_stop", {"action": "process", "session_id": session_id})
                    if TEST_DIRECTORY.exists():
                        shutil.rmtree(TEST_DIRECTORY)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--hold-seconds", type=int, default=0)
    args = parser.parse_args()
    asyncio.run(verify(max(0, min(args.hold_seconds, 300))))
