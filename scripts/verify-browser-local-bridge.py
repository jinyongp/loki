#!/usr/bin/env python3
from __future__ import annotations

import json
import base64
import hashlib
import grp
import os
from pathlib import Path
import pwd
import shutil
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit


MCP_URL = "http://127.0.0.1:8765/mcp"
TOKEN_PATH = Path("/etc/loki/token")
SCREENSHOT_PATH = ".browser-downloads/loki-browser-save-e2e.png"
SCREENSHOT_HOST_PATH = Path("/srv/workspace/loki") / SCREENSHOT_PATH
TEST_CWD = ".loki/browser-e2e"
TEST_DIRECTORY = Path("/srv/workspace/loki") / TEST_CWD
TARGET_PORT = 50873
TARGET_URL = f"http://127.0.0.1:{TARGET_PORT}/"


def call_tool(name: str, arguments: dict[str, object], request_id: int) -> dict[str, object]:
    payload = {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": "tools/call",
        "params": {"name": name, "arguments": arguments},
    }
    request = urllib.request.Request(
        MCP_URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {TOKEN_PATH.read_text(encoding='utf-8').strip()}",
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": "2025-06-18",
        },
    )
    deadline = time.monotonic() + 20
    while True:
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                body = response.read().decode("utf-8")
            break
        except urllib.error.URLError:
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.25)
    data_lines = [line[5:].strip() for line in body.splitlines() if line.startswith("data:")]
    envelope = json.loads(data_lines[-1] if data_lines else body)
    if "error" in envelope:
        raise RuntimeError(f"{name}: {envelope['error']}")
    result = envelope.get("result")
    if not isinstance(result, dict):
        raise RuntimeError(f"{name}: invalid MCP response")
    if result.get("isError"):
        raise RuntimeError(f"{name}: {result.get('content')}")
    structured = result.get("structuredContent")
    if isinstance(structured, dict):
        nested = structured.get("result")
        return nested if isinstance(nested, dict) else structured
    for item in result.get("content", []):
        if isinstance(item, dict) and item.get("type") == "text":
            parsed = json.loads(str(item.get("text", "{}")))
            if isinstance(parsed, dict):
                return parsed
        if isinstance(item, dict) and item.get("type") == "image":
            encoded = item.get("data")
            mime_type = item.get("mimeType")
            if isinstance(encoded, str) and isinstance(mime_type, str):
                return {
                    "mime_type": mime_type,
                    "bytes": len(base64.b64decode(encoded, validate=True)),
                }
    return {}


