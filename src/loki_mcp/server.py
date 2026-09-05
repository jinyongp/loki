from __future__ import annotations

from contextlib import asynccontextmanager
from importlib.metadata import version as package_version
from pathlib import Path
import hmac
import json
import logging
import os
import secrets
from typing import Protocol
from urllib.parse import urlsplit

import jwt
from mcp.server import MCPServer
from mcp.server.mcpserver.exceptions import ToolError, UnexpectedToolError
from mcp.server.transport_security import TransportSecuritySettings
from mcp.types import ToolAnnotations
from pydantic import ValidationError
import uvicorn

from .artifacts import ArtifactStore
from .catalog import CatalogTools
from .config import load_config
from .developer_widget import (
    DEVELOPER_VIEWER_MIME_TYPE,
    DEVELOPER_VIEWER_URI,
    developer_viewer_html,
)
from .errors import tool_error_boundary
from .image_widget import IMAGE_VIEWER_MIME_TYPE, IMAGE_VIEWER_URI, image_viewer_html
from .previews import PreviewProxy, PreviewStore
from .preview_widget import LIVE_PREVIEW_MIME_TYPE, LIVE_PREVIEW_URI, live_preview_html
from .tools import WorkspaceTools


CONFIG_PATH = Path(os.environ.get("LOKI_MCP_CONFIG", "/etc/loki/config.toml"))
TOKEN_PATH = Path(os.environ.get("LOKI_MCP_TOKEN_FILE", "/etc/loki/token"))
CLOUDFLARE_JWKS_PATH = Path(
    os.environ.get("LOKI_MCP_CLOUDFLARE_JWKS_FILE", "/etc/loki/cloudflare-jwks.json")
)
LOGGER = logging.getLogger(__name__)
READ_ONLY = ToolAnnotations(readOnlyHint=True, destructiveHint=False, idempotentHint=True, openWorldHint=False)
OPEN_READ = ToolAnnotations(readOnlyHint=True, destructiveHint=False, idempotentHint=False, openWorldHint=True)
LOCAL_WRITE = ToolAnnotations(readOnlyHint=False, destructiveHint=False, idempotentHint=False, openWorldHint=False)
PUBLIC_WRITE = ToolAnnotations(readOnlyHint=False, destructiveHint=False, idempotentHint=False, openWorldHint=True)
DESTRUCTIVE_WRITE = ToolAnnotations(readOnlyHint=False, destructiveHint=True, idempotentHint=False, openWorldHint=False)
OPEN_WRITE = ToolAnnotations(readOnlyHint=False, destructiveHint=True, idempotentHint=False, openWorldHint=True)

