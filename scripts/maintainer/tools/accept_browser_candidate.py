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
import re
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
        opened = result(await client.request("tools/call", {"name":"new_page", "arguments":{"url":url}}))
        text = "\n".join(item.get("text", "") for item in opened.get("content", []) if item.get("type") == "text")
        selected = re.findall(r"^(\d+): .* \[selected\]", text, re.MULTILINE)
        if len(selected) != 1:
            raise ValueError("DevTools did not identify its owned selected fixture page")
        page_id = int(selected[0])
        result(await client.request("tools/call", {"name":"navigate_page", "arguments":{"pageId":page_id, "type":"url", "url":url}}))
        snapshot = result(await client.request("tools/call", {"name":"take_snapshot", "arguments":{"pageId":page_id}}))
        if "Loki native candidate fixture" not in json.dumps(snapshot):
            raise ValueError("DevTools did not observe its independent synthetic fixture")
        engine = next(name for name in tools["loki_browser_files"]["inputSchema"]["properties"]["engine"]["enum"] if name.endswith("/playwright"))
        data = b"Native session transfer fixture\n"
        staged = result(await client.request("tools/call", {"name":"loki_browser_files", "arguments":{"engine":engine, "action":"stage", "name":"fixture.txt", "data":base64.b64encode(data).decode()}}))
        entry = json.loads(next(item["text"] for item in staged["content"] if item["type"] == "text"))
        returned = result(await client.request("resources/read", {"uri":entry["uri"]}))
        if base64.b64decode(returned["contents"][0]["blob"]) != data:
            raise ValueError("native owned file resource changed its staged bytes")
        # Official Playwright resolves a supplied filename against the client
        # workspace. Omit it to exercise its owned output directory on every OS.
        screenshot = result(await client.request("tools/call", {"name":"browser_take_screenshot", "arguments":{"type":"png"}}))
        listing = result(await client.request("tools/call", {"name":"loki_browser_files", "arguments":{"engine":engine, "action":"list"}}))
        entries = json.loads(next(item["text"] for item in listing["content"] if item["type"] == "text"))["files"]
        images = [item for item in entries if item["name"].endswith(".png")]
        if len(images) != 1:
            raise ValueError("Native screenshot did not create exactly one owned PNG: " + json.dumps(entries)[:4096])
        readback = result(await client.request("tools/call", {"name":"loki_browser_files", "arguments":{"engine":engine, "action":"read", "name":images[0]["name"]}}))
        if not any(item["type"] == "image" and base64.b64decode(item["data"]).startswith(b"\x89PNG\r\n\x1a\n") for item in readback["content"]):
            raise ValueError("native owned screenshot did not return PNG image bytes")
        if not any(item["type"] == "image" and base64.b64decode(item["data"]).startswith(b"\x89PNG\r\n\x1a\n") for item in screenshot["content"]):
            raise ValueError("native screenshot did not return inline PNG image bytes")
        invoke("tools", "disable", "browser")
        denied = await client.request("tools/call", {"name":"browser_navigate", "arguments":{"url":url}})
        if "error" not in denied and not denied.get("result", {}).get("isError"):
            raise ValueError("cached browser invocation survived disabled activation")
    finally:
        await client.close()


def accept(candidate, protocol_check=protocol):
    document = json.loads((candidate / "candidate.json").read_text(encoding="utf-8"))
    if document.get("schema") != 1 or document.get("release") != "0.2.1" or document.get("target", {}).get("mode") != "project-host" or document.get("manager") != "manager" or document.get("catalog") != "release/catalog.json":
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
            completed = subprocess.run([str(binary), "--root", str(root), *arguments], capture_output=True, text=True)
            if completed.returncode:
                raise ValueError("Candidate command failed: " + " ".join(arguments) + "\n" + completed.stdout + completed.stderr)
            return completed.stdout
        invoke("tools", "configure", "--mode", "project-host")
        invoke("tools", "install", "browser", "--catalog", str(candidate / "release" / "catalog.json"), "--archives", str(candidate / "release" / "archives"))
        status = json.loads(invoke("status"))
        if status["tools"]["browser"]["enabled"]:
            raise ValueError("installation implicitly enabled browser tools")
        invoke("tools", "enable", "browser")
        invoke("doctor")
        asyncio.run(protocol_check(binary, root, workspace, invoke))
        invoke("tools", "remove", "browser")
        if marker.read_text(encoding="utf-8") != "Keep this project file":
            raise ValueError("browser lifecycle changed the project workspace")
        print(json.dumps({"native_browser_acceptance":"pass", "target":document["target"], "engines":["playwright", "devtools"], "scope":"isolated local candidate; real desktop SSH and full authority remain separate"}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path)
    args = parser.parse_args()
    accept(args.candidate.resolve())