def main() -> None:
    session_id: str | None = None
    browser_started = False
    request_id = 1

    def call(name: str, arguments: dict[str, object] | None = None) -> dict[str, object]:
        nonlocal request_id
        result = call_tool(name, arguments or {}, request_id)
        request_id += 1
        return result

    try:
        if TEST_DIRECTORY.exists():
            shutil.rmtree(TEST_DIRECTORY)
        TEST_DIRECTORY.mkdir(parents=True)
        shutil.copy2(Path(__file__).with_name("verify-preview-server.mjs"), TEST_DIRECTORY / "server.mjs")
        runner = pwd.getpwnam("runner")
        workspace_group = grp.getgrnam("workspace")
        os.chown(TEST_DIRECTORY, runner.pw_uid, workspace_group.gr_gid)
        os.chown(TEST_DIRECTORY / "server.mjs", runner.pw_uid, workspace_group.gr_gid)
        command_checks = (
            ("jq", ["--version"]),
            ("fd", ["--version"]),
            ("hyperfine", ["--shell=none", "--runs", "1", "jq --version"]),
            ("actionlint", ["-version"]),
            ("actions-up", ["--version"]),
        )
        for executable, arguments in command_checks:
            checked = call(
                "command_run",
                {"action": "exec", "executable": executable, "arguments": arguments, "cwd": TEST_CWD},
            )
            if checked.get("exit_code") != 0:
                raise RuntimeError(f"{executable} execution failed: {checked}")

        started = call("command_start", {
            "action": "exec", "executable": "node",
            "arguments": ["server.mjs", str(TARGET_PORT)], "cwd": TEST_CWD,
        })
        session_id_value = started.get("session_id")
        if not isinstance(session_id_value, str):
            raise RuntimeError(f"command_start did not return a session id: {started}")
        session_id = session_id_value

        deadline = time.monotonic() + 30
        while True:
            port = call("system_inspect", {"action": "port", "port": TARGET_PORT})
            if port.get("in_use") is True:
                break
            if time.monotonic() >= deadline:
                output = call("process_inspect", {"action": "read", "session_id": session_id})
                raise RuntimeError(f"verification server did not listen on {TARGET_PORT}: {output}")
            time.sleep(0.5)

        with urllib.request.urlopen(TARGET_URL, timeout=10) as response:
            page = response.read().decode("utf-8", errors="replace")
        if response.status != 200 or not page.strip():
            raise RuntimeError("verification route did not return a non-empty HTTP 200 response")

        call("browser_session", {"action": "start"})
        browser_started = True
        navigated = call("browser_session", {"action": "navigate", "url": TARGET_URL})
        if not str(navigated.get("url", "")).startswith(TARGET_URL):
            raise RuntimeError(f"browser did not reach the checkbox documentation: {navigated}")
        deadline = time.monotonic() + 15
        while True:
            state = call("browser_observe", {"action": "state"})
            interactive = state.get("interactive_elements")
            if (
                str(state.get("url", "")).startswith(TARGET_URL)
                and isinstance(interactive, list)
                and any(
                    isinstance(element, dict)
                    and "loki browser verifier" in str(element.get("text", "")).lower()
                    for element in interactive
                )
            ):
                break
            if time.monotonic() >= deadline:
                raise RuntimeError(f"browser did not render the interactive verification page: {state}")
            time.sleep(0.5)

        owned_tab_id = state.get("active_tab_id")
        tabs = state.get("tabs")
        if not isinstance(owned_tab_id, str) or not isinstance(tabs, list):
            raise RuntimeError(f"browser state did not expose owned tab metadata: {state}")
        if sum(bool(tab.get("active")) for tab in tabs if isinstance(tab, dict)) != 1:
            raise RuntimeError(f"browser state did not identify exactly one owned tab: {state}")
        opened = call("browser_session", {"action": "navigate", "url": "https://example.com/", "new_tab": True})
        opened_tab_id = opened.get("active_tab_id")
        if not isinstance(opened_tab_id, str) or opened_tab_id == owned_tab_id:
            raise RuntimeError(f"new browser tab was not adopted by the controller: {opened}")
        listed = call("browser_observe", {"action": "tabs"})
        listed_tabs = listed.get("tabs")
        if listed.get("active_tab_id") != opened_tab_id or not isinstance(listed_tabs, list):
            raise RuntimeError(f"browser tab ownership was not retained: {listed}")
        switched = call("browser_interact", {"action": "switch_tab", "tab_id": owned_tab_id})
        if switched.get("tab_id") != owned_tab_id or not str(switched.get("url", "")).startswith(TARGET_URL):
            raise RuntimeError(f"browser did not restore the owned development tab: {switched}")
        closed = call("browser_interact", {"action": "close_tab", "tab_id": opened_tab_id})
        if closed.get("active_tab_id") != owned_tab_id:
            raise RuntimeError(f"closing a background tab changed browser ownership: {closed}")
        state = call("browser_observe", {"action": "state"})
        if state.get("active_tab_id") != owned_tab_id or not str(state.get("url", "")).startswith(TARGET_URL):
            raise RuntimeError(f"browser operation drifted from the owned tab: {state}")

        network = call("browser_observe", {"action": "network", "limit": 500})
        requests = network.get("requests")
        if not isinstance(requests, list) or not requests:
            raise RuntimeError(f"browser network collector did not retain requests: {network}")
        document = next(
            (
                item
                for item in requests
                if isinstance(item, dict)
                and str(item.get("url", "")).startswith(TARGET_URL)
                and item.get("status") == 200
            ),
            None,
        )
        if document is None or not isinstance(document.get("request_id"), str):
            raise RuntimeError(f"browser network collector missed the document request: {network}")
        request = call(
            "browser_observe",
            {"action": "request", "request_id": document["request_id"], "include_body": True, "max_body_chars": 65536},
        )
        body = str(request.get("body", ""))
        if "<!DOCTYPE html>" not in body or "Loki browser verifier" not in body:
            raise RuntimeError(f"browser request body was unavailable or invalid: {request}")
        console = call("browser_observe", {"action": "console", "limit": 100})
        if not isinstance(console.get("events"), list):
            raise RuntimeError(f"browser console collector returned an invalid result: {console}")
        page_errors = call("browser_observe", {"action": "errors", "limit": 100})
        if not isinstance(page_errors.get("errors"), list):
            raise RuntimeError(f"browser page error collector returned an invalid result: {page_errors}")
        deadline = time.monotonic() + 10
        while True:
            websockets = call("browser_observe", {"action": "websockets", "limit": 100})
            websocket_events = websockets.get("events")
            if isinstance(websocket_events, list) and any(
                isinstance(item, dict) and item.get("kind") == "created" for item in websocket_events
            ):
                break
            if time.monotonic() >= deadline:
                raise RuntimeError(f"browser WebSocket collector missed Vite HMR: {websockets}")
            time.sleep(0.25)
        diagnostics = call("browser_observe", {"action": "diagnostics", "limit": 20})
        if diagnostics.get("page", {}).get("url") != state.get("url"):
            raise RuntimeError(f"browser diagnostics returned the wrong active page: {diagnostics}")
        if int(diagnostics.get("summary", {}).get("network_requests", 0)) <= 0:
            raise RuntimeError(f"browser diagnostics missed network activity: {diagnostics}")

        saved = call(
            "browser_save_screenshot",
            {"path": SCREENSHOT_PATH, "full_page": True, "overwrite": True},
        )
        if saved.get("path") != SCREENSHOT_PATH or saved.get("mime_type") != "image/png":
            raise RuntimeError(f"browser screenshot was not saved: {saved}")
        image = call("read_image", {"path": SCREENSHOT_PATH})
        if image.get("mime_type") != "image/png" or int(image.get("bytes", 0)) <= 0:
            raise RuntimeError(f"saved browser screenshot was not readable: {image}")
        if os.environ.get("LOKI_VERIFY_PUBLIC_ARTIFACT") == "1":
            shared = call("share_image", {"path": SCREENSHOT_PATH, "ttl_seconds": 60})
            shared_url = str(shared.get("url", ""))
            shared_path = urlsplit(shared_url).path
            artifact_request = urllib.request.Request(
                f"http://127.0.0.1:8765{shared_path}",
                headers={"Host": "mcp.streamliner.im"},
            )
            with urllib.request.urlopen(artifact_request, timeout=10) as response:
                artifact = response.read()
            if (
                response.status != 200
                or response.headers.get_content_type() != "image/png"
                or hashlib.sha256(artifact).hexdigest() != shared.get("sha256")
            ):
                raise RuntimeError(f"shared browser screenshot was invalid: {shared}")

        blocked = False
        try:
            call("browser_session", {"action": "navigate", "url": f"http://localhost:{TARGET_PORT}/"})
        except RuntimeError:
            blocked = True
        if not blocked:
            raise RuntimeError("localhost hostname bypass was not blocked")
        print(f"Generic local browser bridge E2E passed at {TARGET_URL}")
    finally:
        if browser_started:
            call("browser_session", {"action": "stop"})
        if session_id is not None:
            call("runtime_stop", {"action": "process", "session_id": session_id})
        SCREENSHOT_HOST_PATH.unlink(missing_ok=True)
        if TEST_DIRECTORY.exists():
            shutil.rmtree(TEST_DIRECTORY)


if __name__ == "__main__":
    main()