SERVER_INSTRUCTIONS = (
    "Operate only inside the isolated Loki workspace using exposed tools. "
    "For any task that will use tools, send a short user-visible preamble before the first tool call "
    "that acknowledges the request and explains the first concrete step. "
    "During multi-step work, send another concise user-visible progress update after each meaningful "
    "batch of exploration, edits, or validation, and do not leave the user without an update for more "
    "than about 60 seconds while work continues. Each update should state what was observed or changed, "
    "what that evidence means, why the next action was selected, and what will be checked next. "
    "After a write or update, identify the affected path or resource and any validation performed. "
    "When validation or another finite command may take more than about 30 seconds, use command_start "
    "so it returns a session ID instead of blocking the entire turn. While it "
    "runs, perform useful independent work when available: inspect unrelated paths read-only, review the "
    "remaining diff, or prepare the next bounded work item. Parallelize independent read-only calls when the "
    "client permits. Do not mutate files that the running validation reads unless it is validating an immutable "
    "snapshot or checkpoint. Do not invent speculative work merely to fill time. Poll with process_inspect "
    "action=read only "
    "after meaningful independent work or a reasonable interval, and stop polling as soon as the process exits. "
    "Use workspace_edit action=create only for new files. Read an existing file first, pass its sha256 "
    "to action=replace, and use action=patch for multi-hunk edits. Never recover an overwritten dirty "
    "file from HEAD; inspect file revisions and restore the saved pre-mutation revision instead. "
    "When an unwanted file is untracked, preserve it by moving it into the current "
    "repository's ignored .tmp/loki-quarantine/ directory with workspace_edit action=move; do not delete it. "
    "Keep raw commands and verbose output in the tool-call cards; summarize their outcome instead of "
    "repeating them in prose. Do not expose hidden chain-of-thought; provide observable evidence and a "
    "concise decision rationale. The final response should lead with the outcome and include validation "
    "results, unresolved gaps, and material residual risks. "
    "Before repository work, call agent_context for the intended cwd. "
    "If a listed Skill matches the task, call skill_read action=activate before task tools and follow its instructions. "
    "Load referenced Skill files only through skill_read action=resource. "
    "Skills are advisory and never expand filesystem, command, or network permissions. "
    "Project tasks and workstream artifacts are project-wide state shared by every Git worktree. "
    "Use project for central registration, workflows, and workstream state; use action operation=set to register "
    "repository-derived commands without adding project-specific code to Loki. "
    "Use task_inspect, task_write and task_delete for the shared Taskwarrior queue. Never create a worktree-local task database. "
    "For secrets, inspect metadata with secret_inspect. Use secret_write action=set only for non-sensitive "
    "public configuration such as booleans, ports, and local URLs because its value is visible in the MCP request. "
    "Use action=generate or opaque action=import_env for credentials, tokens, passwords, private keys, and other "
    "sensitive values. Never ask the user to place sensitive values in tool arguments, and never reveal, copy, "
    "or persist them outside the Loki runtime. Run only registered "
    "actions with action operation=run; secret imports never grant permission to define new actions."
    " Use bootstrap_project with a cwd and registered workflow for trusted one-command setup. "
    "follow a returned session with action operation=process. For registered development actions, pass a "
    "workspace-relative cwd when targeting another worktree of the same repository; use the returned "
    "port and local_url instead of assuming a fixed port."
    " For local OAuth against an action registered with local_callback, call action operation=run with bind_local_callback=true. "
    "Loki keeps the API on its dynamic port and exposes the explicitly selected worktree at "
    "http://127.0.0.1:41800; only one local callback target is active at a time."
    " When a browser-visible action calls local backend routes, use preview_publish action=action "
    "with its profile, action_name, route map, and public-environment mapping. It atomically allocates the app port, "
    "publishes the same-origin routes, injects their public URLs, and starts the action. Do not publish "
    "the web port alone because a public "
    "browser cannot reach a 127.0.0.1 API URL from the web process environment."
)


def tool_invocation_meta(name: str) -> dict[str, str]:
    """Return compact, host-visible status labels for ordinary MCP tool cards."""
    if name.startswith("browser_"):
        invoking, invoked = "Working in the browser…", "Browser step complete."
    elif name.startswith("git_"):
        invoking, invoked = "Inspecting Git state…", "Git step complete."
    elif name.startswith(("write_", "replace_", "apply_", "move_", "create_", "edit_")):
        invoking, invoked = "Updating workspace files…", "Workspace update complete."
    elif name.startswith(("run_", "start_", "exec_", "npm", "fnm", "just")):
        invoking, invoked = "Running a workspace command…", "Workspace command complete."
    elif name.startswith(("stop_", "remove_", "revoke_")):
        invoking, invoked = "Cleaning up a workspace resource…", "Workspace cleanup complete."
    elif name.startswith(("share_", "workspace_bundle")):
        invoking, invoked = "Publishing a temporary artifact…", "Temporary artifact ready."
    else:
        invoking, invoked = "Inspecting workspace state…", "Workspace inspection complete."
    return {
        "openai/toolInvocation/invoking": invoking,
        "openai/toolInvocation/invoked": invoked,
    }


class AccessTokenVerifier(Protocol):
    def verify(self, token: str) -> bool: ...


class LokiMCPServer(MCPServer):
    async def call_tool(self, name: str, arguments: dict, context: object | None = None):
        try:
            return await super().call_tool(name, arguments, context)
        except ToolError as error:
            if isinstance(error.__cause__, ValidationError):
                fields = sorted({
                    ".".join(str(part) for part in item["loc"])
                    for item in error.__cause__.errors()
                })
                field_text = ", ".join(fields) or "request"
                raise ToolError(
                    f"invalid arguments: {field_text}; inspect the tool schema and retry"
                ) from error.__cause__
            if isinstance(error, UnexpectedToolError):
                incident_id = secrets.token_hex(6)
                LOGGER.exception("Unexpected tool failure [%s] in %s", incident_id, name)
                raise ToolError(
                    f"unexpected server failure (reference {incident_id}); run diagnostics and retry"
                ) from error
            raise


