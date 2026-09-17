#!/usr/bin/env python3
from __future__ import annotations

import asyncio
import io
import json
from pathlib import Path
import shutil
import subprocess
import time
import urllib.request
import zipfile

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client


TOKEN_PATH = Path("/etc/loki/token")
MCP_URL = "http://127.0.0.1:8765/mcp"
WORKSPACE = Path("/srv/workspace/loki")
TEST_DIRECTORY = WORKSPACE / ".loki" / "artifact-viewer-e2e"
VIEWER_URI = "ui://loki/developer-output-v1.html"


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


def download(url: str) -> tuple[bytes, str]:
    request = urllib.request.Request(url, headers={"User-Agent": "Loki artifact viewer E2E"})
    with urllib.request.urlopen(request, timeout=15) as response:
        return response.read(), response.headers.get("Content-Disposition", "")


async def verify() -> None:
    await wait_for_server()
    resolved = TEST_DIRECTORY.resolve()
    if resolved != WORKSPACE / ".loki" / "artifact-viewer-e2e":
        raise RuntimeError("unsafe artifact verification directory")
    if TEST_DIRECTORY.exists():
        shutil.rmtree(TEST_DIRECTORY)
    try:
        token = TOKEN_PATH.read_text(encoding="utf-8").strip()
        async with httpx2.AsyncClient(headers={"Authorization": f"Bearer {token}"}) as http_client:
            async with streamable_http_client(MCP_URL, http_client=http_client) as streams:
                async with ClientSession(*streams) as session:
                    await session.initialize()
                    catalog = await session.list_tools()
                    descriptors = {tool.name: tool.model_dump(by_alias=True) for tool in catalog.tools}
                    expected = {"artifact_publish", "shared_resources", "revoke_share", "developer_view"}
                    assert expected <= descriptors.keys()
                    assert descriptors["developer_view"]["_meta"]["ui"]["resourceUri"] == VIEWER_URI

                    resource = await session.read_resource(VIEWER_URI)
                    content = resource.model_dump(by_alias=True)["contents"][0]
                    assert content["mimeType"] == "text/html;profile=mcp-app"
                    assert "navigator.clipboard.writeText" in content["text"]
                    assert "requestDisplayMode" in content["text"]

                    fixture = ".loki/artifact-viewer-e2e"
                    await call(session, "workspace_edit", {
                        "action": "create", "path": f"{fixture}/notes.txt", "content": "alpha\nbeta\n",
                    })
                    await call(session, "workspace_edit", {
                        "action": "create",
                        "path": f"{fixture}/report.xml",
                        "content": '<testsuite tests="3" failures="1" errors="0" skipped="1"/>',
                    })
                    subprocess.run(
                        ["/usr/sbin/runuser", "-u", "runner", "--", "git", "init", "-q", str(TEST_DIRECTORY)],
                        env={"HOME": "/home/runner", "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"},
                        check=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                    )
                    shared = await call(session, "artifact_publish", {
                        "action": "file", "path": f"{fixture}/notes.txt", "ttl_seconds": 120,
                    })
                    file_data, disposition = await asyncio.to_thread(download, str(shared["url"]))
                    assert file_data == b"alpha\nbeta\n"
                    assert disposition.startswith("attachment;")

                    bundle = await call(session, "artifact_publish", {
                        "action": "bundle", "paths": [fixture], "filename": "artifact-viewer-e2e.zip", "ttl_seconds": 120,
                    })
                    bundle_data, bundle_disposition = await asyncio.to_thread(download, str(bundle["url"]))
                    assert bundle_disposition.startswith("attachment;")
                    with zipfile.ZipFile(io.BytesIO(bundle_data)) as archive:
                        assert archive.namelist() == [
                            f"{fixture}/notes.txt", f"{fixture}/report.xml",
                        ]

                    report = await call(session, "developer_view", {"action": "test_report", "path": f"{fixture}/report.xml"})
                    assert report["stats"]["tests"] == 3 and report["stats"]["failures"] == 1
                    diff = await call(session, "developer_view", {
                        "action": "git_diff", "cwd": ".loki/artifact-viewer-e2e", "staged": False,
                    })
                    assert diff["kind"] == "diff"

                    await call(session, "workspace_edit", {
                        "action": "create",
                        "path": f"{fixture}/log_probe.py",
                        "content": "print('viewer-log-ready')\n",
                    })
                    started = await call(session, "command_start", {
                        "action": "exec", "executable": "python3",
                        "arguments": ["log_probe.py"],
                        "cwd": fixture,
                    })
                    session_id = str(started["session_id"])
                    deadline = time.monotonic() + 5
                    log: dict[str, object] = {}
                    while time.monotonic() < deadline:
                        log = await call(session, "developer_view", {"action": "process_log", "session_id": session_id})
                        if "viewer-log-ready" in str(log["content"]):
                            break
                        await asyncio.sleep(0.05)
                    assert "viewer-log-ready" in str(log["content"])

                    listed = await call(session, "shared_resources", {"kind": "artifacts"})
                    ids = {item["share_id"] for item in listed["artifacts"]}
                    assert shared["share_id"] in ids and bundle["share_id"] in ids
                    await call(session, "revoke_share", {"kind": "artifact", "share_id": shared["share_id"]})
                    await call(session, "revoke_share", {"kind": "artifact", "share_id": bundle["share_id"]})

                    print(json.dumps({
                        "artifact_tools": 3,
                        "viewer_tools": 1,
                        "attachment": "downloaded",
                        "bundle_files": bundle["file_count"],
                        "viewer_resource": VIEWER_URI,
                    }, separators=(",", ":")))
    finally:
        if TEST_DIRECTORY.exists():
            shutil.rmtree(TEST_DIRECTORY)


if __name__ == "__main__":
    asyncio.run(verify())
