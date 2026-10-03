#!/usr/bin/env python3
"""Final native project-host browser acceptance of an exact local candidate.

Run after all candidate preparation. This uses temporary synthetic workspaces
and a separate management root, with both official engines and owned results.
It does not replace real desktop SSH acceptance or full-runtime authority tests.
"""
import argparse
import asyncio
import base64
import json
from pathlib import Path
import subprocess
import tempfile
import zipfile

from accept_manager_bundle import accept as accept_manager
from build_browser_bundle import digest
from mcp_probe import Client, result


async def protocol(binary, root, workspace, invoke):
    client = Client([str(binary), "--root", str(root), "tools", "serve", "browser", "--workspace", str(workspace), "--engine", "both"], workspace)
    await client.start()
    try:
        await client.initialize()
        definitions = result(await client.request("tools/list"))["tools"]
        tools = {tool["name"]:tool for tool in definitions}
        if len(tools) != len(definitions) or not {"browser_navigate", "navigate_page", "loki_browser_files"} <= tools.keys() or "browser_run_code_unsafe" in tools:
            raise ValueError("combined native browser discovery/unsafe capability contract differs")
        url = "data:text/html,<html><body><h1>Loki native candidate fixture</h1></body></html>"
        result(await client.request("tools/call", {"name":"browser_navigate", "arguments":{"url":url}}))
        snapshot = result(await client.request("tools/call", {"name":"browser_snapshot", "arguments":{}}))
        if "Loki native candidate fixture" not in json.dumps(snapshot):
            raise ValueError("Playwright did not observe the synthetic native fixture")
        result(await client.request("tools/call", {"name":"navigate_page", "arguments":{"type":"url", "url":url}}))
        snapshot = result(await client.request("tools/call", {"name":"take_snapshot", "arguments":{}}))
        if "Loki native candidate fixture" not in json.dumps(snapshot):
            raise ValueError("DevTools did not observe its independent synthetic fixture")
        engine = next(name for name in tools["loki_browser_files"]["inputSchema"]["properties"]["engine"]["enum"] if name.endswith("/playwright"))
        data = b"Native session transfer fixture\n"
        staged = result(await client.request("tools/call", {"name":"loki_browser_files", "arguments":{"engine":engine, "action":"stage", "name":"fixture.txt", "data":base64.b64encode(data).decode()}}))
        entry = json.loads(next(item["text"] for item in staged["content"] if item["type"] == "text"))
        returned = result(await client.request("resources/read", {"uri":entry["uri"]}))
        if base64.b64decode(returned["contents"][0]["blob"]) != data:
            raise ValueError("native owned file resource changed its staged bytes")
        screenshot = result(await client.request("tools/call", {"name":"browser_take_screenshot", "arguments":{"type":"png", "filename":"native-fixture.png"}}))
        if not any(item["type"] == "image" and len(base64.b64decode(item["data"])) > 0 for item in screenshot["content"]):
            readback = result(await client.request("tools/call", {"name":"loki_browser_files", "arguments":{"engine":engine, "action":"read", "name":"native-fixture.png"}}))
            if not any(item["type"] == "image" and len(base64.b64decode(item["data"])) > 0 for item in readback["content"]):
                raise ValueError("native screenshot did not return usable image bytes")
        invoke("tools", "disable", "browser")
        denied = await client.request("tools/call", {"name":"browser_navigate", "arguments":{"url":url}})
        if "error" not in denied and not denied.get("result", {}).get("isError"):
            raise ValueError("cached browser invocation survived disabled activation")
    finally:
        await client.close()


def accept(candidate):
    document = json.loads((candidate / "candidate.json").read_text(encoding="utf-8"))
    if document.get("schema") != 1 or document.get("release") != "0.2.0" or document.get("target", {}).get("mode") != "project-host" or document.get("manager") != "manager" or document.get("catalog") != "release/catalog.json":
        raise ValueError("browser acceptance requires a complete project-host candidate")
    manager = candidate / "manager"
    accept_manager(manager)
    receipt = json.loads((manager / "manager-receipt.json").read_text(encoding="utf-8"))
    archive = manager / receipt["archive"]
    if digest(archive) != receipt["archive_sha256"]:
        raise ValueError("manager candidate changed after acceptance")
    with tempfile.TemporaryDirectory(prefix="loki-browser-accept-") as temporary:
        scratch = Path(temporary)
        binary = scratch / receipt["binary"]
        with zipfile.ZipFile(archive) as packed:
            binary.write_bytes(packed.read(receipt["binary"]))
        if digest(binary) != receipt["binary_sha256"]:
            raise ValueError("browser acceptance command differs from its native receipt")
        binary.chmod(0o755)
        root, workspace = scratch / "management", scratch / "workspace"
        workspace.mkdir()
        marker = workspace / "user-fixture.txt"
        marker.write_text("Keep this project file", encoding="utf-8")
        def invoke(*arguments):
            return subprocess.check_output([str(binary), "--root", str(root), *arguments], text=True)
        invoke("tools", "configure", "--mode", "project-host")
        invoke("tools", "install", "browser", "--catalog", str(candidate / "release" / "catalog.json"), "--archives", str(candidate / "release" / "archives"))
        status = json.loads(invoke("status"))
        if status["tools"]["browser"]["enabled"]:
            raise ValueError("installation implicitly enabled browser tools")
        invoke("tools", "enable", "browser")
        invoke("doctor")
        asyncio.run(protocol(binary, root, workspace, invoke))
        invoke("tools", "remove", "browser")
        if marker.read_text(encoding="utf-8") != "Keep this project file":
            raise ValueError("browser lifecycle changed the project workspace")
        print(json.dumps({"native_browser_acceptance":"pass", "target":document["target"], "engines":["playwright", "devtools"], "scope":"isolated local candidate; real desktop SSH and full authority remain separate"}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path)
    args = parser.parse_args()
    accept(args.candidate.resolve())