class CloudflareAccessVerifier:
    def __init__(self, team_domain: str, audience: str, jwks_path: Path) -> None:
        self.issuer = f"https://{team_domain}"
        self.audience = audience
        self.jwks_path = jwks_path

    def _signing_key(self, token: str) -> object:
        header = jwt.get_unverified_header(token)
        key_id = header.get("kid")
        if not isinstance(key_id, str) or not key_id:
            raise jwt.InvalidTokenError("JWT is missing a key identifier")

        document = json.loads(self.jwks_path.read_text(encoding="utf-8"))
        keys = document.get("keys")
        if not isinstance(keys, list):
            raise jwt.InvalidTokenError("JWKS document is invalid")
        for key_data in keys:
            if isinstance(key_data, dict) and key_data.get("kid") == key_id:
                return jwt.PyJWK.from_dict(key_data, algorithm="RS256").key
        raise jwt.InvalidTokenError("JWT signing key was not found")

    def verify(self, token: str) -> bool:
        try:
            signing_key = self._signing_key(token)
            jwt.decode(
                token,
                signing_key,
                algorithms=["RS256"],
                audience=self.audience,
                issuer=self.issuer,
                options={"require": ["exp"]},
            )
        except (OSError, ValueError, jwt.PyJWTError) as error:
            LOGGER.warning("Cloudflare Access JWT rejected: %s", type(error).__name__)
            return False
        return True


class RequestAuthApp:
    def __init__(
        self,
        app: object,
        token: str,
        *,
        cloudflare_verifier: AccessTokenVerifier | None = None,
        artifact_store: ArtifactStore | None = None,
        preview_proxy: PreviewProxy | None = None,
        preview_verifier: AccessTokenVerifier | None = None,
    ) -> None:
        self.app = app
        self.token = token.encode("utf-8")
        self.cloudflare_verifier = cloudflare_verifier
        self.artifact_store = artifact_store
        self.preview_proxy = preview_proxy
        self.preview_verifier = preview_verifier

    async def __call__(self, scope: dict, receive: object, send: object) -> None:
        if self.preview_proxy is not None and self.preview_proxy.resolve(scope) is not None:
            if self.preview_verifier is not None and not self._cloudflare_authorized(
                scope, self.preview_verifier
            ):
                await self._unauthorized(scope, send)
                return
            await self.preview_proxy(scope, receive, send)
            return
        if scope.get("type") == "http":
            path = str(scope.get("path", ""))
            if path.startswith("/artifacts/"):
                if self.artifact_store is not None:
                    await self.artifact_store(scope, receive, send)
                else:
                    body = b"not found"
                    await send({
                        "type": "http.response.start",
                        "status": 404,
                        "headers": [
                            (b"content-type", b"text/plain; charset=utf-8"),
                            (b"content-length", str(len(body)).encode("ascii")),
                            (b"cache-control", b"no-store"),
                        ],
                    })
                    await send({"type": "http.response.body", "body": body})
                return
            headers = {key.lower(): value for key, value in scope.get("headers", [])}
            authorization = headers.get(b"authorization", b"")
            expected = b"Bearer " + self.token
            cloudflare_authorized = self._cloudflare_authorized(scope, self.cloudflare_verifier)
            if not hmac.compare_digest(authorization, expected) and not cloudflare_authorized:
                await self._unauthorized(scope, send)
                return
        await self.app(scope, receive, send)

    @staticmethod
    def _cloudflare_authorized(scope: dict, verifier: AccessTokenVerifier | None) -> bool:
        if verifier is None:
            return False
        headers = {key.lower(): value for key, value in scope.get("headers", [])}
        assertion = headers.get(b"cf-access-jwt-assertion", b"")
        if not assertion:
            return False
        try:
            assertion_text = assertion.decode("ascii")
        except UnicodeDecodeError:
            return False
        return verifier.verify(assertion_text)

    @staticmethod
    async def _unauthorized(scope: dict, send: object) -> None:
        if scope.get("type") == "websocket":
            await send({"type": "websocket.close", "code": 4401, "reason": "unauthorized"})
            return
        body = json.dumps({"error": "unauthorized"}, separators=(",", ":")).encode("utf-8")
        await send({
            "type": "http.response.start",
            "status": 401,
            "headers": [
                (b"content-type", b"application/json"),
                (b"content-length", str(len(body)).encode("ascii")),
                (b"www-authenticate", b"Bearer"),
                (b"cache-control", b"no-store"),
            ],
        })
        await send({"type": "http.response.body", "body": body})


