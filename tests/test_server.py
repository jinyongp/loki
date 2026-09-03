import asyncio
import json
import time
from pathlib import Path

import jwt
import pytest
from cryptography.hazmat.primitives.asymmetric import rsa

import loki_mcp.server as server_module
from loki_mcp.artifacts import ArtifactStore
from loki_mcp.developer_widget import (
    DEVELOPER_VIEWER_MIME_TYPE,
    DEVELOPER_VIEWER_URI,
    developer_viewer_html,
)
from loki_mcp.errors import tool_error_boundary
from loki_mcp.image_widget import IMAGE_VIEWER_MIME_TYPE, IMAGE_VIEWER_URI, image_viewer_html
from loki_mcp.preview_widget import LIVE_PREVIEW_MIME_TYPE, LIVE_PREVIEW_URI, live_preview_html
from loki_mcp.server import CloudflareAccessVerifier, LokiMCPServer, RequestAuthApp
from mcp.server.mcpserver.exceptions import ToolError


class AcceptedApp:
    async def __call__(self, scope: dict, receive: object, send: object) -> None:
        await send({"type": "http.response.start", "status": 204, "headers": []})
        await send({"type": "http.response.body", "body": b""})


class AcceptVerifier:
    def verify(self, token: str) -> bool:
        return token == "signed-token"


def call_app(headers: list[tuple[bytes, bytes]], *, cloudflare_verifier: object | None = None) -> list[dict]:
    messages: list[dict] = []

    async def receive() -> dict:
        return {"type": "http.request", "body": b"", "more_body": False}

    async def send(message: dict) -> None:
        messages.append(message)

    app = RequestAuthApp(AcceptedApp(), "test-token", cloudflare_verifier=cloudflare_verifier)
    asyncio.run(app({"type": "http", "headers": headers}, receive, send))
    return messages


def test_accepts_static_bearer_token() -> None:
    messages = call_app([(b"authorization", b"Bearer test-token")])
    assert messages[0]["status"] == 204


def test_accepts_cloudflare_access_assertion_when_trusted() -> None:
    messages = call_app(
        [(b"cf-access-jwt-assertion", b"signed-token")],
        cloudflare_verifier=AcceptVerifier(),
    )
    assert messages[0]["status"] == 204


def test_rejects_cloudflare_assertion_when_not_trusted() -> None:
    messages = call_app([(b"cf-access-jwt-assertion", b"signed-token")])
    assert messages[0]["status"] == 401


def test_rejects_missing_credentials() -> None:
    messages = call_app([])
    assert messages[0]["status"] == 401


def test_artifact_route_uses_opaque_link_instead_of_mcp_auth() -> None:
    store = ArtifactStore(
        "https://mcp.example.com/artifacts",
        {"mcp.example.com"},
    )
    published = store.publish(
        data=b"image",
        filename="result.png",
        mime_type="image/png",
        sha256="a" * 64,
        ttl_seconds=60,
    )
    path = "/artifacts/" + str(published["url"]).rsplit("/", 1)[-1]
    messages: list[dict] = []

    async def receive() -> dict:
        return {"type": "http.request", "body": b"", "more_body": False}

    async def send(message: dict) -> None:
        messages.append(message)

    app = RequestAuthApp(AcceptedApp(), "test-token", artifact_store=store)
    asyncio.run(app({
        "type": "http",
        "method": "GET",
        "path": path,
        "headers": [(b"host", b"mcp.example.com")],
    }, receive, send))
    assert messages[0]["status"] == 200
    assert messages[1]["body"] == b"image"


def test_preview_route_requires_its_cloudflare_access_audience() -> None:
    class AcceptedPreview:
        def resolve(self, scope: dict) -> object | None:
            return object()

        async def __call__(self, scope: dict, receive: object, send: object) -> None:
            await send({"type": "http.response.start", "status": 204, "headers": []})
            await send({"type": "http.response.body", "body": b""})

    async def call(headers: list[tuple[bytes, bytes]]) -> list[dict]:
        messages: list[dict] = []

        async def receive() -> dict:
            return {"type": "http.request", "body": b"", "more_body": False}

        async def send(message: dict) -> None:
            messages.append(message)

        app = RequestAuthApp(
            AcceptedApp(),
            "test-token",
            preview_proxy=AcceptedPreview(),
            preview_verifier=AcceptVerifier(),
        )
        await app({"type": "http", "headers": headers}, receive, send)
        return messages

    rejected = asyncio.run(call([]))
    accepted = asyncio.run(call([(b"cf-access-jwt-assertion", b"signed-token")]))
    assert rejected[0]["status"] == 401
    assert accepted[0]["status"] == 204


