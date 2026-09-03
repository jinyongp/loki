#!/usr/bin/env python3
from __future__ import annotations

import asyncio
import json
from pathlib import Path
import time

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client


TOKEN_PATH = Path("/etc/loki/token")
MCP_URL = "http://127.0.0.1:8765/mcp"
RESOURCE_URI = "ui://loki/image-viewer-v3.html"
PREVIEW_URI = "ui://loki/live-preview-v1.html"


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
    headers = {"Authorization": f"Bearer {token}"}
    async with httpx2.AsyncClient(headers=headers) as http_client:
        async with streamable_http_client(MCP_URL, http_client=http_client) as streams:
            async with ClientSession(*streams) as session:
                await session.initialize()
                tools = await session.list_tools()
                browser_tools = sorted(tool.name for tool in tools.tools if tool.name.startswith("browser_"))
                assert len(browser_tools) == 6
                assert "browser_observe" in browser_tools
                descriptor = next(tool for tool in tools.tools if tool.name == "share_image")
                tool_data = descriptor.model_dump(by_alias=True)
                assert tool_data["_meta"]["ui"]["resourceUri"] == RESOURCE_URI
                assert tool_data["_meta"]["openai/outputTemplate"] == RESOURCE_URI
                browser_descriptor = next(
                    tool for tool in tools.tools if tool.name == "browser_share_screenshot"
                )
                browser_tool_data = browser_descriptor.model_dump(by_alias=True)
                assert browser_tool_data["_meta"]["ui"]["resourceUri"] == RESOURCE_URI
                preview_descriptor = next(
                    tool for tool in tools.tools if tool.name == "preview_publish"
                )
                preview_tool_data = preview_descriptor.model_dump(by_alias=True)
                assert preview_tool_data["_meta"]["ui"]["resourceUri"] == PREVIEW_URI
                assert preview_tool_data["_meta"]["openai/outputTemplate"] == PREVIEW_URI

                resource = await session.read_resource(RESOURCE_URI)
                resource_data = resource.model_dump(by_alias=True)
                content = resource_data["contents"][0]
                assert content["mimeType"] == "text/html;profile=mcp-app"
                assert "ui/notifications/tool-result" in content["text"]
                assert "openai:set_globals" in content["text"]
                assert "notifyIntrinsicHeight" in content["text"]
                assert content["_meta"]["ui"]["csp"]["resourceDomains"] == [
                    "https://mcp.streamliner.im"
                ]
                assert content["_meta"]["ui"]["domain"] == "https://mcp.streamliner.im"

                preview_resource = await session.read_resource(PREVIEW_URI)
                preview_data = preview_resource.model_dump(by_alias=True)["contents"][0]
                assert preview_data["mimeType"] == "text/html;profile=mcp-app"
                assert "ui/notifications/tool-result" in preview_data["text"]
                assert "frame.src = currentUrl" in preview_data["text"]
                assert preview_data["_meta"]["ui"]["csp"]["frameDomains"] == [
                    "https://*.streamliner.im"
                ]
                assert preview_data["_meta"]["ui"]["domain"] == "https://mcp.streamliner.im"
                print(json.dumps({
                    "tool": descriptor.name,
                    "browser_tool": browser_descriptor.name,
                    "resource": RESOURCE_URI,
                    "mime_type": content["mimeType"],
                    "resource_domains": content["_meta"]["ui"]["csp"]["resourceDomains"],
                    "preview_resource": PREVIEW_URI,
                    "frame_domains": preview_data["_meta"]["ui"]["csp"]["frameDomains"],
                    "browser_tool_count": len(browser_tools),
                }, separators=(",", ":")))


if __name__ == "__main__":
    asyncio.run(verify())