def create_app() -> tuple[RequestAuthApp, str, int]:
    config = load_config(CONFIG_PATH)
    token = TOKEN_PATH.read_text(encoding="utf-8").strip()
    if len(token) < 43:
        raise RuntimeError("MCP bearer token must contain at least 256 bits of entropy")

    artifact_store = None
    if config.artifact_base_url is not None:
        artifact_hosts = {
            "127.0.0.1",
            f"127.0.0.1:{config.port}",
            "localhost",
            f"localhost:{config.port}",
            *config.public_hosts,
        }
        artifact_store = ArtifactStore(config.artifact_base_url, artifact_hosts)
    preview_store = None
    if config.preview_base_domain is not None:
        preview_store = PreviewStore(config.preview_base_domain)
    tools = WorkspaceTools(config, artifact_store=artifact_store, preview_store=preview_store)
    catalog = CatalogTools(tools)
    preview_proxy = None
    if preview_store is not None:
        preview_proxy = PreviewProxy(preview_store, tools.preview_port_allowed)
    @asynccontextmanager
    async def lifespan(_: MCPServer):
        try:
            yield
        finally:
            tools.close()

    mcp = LokiMCPServer(
        "loki",
        version=package_version("loki-mcp"),
        instructions=SERVER_INSTRUCTIONS,
        log_level="INFO",
        lifespan=lifespan,
    )
    registered_tool_names: list[str] = []

    def register(
        function: object,
        annotations: ToolAnnotations,
        *,
        meta: dict[str, object] | None = None,
    ) -> None:
        name = getattr(function, "__name__", None)
        if not isinstance(name, str) or not name:
            raise TypeError("registered tool must have a stable function name")
        registered_tool_names.append(name)
        effective_meta: dict[str, object] = tool_invocation_meta(name)
        if meta is not None:
            effective_meta.update(meta)
        mcp.tool(annotations=annotations, meta=effective_meta)(tool_error_boundary(function))

    artifact_resource_domains: list[str] = []
    widget_domain: str | None = None
    if config.artifact_base_url is not None:
        parsed_artifact_url = urlsplit(config.artifact_base_url)
        widget_domain = f"{parsed_artifact_url.scheme}://{parsed_artifact_url.netloc}"
        artifact_resource_domains.append(widget_domain)

    mcp.resource(
        IMAGE_VIEWER_URI,
        name="Loki image viewer",
        description="Render a temporary Loki workspace image inside an MCP Apps host.",
        mime_type=IMAGE_VIEWER_MIME_TYPE,
        meta={
            "ui": {
                "prefersBorder": True,
                "domain": widget_domain,
                "csp": {"resourceDomains": artifact_resource_domains},
            },
            "openai/widgetPrefersBorder": True,
            "openai/widgetDomain": widget_domain,
            "openai/widgetCSP": {"resource_domains": artifact_resource_domains},
        },
    )(image_viewer_html)

    preview_frame_domains = (
        [f"https://*.{config.preview_base_domain}"]
        if config.preview_base_domain is not None
        else []
    )
    mcp.resource(
        LIVE_PREVIEW_URI,
        name="Loki live development preview",
        description="Render a temporary Loki development server inside an MCP Apps host.",
        mime_type=LIVE_PREVIEW_MIME_TYPE,
        meta={
            "ui": {
                "prefersBorder": True,
                "domain": widget_domain,
                "csp": {"frameDomains": preview_frame_domains},
            },
            "openai/widgetPrefersBorder": True,
            "openai/widgetDomain": widget_domain,
            "openai/widgetCSP": {"frame_domains": preview_frame_domains},
        },
    )(live_preview_html)

    mcp.resource(
        DEVELOPER_VIEWER_URI,
        name="Loki developer output viewer",
        description="Render Git diffs, test reports, and managed process logs.",
        mime_type=DEVELOPER_VIEWER_MIME_TYPE,
        meta={
            "ui": {"prefersBorder": True, "domain": widget_domain, "csp": {}},
            "openai/widgetPrefersBorder": True,
            "openai/widgetDomain": widget_domain,
            "openai/widgetCSP": {"resource_domains": [], "connect_domains": []},
        },
    )(developer_viewer_html)

    image_viewer_meta = {
        "ui": {"resourceUri": IMAGE_VIEWER_URI},
        "openai/outputTemplate": IMAGE_VIEWER_URI,
        "openai/toolInvocation/invoking": "Preparing image…",
        "openai/toolInvocation/invoked": "Image ready.",
    }
    live_preview_meta = {
        "ui": {"resourceUri": LIVE_PREVIEW_URI},
        "openai/outputTemplate": LIVE_PREVIEW_URI,
        "openai/toolInvocation/invoking": "Publishing live preview…",
        "openai/toolInvocation/invoked": "Live preview ready.",
    }
    developer_viewer_meta = {
        "ui": {"resourceUri": DEVELOPER_VIEWER_URI},
        "openai/outputTemplate": DEVELOPER_VIEWER_URI,
        "openai/toolInvocation/invoking": "Preparing developer output…",
        "openai/toolInvocation/invoked": "Developer output ready.",
    }

    register(catalog.system_inspect, READ_ONLY)
    register(catalog.runtime_stop, DESTRUCTIVE_WRITE)
    register(catalog.preview_publish, PUBLIC_WRITE, meta=live_preview_meta)
    register(catalog.shared_resources, READ_ONLY)
    register(catalog.revoke_share, DESTRUCTIVE_WRITE)
    register(catalog.browser_session, OPEN_WRITE)
    register(catalog.browser_observe, OPEN_READ)
    register(catalog.browser_interact, OPEN_WRITE)
    register(tools.browser_screenshot, OPEN_READ)
    register(tools.browser_save_screenshot, LOCAL_WRITE)
    register(tools.browser_share_screenshot, PUBLIC_WRITE, meta=image_viewer_meta)
    register(catalog.workspace_read, READ_ONLY)
    register(tools.read_image, READ_ONLY)
    register(
        tools.share_image,
        PUBLIC_WRITE,
        meta=image_viewer_meta,
    )
    register(catalog.artifact_publish, PUBLIC_WRITE)
    register(tools.write_image, LOCAL_WRITE)
    register(catalog.workspace_edit, LOCAL_WRITE)
    register(catalog.project, LOCAL_WRITE)
    register(catalog.task_inspect, READ_ONLY)
    register(catalog.task_write, LOCAL_WRITE)
    register(catalog.task_delete, DESTRUCTIVE_WRITE)
    register(catalog.restore_workspace_file, DESTRUCTIVE_WRITE)
    register(tools.remove_tracked_file, DESTRUCTIVE_WRITE)
    register(tools.agent_context, READ_ONLY)
    register(catalog.skill_read, READ_ONLY)
    register(catalog.skill_write, LOCAL_WRITE)
    register(catalog.git_inspect, READ_ONLY)
    register(catalog.git_stage, LOCAL_WRITE)
    register(catalog.developer_view, READ_ONLY, meta=developer_viewer_meta)
    register(catalog.secret_inspect, READ_ONLY)
    register(catalog.secret_write, LOCAL_WRITE)
    register(catalog.secret_delete, DESTRUCTIVE_WRITE)
    register(tools.bootstrap_project, OPEN_WRITE)
    register(catalog.action, OPEN_WRITE)
    register(catalog.command_run, OPEN_WRITE)
    register(catalog.command_start, OPEN_WRITE)
    register(catalog.process_inspect, READ_ONLY)
    tools.set_tool_catalog(registered_tool_names)

    allowed_hosts = [f"127.0.0.1:{config.port}", f"localhost:{config.port}"]
    for public_host in config.public_hosts:
        allowed_hosts.extend([public_host, f"{public_host}:*"])

    transport_security = TransportSecuritySettings(
        enable_dns_rebinding_protection=True,
        allowed_hosts=allowed_hosts,
        allowed_origins=[],
    )
    app = mcp.streamable_http_app(
        streamable_http_path="/mcp",
        stateless_http=True,
        json_response=True,
        max_request_body_size=16_777_216,
        transport_security=transport_security,
        host=config.host,
    )
    cloudflare_verifier = None
    if config.cloudflare_access_team_domain is not None:
        assert config.cloudflare_access_audience is not None
        cloudflare_verifier = CloudflareAccessVerifier(
            config.cloudflare_access_team_domain,
            config.cloudflare_access_audience,
            CLOUDFLARE_JWKS_PATH,
        )
    preview_verifier = None
    if config.preview_access_audience is not None:
        assert config.cloudflare_access_team_domain is not None
        preview_verifier = CloudflareAccessVerifier(
            config.cloudflare_access_team_domain,
            config.preview_access_audience,
            CLOUDFLARE_JWKS_PATH,
        )
    return RequestAuthApp(
        app,
        token,
        cloudflare_verifier=cloudflare_verifier,
        artifact_store=artifact_store,
        preview_proxy=preview_proxy,
        preview_verifier=preview_verifier,
    ), config.host, config.port


def main() -> None:
    app, host, port = create_app()
    uvicorn.run(app, host=host, port=port, access_log=False, server_header=False)


if __name__ == "__main__":
    main()