def test_create_app_registers_all_wrapped_tools(tmp_path: Path, monkeypatch: object) -> None:
    config_path = tmp_path / "config.toml"
    audit_path = tmp_path / "audit.jsonl"
    config_path.write_text(
        f'root = "{tmp_path.as_posix()}"\n'
        f'audit_log = "{audit_path.as_posix()}"\n'
        'host = "127.0.0.1"\nport = 8765\n',
        encoding="utf-8",
    )
    token_path = tmp_path / "token"
    token_path.write_text("x" * 48, encoding="utf-8")
    monkeypatch.setattr(server_module, "CONFIG_PATH", config_path)
    monkeypatch.setattr(server_module, "TOKEN_PATH", token_path)
    app, host, port = server_module.create_app()
    assert app is not None
    assert (host, port) == ("127.0.0.1", 8765)


def test_server_instructions_require_codex_style_progress_updates() -> None:
    instructions = server_module.SERVER_INSTRUCTIONS
    assert "user-visible preamble before the first tool call" in instructions
    assert "after each meaningful batch" in instructions
    assert "more than about 60 seconds" in instructions
    assert "use command_start" in instructions
    assert "useful independent work" in instructions
    assert "Do not mutate files that the running validation reads" in instructions
    assert "Do not invent speculative work" in instructions
    assert "what that evidence means" in instructions
    assert "raw commands and verbose output in the tool-call cards" in instructions
    assert "Do not expose hidden chain-of-thought" in instructions
    assert ".tmp/loki-quarantine/" in instructions
    assert "Use bootstrap_project" in instructions
    assert "another worktree of the same repository" in instructions
    assert "returned port and local_url" in instructions
    assert "action=set only for non-sensitive" in instructions
    assert "visible in the MCP request" in instructions
    assert "action=generate or opaque action=import_env" in instructions
    assert "bind_local_callback=true" in instructions
    assert "http://127.0.0.1:41800" in instructions


def test_tool_invocation_meta_describes_collapsed_tool_cards() -> None:
    assert server_module.tool_invocation_meta("read_file") == {
        "openai/toolInvocation/invoking": "Inspecting workspace state…",
        "openai/toolInvocation/invoked": "Workspace inspection complete.",
    }
    assert server_module.tool_invocation_meta("apply_patch")["openai/toolInvocation/invoking"] == "Updating workspace files…"
    assert server_module.tool_invocation_meta("exec_command")["openai/toolInvocation/invoking"] == "Running a workspace command…"
    assert server_module.tool_invocation_meta("browser_state")["openai/toolInvocation/invoking"] == "Working in the browser…"


def test_image_viewer_uses_mcp_apps_result_notification() -> None:
    html = image_viewer_html()
    assert IMAGE_VIEWER_URI == "ui://loki/image-viewer-v3.html"
    assert IMAGE_VIEWER_MIME_TYPE == "text/html;profile=mcp-app"
    assert "ui/notifications/tool-result" in html
    assert "openai:set_globals" in html
    assert "structuredContent" in html
    assert "window.openai?.toolOutput" in html
    assert "image.src = url.href" in html
    assert "notifyIntrinsicHeight" in html


def test_live_preview_uses_nested_frame_and_mcp_apps_result_notification() -> None:
    html = live_preview_html()
    assert LIVE_PREVIEW_URI == "ui://loki/live-preview-v1.html"
    assert LIVE_PREVIEW_MIME_TYPE == "text/html;profile=mcp-app"
    assert "ui/notifications/tool-result" in html
    assert "openai:set_globals" in html
    assert "structuredContent" in html
    assert "window.openai?.toolOutput" in html
    assert "frame.src = currentUrl" in html
    assert "requestDisplayMode" in html
    assert "allow-scripts allow-same-origin" in html
    assert "Open in new tab" in html
    assert "routeCount > 1" in html
    assert "${routeCount} routes" in html


