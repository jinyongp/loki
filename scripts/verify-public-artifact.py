#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import json
from pathlib import Path
import sys
import urllib.error
import urllib.request


MCP_URL = "http://127.0.0.1:8765/mcp"
PUBLIC_MCP_URL = "https://mcp.streamliner.im/mcp"
TOKEN_PATH = Path("/etc/loki/token")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit("usage: verify-public-artifact.py WORKSPACE_IMAGE_PATH")

    payload = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": "share_image",
            "arguments": {"path": sys.argv[1], "ttl_seconds": 900},
        },
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
    with urllib.request.urlopen(request, timeout=30) as response:
        body = response.read().decode("utf-8")
    data_lines = [line[5:].strip() for line in body.splitlines() if line.startswith("data:")]
    envelope = json.loads(data_lines[-1] if data_lines else body)
    result = envelope["result"]
    if result.get("isError"):
        raise RuntimeError(result.get("content"))
    structured = result["structuredContent"]
    shared = structured.get("result", structured)

    try:
        with urllib.request.urlopen(shared["url"], timeout=30) as response:
            artifact = response.read()
            content_type = response.headers.get_content_type()
            status = response.status
    except urllib.error.HTTPError as exc:
        details = {
            "status": exc.code,
            "url": shared["url"],
            "server": exc.headers.get("Server"),
            "location": exc.headers.get("Location"),
            "cf_ray": exc.headers.get("Cf-Ray"),
            "body": exc.read(500).decode("utf-8", errors="replace"),
        }
        raise RuntimeError(json.dumps(details)) from exc

    digest = hashlib.sha256(artifact).hexdigest()
    if status != 200 or content_type != shared["mime_type"] or digest != shared["sha256"]:
        raise RuntimeError(
            f"public artifact mismatch: status={status}, content_type={content_type}, sha256={digest}"
        )

    protected_request = urllib.request.Request(
        PUBLIC_MCP_URL,
        headers={"User-Agent": "Loki artifact verification"},
    )
    protected_status = 200
    try:
        urllib.request.build_opener(NoRedirect).open(protected_request, timeout=30)
    except urllib.error.HTTPError as exc:
        protected_status = exc.code
    if protected_status == 200:
        raise RuntimeError("public MCP endpoint unexpectedly allowed an unauthenticated request")

    print(
        json.dumps(
            {
                "status": status,
                "content_type": content_type,
                "bytes": len(artifact),
                "sha256": digest,
                "url": shared["url"],
                "expires_at": shared["expires_at"],
                "unauthenticated_mcp_status": protected_status,
            }
        )
    )


if __name__ == "__main__":
    main()