def test_developer_viewer_renders_safe_output_and_controls() -> None:
    html = developer_viewer_html()
    assert DEVELOPER_VIEWER_URI == "ui://loki/developer-output-v1.html"
    assert DEVELOPER_VIEWER_MIME_TYPE == "text/html;profile=mcp-app"
    assert "ui/notifications/tool-result" in html
    assert "openai:set_globals" in html
    assert "structuredContent" in html
    assert "textContent = text" in html
    assert "requestDisplayMode" in html
    assert "navigator.clipboard.writeText" in html


def test_server_sanitizes_argument_validation_values() -> None:
    mcp = LokiMCPServer("test")

    def requires_number(value: int) -> int:
        return value

    mcp.tool()(tool_error_boundary(requires_number))
    with pytest.raises(ToolError) as captured:
        asyncio.run(mcp.call_tool("requires_number", {"value": "private-value"}))
    message = str(captured.value)
    assert "invalid arguments: value" in message
    assert "private-value" not in message


def test_server_gives_unexpected_failures_a_safe_reference() -> None:
    mcp = LokiMCPServer("test")

    def crashes() -> None:
        raise AttributeError("private internal detail")

    mcp.tool()(tool_error_boundary(crashes))
    with pytest.raises(ToolError) as captured:
        asyncio.run(mcp.call_tool("crashes", {}))
    message = str(captured.value)
    assert "unexpected server failure (reference " in message
    assert "private internal detail" not in message


def write_jwks(path: Path, public_key: object, key_id: str = "test-key") -> None:
    key_data = jwt.algorithms.RSAAlgorithm.to_jwk(public_key, as_dict=True)
    key_data["kid"] = key_id
    path.write_text(json.dumps({"keys": [key_data]}), encoding="utf-8")


def test_cloudflare_verifier_checks_signature_issuer_audience_and_expiry(tmp_path: Path) -> None:
    team_domain = "example.cloudflareaccess.com"
    audience = "a" * 64
    private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    jwks_path = tmp_path / "cloudflare-jwks.json"
    write_jwks(jwks_path, private_key.public_key())
    verifier = CloudflareAccessVerifier(team_domain, audience, jwks_path)
    now = int(time.time())
    claims = {
        "iss": f"https://{team_domain}",
        "aud": [audience],
        "exp": now + 60,
    }
    token = jwt.encode(claims, private_key, algorithm="RS256", headers={"kid": "test-key"})
    assert verifier.verify(token) is True

    claims["aud"] = ["b" * 64]
    wrong_audience = jwt.encode(claims, private_key, algorithm="RS256", headers={"kid": "test-key"})
    assert verifier.verify(wrong_audience) is False

    other_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    bad_signature = jwt.encode(
        claims | {"aud": [audience]},
        other_key,
        algorithm="RS256",
        headers={"kid": "test-key"},
    )
    assert verifier.verify(bad_signature) is False

    expired = jwt.encode(
        claims | {"aud": [audience], "exp": now - 1},
        private_key,
        algorithm="RS256",
        headers={"kid": "test-key"},
    )
    assert verifier.verify(expired) is False


def test_cloudflare_verifier_reloads_rotated_local_jwks(tmp_path: Path) -> None:
    private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    jwks_path = tmp_path / "cloudflare-jwks.json"
    verifier = CloudflareAccessVerifier("example.cloudflareaccess.com", "a" * 64, jwks_path)
    now = int(time.time())
    token = jwt.encode({
        "iss": "https://example.cloudflareaccess.com",
        "aud": ["a" * 64],
        "exp": now + 60,
    }, private_key, algorithm="RS256", headers={"kid": "rotated-key"})

    write_jwks(jwks_path, private_key.public_key(), "old-key")
    assert verifier.verify(token) is False
    write_jwks(jwks_path, private_key.public_key(), "rotated-key")
    assert verifier.verify(token) is True
