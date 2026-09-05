from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from threading import Event, RLock, Timer
from typing import Annotated, Any, TypedDict
import base64
import binascii
import difflib
import hashlib
import importlib.metadata
import io
import json
import mimetypes
import os
import re
import shlex
import socket
import subprocess
import sys
import time
import xml.etree.ElementTree as ElementTree
import zipfile
from urllib.parse import urlsplit

from mcp.server.mcpserver.utilities.types import Image
from mcp.types import CallToolResult, ResourceLink, TextContent

from .artifacts import ArtifactStore
from .config import CommandSpec, ServerConfig
from .errors import OperationError
from .policy import PolicyError, WorkspacePolicy, atomic_write, atomic_write_bytes
from .previews import PreviewStore
from .processes import ProcessManager
from .skills import SkillRegistry


RG_EXCLUDES = (
    "!.git/**",
    "!.ssh/**",
    "!.gnupg/**",
    "!.env",
    "!.env.local",
    "!.env.production",
    "!*.pem",
    "!*.key",
    "!*.p12",
    "!*.pfx",
)

MAX_PREVIEW_TTL_SECONDS = 24 * 60 * 60

PATCH_REJECTED_MARKERS = (
    b"GIT binary patch",
    b"Binary files ",
    b"rename from ",
    b"rename to ",
    b"copy from ",
    b"copy to ",
    b"deleted file mode ",
    b"new file mode 120000",
    b"old file mode 120000",
    b"new mode 120000",
    b"old mode 120000",
    b"+++ /dev/null",
)

NPM_SUBCOMMANDS = frozenset({
    "--version",
    "audit",
    "ci",
    "exec",
    "explain",
    "help",
    "install",
    "list",
    "ls",
    "outdated",
    "pack",
    "query",
    "run",
    "search",
    "test",
    "view",
    "why",
})

FNM_SUBCOMMANDS = frozenset({
    "--version",
    "alias",
    "current",
    "default",
    "env",
    "exec",
    "install",
    "list",
    "list-remote",
    "unalias",
    "uninstall",
    "use",
})

EXECUTABLES = {
    "actionlint": "/home/linuxbrew/.linuxbrew/bin/actionlint",
    "actions-up": "/home/linuxbrew/.linuxbrew/bin/actions-up",
    "awk": "/usr/bin/awk",
    "cargo": "/workspace/.loki/cargo/bin/cargo",
    "cp": "/usr/bin/cp",
    "find": "/usr/bin/find",
    "fd": "/home/linuxbrew/.linuxbrew/bin/fd",
    "fnm": "/home/linuxbrew/.linuxbrew/bin/fnm",
    "gh": "/home/linuxbrew/.linuxbrew/bin/gh",
    "git": "/usr/bin/git",
    "go": "/home/linuxbrew/.linuxbrew/bin/go",
    "grep": "/usr/bin/grep",
    "head": "/usr/bin/head",
    "hyperfine": "/home/linuxbrew/.linuxbrew/bin/hyperfine",
    "just": "/home/linuxbrew/.linuxbrew/bin/just",
    "jq": "/usr/bin/jq",
    "ls": "/usr/bin/ls",
    "mkdir": "/usr/bin/mkdir",
    "mv": "/usr/bin/mv",
    "node": "node",
    "npm": "npm",
    "pnpm": "pnpm",
    "printenv": "/usr/bin/printenv",
    "pwd": "/usr/bin/pwd",
    "python": "/usr/bin/python3",
    "python3": "/usr/bin/python3",
    "rg": "/usr/bin/rg",
    "rustc": "/workspace/.loki/cargo/bin/rustc",
    "rustdoc": "/workspace/.loki/cargo/bin/rustdoc",
    "rustfmt": "/workspace/.loki/cargo/bin/rustfmt",
    "rustup": "/home/linuxbrew/.linuxbrew/bin/rustup",
    "sed": "/usr/bin/sed",
    "sort": "/usr/bin/sort",
    "tail": "/usr/bin/tail",
    "task": "/usr/local/libexec/loki-task",
    "tar": "/usr/bin/tar",
    "touch": "/usr/bin/touch",
    "uniq": "/usr/bin/uniq",
    "wc": "/usr/bin/wc",
}
IMAGE_FORMATS = {
    ".gif": ("gif", (b"GIF87a", b"GIF89a")),
    ".jpeg": ("jpeg", (b"\xff\xd8\xff",)),
    ".jpg": ("jpeg", (b"\xff\xd8\xff",)),
    ".png": ("png", (b"\x89PNG\r\n\x1a\n",)),
    ".webp": ("webp", (b"RIFF",)),
}
IMAGE_MIME_FORMATS = {
    "image/gif": ".gif",
    "image/jpeg": ".jpg",
    "image/png": ".png",
    "image/webp": ".webp",
}
MAX_IMAGE_BYTES = 10 * 1024 * 1024
MAX_SHARED_FILE_BYTES = 32 * 1024 * 1024
MAX_BUNDLE_FILES = 512
MAX_DEVELOPER_VIEW_BYTES = 1024 * 1024
MAX_FILE_REVISION_BYTES = 64 * 1024 * 1024
MAX_FILE_REVISION_STORE_BYTES = 512 * 1024 * 1024
MAX_FILE_REVISIONS = 1_000
PORT_GUARD_SOCKET = "/run/loki/port-guard/control.sock"
BROWSER_SOCKET = "/run/loki/browser/control.sock"
BROWSER_CATALOG_REVISION = "2026-09-03.1"
TOOL_CATALOG_REVISION = "2026-09-04.4"

GIT_READABLE_CONFIG_KEYS = frozenset({
    "commit.template",
    "commit.gpgsign",
    "gpg.format",
    "user.name",
    "user.email",
})
TRUSTED_GIT_TEMPLATE_ROOTS = (
    Path("/home/runner/.dotfiles/git/templates"),
    Path("/home/runner/.config/git/templates"),
)
BROWSER_TOOL_NAMES = (
    "browser_session",
    "browser_observe",
    "browser_interact",
    "browser_screenshot",
    "browser_save_screenshot",
    "browser_share_screenshot",
)


class ReadImageResult(TypedDict):
    path: str
    mime_type: str
    bytes: int
    sha256: str


class ShareImageResult(ReadImageResult):
    url: str
    expires_at: str
    display_markdown: str


class BrowserShareImageResult(ShareImageResult):
    full_page: bool


class ShareFileResult(TypedDict):
    path: str
    filename: str
    mime_type: str
    bytes: int
    sha256: str
    share_id: str
    url: str
    expires_at: str


class BundleResult(ShareFileResult):
    file_count: int
    input_bytes: int
    excluded_entries: int


class DeveloperViewResult(TypedDict):
    kind: str
    title: str
    subtitle: str
    content: str
    truncated: bool
    stats: dict[str, Any]


class WorkspaceTools:
    def __init__(
        self,
        config: ServerConfig,
        artifact_store: ArtifactStore | None = None,
        preview_store: PreviewStore | None = None,
    ) -> None:
        self.config = config
        self.artifact_store = artifact_store
        self.preview_store = preview_store
        self.policy = WorkspacePolicy(config.root)
        self.skills = SkillRegistry(self.policy)
        self.processes = ProcessManager(config, self.policy)
        self._mutation_lock = RLock()
        self._started_at = time.monotonic()
        self._tool_catalog: tuple[str, ...] = ()

    def set_tool_catalog(self, names: list[str]) -> None:
        """Record the exact public catalog after server registration completes."""
        if len(names) != len(set(names)):
            raise ValueError("tool catalog contains duplicate names")
        self._tool_catalog = tuple(names)

    def _tool_catalog_info(self) -> dict[str, Any]:
        encoded = "\n".join(self._tool_catalog).encode("utf-8")
        return {
            "revision": TOOL_CATALOG_REVISION,
            "count": len(self._tool_catalog),
            "tools": list(self._tool_catalog),
            "sha256": hashlib.sha256(encoded).hexdigest(),
            "client_sync": (
                "ChatGPT custom-app actions are a frozen snapshot; refresh the app actions "
                "and start a new chat when this revision changes"
            ),
        }

    def close(self) -> None:
        self.processes.close()
        if self.artifact_store is not None:
            self.artifact_store.clear()
        if self.preview_store is not None:
            self.preview_store.clear()

    def task_request(self, cwd: str, **request: Any) -> dict[str, Any]:
        working = self._command_cwd(cwd)
        relative = working.relative_to(self.policy.root).as_posix() or "."
        return self._runtime_request("task", cwd=relative, **request)

    def project_state(
        self,
        action: str,
        cwd: str = ".",
        workstream: str | None = None,
        filename: str | None = None,
        content: str | None = None,
        expected_sha256: str | None = None,
        goal: str | None = None,
        slug_base: str | None = None,
        slug: str | None = None,
        depth: str = "standard",
        intent_source_kind: str = "plan-local",
        intent_source: str | None = None,
    ) -> dict[str, Any]:
        """Read or update project-wide workstream state shared by all Git worktrees."""
        working_directory = self._command_cwd(cwd)
        relative_cwd = working_directory.relative_to(self.policy.root).as_posix() or "."
        if action == "status":
            return self._runtime_request("project_state", action=action, cwd=relative_cwd)
        if action == "list":
            return self._runtime_request("project_state", action=action, cwd=relative_cwd)
        if action == "read":
            if filename is None:
                raise PolicyError("filename is required for project_state action=read")
            return self._runtime_request(
                "project_state", action=action, cwd=relative_cwd,
                workstream=workstream, filename=filename,
            )
        if action == "init":
            if goal is None:
                raise PolicyError("goal is required for project_state action=init")
            return self._runtime_request(
                "project_state", action=action, cwd=relative_cwd,
                goal=goal,
                slug_base=slug_base,
                slug=slug,
                depth=depth,
                intent_source_kind=intent_source_kind,
                intent_source=intent_source,
            )
        if action == "bind":
            if workstream is None:
                raise PolicyError("workstream is required for project_state action=bind")
            return self._runtime_request(
                "project_state", action=action, cwd=relative_cwd,
                workstream=workstream,
            )
        if action == "write":
            if filename is None or content is None:
                raise PolicyError("filename and content are required for project_state action=write")
            return self._runtime_request(
                "project_state", action=action, cwd=relative_cwd,
                workstream=workstream, filename=filename, content=content,
                expected_sha256=expected_sha256,
            )
        raise PolicyError("project_state action must be status, list, read, init, bind, or write")

    def project_configuration(
        self,
        action: str,
        cwd: str = ".",
        name: str | None = None,
        workflow: str | None = None,
        steps: list[str] | None = None,
        required_secrets: list[str] | None = None,
        timeout_seconds: int = 3_600,
    ) -> dict[str, Any]:
        """Manage central project registration and workflows without project-local config files."""
        working_directory = self._command_cwd(cwd)
        relative_cwd = working_directory.relative_to(self.policy.root).as_posix() or "."
        if action == "register":
            result = self._runtime_request("project_register", cwd=relative_cwd, name=name)
        elif action == "unregister":
            result = self._runtime_request("project_unregister", cwd=relative_cwd)
        elif action == "registration":
            result = self._runtime_request("project_status", cwd=relative_cwd)
        elif action == "workflow":
            if workflow is None:
                raise PolicyError("workflow is required for project action=workflow")
            result = self._runtime_request(
                "project_workflow", cwd=relative_cwd, workflow=workflow,
            )
        elif action == "set_workflow":
            if workflow is None:
                raise PolicyError("workflow is required for project action=set_workflow")
            clean_steps = [self._split_reference(item, "step") for item in (steps or [])]
            if not clean_steps:
                raise PolicyError("steps are required for project action=set_workflow")
            grouped: dict[str, list[str]] = {}
            for reference in required_secrets or []:
                profile, secret = self._split_reference(reference, "required secret")
                grouped.setdefault(profile, []).append(secret)
            result = self._runtime_request(
                "project_set_workflow", cwd=relative_cwd, workflow=workflow,
                steps=clean_steps, required_secrets=grouped,
                timeout_seconds=timeout_seconds,
            )
        elif action == "remove_workflow":
            if workflow is None:
                raise PolicyError("workflow is required for project action=remove_workflow")
            result = self._runtime_request(
                "project_remove_workflow", cwd=relative_cwd, workflow=workflow,
            )
        else:
            raise PolicyError(
                "project configuration action must be register, unregister, registration, "
                "workflow, set_workflow, or remove_workflow"
            )
        self._audit("project_configuration", True, {
            "action": action, "cwd": relative_cwd, "workflow": workflow,
        })
        return result

    @staticmethod
    def _split_reference(value: str, label: str) -> list[str]:
        if not isinstance(value, str) or value.count("/") != 1:
            raise PolicyError(f"{label} must use PROFILE/NAME")
        profile, item = value.split("/", 1)
        if not profile or not item:
            raise PolicyError(f"{label} must use PROFILE/NAME")
        return [profile, item]

    def server_info(self) -> dict[str, Any]:
        """Return Loki version, schema revision, uptime, capabilities, and limits."""
        result = {
            "name": "loki",
            "version": importlib.metadata.version("loki-mcp"),
            "schema_revision": "2026-09-04.1",
            "mcp_sdk_version": importlib.metadata.version("mcp"),
            "python_version": sys.version.split()[0],
            "uptime_seconds": round(time.monotonic() - self._started_at, 3),
            "workspace": "/workspace",
            "tool_catalog": self._tool_catalog_info(),
            "capabilities": {
                "text_files": True,
                "images": ["gif", "jpeg", "png", "webp"],
                "temporary_image_links": self.artifact_store is not None,
                "temporary_file_links": self.artifact_store is not None,
                "workspace_bundles": self.artifact_store is not None,
                "developer_output_viewer": True,
                "shared_project_state": True,
                "temporary_live_previews": self.preview_store is not None,
                "command_execution": True,
                "managed_processes": True,
                "workspace_port_control": True,
                "go_toolchain": True,
                "rust_toolchain": True,
                "git_checkpoints": True,
                "file_revisions": True,
                "git_partial_staging": True,
                "signed_git_commits": True,
                "secret_profiles": Path("/run/loki/runtime/control.sock").is_socket(),
                "secret_management": {
                    "opaque_staged_imports": True,
                    "agent_profile_lifecycle": True,
                    "direct_value_access": False,
                    "action_registration": "root-only",
                },
                "agent_skills": {
                    "revision": "2026-09-03.1",
                    "builtin_root": "builtin",
                    "shared_root": ".agents/skills",
                    "project_override": True,
                    "precedence": ["project", "shared", "builtin"],
                    "dynamic_catalog": True,
                },
                "github_https": True,
                "structured_browser": Path(BROWSER_SOCKET).exists(),
                "browser_devtools": Path(BROWSER_SOCKET).exists(),
                "browser_tool_catalog": {
                    "revision": BROWSER_CATALOG_REVISION,
                    "count": len(BROWSER_TOOL_NAMES),
                    "tools": list(BROWSER_TOOL_NAMES),
                },
            },
            "limits": {
                "max_file_bytes": self.config.max_file_bytes,
                "max_write_bytes": self.config.max_write_bytes,
                "max_image_bytes": MAX_IMAGE_BYTES,
                "max_shared_file_bytes": MAX_SHARED_FILE_BYTES,
                "max_bundle_files": MAX_BUNDLE_FILES,
                "max_processes": self.config.max_processes,
            },
        }
        self._audit("server_info", True, {})
        return result

    def diagnostics(self) -> dict[str, Any]:
        """Return non-sensitive workspace, toolchain, credential-mount, and repository health checks."""
        toolchain = {
            name: os.access(path, os.X_OK)
            for name, path in {
                "fnm": "/home/linuxbrew/.linuxbrew/bin/fnm",
                "fd": "/home/linuxbrew/.linuxbrew/bin/fd",
                "gh": "/home/linuxbrew/.linuxbrew/bin/gh",
                "git": "/usr/bin/git",
                "go": "/home/linuxbrew/.linuxbrew/bin/go",
                "just": "/home/linuxbrew/.linuxbrew/bin/just",
                "jq": "/usr/bin/jq",
                "hyperfine": "/home/linuxbrew/.linuxbrew/bin/hyperfine",
                "actionlint": "/home/linuxbrew/.linuxbrew/bin/actionlint",
                "actions-up": "/home/linuxbrew/.linuxbrew/bin/actions-up",
                "python3": "/usr/bin/python3",
                "rg": "/usr/bin/rg",
                "rustup": "/home/linuxbrew/.linuxbrew/bin/rustup",
                "cargo": "/workspace/.loki/cargo/bin/cargo",
                "rustc": "/workspace/.loki/cargo/bin/rustc",
            }.items()
        }
        repositories: list[str] = []
        for path in self.policy.root.iterdir():
            try:
                if path.is_dir() and not path.is_symlink() and (path / ".git").exists():
                    repositories.append(path.name)
            except OSError:
                # Runtime caches and credential mounts can be intentionally
                # unreadable to the sandboxed MCP process.
                continue
        repositories.sort()
        git_identity_name = self._git_config_value("user.name")
        git_identity_email = self._git_config_value("user.email")
        git_signing_format = self._git_config_value("gpg.format")
        git_signing_required = self._git_config_value("commit.gpgsign").lower() == "true"
        git_signing = {
            "identity_configured": bool(git_identity_name and git_identity_email),
            "format": git_signing_format or None,
            "commit_signing_required": git_signing_required,
            "public_key_available": Path("/home/runner/.ssh/id_ed25519.pub").is_file(),
            "agent_socket_available": Path("/run/loki/signing/agent.sock").is_socket(),
        }
        signing_ready = (
            git_signing["identity_configured"]
            and git_signing["format"] == "ssh"
            and git_signing["commit_signing_required"]
            and git_signing["public_key_available"]
            and git_signing["agent_socket_available"]
        )
        result = {
            "healthy": (
                all(toolchain.values())
                and os.access(self.policy.root, os.R_OK | os.W_OK)
                and signing_ready
            ),
            "workspace": {
                "readable": os.access(self.policy.root, os.R_OK),
                "writable": os.access(self.policy.root, os.W_OK),
            },
            "audit_log": {
                "directory_writable": os.access(self.config.audit_log.parent, os.W_OK),
            },
            "github": {
                "config_mounted": Path("/home/runner/.config/gh/hosts.yml").is_file(),
                "protocol": "https",
            },
            "git_signing": git_signing,
            "toolchain": toolchain,
            "repositories": repositories,
            "configured_executables": sorted(self.config.executables or {}),
            "tool_catalog": self._tool_catalog_info(),
            "browser": {
                "socket_available": Path(BROWSER_SOCKET).exists(),
                "catalog_revision": BROWSER_CATALOG_REVISION,
                "expected_tool_count": len(BROWSER_TOOL_NAMES),
                "expected_tools": list(BROWSER_TOOL_NAMES),
            },
        }
        self._audit("diagnostics", result["healthy"], {"repository_count": len(repositories)})
        return result

    def _git_config_value(self, key: str) -> str:
        try:
            result = self._git(["config", "--global", "--includes", "--get", key])
        except (OSError, subprocess.SubprocessError):
            return ""
        if result["exit_code"] != 0:
            return ""
        return str(result["output"]).strip()

    def port_info(self, port: int) -> dict[str, Any]:
        """Inspect a listening development port owned by runner inside the workspace."""
        try:
            result = self._inspect_preview_port(port)
            self._audit("port_info", True, {"port": port, "in_use": result.get("in_use", False)})
            return result
        except Exception as error:
            self._audit("port_info", False, {"port": port}, error)
            raise

    def stop_port(self, port: int) -> dict[str, Any]:
        """Stop the runner-owned workspace process listening on a development port."""
        try:
            result = self._port_guard("stop", port)
            self._audit("stop_port", True, {"port": port, "stopped": result.get("stopped", False)})
            return result
        except Exception as error:
            self._audit("stop_port", False, {"port": port}, error)
            raise

    def share_dev_server(self, port: int, ttl_seconds: int = 900) -> dict[str, Any]:
        """Publish a temporary capability URL for a runner-owned workspace development server."""
        try:
            if self.preview_store is None:
                raise OperationError("temporary live preview sharing is not configured")
            if not 60 <= ttl_seconds <= MAX_PREVIEW_TTL_SECONDS:
                raise PolicyError("preview lifetime must be between 60 and 86400 seconds")
            inspected = self._inspect_preview_port(port)
            listeners = inspected.get("listeners")
            if inspected.get("in_use") is not True or not isinstance(listeners, list) or not listeners:
                raise PolicyError("port is not a runner-owned workspace development server")
            listener = listeners[0]
            if not isinstance(listener, dict):
                raise PolicyError("port listener metadata is invalid")
            self._reject_public_loopbacks(listener)
            try:
                result = self.preview_store.publish(
                    port=port,
                    cwd=str(listener.get("cwd", "/workspace")),
                    command=str(listener.get("command", "unknown")),
                    ttl_seconds=ttl_seconds,
                )
            except RuntimeError as error:
                raise OperationError(str(error)) from error
            self._audit("share_dev_server", True, {
                "share_id": result["share_id"],
                "port": port,
                "ttl_seconds": ttl_seconds,
            })
            return result
        except Exception as error:
            self._audit("share_dev_server", False, {"port": port, "ttl_seconds": ttl_seconds}, error)
            raise

    def share_dev_stack(
        self, routes: dict[str, int], ttl_seconds: int = 900,
    ) -> dict[str, Any]:
        """Publish an arbitrary same-origin route-to-port preview stack."""
        ports = list(routes.values())
        try:
            if self.preview_store is None:
                raise OperationError("temporary live preview sharing is not configured")
            if not 60 <= ttl_seconds <= MAX_PREVIEW_TTL_SECONDS:
                raise PolicyError("preview lifetime must be between 60 and 86400 seconds")
            if "/" not in routes:
                raise PolicyError("preview stack routes must include /")
            listeners: dict[int, dict[str, Any]] = {}
            for port in ports:
                inspected = self._inspect_preview_port(port)
                candidates = inspected.get("listeners")
                if (
                    inspected.get("in_use") is not True
                    or not isinstance(candidates, list)
                    or not candidates
                    or not isinstance(candidates[0], dict)
                ):
                    raise PolicyError(
                        f"port {port} is not a runner-owned workspace development server"
                    )
                listeners[port] = candidates[0]
            root = listeners[routes["/"]]
            self._reject_public_loopbacks(root)
            result = self.preview_store.publish_stack(
                routes=routes,
                cwd=str(root.get("cwd", "/workspace")),
                command=str(root.get("command", "unknown")),
                ttl_seconds=ttl_seconds,
            )
            self._audit("share_dev_stack", True, {
                "share_id": result["share_id"], "ports": ports, "ttl_seconds": ttl_seconds,
            })
            return result
        except Exception as error:
            self._audit("share_dev_stack", False, {
                "ports": ports, "ttl_seconds": ttl_seconds,
            }, error)
            raise

    def run_preview_action(
        self, profile: str, action: str, backend_routes: dict[str, int],
        environment_routes: dict[str, str], ttl_seconds: int = 900,
        cwd: str | None = None,
    ) -> dict[str, Any]:
        """Atomically allocate a dynamic web port, publish its stack, inject public URLs, and start the registered action."""
        preview: dict[str, Any] | None = None
        try:
            if self.preview_store is None:
                raise OperationError("temporary live preview sharing is not configured")
            if not 60 <= ttl_seconds <= MAX_PREVIEW_TTL_SECONDS:
                raise PolicyError("preview lifetime must be between 60 and 86400 seconds")
            if "/" in backend_routes:
                raise PolicyError("the registered action owns the preview root route")
            prepared = self._runtime_request(
                "prepare_action", profile=profile, action_name=action, cwd=cwd,
            )
            backend_routes = {**prepared.get("backend_routes", {}), **backend_routes}
            environment_routes = {**prepared.get("environment_routes", {}), **environment_routes}
            missing = set(prepared.get("required_environment", [])) - set(environment_routes)
            if missing:
                raise PolicyError("PREVIEW_MAPPING_REQUIRED: " + ", ".join(sorted(missing)))
            backend_ports = list(backend_routes.values())
            listeners: dict[int, dict[str, Any]] = {}
            for port in backend_ports:
                inspected = self._inspect_preview_port(port)
                candidates = inspected.get("listeners")
                if (
                    inspected.get("in_use") is not True
                    or not isinstance(candidates, list)
                    or not candidates
                    or not isinstance(candidates[0], dict)
                ):
                    raise PolicyError(
                        f"port {port} is not a runner-owned workspace development server"
                    )
                listeners[port] = candidates[0]
            root_port = int(prepared["port"])
            if root_port in backend_ports:
                raise OperationError("allocated root port conflicts with a preview backend")
            routes = {"/": root_port, **backend_routes}
            preview = self.preview_store.publish_stack(
                routes=routes,
                cwd=cwd or "/workspace",
                command=f"{profile}/{action}",
                ttl_seconds=ttl_seconds,
            )
            public_environment = self._preview_public_environment(
                preview, environment_routes,
            )
            for name, suffix in prepared.get("environment_suffixes", {}).items():
                if name in public_environment:
                    public_environment[name] += suffix
            process = self._runtime_request(
                "run_action", profile=profile, action_name=action,
                launch_token=prepared["launch_token"],
                public_environment=public_environment, cwd=cwd,
            )
            deadline = time.monotonic() + 20
            while not self.preview_port_allowed(root_port):
                if time.monotonic() >= deadline:
                    self._runtime_request("stop_process", session_id=process["session_id"])
                    raise OperationError("PREVIEW_NOT_READY: frontend did not start listening")
                time.sleep(0.2)
            result = {
                **process,
                "share_id": preview["share_id"],
                "url": preview["url"],
                "routes": preview["routes"],
                "expires_at": preview["expires_at"],
                "display_markdown": preview["display_markdown"],
                "public_environment": public_environment,
            }
            self._audit("run_preview_action", True, {
                "profile": profile, "action": action,
                "session_id": result["session_id"], "share_id": result["share_id"],
                "ports": [root_port, *backend_ports], "ttl_seconds": ttl_seconds,
            })
            return result
        except Exception as error:
            if preview is not None:
                self.preview_store.revoke(str(preview["share_id"]))
            self._audit("run_preview_action", False, {
                "profile": profile, "action": action,
                "backend_routes": backend_routes, "ttl_seconds": ttl_seconds,
            }, error)
            raise

    @staticmethod
    def _preview_public_environment(
        preview: dict[str, Any], environment_routes: dict[str, str],
    ) -> dict[str, str]:
        url = str(preview["url"])
        routes = preview.get("routes", {})
        if not isinstance(routes, dict):
            raise PolicyError("preview routes are invalid")
        result: dict[str, str] = {}
        for name, route in environment_routes.items():
            if not isinstance(name, str) or not isinstance(route, str) or route not in routes:
                raise PolicyError("preview environment route is invalid")
            result[name] = url if route == "/" else f"{url}{route}"
        return result

    @staticmethod
    def _reject_public_loopbacks(listener: dict[str, Any]) -> None:
        pid = listener.get("pid")
        if not isinstance(pid, int) or pid <= 0:
            return
        try:
            environment = Path(f"/proc/{pid}/environ").read_bytes()
        except FileNotFoundError:
            return
        except PermissionError as error:
            raise PolicyError("PREVIEW_ENV_UNREADABLE: use preview_publish action=action") from error
        for entry in environment.split(b"\0"):
            name, _, value = entry.partition(b"=")
            if name.startswith((b"PUBLIC_", b"VITE_", b"NEXT_PUBLIC_")):
                try:
                    host = urlsplit(value.decode("utf-8")).hostname
                except (ValueError, UnicodeDecodeError):
                    continue
                if host in {"127.0.0.1", "localhost", "::1", "0.0.0.0"}:
                    raise PolicyError("PREVIEW_LOCAL_URL: use preview_publish action=action with public route mappings")

    def list_shared_servers(self) -> dict[str, Any]:
        """List active temporary live preview URLs without accessing their content."""
        if self.preview_store is None:
            return {"previews": [], "configured": False}
        previews = self.preview_store.list()
        result = {"previews": previews, "configured": True}
        self._audit("list_shared_servers", True, {"count": len(previews)})
        return result

    def stop_shared_server(self, share_id: str) -> dict[str, Any]:
        """Revoke one temporary live preview URL without stopping its development server."""
        try:
            if self.preview_store is None:
                raise OperationError("temporary live preview sharing is not configured")
            revoked = self.preview_store.revoke(share_id)
            if revoked is None:
                raise PolicyError("preview share was not found or has expired")
            result = {"revoked": True, "share_id": share_id, "port": revoked["port"]}
            self._audit("stop_shared_server", True, result)
            return result
        except Exception as error:
            self._audit("stop_shared_server", False, {"share_id": share_id}, error)
            raise

    def preview_port_allowed(self, port: int) -> bool:
        try:
            inspected = self._inspect_preview_port(port)
        except Exception:
            return False
        listeners = inspected.get("listeners")
        return bool(inspected.get("in_use") is True and isinstance(listeners, list) and listeners)

    def _inspect_preview_port(self, port: int) -> dict[str, Any]:
        """Inspect a workspace process, then a trusted loopback Compose port."""
        guarded: dict[str, Any] | None = None
        guard_error: Exception | None = None
        try:
            guarded = self._port_guard("inspect", port)
            listeners = guarded.get("listeners")
            if guarded.get("in_use") is True and isinstance(listeners, list) and listeners:
                return guarded
        except Exception as error:
            guard_error = error
        try:
            docker = self._runtime_request("inspect_docker_port", port=int(port))
        except Exception:
            if guard_error is not None:
                raise guard_error
            return guarded or {"port": int(port), "in_use": False, "listeners": []}
        listeners = docker.get("listeners")
        if docker.get("in_use") is True and isinstance(listeners, list) and listeners:
            return docker
        if guard_error is not None:
            raise guard_error
        return guarded or docker

    def browser_start(self) -> dict[str, Any]:
        """Start the isolated headless browser session if it is not already running."""
        return self._browser_tool("browser_start", "start", {})

    def browser_navigate(self, url: str, new_tab: bool = False) -> dict[str, Any]:
        """Navigate to a public URL or runner-owned workspace server at 127.0.0.1."""
        return self._browser_tool("browser_navigate", "navigate", {"url": url, "new_tab": new_tab})

    def browser_state(self) -> dict[str, Any]:
        """Return page metadata and bounded indexed interactive elements for the active tab."""
        return self._browser_tool("browser_state", "state", {})

    def browser_console(
        self,
        level: str | None = None,
        since_sequence: int = 0,
        limit: int = 100,
    ) -> dict[str, Any]:
        """Read bounded console and browser log events; use sequence for incremental polling."""
        return self._browser_tool(
            "browser_console",
            "console",
            {"level": level, "since_sequence": since_sequence, "limit": limit},
        )

    def browser_network(
        self,
        status_min: int | None = None,
        failed_only: bool = False,
        resource_type: str | None = None,
        since_sequence: int = 0,
        limit: int = 100,
    ) -> dict[str, Any]:
        """List bounded HTTP requests with sensitive headers/query values redacted."""
        return self._browser_tool(
            "browser_network",
            "network",
            {
                "status_min": status_min,
                "failed_only": failed_only,
                "resource_type": resource_type,
                "since_sequence": since_sequence,
                "limit": limit,
            },
        )

    def browser_request(
        self,
        request_id: str,
        include_body: bool = False,
        max_body_chars: int = 65_536,
    ) -> dict[str, Any]:
        """Inspect one request; optionally include a bounded finished text response body."""
        return self._browser_tool(
            "browser_request",
            "request",
            {
                "request_id": request_id,
                "include_body": include_body,
                "max_body_chars": max_body_chars,
            },
            audit={"request_id": request_id, "include_body": include_body},
        )

    def browser_websockets(self, since_sequence: int = 0, limit: int = 100) -> dict[str, Any]:
        """Read WebSocket lifecycle and frame metadata without exposing frame payloads."""
        return self._browser_tool(
            "browser_websockets",
            "websockets",
            {"since_sequence": since_sequence, "limit": limit},
        )

    def browser_page_errors(self, since_sequence: int = 0, limit: int = 100) -> dict[str, Any]:
        """Read bounded unhandled page exceptions with source locations and compact stacks."""
        return self._browser_tool(
            "browser_page_errors",
            "page_errors",
            {"since_sequence": since_sequence, "limit": limit},
        )

    def browser_diagnostics(self, since_sequence: int = 0, limit: int = 50) -> dict[str, Any]:
        """Summarize the active page, console, errors, failed requests, and WebSocket activity."""
        return self._browser_tool(
            "browser_diagnostics",
            "debug_diagnostics",
            {"since_sequence": since_sequence, "limit": limit},
        )

    def browser_click(
        self,
        index: int | None = None,
        x: int | None = None,
        y: int | None = None,
        new_tab: bool = False,
    ) -> dict[str, Any]:
        """Click an indexed element from browser_state or a viewport coordinate pair."""
        return self._browser_tool(
            "browser_click",
            "click",
            {"index": index, "x": x, "y": y, "new_tab": new_tab},
        )

    def browser_type(self, index: int, text: str) -> dict[str, Any]:
        """Clear and type text into an indexed input element from browser_state."""
        return self._browser_tool("browser_type", "type", {"index": index, "text": text}, audit={"index": index})

    def browser_press(self, key: str) -> dict[str, Any]:
        """Press one approved navigation or editing key in the active browser tab."""
        return self._browser_tool("browser_press", "press", {"key": key})

    def browser_scroll(self, direction: str = "down", amount: int = 500) -> dict[str, Any]:
        """Scroll the active page up or down by a bounded pixel amount."""
        return self._browser_tool("browser_scroll", "scroll", {"direction": direction, "amount": amount})

    def browser_back(self) -> dict[str, Any]:
        """Navigate the active browser tab backward in history."""
        return self._browser_tool("browser_back", "back", {})

    def browser_list_tabs(self) -> dict[str, Any]:
        """List tabs in the isolated browser session."""
        return self._browser_tool("browser_list_tabs", "list_tabs", {})

    def browser_switch_tab(self, tab_id: str) -> dict[str, Any]:
        """Switch to a tab using the four-character id returned by browser_list_tabs."""
        return self._browser_tool("browser_switch_tab", "switch_tab", {"tab_id": tab_id})

    def browser_close_tab(self, tab_id: str) -> dict[str, Any]:
        """Close a tab using the four-character id returned by browser_list_tabs."""
        return self._browser_tool("browser_close_tab", "close_tab", {"tab_id": tab_id})

    def browser_screenshot(self, full_page: bool = False) -> Image:
        """Return a bounded PNG screenshot of the active browser tab as MCP image content."""
        try:
            content = self._browser_screenshot_bytes(full_page)
            self._audit("browser_screenshot", True, {"bytes": len(content), "full_page": full_page})
            return Image(data=content, format="png")
        except Exception as error:
            self._audit("browser_screenshot", False, {"full_page": full_page}, error)
            raise

    def browser_save_screenshot(
        self,
        path: str,
        full_page: bool = False,
        overwrite: bool = False,
        expected_sha256: str | None = None,
    ) -> dict[str, Any]:
        """Capture and atomically save a bounded PNG screenshot inside the workspace."""
        try:
            target = self.policy.resolve(path, must_exist=False)
            if target.suffix.lower() != ".png":
                raise PolicyError("browser screenshot path must use a .png extension")
            content = self._browser_screenshot_bytes(full_page)
            with self._mutation_lock:
                target = self.policy.resolve(path, must_exist=False)
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                target = self.policy.resolve(path, must_exist=False)
                previous_revision = None
                if target.exists():
                    current_sha256 = hashlib.sha256(target.read_bytes()).hexdigest()
                    if not overwrite:
                        raise FileExistsError(path)
                    if expected_sha256 != current_sha256:
                        raise PolicyError("existing screenshot changed or was not read before overwrite")
                    previous_revision = self._capture_file_revision(target, "browser_save_screenshot")
                atomic_write_bytes(target, content, overwrite=overwrite)
            result = {
                "path": self.policy.relative(target),
                "mime_type": "image/png",
                "bytes": len(content),
                "sha256": hashlib.sha256(content).hexdigest(),
                "full_page": full_page,
                "previous_revision": previous_revision,
            }
            self._audit("browser_save_screenshot", True, result)
            return result
        except Exception as error:
            self._audit(
                "browser_save_screenshot",
                False,
                {"path": path, "full_page": full_page, "overwrite": overwrite},
                error,
            )
            raise

    def browser_share_screenshot(
        self,
        full_page: bool = False,
        ttl_seconds: int = 900,
    ) -> Annotated[CallToolResult, BrowserShareImageResult]:
        """Capture the active browser tab and immediately render that exact screenshot."""
        try:
            content = self._browser_screenshot_bytes(full_page)
            metadata: ReadImageResult = {
                "path": "browser://active-tab",
                "mime_type": "image/png",
                "bytes": len(content),
                "sha256": hashlib.sha256(content).hexdigest(),
            }
            result = self._publish_image(
                content=content,
                filename="browser-screenshot.png",
                metadata=metadata,
                ttl_seconds=ttl_seconds,
            )
            assert result.structured_content is not None
            result.structured_content["full_page"] = full_page
            self._audit("browser_share_screenshot", True, {
                "bytes": len(content),
                "full_page": full_page,
                "ttl_seconds": ttl_seconds,
            })
            return result
        except Exception as error:
            self._audit(
                "browser_share_screenshot",
                False,
                {"full_page": full_page, "ttl_seconds": ttl_seconds},
                error,
            )
            raise

    def _browser_screenshot_bytes(self, full_page: bool) -> bytes:
        result = self._browser_rpc("screenshot", {"full_page": full_page})
        encoded = result.get("data_base64")
        if not isinstance(encoded, str):
            raise PolicyError("browser screenshot response is invalid")
        try:
            content = base64.b64decode(encoded, validate=True)
        except (binascii.Error, ValueError) as error:
            raise PolicyError("browser screenshot response is invalid") from error
        if len(content) > MAX_IMAGE_BYTES or not content.startswith(b"\x89PNG\r\n\x1a\n"):
            raise PolicyError("browser screenshot response is invalid")
        return content

    def browser_stop(self) -> dict[str, Any]:
        """Stop the isolated browser session and its child processes."""
        return self._browser_tool("browser_stop", "stop", {})

    def workspace_info(self) -> dict[str, Any]:
        """Return repository state, configured checks/processes, and workspace limits."""
        branch = None
        repository = (self.policy.root / ".git").is_dir()
        if repository:
            result = self._git(["rev-parse", "--abbrev-ref", "HEAD"], timeout=10)
            if result["exit_code"] == 0:
                branch = result["output"].strip()
        value = {
            "root": "/workspace",
            "repository": repository,
            "branch": branch,
            "checks": sorted(self.config.checks),
            "processes": sorted(self.config.processes),
            "executables": sorted({**EXECUTABLES, **(self.config.executables or {})}),
            "running_processes": sum(
                item["status"] == "running" for item in self.processes.list()["processes"]
            ),
            "limits": {
                "max_file_bytes": self.config.max_file_bytes,
                "max_write_bytes": self.config.max_write_bytes,
                "max_patch_bytes": self.config.max_patch_bytes,
                "max_patch_files": self.config.max_patch_files,
                "max_processes": self.config.max_processes,
            },
        }
        self._audit("workspace_info", True, {})
        return value

    def list_files(
        self,
        path: str = ".",
        max_depth: int = 3,
        offset: int = 0,
        limit: int = 200,
    ) -> dict[str, Any]:
        """List workspace files and directories with bounded depth and pagination."""
        try:
            root = self.policy.resolve(path)
            if not root.is_dir():
                raise PolicyError("path must be a directory")
            depth_limit = max(1, min(int(max_depth), 10))
            start = max(0, int(offset))
            page_size = max(1, min(int(limit), self.config.max_list_entries))
            entries: list[dict[str, Any]] = []
            seen = 0
            has_more = False
            base_depth = len(root.parts)

            for current, directories, filenames in os.walk(root, topdown=True, followlinks=False):
                current_path = Path(current)
                current_depth = len(current_path.parts) - base_depth
                visible_directories = sorted(
                    name for name in directories if self.policy.allowed_entry(current_path / name)
                )
                directories[:] = visible_directories if current_depth + 1 < depth_limit else []
                names = [(name, "directory") for name in visible_directories]
                names.extend((name, "file") for name in sorted(filenames))

                for name, kind in names:
                    candidate = current_path / name
                    if not self.policy.allowed_entry(candidate):
                        continue
                    if seen < start:
                        seen += 1
                        continue
                    if len(entries) >= page_size:
                        has_more = True
                        break
                    item: dict[str, Any] = {"path": self.policy.relative(candidate), "type": kind}
                    if kind == "file":
                        item["size"] = candidate.stat().st_size
                    entries.append(item)
                    seen += 1
                if has_more:
                    break

            result = {
                "entries": entries,
                "offset": start,
                "next_offset": start + len(entries),
                "has_more": has_more,
            }
            self._audit("list_files", True, {"path": path, "count": len(entries)})
            return result
        except Exception as error:
            self._audit("list_files", False, {"path": path}, error)
            raise

    def read_file(self, path: str, offset: int = 0, limit: int = 400) -> dict[str, Any]:
        """Read a UTF-8 text file by zero-based line offset and bounded line count."""
        try:
            target = self._regular_file(path)
            size = target.stat().st_size
            if size > self.config.max_file_bytes:
                raise PolicyError("file exceeds read limit")
            start = max(0, int(offset))
            line_limit = max(1, min(int(limit), self.config.max_read_lines))

            lines: list[str] = []
            output_size = 0
            eof = True
            content = target.read_bytes()
            if b"\x00" in content:
                raise PolicyError("binary files are not supported")
            try:
                all_lines = content.decode("utf-8").splitlines(keepends=True)
            except UnicodeDecodeError as error:
                raise PolicyError("file is not valid UTF-8 text") from error
            for line in all_lines[start:]:
                if len(lines) >= line_limit:
                    eof = False
                    break
                encoded_size = len(line.encode("utf-8"))
                if output_size + encoded_size > self.config.max_output_bytes:
                    if not lines:
                        raise PolicyError("a single line exceeds the output limit")
                    eof = False
                    break
                lines.append(line)
                output_size += encoded_size

            result = {
                "path": path,
                "offset": start,
                "next_offset": start + len(lines),
                "start_line": start + 1,
                "end_line": start + len(lines),
                "content": "".join(lines),
                "eof": eof,
                "size": size,
                "sha256": hashlib.sha256(content).hexdigest(),
            }
            self._audit("read_file", True, {"path": path, "lines": len(lines), "bytes": output_size})
            return result
        except Exception as error:
            self._audit("read_file", False, {"path": path}, error)
            raise

    def read_image(self, path: str) -> Annotated[CallToolResult, ReadImageResult]:
        """Return image metadata plus bounded PNG, JPEG, WebP, or GIF MCP image content."""
        try:
            _, content, format_name, metadata = self._load_image(path)
            self._audit("read_image", True, metadata)
            return CallToolResult(
                content=[Image(data=content, format=format_name).to_image_content()],
                structured_content=metadata,
            )
        except Exception as error:
            self._audit("read_image", False, {"path": path}, error)
            raise

    def share_image(
        self,
        path: str,
        ttl_seconds: int = 900,
    ) -> Annotated[CallToolResult, ShareImageResult]:
        """Publish a temporary image and render it in the attached image viewer UI."""
        try:
            target, content, _, metadata = self._load_image(path)
            result = self._publish_image(
                content=content,
                filename=target.name,
                metadata=metadata,
                ttl_seconds=ttl_seconds,
            )
            self._audit("share_image", True, {
                "path": path,
                "bytes": metadata["bytes"],
                "ttl_seconds": ttl_seconds,
            })
            return result
        except Exception as error:
            self._audit("share_image", False, {"path": path}, error)
            raise

    def _publish_image(
        self,
        *,
        content: bytes,
        filename: str,
        metadata: ReadImageResult,
        ttl_seconds: int,
    ) -> CallToolResult:
        if self.artifact_store is None:
            raise OperationError("temporary image sharing is not configured")
        if not 60 <= ttl_seconds <= 3_600:
            raise PolicyError("image link lifetime must be between 60 and 3600 seconds")
        try:
            published = self.artifact_store.publish(
                data=content,
                filename=filename,
                mime_type=str(metadata["mime_type"]),
                sha256=str(metadata["sha256"]),
                ttl_seconds=ttl_seconds,
            )
        except RuntimeError as error:
            raise OperationError(str(error)) from error
        url = str(published["url"])
        result: ShareImageResult = {
            **metadata,
            "url": url,
            "expires_at": str(published["expires_at"]),
            "display_markdown": f"![Loki image]({url})",
        }
        return CallToolResult(
            content=[TextContent(type="text", text=f"Temporary image URL: {url}")],
            structured_content=result,
        )

    def share_file(
        self,
        path: str,
        ttl_seconds: int = 900,
    ) -> Annotated[CallToolResult, ShareFileResult]:
        """Publish one workspace file as a temporary downloadable attachment URL."""
        try:
            target = self._regular_file(path)
            size = target.stat().st_size
            if size > MAX_SHARED_FILE_BYTES:
                raise PolicyError("file exceeds the 32 MiB sharing limit")
            content = target.read_bytes()
            mime_type = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
            result = self._publish_attachment(
                content=content,
                filename=target.name,
                path=path,
                mime_type=mime_type,
                ttl_seconds=ttl_seconds,
            )
            self._audit("share_file", True, {"path": path, "bytes": size, "ttl_seconds": ttl_seconds})
            return result
        except Exception as error:
            self._audit("share_file", False, {"path": path, "ttl_seconds": ttl_seconds}, error)
            raise

    def workspace_bundle(
        self,
        paths: list[str],
        filename: str = "loki-workspace.zip",
        ttl_seconds: int = 900,
    ) -> Annotated[CallToolResult, BundleResult]:
        """Create a bounded ZIP from selected workspace paths and publish it as a temporary attachment."""
        try:
            if not paths or len(paths) > 64:
                raise PolicyError("paths must contain between 1 and 64 entries")
            if not filename.lower().endswith(".zip") or Path(filename).name != filename:
                raise PolicyError("bundle filename must be a plain .zip filename")
            files: dict[str, Path] = {}
            total_bytes = 0
            excluded_entries = 0
            for requested in paths:
                target = self.policy.resolve(requested)
                candidates = [target] if target.is_file() else sorted(target.rglob("*"))
                for candidate in candidates:
                    if candidate.is_dir():
                        continue
                    relative = self.policy.relative(candidate)
                    if not self.policy.allowed_entry(candidate):
                        excluded_entries += 1
                        continue
                    if not candidate.is_file():
                        raise PolicyError(f"bundle entry is not a regular file: {relative}")
                    if relative in files:
                        continue
                    files[relative] = candidate
                    if len(files) > MAX_BUNDLE_FILES:
                        raise PolicyError("bundle exceeds the 512 file limit")
                    total_bytes += candidate.stat().st_size
                    if total_bytes > MAX_SHARED_FILE_BYTES:
                        raise PolicyError("bundle input exceeds the 32 MiB limit")
            if not files:
                raise PolicyError("bundle contains no regular files")
            buffer = io.BytesIO()
            with zipfile.ZipFile(buffer, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as archive:
                for relative, candidate in sorted(files.items()):
                    archive.write(candidate, arcname=relative)
            content = buffer.getvalue()
            if len(content) > MAX_SHARED_FILE_BYTES:
                raise PolicyError("compressed bundle exceeds the 32 MiB limit")
            result = self._publish_attachment(
                content=content,
                filename=filename,
                path=",".join(paths),
                mime_type="application/zip",
                ttl_seconds=ttl_seconds,
                extra={
                    "file_count": len(files),
                    "input_bytes": total_bytes,
                    "excluded_entries": excluded_entries,
                },
            )
            self._audit("workspace_bundle", True, {
                "paths": paths, "file_count": len(files), "bytes": len(content), "ttl_seconds": ttl_seconds,
            })
            return result
        except Exception as error:
            self._audit("workspace_bundle", False, {"paths": paths, "ttl_seconds": ttl_seconds}, error)
            raise

    def list_shared_files(self) -> dict[str, Any]:
        """List active temporary image, file, and bundle links."""
        if self.artifact_store is None:
            return {"artifacts": [], "configured": False}
        artifacts = self.artifact_store.list()
        result = {"artifacts": artifacts, "configured": True}
        self._audit("list_shared_files", True, {"count": len(artifacts)})
        return result

    def revoke_shared_file(self, share_id: str) -> dict[str, Any]:
        """Revoke one temporary image, file, or bundle link before its expiry."""
        try:
            if self.artifact_store is None:
                raise OperationError("temporary file sharing is not configured")
            revoked = self.artifact_store.revoke(share_id)
            if revoked is None:
                raise PolicyError("artifact share was not found or has expired")
            result = {"revoked": True, **revoked}
            self._audit("revoke_shared_file", True, result)
            return result
        except Exception as error:
            self._audit("revoke_shared_file", False, {"share_id": share_id}, error)
            raise

    def _publish_attachment(
        self,
        *,
        content: bytes,
        filename: str,
        path: str,
        mime_type: str,
        ttl_seconds: int,
        extra: dict[str, Any] | None = None,
    ) -> CallToolResult:
        if self.artifact_store is None:
            raise OperationError("temporary file sharing is not configured")
        if not 60 <= ttl_seconds <= 3_600:
            raise PolicyError("file link lifetime must be between 60 and 3600 seconds")
        digest = hashlib.sha256(content).hexdigest()
        try:
            published = self.artifact_store.publish(
                data=content,
                filename=filename,
                mime_type=mime_type,
                sha256=digest,
                ttl_seconds=ttl_seconds,
                disposition="attachment",
            )
        except RuntimeError as error:
            raise OperationError(str(error)) from error
        url = str(published["url"])
        result: dict[str, Any] = {
            "path": path,
            "filename": filename,
            "mime_type": mime_type,
            "bytes": len(content),
            "sha256": digest,
            "share_id": str(published["share_id"]),
            "url": url,
            "expires_at": str(published["expires_at"]),
            **(extra or {}),
        }
        return CallToolResult(
            content=[
                TextContent(type="text", text=f"Temporary download: {url}"),
                ResourceLink(
                    name=filename,
                    uri=url,
                    description=f"Temporary Loki workspace artifact ({len(content)} bytes)",
                    mimeType=mime_type,
                    size=len(content),
                ),
            ],
            structured_content=result,
        )

    def _load_image(self, path: str) -> tuple[Path, bytes, str, ReadImageResult]:
        target = self._regular_file(path)
        size = target.stat().st_size
        if size > min(self.config.max_file_bytes, MAX_IMAGE_BYTES):
            raise PolicyError("image exceeds read limit")
        image_format = IMAGE_FORMATS.get(target.suffix.lower())
        if image_format is None:
            raise PolicyError("unsupported image format")
        format_name, signatures = image_format
        content = target.read_bytes()
        header = content[:12]
        valid = any(header.startswith(signature) for signature in signatures)
        if format_name == "webp":
            valid = header.startswith(b"RIFF") and header[8:12] == b"WEBP"
        if not valid:
            raise PolicyError("image signature does not match its extension")
        metadata: ReadImageResult = {
            "path": self.policy.relative(target),
            "mime_type": f"image/{format_name}",
            "bytes": size,
            "sha256": hashlib.sha256(content).hexdigest(),
        }
        return target, content, format_name, metadata

    def write_image(
        self, path: str, data_base64: str, mime_type: str, overwrite: bool = False,
        expected_sha256: str | None = None,
    ) -> dict[str, Any]:
        """Decode and atomically save a bounded base64 PNG, JPEG, WebP, or GIF in the workspace."""
        try:
            expected_suffix = IMAGE_MIME_FORMATS.get(mime_type.lower())
            if expected_suffix is None:
                raise PolicyError("unsupported image MIME type")
            target = self.policy.resolve(path, must_exist=False)
            if target.suffix.lower() not in IMAGE_FORMATS:
                raise PolicyError("image path must use a supported extension")
            actual_format = IMAGE_FORMATS[target.suffix.lower()][0]
            expected_format = IMAGE_FORMATS[expected_suffix][0]
            if actual_format != expected_format:
                raise PolicyError("image MIME type does not match path extension")
            prefix = f"data:{mime_type};base64,"
            encoded = data_base64[len(prefix):] if data_base64.startswith(prefix) else data_base64
            if len(encoded) > ((MAX_IMAGE_BYTES + 2) // 3) * 4:
                raise PolicyError("encoded image exceeds write limit")
            try:
                content = base64.b64decode(encoded, validate=True)
            except (binascii.Error, ValueError) as error:
                raise PolicyError("invalid base64 image data") from error
            if len(content) > MAX_IMAGE_BYTES:
                raise PolicyError("image exceeds write limit")
            format_name, signatures = IMAGE_FORMATS[target.suffix.lower()]
            valid = any(content.startswith(signature) for signature in signatures)
            if format_name == "webp":
                valid = content.startswith(b"RIFF") and len(content) >= 12 and content[8:12] == b"WEBP"
            if not valid:
                raise PolicyError("image signature does not match its extension")
            with self._mutation_lock:
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                target = self.policy.resolve(path, must_exist=False)
                previous_revision = None
                if target.exists():
                    current_sha256 = hashlib.sha256(target.read_bytes()).hexdigest()
                    if not overwrite:
                        raise FileExistsError(path)
                    if expected_sha256 != current_sha256:
                        raise PolicyError("existing image changed or was not read before overwrite")
                    previous_revision = self._capture_file_revision(target, "write_image")
                atomic_write_bytes(target, content, overwrite=target.exists())
            digest = hashlib.sha256(content).hexdigest()
            self._audit("write_image", True, {"path": path, "bytes": len(content), "sha256": digest})
            return {
                "path": self.policy.relative(target), "bytes": len(content),
                "mime_type": mime_type.lower(), "sha256": digest,
                "previous_revision": previous_revision,
            }
        except Exception as error:
            self._audit("write_image", False, {"path": path}, error)
            raise

    def search_text(
        self,
        query: str,
        path: str = ".",
        max_results: int = 100,
        regex: bool = False,
    ) -> dict[str, Any]:
        """Search workspace text quickly; fixed-string by default, optional regex."""
        try:
            if not query or len(query) > 500:
                raise ValueError("query length must be between 1 and 500 characters")
            target = self.policy.resolve(path)
            relative = self.policy.relative(target)
            limit = max(1, min(int(max_results), self.config.max_search_results))
            command = [
                "/usr/bin/rg",
                "--json",
                "--line-number",
                "--column",
                "--hidden",
                "--no-config",
                "--no-messages",
                "--max-filesize",
                str(self.config.max_file_bytes),
            ]
            if not regex:
                command.append("--fixed-strings")
            for pattern in RG_EXCLUDES:
                command.extend(["--glob", pattern])
            command.extend(["--", query, relative])

            process = subprocess.Popen(
                command,
                cwd=self.policy.root,
                env=self._environment(),
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
                encoding="utf-8",
                errors="replace",
                shell=False,
            )
            matches: list[dict[str, Any]] = []
            truncated = False
            timed_out = Event()

            def terminate_search() -> None:
                if process.poll() is None:
                    timed_out.set()
                    process.kill()

            timer = Timer(15, terminate_search)
            timer.daemon = True
            timer.start()
            assert process.stdout is not None
            try:
                for line in process.stdout:
                    event = json.loads(line)
                    if event.get("type") != "match":
                        continue
                    data = event["data"]
                    submatches = data.get("submatches", [])
                    column = int(submatches[0]["start"]) + 1 if submatches else 1
                    matched_path = str(data["path"]["text"]).removeprefix("./")
                    matches.append({
                        "path": matched_path,
                        "line": int(data["line_number"]),
                        "column": column,
                        "text": str(data["lines"]["text"]).rstrip("\r\n")[:2_000],
                    })
                    if len(matches) >= limit:
                        truncated = True
                        process.terminate()
                        break
            finally:
                timer.cancel()
                process.stdout.close()
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=2)
            if timed_out.is_set():
                raise PolicyError("search timed out")
            if process.returncode not in {0, 1, -15} and not truncated:
                raise OperationError(
                    f"text search failed with ripgrep exit code {process.returncode}; narrow the path or run diagnostics"
                )
            result = {"matches": matches, "truncated": truncated}
            self._audit("search_text", True, {"path": path, "count": len(matches), "regex": regex})
            return result
        except Exception as error:
            self._audit("search_text", False, {"path": path, "regex": regex}, error)
            raise

    def write_file(self, path: str, content: str) -> dict[str, Any]:
        """Atomically create a new UTF-8 text file; existing files are never overwritten."""
        try:
            encoded_size = len(content.encode("utf-8"))
            if encoded_size > self.config.max_write_bytes:
                raise PolicyError("content exceeds write limit")
            with self._mutation_lock:
                target = self.policy.resolve(path, must_exist=False)
                if target.exists():
                    raise FileExistsError("destination already exists; use replace_text or apply_patch")
                target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                target = self.policy.resolve(path, must_exist=False)
                atomic_write(target, content, overwrite=False)
            result = {"path": path, "bytes": encoded_size}
            self._audit("write_file", True, result)
            return result
        except Exception as error:
            self._audit("write_file", False, {"path": path}, error)
            raise

    def replace_text(
        self,
        path: str,
        old: str,
        new: str,
        expected_sha256: str,
        expected_replacements: int = 1,
    ) -> dict[str, Any]:
        """Replace exact text after hash/match checks and preserve the original file revision."""
        try:
            if not old:
                raise ValueError("old text must not be empty")
            if expected_replacements < 1:
                raise ValueError("expected_replacements must be positive")
            with self._mutation_lock:
                target = self._regular_file(path)
                if target.stat().st_size > self.config.max_write_bytes:
                    raise PolicyError("file exceeds write limit")
                current_bytes = target.read_bytes()
                current_sha256 = hashlib.sha256(current_bytes).hexdigest()
                if current_sha256 != expected_sha256:
                    raise PolicyError("file changed since it was read; read it again before editing")
                current = current_bytes.decode("utf-8")
                actual = current.count(old)
                if actual != expected_replacements:
                    raise ValueError(f"expected {expected_replacements} replacements, found {actual}")
                updated = current.replace(old, new)
                if len(updated.encode("utf-8")) > self.config.max_write_bytes:
                    raise PolicyError("result exceeds write limit")
                revision = self._capture_file_revision(target, "replace_text")
                mode = target.stat().st_mode & 0o777
                atomic_write(target, updated)
                os.chmod(target, mode)
            result = {
                "path": path, "replacements": actual, "previous_revision": revision,
                "sha256": hashlib.sha256(updated.encode("utf-8")).hexdigest(),
            }
            self._audit("replace_text", True, result)
            return result
        except Exception as error:
            self._audit("replace_text", False, {"path": path}, error)
            raise

    def apply_patch(self, patch: str) -> dict[str, Any]:
        """Validate and apply a bounded unified diff with git apply; create/modify only."""
        digest = hashlib.sha256(patch.encode("utf-8")).hexdigest()
        try:
            encoded = patch.encode("utf-8")
            if not encoded or len(encoded) > self.config.max_patch_bytes:
                raise PolicyError("patch is empty or exceeds the patch limit")
            if b"\x00" in encoded or any(marker in encoded for marker in PATCH_REJECTED_MARKERS):
                raise PolicyError("binary, delete, rename, copy, and symlink patches are not allowed")

            with self._mutation_lock:
                numstat = self._run_input(
                    ["/usr/bin/git", "apply", "--numstat", "-z"],
                    encoded,
                    timeout=15,
                )
                if numstat["exit_code"] != 0:
                    raise ValueError(f"invalid patch: {numstat['output']}")
                files = self._parse_numstat(numstat["raw_output"])
                if not files:
                    raise ValueError("patch contains no file changes")
                if len(files) > self.config.max_patch_files:
                    raise PolicyError("patch changes too many files")
                for item in files:
                    self.policy.resolve(item["path"], must_exist=False)

                checked = self._run_input(["/usr/bin/git", "apply", "--check"], encoded, timeout=30)
                if checked["exit_code"] != 0:
                    raise ValueError(f"patch check failed: {checked['output']}")
                revisions: dict[str, str] = {}
                for item in files:
                    target = self.policy.resolve(item["path"], must_exist=False)
                    if target.exists():
                        revisions[item["path"]] = self._capture_file_revision(target, "apply_patch")
                applied = self._run_input(["/usr/bin/git", "apply"], encoded, timeout=30)
                if applied["exit_code"] != 0:
                    raise ValueError(f"patch apply failed: {applied['output']}")

            result = {
                "files": files, "patch_sha256": digest, "warnings": applied["output"],
                "previous_revisions": revisions,
            }
            self._audit("apply_patch", True, {"files": [item["path"] for item in files], "patch_sha256": digest})
            return result
        except Exception as error:
            self._audit("apply_patch", False, {"patch_sha256": digest}, error)
            raise

    def list_file_revisions(self, path: str, limit: int = 20) -> dict[str, Any]:
        """List saved pre-mutation revisions for one workspace file without returning contents."""
        target = self.policy.resolve(path, must_exist=False)
        relative = self.policy.relative(target)
        records = []
        for metadata_path in self._file_revision_dir().glob("*.json"):
            try:
                record = json.loads(metadata_path.read_text(encoding="utf-8"))
            except (OSError, ValueError):
                continue
            if record.get("path") == relative:
                records.append(record)
        records.sort(key=lambda item: item["created_at"], reverse=True)
        result = {"path": relative, "revisions": records[:max(1, min(int(limit), 100))]}
        self._audit("list_file_revisions", True, {"path": relative, "count": len(result["revisions"])})
        return result

    def show_file_revision_diff(self, path: str, revision: str) -> dict[str, Any]:
        """Compare a saved file revision with the current workspace file using a bounded unified diff."""
        target = self._regular_file(path)
        metadata, previous = self._load_file_revision(revision, self.policy.relative(target))
        current = target.read_bytes()
        try:
            before_text = previous.decode("utf-8").splitlines(keepends=True)
            current_text = current.decode("utf-8").splitlines(keepends=True)
        except UnicodeDecodeError:
            result = {"path": path, "revision": revision, "binary": True, "diff": None}
        else:
            diff = "".join(difflib.unified_diff(
                before_text, current_text, fromfile=f"{path}@{revision[:12]}", tofile=path,
            ))
            encoded = diff.encode("utf-8")
            truncated = len(encoded) > self.config.max_output_bytes
            if truncated:
                diff = encoded[:self.config.max_output_bytes].decode("utf-8", "replace")
            result = {
                "path": path, "revision": revision, "binary": False,
                "diff": diff, "truncated": truncated, "previous_sha256": metadata["sha256"],
                "current_sha256": hashlib.sha256(current).hexdigest(),
            }
        self._audit("show_file_revision_diff", True, {"path": path, "revision": revision})
        return result

    def restore_file_revision(self, path: str, revision: str, expected_sha256: str) -> dict[str, Any]:
        """Restore a saved revision only when the current file still matches the caller's expected hash."""
        with self._mutation_lock:
            target = self.policy.resolve(path, must_exist=False)
            relative = self.policy.relative(target)
            metadata, previous = self._load_file_revision(revision, relative)
            undo_revision = None
            if target.exists():
                if not target.is_file():
                    raise PolicyError("restore target must be a regular file")
                current_sha256 = hashlib.sha256(target.read_bytes()).hexdigest()
                if current_sha256 != expected_sha256:
                    raise PolicyError("file changed since it was read; inspect it again before restoring")
                undo_revision = self._capture_file_revision(target, "restore_file_revision")
            elif expected_sha256 != "missing":
                raise PolicyError("restore target is missing; use expected_sha256='missing' after confirming")
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            atomic_write_bytes(target, previous)
            os.chmod(target, int(metadata["mode"]))
        result = {
            "path": path, "restored_revision": revision, "undo_revision": undo_revision,
            "sha256": metadata["sha256"], "bytes": metadata["bytes"],
        }
        self._audit("restore_file_revision", True, result)
        return result

    def _file_revision_dir(self) -> Path:
        directory = self.config.audit_log.parent / "file-revisions"
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        return directory

    def _capture_file_revision(self, target: Path, operation: str) -> str:
        if not target.is_file() or target.is_symlink():
            raise PolicyError("only regular files can be revisioned")
        content = target.read_bytes()
        if len(content) > MAX_FILE_REVISION_BYTES:
            raise PolicyError("file exceeds revision backup limit; mutation was not applied")
        relative = self.policy.relative(target)
        digest = hashlib.sha256(content).hexdigest()
        revision = hashlib.sha256(
            f"{relative}\0{digest}\0{time.time_ns()}".encode("utf-8")
        ).hexdigest()
        directory = self._file_revision_dir()
        atomic_write_bytes(directory / f"{revision}.bin", content, overwrite=False)
        metadata = {
            "revision": revision, "path": relative, "operation": operation,
            "created_at": datetime.now(timezone.utc).isoformat(), "bytes": len(content),
            "sha256": digest, "mode": target.stat().st_mode & 0o777,
        }
        atomic_write(directory / f"{revision}.json", json.dumps(metadata, separators=(",", ":")), overwrite=False)
        self._prune_file_revisions(directory)
        return revision

    def _load_file_revision(self, revision: str, expected_path: str) -> tuple[dict[str, Any], bytes]:
        if re.fullmatch(r"[0-9a-f]{64}", revision) is None:
            raise PolicyError("invalid file revision")
        directory = self._file_revision_dir()
        metadata_path = directory / f"{revision}.json"
        content_path = directory / f"{revision}.bin"
        if not metadata_path.is_file() or not content_path.is_file():
            raise FileNotFoundError("unknown file revision")
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        content = content_path.read_bytes()
        if metadata.get("path") != expected_path:
            raise PolicyError("file revision belongs to a different path")
        if hashlib.sha256(content).hexdigest() != metadata.get("sha256"):
            raise PolicyError("file revision integrity check failed")
        return metadata, content

    @staticmethod
    def _prune_file_revisions(directory: Path) -> None:
        metadata_paths = sorted(directory.glob("*.json"), key=lambda path: path.stat().st_mtime_ns)
        total = sum((path.with_suffix(".bin").stat().st_size if path.with_suffix(".bin").exists() else 0) for path in metadata_paths)
        while metadata_paths and (len(metadata_paths) > MAX_FILE_REVISIONS or total > MAX_FILE_REVISION_STORE_BYTES):
            metadata_path = metadata_paths.pop(0)
            content_path = metadata_path.with_suffix(".bin")
            if content_path.exists():
                total -= content_path.stat().st_size
                content_path.unlink()
            metadata_path.unlink(missing_ok=True)

    def move_path(self, source: str, destination: str) -> dict[str, Any]:
        """Move one regular file to a new workspace path without overwriting."""
        try:
            with self._mutation_lock:
                source_path = self._regular_file(source)
                destination_path = self.policy.resolve(destination, must_exist=False)
                if destination_path.exists():
                    raise FileExistsError(destination)
                destination_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                destination_path = self.policy.resolve(destination, must_exist=False)
                revision = self._capture_file_revision(source_path, "move_path")
                os.rename(source_path, destination_path)
            result = {"source": source, "destination": destination, "previous_revision": revision}
            self._audit("move_path", True, result)
            return result
        except Exception as error:
            self._audit("move_path", False, {"source": source, "destination": destination}, error)
            raise

    def remove_tracked_file(self, path: str) -> dict[str, Any]:
        """Remove one Git-tracked file; quarantine unwanted untracked files with move_path instead."""
        try:
            with self._mutation_lock:
                target = self._regular_file(path)
                tracked = self._git(["ls-files", "--error-unmatch", "--", self.policy.relative(target)], timeout=10)
                if tracked["exit_code"] != 0:
                    raise PolicyError(
                        "only Git-tracked files may be removed; move unwanted untracked files into "
                        "the repository's ignored .tmp/loki-quarantine/ directory with move_path"
                    )
                revision = self._capture_file_revision(target, "remove_tracked_file")
                target.unlink()
            result = {"path": path, "removed": True, "previous_revision": revision}
            self._audit("remove_tracked_file", True, result)
            return result
        except Exception as error:
            self._audit("remove_tracked_file", False, {"path": path}, error)
            raise

    def agent_context(self, cwd: str = ".") -> dict[str, Any]:
        """Read applicable AGENTS.md and Skills for a workspace-relative or /workspace absolute cwd."""
        try:
            result = self.skills.agent_context(cwd)
            self._audit("agent_context", True, {"cwd": cwd, "skill_count": len(result["skills"])})
            return result
        except Exception as error:
            self._audit("agent_context", False, {"cwd": cwd}, error)
            raise

    def list_skills(self, cwd: str = ".") -> dict[str, Any]:
        """List shared and project Agent Skills, including overrides and validation errors."""
        try:
            result = self.skills.list_skills(cwd)
            self._audit("list_skills", True, {"cwd": cwd, "skill_count": len(result["skills"])})
            return result
        except Exception as error:
            self._audit("list_skills", False, {"cwd": cwd}, error)
            raise

    def activate_skill(self, name: str, cwd: str = ".") -> dict[str, Any]:
        """Load a matching Skill's complete instructions before using task tools."""
        try:
            result = self.skills.activate_skill(name, cwd)
            self._audit("activate_skill", True, {"name": name, "cwd": cwd, "revision": result["revision"]})
            return result
        except Exception as error:
            self._audit("activate_skill", False, {"name": name, "cwd": cwd}, error)
            raise

    def read_skill_resource(self, name: str, path: str, cwd: str = ".") -> dict[str, Any]:
        """Read one activated Skill resource from assets, references, or scripts."""
        try:
            result = self.skills.read_resource(name, path, cwd)
            self._audit("read_skill_resource", True, {"name": name, "path": path, "cwd": cwd})
            return result
        except Exception as error:
            self._audit("read_skill_resource", False, {"name": name, "path": path, "cwd": cwd}, error)
            raise

    def create_skill(
        self,
        scope: str,
        name: str,
        description: str,
        instructions: str,
        cwd: str = ".",
        metadata: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        """Create and validate a shared or project Agent Skill without expanding Loki permissions."""
        try:
            with self._mutation_lock:
                result = self.skills.create(scope, cwd, name, description, instructions, metadata)
            self._audit("create_skill", True, {"scope": scope, "name": name, "cwd": cwd})
            return result
        except Exception as error:
            self._audit("create_skill", False, {"scope": scope, "name": name, "cwd": cwd}, error)
            raise

    def edit_skill(self, name: str, patch: str, expected_sha256: str, cwd: str = ".") -> dict[str, Any]:
        """Atomically patch SKILL.md after optimistic revision and schema validation."""
        try:
            with self._mutation_lock:
                result = self.skills.edit(name, cwd, patch, expected_sha256)
            self._audit("edit_skill", True, {"name": name, "cwd": cwd, "revision": result["revision"]})
            return result
        except Exception as error:
            self._audit("edit_skill", False, {"name": name, "cwd": cwd}, error)
            raise

    def write_skill_resource(
        self,
        name: str,
        path: str,
        content: str,
        cwd: str = ".",
        overwrite: bool = False,
        expected_sha256: str | None = None,
        encoding: str = "utf-8",
    ) -> dict[str, Any]:
        """Atomically create or update an activated Skill resource with optional revision checking."""
        try:
            with self._mutation_lock:
                result = self.skills.write_resource(
                    name, path, content, cwd, overwrite, expected_sha256, encoding,
                )
            self._audit("write_skill_resource", True, {"name": name, "path": path, "cwd": cwd})
            return result
        except Exception as error:
            self._audit("write_skill_resource", False, {"name": name, "path": path, "cwd": cwd}, error)
            raise

    def validate_skill(self, name: str, cwd: str = ".") -> dict[str, Any]:
        """Validate one selected Agent Skill and enumerate its bounded resources."""
        try:
            result = self.skills.validate(name, cwd, available_tools=set(self._tool_catalog))
            self._audit("validate_skill", True, {"name": name, "cwd": cwd})
            return result
        except Exception as error:
            self._audit("validate_skill", False, {"name": name, "cwd": cwd}, error)
            raise

    def git_status(self, cwd: str = ".") -> dict[str, Any]:
        """Return concise branch and working-tree status without changing Git state."""
        working_directory = self._command_cwd(cwd)
        result = self._git(["status", "--short", "--branch", "--untracked-files=all"], cwd=working_directory)
        self._audit("git_status", result["exit_code"] == 0, {"cwd": cwd, "exit_code": result["exit_code"]})
        return result

    def git_diff(self, staged: bool = False, path: str | None = None, cwd: str = ".") -> dict[str, Any]:
        """Return a bounded Git diff for the worktree or index, optionally scoped to a path."""
        working_directory = self._command_cwd(cwd)
        arguments = ["diff", "--no-ext-diff"]
        if staged:
            arguments.append("--cached")
        if path is not None:
            base = self.policy.relative(working_directory)
            requested = path if base == "." else f"{base}/{path}"
            target = self.policy.resolve(requested)
            try:
                relative = target.relative_to(working_directory).as_posix()
            except ValueError as error:
                raise PolicyError("diff path must stay inside cwd") from error
            arguments.extend(["--", relative])
        result = self._git(arguments, cwd=working_directory)
        self._audit("git_diff", result["exit_code"] == 0, {"staged": staged, "path": path, "cwd": cwd})
        return result

    def git_commit_context(self, cwd: str = ".") -> dict[str, Any]:
        """Return the effective non-secret commit template and its Git config origins."""
        working_directory = self._command_cwd(cwd)
        origins_result = self._git(
            ["config", "--show-origin", "--get-all", "commit.template"],
            cwd=working_directory,
        )
        if origins_result["exit_code"] == 1:
            result = {"configured": False, "origins": [], "template": None}
            self._audit("git_commit_context", True, {"cwd": cwd, "configured": False})
            return result
        if origins_result["exit_code"] != 0:
            raise OperationError(f"unable to inspect commit template: {origins_result['output']}")
        origins: list[dict[str, str]] = []
        for line in str(origins_result["output"]).splitlines():
            source, separator, value = line.partition("\t")
            origins.append({"source": source, "value": value if separator else ""})
        path_result = self._git(
            ["config", "--path", "--get", "commit.template"],
            cwd=working_directory,
        )
        if path_result["exit_code"] != 0:
            raise OperationError(f"unable to resolve commit template: {path_result['output']}")
        configured_path = Path(str(path_result["output"]).strip())
        candidate = (
            configured_path if configured_path.is_absolute()
            else working_directory / configured_path
        )
        target = candidate.resolve(strict=True)
        if candidate.is_symlink() or not target.is_file():
            raise PolicyError("commit template must be a regular non-symbolic file")
        try:
            relative = target.relative_to(self.policy.root).as_posix()
        except ValueError:
            if not any(
                target == root.resolve() or root.resolve() in target.parents
                for root in TRUSTED_GIT_TEMPLATE_ROOTS
            ):
                raise PolicyError("commit template is outside trusted template roots")
        else:
            self.policy.resolve(relative)
        content = target.read_bytes()
        if len(content) > min(self.config.max_file_bytes, 65_536):
            raise PolicyError("commit template exceeds read limit")
        if b"\x00" in content:
            raise PolicyError("commit template must be UTF-8 text")
        try:
            text = content.decode("utf-8")
        except UnicodeDecodeError as error:
            raise PolicyError("commit template must be UTF-8 text") from error
        result = {
            "configured": True,
            "origins": origins,
            "template": {
                "path": str(configured_path),
                "content": text,
                "sha256": hashlib.sha256(content).hexdigest(),
            },
        }
        self._audit("git_commit_context", True, {"cwd": cwd, "configured": True})
        return result

    def view_git_diff(
        self,
        staged: bool = False,
        path: str | None = None,
        cwd: str = ".",
    ) -> Annotated[CallToolResult, DeveloperViewResult]:
        """Render a bounded staged or worktree Git diff in the attached developer viewer UI."""
        try:
            diff = self.git_diff(staged=staged, path=path, cwd=cwd)
            if diff["exit_code"] != 0:
                raise OperationError(f"git diff failed: {diff['output']}")
            content = str(diff["output"])
            result = self._developer_view_result(
                kind="diff",
                title="Staged changes" if staged else "Worktree changes",
                subtitle=f"{cwd}{f' · {path}' if path else ''}",
                content=content,
                truncated=bool(diff.get("truncated", False)),
                stats={
                    "files": len(re.findall(r"^diff --git ", content, flags=re.MULTILINE)),
                    "additions": len(re.findall(r"^\+(?!\+\+)", content, flags=re.MULTILINE)),
                    "deletions": len(re.findall(r"^-(?!--)", content, flags=re.MULTILINE)),
                },
            )
            self._audit("view_git_diff", True, {"cwd": cwd, "path": path, "staged": staged})
            return result
        except Exception as error:
            self._audit("view_git_diff", False, {"cwd": cwd, "path": path, "staged": staged}, error)
            raise

    def view_test_report(self, path: str) -> Annotated[CallToolResult, DeveloperViewResult]:
        """Render a bounded JUnit XML, TAP, JSON, or plain-text test report in the developer viewer UI."""
        try:
            target = self._regular_file(path)
            if target.stat().st_size > min(self.config.max_file_bytes, MAX_DEVELOPER_VIEW_BYTES):
                raise PolicyError("test report exceeds viewer limit")
            try:
                content = target.read_text(encoding="utf-8")
            except UnicodeDecodeError as error:
                raise PolicyError("test report must be UTF-8 text") from error
            stats: dict[str, Any] = {"format": target.suffix.lower().removeprefix(".") or "text"}
            if target.suffix.lower() == ".xml" or content.lstrip().startswith("<testsuite"):
                try:
                    root = ElementTree.fromstring(content)
                except ElementTree.ParseError as error:
                    raise PolicyError(f"invalid JUnit XML: {error}") from error
                suites = [root] if root.tag == "testsuite" else list(root.iter("testsuite"))
                stats = {
                    "format": "junit",
                    "tests": sum(int(item.attrib.get("tests", "0")) for item in suites),
                    "failures": sum(int(item.attrib.get("failures", "0")) for item in suites),
                    "errors": sum(int(item.attrib.get("errors", "0")) for item in suites),
                    "skipped": sum(int(item.attrib.get("skipped", "0")) for item in suites),
                }
            result = self._developer_view_result(
                kind="test",
                title="Test report",
                subtitle=path,
                content=content,
                truncated=False,
                stats=stats,
            )
            self._audit("view_test_report", True, {"path": path, "bytes": target.stat().st_size})
            return result
        except Exception as error:
            self._audit("view_test_report", False, {"path": path}, error)
            raise

    def view_process_log(
        self,
        session_id: str,
        offset: int | None = None,
        limit: int = 65_536,
    ) -> Annotated[CallToolResult, DeveloperViewResult]:
        """Render bounded output from one Loki-managed process in the attached developer viewer UI."""
        try:
            process = self.processes.read(session_id, offset=offset, limit=limit)
            content = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", str(process["output"]))
            result = self._developer_view_result(
                kind="log",
                title="Process log",
                subtitle=f"session {session_id} · {process.get('status', 'unknown')}",
                content=content,
                truncated=bool(process.get("has_more", False) or process.get("output_lost", False)),
                stats={
                    "status": process.get("status"),
                    "exit_code": process.get("exit_code"),
                    "next_offset": process.get("next_offset"),
                },
            )
            self._audit("view_process_log", True, {"session_id": session_id, "bytes": len(content.encode("utf-8"))})
            return result
        except Exception as error:
            self._audit("view_process_log", False, {"session_id": session_id}, error)
            raise

    @staticmethod
    def _developer_view_result(
        *,
        kind: str,
        title: str,
        subtitle: str,
        content: str,
        truncated: bool,
        stats: dict[str, Any],
    ) -> CallToolResult:
        structured: DeveloperViewResult = {
            "kind": kind,
            "title": title,
            "subtitle": subtitle,
            "content": content,
            "truncated": truncated,
            "stats": stats,
        }
        return CallToolResult(
            content=[TextContent(type="text", text=f"{title}: {subtitle}")],
            structured_content=structured,
        )

    def git_index_state(self, cwd: str = ".") -> dict[str, Any]:
        """Return a stable digest of the Git index for optimistic partial-staging operations."""
        working_directory = self._command_cwd(cwd)
        digest = self._git_index_sha256(working_directory)
        result = {"cwd": cwd, "index_sha256": digest}
        self._audit("git_index_state", True, result)
        return result

    def git_stage_paths(
        self,
        paths: list[str],
        cwd: str = ".",
        expected_index_sha256: str | None = None,
    ) -> dict[str, Any]:
        """Stage complete workspace paths while preserving every unrelated index entry."""
        return self._git_mutate_paths("stage", paths, cwd, expected_index_sha256)

    def git_unstage_paths(
        self,
        paths: list[str],
        cwd: str = ".",
        expected_index_sha256: str | None = None,
    ) -> dict[str, Any]:
        """Unstage complete paths without changing worktree content or unrelated index entries."""
        return self._git_mutate_paths("unstage", paths, cwd, expected_index_sha256)

    def git_stage_patch(
        self,
        patch: str,
        cwd: str = ".",
        reverse: bool = False,
        expected_index_sha256: str | None = None,
    ) -> dict[str, Any]:
        """Apply selected text diff hunks to the Git index only; reverse unstages the same hunks."""
        digest = hashlib.sha256(patch.encode("utf-8")).hexdigest()
        try:
            encoded = patch.encode("utf-8")
            if not encoded or len(encoded) > self.config.max_patch_bytes:
                raise PolicyError("patch is empty or exceeds the patch limit")
            rejected = PATCH_REJECTED_MARKERS + (b"new file mode ",)
            if b"\x00" in encoded or any(marker in encoded for marker in rejected):
                raise PolicyError("only modifications to regular text files may be partially staged")
            working_directory = self._command_cwd(cwd)
            with self._mutation_lock:
                before = self._git_index_sha256(working_directory)
                if expected_index_sha256 is not None and before != expected_index_sha256:
                    raise PolicyError("Git index changed; inspect it again before staging")
                arguments = ["/usr/bin/git", "apply", "--cached"]
                if reverse:
                    arguments.append("--reverse")
                numstat = self._run_input(
                    ["/usr/bin/git", "apply", "--numstat", "-z"], encoded, timeout=15, cwd=working_directory,
                )
                if numstat["exit_code"] != 0:
                    raise ValueError(f"invalid staging patch: {numstat['output']}")
                files = self._parse_numstat(numstat["raw_output"])
                if not files or len(files) > self.config.max_patch_files:
                    raise PolicyError("staging patch has no files or changes too many files")
                for item in files:
                    self._resolve_git_path(working_directory, item["path"], must_exist=True)
                checked = self._run_input([*arguments, "--check"], encoded, timeout=30, cwd=working_directory)
                if checked["exit_code"] != 0:
                    raise ValueError(f"staging patch check failed: {checked['output']}")
                applied = self._run_input(arguments, encoded, timeout=30, cwd=working_directory)
                if applied["exit_code"] != 0:
                    raise ValueError(f"staging patch failed: {applied['output']}")
                after = self._git_index_sha256(working_directory)
            result = {
                "files": files, "reverse": reverse, "patch_sha256": digest,
                "previous_index_sha256": before, "index_sha256": after,
            }
            self._audit("git_stage_patch", True, {"cwd": cwd, "reverse": reverse, "files": [x["path"] for x in files]})
            return result
        except Exception as error:
            self._audit("git_stage_patch", False, {"cwd": cwd, "reverse": reverse, "patch_sha256": digest}, error)
            raise

    def run_check(self, name: str) -> dict[str, Any]:
        """Run a fast root-configured check to completion; use start_check when it may take over 30 seconds."""
        try:
            spec = self.config.checks.get(name)
            if spec is None:
                raise PolicyError(f"unknown check: {name}")
            result = self._run_spec(spec)
            self._audit("run_check", result["exit_code"] == 0, {"name": name, "exit_code": result["exit_code"]})
            return {"name": name, **result}
        except Exception as error:
            self._audit("run_check", False, {"name": name}, error)
            raise

    def start_check(self, name: str) -> dict[str, Any]:
        """Start a potentially slow configured check and return a session_id for useful parallel work and later read_process calls."""
        try:
            result = self.processes.start_check(name)
            self._audit("start_check", True, {"name": name, "session_id": result["session_id"]})
            return {"name": name, **result}
        except Exception as error:
            self._audit("start_check", False, {"name": name}, error)
            raise

    def npm(
        self,
        arguments: list[str],
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 300,
    ) -> dict[str, Any]:
        """Run an approved npm command with Node selected by FNM."""
        subcommand = arguments[0] if arguments else None
        try:
            if subcommand not in NPM_SUBCOMMANDS:
                raise PolicyError("npm subcommand is not allowed")
            self._validate_tool_arguments(arguments)
            working_directory = self.policy.resolve(cwd)
            if not working_directory.is_dir():
                raise PolicyError("command cwd must be a directory")
            version = self._node_version(working_directory, node_version)
            timeout = max(1, min(int(timeout_seconds), 1_800))
            result = self._run(
                [
                    "/home/linuxbrew/.linuxbrew/bin/fnm",
                    "exec",
                    "--using",
                    version,
                    "npm",
                    *arguments,
                ],
                timeout=timeout,
                cwd=working_directory,
                environment=self._tool_environment(),
                max_output_bytes=self.config.max_output_bytes,
            )
            self._audit(
                "npm",
                result["exit_code"] == 0,
                {"subcommand": subcommand, "argument_count": len(arguments), "cwd": cwd, "exit_code": result["exit_code"]},
            )
            return {"subcommand": subcommand, "node_version": version, **result}
        except Exception as error:
            self._audit("npm", False, {"subcommand": subcommand, "cwd": cwd}, error)
            raise

    def fnm(self, arguments: list[str], timeout_seconds: int = 300) -> dict[str, Any]:
        """Manage workspace-local Node versions with FNM."""
        subcommand = arguments[0] if arguments else None
        try:
            if subcommand not in FNM_SUBCOMMANDS:
                raise PolicyError("FNM subcommand is not allowed")
            self._validate_tool_arguments(arguments)
            timeout = max(1, min(int(timeout_seconds), 1_800))
            result = self._run(
                ["/home/linuxbrew/.linuxbrew/bin/fnm", *arguments],
                timeout=timeout,
                environment=self._tool_environment(),
                max_output_bytes=self.config.max_output_bytes,
            )
            self._audit(
                "fnm",
                result["exit_code"] == 0,
                {"subcommand": subcommand, "argument_count": len(arguments), "exit_code": result["exit_code"]},
            )
            return {"subcommand": subcommand, **result}
        except Exception as error:
            self._audit("fnm", False, {"subcommand": subcommand}, error)
            raise

    def just(
        self,
        arguments: list[str] | None = None,
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 900,
    ) -> dict[str, Any]:
        """Run a Just recipe inside the workspace with FNM-managed Node available."""
        clean_arguments = arguments or []
        try:
            self._validate_tool_arguments(clean_arguments, allow_empty=True)
            working_directory = self.policy.resolve(cwd)
            if not working_directory.is_dir():
                raise PolicyError("command cwd must be a directory")
            version = self._node_version(working_directory, node_version)
            timeout = max(1, min(int(timeout_seconds), 1_800))
            result = self._run(
                [
                    "/home/linuxbrew/.linuxbrew/bin/fnm",
                    "exec",
                    "--using",
                    version,
                    "/home/linuxbrew/.linuxbrew/bin/just",
                    *clean_arguments,
                ],
                timeout=timeout,
                cwd=working_directory,
                environment=self._tool_environment(),
                max_output_bytes=self.config.max_output_bytes,
            )
            self._audit(
                "just",
                result["exit_code"] == 0,
                {"argument_count": len(clean_arguments), "cwd": cwd, "exit_code": result["exit_code"]},
            )
            return {"node_version": version, **result}
        except Exception as error:
            self._audit("just", False, {"cwd": cwd}, error)
            raise

    def run_pnpm(
        self,
        arguments: list[str],
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 900,
    ) -> dict[str, Any]:
        """Run finite pnpm argv to completion; use start_pnpm for dev, watch, preview, or serve."""
        try:
            self._validate_tool_arguments(arguments)
            working_directory = self.policy.resolve(cwd)
            if not working_directory.is_dir():
                raise PolicyError("command cwd must be a directory")
            version = self._node_version(working_directory, node_version)
            command = self._pnpm_command(arguments, version)
            timeout = max(1, min(int(timeout_seconds), 1_800))
            result = self._run(
                command,
                timeout=timeout,
                cwd=working_directory,
                environment=self._tool_environment(),
                max_output_bytes=self.config.max_output_bytes,
            )
            self._audit(
                "run_pnpm",
                result["exit_code"] == 0,
                {"argument_count": len(arguments), "cwd": cwd, "exit_code": result["exit_code"]},
            )
            return {"node_version": version, **result}
        except Exception as error:
            self._audit("run_pnpm", False, {"cwd": cwd}, error)
            raise

    def start_pnpm(
        self,
        arguments: list[str],
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 14_400,
    ) -> dict[str, Any]:
        """Start long-running pnpm argv and return a session_id for read_process or stop_process."""
        try:
            self._validate_tool_arguments(arguments)
            working_directory = self.policy.resolve(cwd)
            if not working_directory.is_dir():
                raise PolicyError("command cwd must be a directory")
            version = self._node_version(working_directory, node_version)
            timeout = max(30, min(int(timeout_seconds), 43_200))
            result = self.processes.start_command(
                name="pnpm",
                command=self._pnpm_command(arguments, version),
                cwd=working_directory,
                environment=self._environment(self._tool_environment()),
                timeout_seconds=timeout,
                max_output_bytes=10_485_760,
            )
            self._audit("start_pnpm", True, {"cwd": cwd, "session_id": result["session_id"]})
            return {"node_version": version, **result}
        except Exception as error:
            self._audit("start_pnpm", False, {"cwd": cwd}, error)
            raise

    def exec_command(
        self,
        executable: str,
        arguments: list[str] | None = None,
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 900,
    ) -> dict[str, Any]:
        """Run a non-secret executable in a relative or /workspace absolute cwd; use the action tool for centrally registered commands."""
        clean_arguments = arguments or []
        try:
            working_directory = self._command_cwd(cwd)
            command, version = self._command_argv(executable, clean_arguments, working_directory, node_version)
            checkpoint = self._checkpoint(working_directory)
            timeout = max(1, min(int(timeout_seconds), 1_800))
            result = self._run(
                command,
                timeout=timeout,
                cwd=working_directory,
                environment=self._tool_environment(),
                max_output_bytes=self.config.max_output_bytes,
            )
            self._audit("exec_command", result["exit_code"] == 0, {
                "executable": executable, "argument_count": len(clean_arguments), "cwd": cwd,
                "exit_code": result["exit_code"],
            })
            return {"executable": executable, "node_version": version, "checkpoint": checkpoint, **result}
        except Exception as error:
            self._audit("exec_command", False, {"executable": executable, "cwd": cwd}, error)
            raise

    def start_command(
        self,
        executable: str,
        arguments: list[str] | None = None,
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 14_400,
    ) -> dict[str, Any]:
        """Start a non-secret executable in a relative or /workspace absolute cwd; use the action tool for centrally registered commands."""
        clean_arguments = arguments or []
        try:
            working_directory = self._command_cwd(cwd)
            command, version = self._command_argv(executable, clean_arguments, working_directory, node_version)
            checkpoint = self._checkpoint(working_directory)
            timeout = max(30, min(int(timeout_seconds), 43_200))
            result = self.processes.start_command(
                name=executable,
                command=command,
                cwd=working_directory,
                environment=self._environment(self._tool_environment()),
                timeout_seconds=timeout,
                max_output_bytes=10_485_760,
            )
            self._audit("start_command", True, {
                "executable": executable, "argument_count": len(clean_arguments), "cwd": cwd,
                "session_id": result["session_id"],
            })
            return {"executable": executable, "node_version": version, "checkpoint": checkpoint, **result}
        except Exception as error:
            self._audit("start_command", False, {"executable": executable, "cwd": cwd}, error)
            raise

    def _command_cwd(self, cwd: str) -> Path:
        working_directory = self.policy.resolve_cwd(cwd)
        if not working_directory.is_dir():
            raise PolicyError("command cwd must be a directory")
        return working_directory

    def _checkpoint(self, cwd: Path) -> str | None:
        repository = subprocess.run(
            ["/usr/bin/git", "-C", str(cwd), "rev-parse", "--show-toplevel"],
            env=self._environment(), stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            text=True, encoding="utf-8", errors="replace", timeout=10, check=False,
        )
        if repository.returncode != 0:
            return None
        repository_root = Path(repository.stdout.strip()).resolve()
        if self.policy.root not in (repository_root, *repository_root.parents):
            raise PolicyError("repository escapes workspace")
        status = subprocess.run(
            ["/usr/bin/git", "-C", str(repository_root), "status", "--porcelain=v1", "-z"],
            env=self._environment(), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            timeout=30, check=False,
        )
        if status.returncode != 0:
            raise PolicyError("unable to inspect repository before command")
        if not status.stdout:
            return None
        head = subprocess.run(
            ["/usr/bin/git", "-C", str(repository_root), "rev-parse", "--verify", "HEAD"],
            env=self._environment(), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            timeout=10, check=False,
        )
        diff_arguments = ["diff", "--binary", "HEAD", "--"] if head.returncode == 0 else [
            "diff", "--binary", "--cached", "--",
        ]
        patch = subprocess.run(
            ["/usr/bin/git", "-C", str(repository_root), *diff_arguments],
            env=self._environment(), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            timeout=60, check=False,
        )
        if patch.returncode != 0:
            raise PolicyError("unable to checkpoint tracked changes")
        patch_bytes = patch.stdout
        if head.returncode != 0:
            worktree_patch = subprocess.run(
                ["/usr/bin/git", "-C", str(repository_root), "diff", "--binary", "--"],
                env=self._environment(), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                timeout=60, check=False,
            )
            if worktree_patch.returncode != 0:
                raise PolicyError("unable to checkpoint unstaged changes")
            patch_bytes += worktree_patch.stdout
        if len(patch_bytes) > 67_108_864:
            raise PolicyError("tracked changes exceed checkpoint limit")
        untracked = subprocess.run(
            ["/usr/bin/git", "-C", str(repository_root), "ls-files", "--others", "--exclude-standard", "-z"],
            env=self._environment(), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            timeout=30, check=False,
        )
        if untracked.returncode != 0:
            raise PolicyError("unable to list untracked files for checkpoint")
        digest = hashlib.sha256(patch_bytes + b"\0" + untracked.stdout).hexdigest()
        checkpoint_dir = self.config.audit_log.parent / "checkpoints"
        checkpoint_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        patch_path = checkpoint_dir / f"{digest}.patch"
        metadata_path = checkpoint_dir / f"{digest}.json"
        if not patch_path.exists():
            patch_path.write_bytes(patch_bytes)
            os.chmod(patch_path, 0o600)
        if not metadata_path.exists():
            relative_root = repository_root.relative_to(self.policy.root).as_posix()
            untracked_files = [item.decode("utf-8", "replace") for item in untracked.stdout.split(b"\0") if item]
            metadata_path.write_text(json.dumps({
                "created_at": datetime.now(timezone.utc).isoformat(),
                "repository": relative_root,
                "untracked": untracked_files,
            }, ensure_ascii=False, separators=(",", ":")), encoding="utf-8")
            os.chmod(metadata_path, 0o600)
        return digest

    def _command_argv(
        self,
        executable: str,
        arguments: list[str],
        cwd: Path,
        node_version: str | None,
    ) -> tuple[list[str], str | None]:
        executables = {**EXECUTABLES, **(self.config.executables or {})}
        if executable not in executables:
            raise PolicyError("executable is not allowed")
        self._validate_tool_arguments(arguments, allow_empty=True)
        self._validate_exec_policy(executable, arguments)
        resolved = executables[executable]
        if executable in {"node", "npm", "pnpm", "just", "actions-up"}:
            version = self._node_version(cwd, node_version)
            command = ["/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", version]
            if executable == "pnpm":
                command.extend(["corepack", "pnpm"])
            elif executable in {"just", "actions-up"}:
                command.append(resolved)
            else:
                command.append(resolved)
            return [*command, *arguments], version
        return [resolved, *arguments], None

    @staticmethod
    def _validate_exec_policy(executable: str, arguments: list[str]) -> None:
        lowered = [item.lower() for item in arguments]
        if executable in {"node"} and any(item in {"-e", "--eval", "-p", "--print"} for item in lowered):
            raise PolicyError("inline Node execution is not allowed")
        if executable in {"python", "python3"} and any(item in {"-c", "-m"} for item in lowered):
            raise PolicyError("inline or module Python execution is not allowed")
        if executable == "go" and len(lowered) >= 2 and lowered[0] == "env" and lowered[1] in {"-w", "-u"}:
            raise PolicyError("persistent Go environment changes are not allowed")
        if executable == "rustup" and lowered and lowered[0] in {"run", "self"}:
            raise PolicyError("Rustup command bypass or self-modification is not allowed")
        if executable == "find" and any(item in {"-delete", "-exec", "-execdir", "-ok", "-okdir"} for item in lowered):
            raise PolicyError("destructive find actions are not allowed")
        if executable == "fd" and any(
            item in {"-x", "--exec", "--exec-batch"}
            or item.startswith("--exec=")
            or item.startswith("--exec-batch=")
            for item in lowered
        ):
            raise PolicyError("fd external command execution is not allowed")
        if executable == "actionlint" and any(
            item in {"-shellcheck", "-pyflakes"}
            or item.startswith("-shellcheck=")
            or item.startswith("-pyflakes=")
            for item in lowered
        ):
            raise PolicyError("actionlint external command overrides are not allowed")
        if executable == "hyperfine":
            WorkspaceTools._validate_hyperfine_arguments(arguments)
        if executable == "task":
            forbidden = {"config", "execute", "import", "purge", "sync", "synchronize", "undo"}
            if any(item.lower() in forbidden or item.lower().startswith("rc.") for item in arguments):
                raise PolicyError("Taskwarrior configuration, hooks, bulk import, sync, and command execution are not allowed")
        if executable == "git":
            if any(
                item in {"-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix"}
                or item.startswith((
                    "--config-env", "--exec-path", "--git-dir=", "--work-tree=",
                    "--namespace=", "--super-prefix=",
                ))
                for item in lowered
            ):
                raise PolicyError("Git command injection options are not allowed")
            subcommand = next((item for item in lowered if not item.startswith("-")), "")
            if any(item in {"--no-gpg-sign", "--no-sign"} for item in lowered):
                raise PolicyError("unsigned Git commits and tags are not allowed")
            if subcommand in {"commit-tree", "fast-import", "mktag"}:
                raise PolicyError("Git object creation that bypasses signing is not allowed")
            if subcommand in {"clean", "reset", "restore"}:
                raise PolicyError("destructive Git subcommand is not allowed")
            if subcommand == "checkout" and "--" in lowered:
                raise PolicyError("destructive Git checkout is not allowed")
            if subcommand == "config" and not WorkspaceTools._allowed_git_config_read(arguments):
                raise PolicyError("only allowlisted Git configuration reads are allowed")
            if subcommand == "push" and any(
                item in {"-f", "--force", "--force-with-lease", "--mirror", "--delete"} or item.startswith("--force=")
                for item in lowered
            ):
                raise PolicyError("destructive Git push options are not allowed")
            if subcommand == "submodule" and "foreach" in lowered:
                raise PolicyError("Git submodule foreach is not allowed")
        if executable == "gh":
            subcommand = lowered[0] if lowered else ""
            if subcommand == "api":
                raise PolicyError("arbitrary GitHub API calls are not allowed")
            if subcommand == "auth" and (len(lowered) < 2 or lowered[1] != "status"):
                raise PolicyError("only GitHub auth status is allowed")
            if subcommand in {"secret", "variable"}:
                raise PolicyError("GitHub secret and variable access is not allowed")
            if len(lowered) >= 2 and (lowered[0], lowered[1]) in {
                ("repo", "delete"), ("repo", "archive"), ("repo", "rename"), ("release", "delete")
            }:
                raise PolicyError("destructive GitHub operation is not allowed")

    @staticmethod
    def _validate_hyperfine_arguments(arguments: list[str]) -> None:
        if len(arguments) == 1 and arguments[0] in {"-h", "--help", "-V", "--version"}:
            return
        value_options = {
            "-w", "--warmup", "-m", "--min-runs", "-M", "--max-runs",
            "-r", "--runs", "--style", "--sort", "-u", "--time-unit",
            "--export-asciidoc", "--export-csv", "--export-json",
            "--export-markdown", "--export-orgmode", "--output", "--input",
            "-n", "--command-name",
        }
        value_prefixes = tuple(f"{name}=" for name in value_options if name.startswith("--"))
        flag_options = {"-N", "--shell=none", "--ignore-failure", "--show-output"}
        commands: list[str] = []
        shell_disabled = False
        index = 0
        while index < len(arguments):
            item = arguments[index]
            if item in {"-N", "--shell=none"}:
                shell_disabled = True
                index += 1
            elif item in flag_options or item.startswith("--ignore-failure="):
                index += 1
            elif item in value_options:
                if index + 1 >= len(arguments):
                    raise PolicyError(f"hyperfine option {item} requires a value")
                index += 2
            elif item.startswith(value_prefixes):
                index += 1
            elif item.startswith("-"):
                raise PolicyError("hyperfine option is not allowed")
            else:
                commands.append(item)
                index += 1
        if not shell_disabled:
            raise PolicyError("hyperfine requires --shell=none")
        if not commands:
            raise PolicyError("hyperfine requires at least one benchmark command")
        allowed = {
            "actionlint", "cargo", "fd", "git", "go", "jq", "just",
            "rg", "rustc", "rustdoc", "rustfmt",
        }
        for command_text in commands:
            try:
                command = shlex.split(command_text)
            except ValueError as error:
                raise PolicyError("invalid hyperfine benchmark command") from error
            if not command or command[0] not in allowed:
                raise PolicyError("hyperfine benchmark executable is not allowed")
            WorkspaceTools._validate_exec_policy(command[0], command[1:])

    @staticmethod
    def _allowed_git_config_read(arguments: list[str]) -> bool:
        if not arguments or arguments[0].lower() != "config":
            return False
        values = [item.lower() for item in arguments[1:]]
        if not values or values[-1] not in GIT_READABLE_CONFIG_KEYS:
            return False
        options = values[:-1]
        allowed_options = {
            "--global", "--local", "--system", "--includes", "--show-origin",
            "--path", "--get", "--get-all",
        }
        if any(item not in allowed_options for item in options):
            return False
        return sum(item in {"--get", "--get-all"} for item in options) == 1

    @staticmethod
    def _pnpm_command(arguments: list[str], version: str) -> list[str]:
        return [
            "/home/linuxbrew/.linuxbrew/bin/fnm",
            "exec",
            "--using",
            version,
            "corepack",
            "pnpm",
            *arguments,
        ]

    @staticmethod
    def _validate_tool_arguments(arguments: list[str], *, allow_empty: bool = False) -> None:
        if not allow_empty and not arguments:
            raise PolicyError("command arguments cannot be empty")
        if len(arguments) > 64:
            raise PolicyError("too many command arguments")
        if any(not isinstance(item, str) or not item or "\x00" in item or len(item) > 4_096 for item in arguments):
            raise PolicyError("invalid command argument")

    def _node_version(self, cwd: Path, requested: str | None) -> str:
        if requested is not None:
            if not requested or len(requested) > 128 or any(character in requested for character in "\x00/\\"):
                raise PolicyError("invalid Node version")
            return requested
        for filename in (".node-version", ".nvmrc"):
            candidate = cwd / filename
            if candidate.is_file() and not candidate.is_symlink():
                return str(candidate)
        versions_directory = self.policy.root / ".loki" / "fnm" / "node-versions"
        installed_versions: list[tuple[tuple[int, int, int], str]] = []
        if versions_directory.is_dir():
            for candidate in versions_directory.iterdir():
                match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", candidate.name)
                if candidate.is_dir() and match is not None:
                    installed_versions.append((tuple(map(int, match.groups())), candidate.name))
        if not installed_versions:
            raise PolicyError("no FNM-managed Node version is installed")
        return max(installed_versions)[1]

    @staticmethod
    def _tool_environment() -> dict[str, str]:
        return {
            "FNM_DIR": "/workspace/.loki/fnm",
            "FNM_NODE_DIST_MIRROR": "https://nodejs.org/dist",
            "FNM_VERSION_FILE_STRATEGY": "recursive",
            "COREPACK_HOME": "/workspace/.loki/corepack",
            "COREPACK_ENABLE_DOWNLOAD_PROMPT": "0",
            "GOCACHE": "/workspace/.loki/go/build-cache",
            "GOMODCACHE": "/workspace/.loki/go/pkg/mod",
            "GOPATH": "/workspace/.loki/go",
            "GOPROXY": "https://proxy.golang.org",
            "CARGO_HOME": "/workspace/.loki/cargo",
            "RUSTUP_HOME": "/workspace/.loki/rustup",
            "CARGO_REGISTRIES_CRATES_IO_PROTOCOL": "sparse",
            "CARGO_NET_GIT_FETCH_WITH_CLI": "true",
            "CI": "1",
            "NPM_CONFIG_REGISTRY": "https://registry.npmjs.org",
            "npm_config_store_dir": "/workspace/.loki/pnpm-store",
            "HTTPS_PROXY": "http://127.0.0.1:8766",
            "HTTP_PROXY": "http://127.0.0.1:8766",
            "https_proxy": "http://127.0.0.1:8766",
            "http_proxy": "http://127.0.0.1:8766",
            "NODE_USE_ENV_PROXY": "1",
            "NO_PROXY": "127.0.0.1,localhost",
            "no_proxy": "127.0.0.1,localhost",
            "GIT_CONFIG_GLOBAL": "/etc/loki/gitconfig",
            "SSH_AUTH_SOCK": "/run/loki/signing/agent.sock",
            "PATH": "/workspace/.loki/cargo/bin:/home/linuxbrew/.linuxbrew/bin:/opt/loki-mcp/venv/bin:/usr/bin:/bin",
        }

    def start_process(self, name: str) -> dict[str, Any]:
        """Start one root-configured long-running process and return its session ID."""
        try:
            result = self.processes.start(name)
            self._audit("start_process", True, {"name": name, "session_id": result["session_id"]})
            return result
        except Exception as error:
            self._audit("start_process", False, {"name": name}, error)
            raise

    @staticmethod
    def _runtime_request(operation: str, **values: object) -> dict[str, Any]:
        # Keep this import lazy: the runtime reuses Loki's command policy while
        # the MCP process only needs the socket client.
        from .runtime import request_runtime
        return request_runtime({"operation": operation, **values})

    def list_secret_profiles(self) -> dict[str, Any]:
        """List secret profile names, key names, and registered actions without returning secret values."""
        result = self._runtime_request("list_profiles")
        self._audit("list_secret_profiles", True, {"count": len(result["profiles"])})
        return result

    def list_secret_imports(self) -> dict[str, Any]:
        """List opaque staged dotenv imports without reading or returning their contents."""
        result = self._runtime_request("list_imports")
        self._audit("list_secret_imports", True, {"count": len(result["imports"])})
        return result

    def create_secret_profile(self, profile: str) -> dict[str, Any]:
        """Create an empty secret profile; this never accepts or returns secret values."""
        result = self._runtime_request("profile_create", profile=profile)
        self._audit("create_secret_profile", True, {"profile": profile})
        return result

    def import_secret_env(self, profile: str, import_id: str) -> dict[str, Any]:
        """Consume a root-staged dotenv import into a profile without exposing its values."""
        result = self._runtime_request(
            "import_staged_env", profile=profile, import_id=import_id,
        )
        self._audit(
            "import_secret_env", True,
            {"profile": profile, "import_id": import_id, "count": result["count"]},
        )
        return result

    def set_public_secret_value(self, profile: str, secret: str, value: str) -> dict[str, Any]:
        """Store a non-sensitive configuration value supplied in the MCP request; never use this for credentials, tokens, passwords, or private keys."""
        result = self._runtime_request(
            "public_value_set", profile=profile, secret=secret, value=value,
        )
        self._audit("set_public_secret_value", True, {"profile": profile, "secret": secret})
        return result

    def remove_secret(self, profile: str, secret: str) -> dict[str, Any]:
        """Remove one named secret when no registered action explicitly references it."""
        result = self._runtime_request(
            "secret_remove", profile=profile, secret=secret,
        )
        self._audit("remove_secret", True, {"profile": profile, "secret": secret})
        return result

    def generate_secret(self, profile: str, secret: str, bytes: int = 32) -> dict[str, Any]:
        """Generate and store a random secret inside the Loki runtime without returning its value; only empty or absent names can be filled."""
        result = self._runtime_request(
            "secret_generate", profile=profile, secret=secret, bytes=bytes,
        )
        self._audit("generate_secret", True, {
            "profile": profile, "secret": secret, "bytes": bytes,
        })
        return result

    def remove_secret_profile(self, profile: str) -> dict[str, Any]:
        """Remove an entire secret profile and all of its registered actions."""
        result = self._runtime_request("profile_remove", profile=profile)
        self._audit("remove_secret_profile", True, {"profile": profile})
        return result

    def get_secret_profile(self, profile: str) -> dict[str, Any]:
        """Inspect one secret profile's metadata and fixed action policies; values are never returned."""
        result = self._runtime_request("get_profile", profile=profile)
        self._audit("get_secret_profile", True, {"profile": profile})
        return result

    def secret_status(self) -> dict[str, Any]:
        """Check whether encrypted Loki state is initialized and count its running processes."""
        result = self._runtime_request("status")
        self._audit("secret_status", True, result)
        return result

    def clear_action_materialization(self, profile: str, action: str) -> dict[str, Any]:
        """Delete only one registered action's fixed secret env target without reading or returning its contents."""
        result = self._runtime_request(
            "clear_action_materialization", profile=profile, action_name=action,
        )
        self._audit("clear_action_materialization", True, result)
        return result

    def set_action_policy(
        self,
        profile: str,
        action: str,
        cwd: str,
        command: list[str],
        secrets: list[str] | None = None,
        all_secrets: bool = False,
        required_secrets: list[str] | None = None,
        timeout_seconds: int = 3_600,
        max_output_bytes: int = 1_048_576,
        materialize_env_file: str | None = None,
        materialize_env_path: str | None = None,
        docker_access: bool = False,
        preferred_port: int | None = None,
        port_environment: str | None = None,
        origin_environment: str | None = None,
        singleton: bool = False,
        lock_probe: str | None = None,
        local_callback: bool = False,
        public_environment: list[str] | None = None,
        preview_environment: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        """Register one validated action policy through the managed MCP identity."""
        working_directory = self._command_cwd(cwd)
        relative_cwd = working_directory.relative_to(self.policy.root).as_posix() or "."
        if preferred_port is None:
            if port_environment is not None or origin_environment is not None:
                raise PolicyError("port_environment and origin_environment require preferred_port")
            dynamic_port = None
        else:
            if port_environment is None:
                raise PolicyError("port_environment is required with preferred_port")
            dynamic_port = {
                "preferred": preferred_port,
                "environment": port_environment,
                **({"origin_environment": origin_environment} if origin_environment else {}),
            }
        result = self._runtime_request(
            "action_set", profile=profile, action_name=action,
            action={
                "cwd": relative_cwd,
                "command": command,
                "secrets": secrets or [],
                "required_secrets": required_secrets or [],
                "all_secrets": all_secrets,
                "materialize_env_file": materialize_env_file,
                "materialize_env_path": materialize_env_path,
                "docker_access": docker_access,
                "dynamic_port": dynamic_port,
                "singleton": singleton,
                "lock_probe": lock_probe,
                "local_callback": local_callback,
                "public_environment": public_environment or [],
                "preview_environment": preview_environment or {},
                "timeout_seconds": timeout_seconds,
                "max_output_bytes": max_output_bytes,
            },
        )
        self._audit("set_action_policy", True, {
            "profile": profile, "action": action, "cwd": relative_cwd,
        })
        return result

    def remove_action_policy(self, profile: str, action: str) -> dict[str, Any]:
        """Remove one registered action policy."""
        result = self._runtime_request(
            "action_remove", profile=profile, action_name=action,
        )
        self._audit("remove_action_policy", True, {"profile": profile, "action": action})
        return result

    def bootstrap_project(self, cwd: str = ".", workflow: str = "development") -> dict[str, Any]:
        """Run a centrally registered project workflow for cwd. Require the managed process to exit with code 0 before declaring success."""
        result = self._runtime_request(
            "bootstrap_project", cwd=cwd, workflow=workflow,
        )
        self._audit("bootstrap_project", True, {
            "cwd": cwd, "workflow": workflow, "accepted": result.get("accepted"),
            "session_id": result.get("session_id"),
        })
        return result

    def run_action(
        self,
        profile: str,
        action: str,
        cwd: str | None = None,
        bind_local_callback: bool = False,
    ) -> dict[str, Any]:
        """Run one centrally registered action with its approved environment and secrets. bind_local_callback exposes actions marked local_callback through the fixed local callback endpoint."""
        result = self._runtime_request(
            "run_action", profile=profile, action_name=action,
            public_environment={}, cwd=cwd,
            bind_local_callback=bind_local_callback,
        )
        self._audit("run_action", True, {
            "profile": profile, "action": action, "session_id": result["session_id"],
            "cwd": cwd, "bind_local_callback": bind_local_callback,
        })
        return result

    def read_action_process(self, session_id: str, offset: int | None = None, limit: int = 65_536) -> dict[str, Any]:
        """Read redacted process output; outcome is running, succeeded, or failed, and only succeeded is completion."""
        result = self._runtime_request("read_process", session_id=session_id, offset=offset, limit=limit)
        self._audit("read_action_process", True, {"session_id": session_id})
        return result

    def list_action_processes(self) -> dict[str, Any]:
        """List active and recently completed registered action sessions without exposing secret values."""
        result = self._runtime_request("list_processes")
        self._audit("list_action_processes", True, {"count": len(result["processes"])})
        return result

    def stop_action_process(self, session_id: str) -> dict[str, Any]:
        """Stop a registered action process group."""
        result = self._runtime_request("stop_process", session_id=session_id)
        self._audit("stop_action_process", True, {"session_id": session_id})
        return result

    def secret_audit_log(self, limit: int = 50) -> dict[str, Any]:
        """Read bounded runtime audit metadata; secret values and process output are excluded."""
        result = self._runtime_request("audit", limit=limit)
        self._audit("secret_audit_log", True, {"count": len(result["records"])})
        return result

    def read_process(self, session_id: str, offset: int | None = None, limit: int = 65_536) -> dict[str, Any]:
        """Read bounded process output; avoid tight polling and do independent safe work before polling a running session again."""
        try:
            result = self.processes.read(session_id, offset, limit)
            self._audit("read_process", True, {"session_id": session_id, "bytes": len(result["output"].encode("utf-8"))})
            return result
        except Exception as error:
            self._audit("read_process", False, {"session_id": session_id}, error)
            raise

    def list_processes(self) -> dict[str, Any]:
        """List active and recently completed managed process sessions."""
        result = self.processes.list()
        self._audit("list_processes", True, {"count": len(result["processes"])})
        return result

    def stop_process(self, session_id: str) -> dict[str, Any]:
        """Stop a managed process group gracefully, then force it if necessary."""
        try:
            result = self.processes.stop(session_id)
            self._audit("stop_process", True, {"session_id": session_id})
            return result
        except Exception as error:
            self._audit("stop_process", False, {"session_id": session_id}, error)
            raise

    def _regular_file(self, path: str) -> Path:
        target = self.policy.resolve(path)
        if not target.is_file():
            raise PolicyError("path must be a regular file")
        return target

    def _git_mutate_paths(
        self,
        operation: str,
        paths: list[str],
        cwd: str,
        expected_index_sha256: str | None,
    ) -> dict[str, Any]:
        try:
            if not paths or len(paths) > self.config.max_patch_files:
                raise PolicyError("paths must contain between 1 and the configured patch file limit")
            working_directory = self._command_cwd(cwd)
            clean_paths: list[str] = []
            for requested in paths:
                target = self._resolve_git_path(
                    working_directory, requested, must_exist=operation == "stage",
                )
                clean_paths.append(target.relative_to(working_directory).as_posix())
            with self._mutation_lock:
                before = self._git_index_sha256(working_directory)
                if expected_index_sha256 is not None and before != expected_index_sha256:
                    raise PolicyError("Git index changed; inspect it again before staging")
                if operation == "stage":
                    command = ["add", "--", *clean_paths]
                elif self._git(["rev-parse", "--verify", "HEAD"], cwd=working_directory)["exit_code"] == 0:
                    command = ["restore", "--staged", "--", *clean_paths]
                else:
                    command = ["rm", "--cached", "--ignore-unmatch", "--", *clean_paths]
                applied = self._git(command, timeout=30, cwd=working_directory)
                if applied["exit_code"] != 0:
                    raise OperationError(f"git {operation} failed: {applied['output']}")
                after = self._git_index_sha256(working_directory)
            result = {
                "operation": operation, "paths": clean_paths,
                "previous_index_sha256": before, "index_sha256": after,
                "output": applied["output"],
            }
            self._audit(f"git_{operation}_paths", True, {"cwd": cwd, "paths": clean_paths})
            return result
        except Exception as error:
            self._audit(f"git_{operation}_paths", False, {"cwd": cwd, "paths": paths}, error)
            raise

    def _git_index_sha256(self, cwd: Path) -> str:
        repository = self._git(["rev-parse", "--show-toplevel"], timeout=10, cwd=cwd)
        if repository["exit_code"] != 0:
            raise PolicyError("cwd is not inside a Git repository")
        repository_root = Path(repository["output"].strip()).resolve()
        if self.policy.root not in (repository_root, *repository_root.parents):
            raise PolicyError("repository escapes workspace")
        index = subprocess.run(
            ["/usr/bin/git", "ls-files", "--stage", "-z"],
            cwd=cwd,
            env=self._environment(),
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=30,
            shell=False,
            check=False,
        )
        if index.returncode != 0:
            raise OperationError(f"unable to inspect Git index: {index.stdout.decode('utf-8', 'replace')}")
        return hashlib.sha256(index.stdout).hexdigest()

    def _resolve_git_path(self, cwd: Path, requested: str, *, must_exist: bool) -> Path:
        if not isinstance(requested, str) or not requested or "\x00" in requested or "\\" in requested:
            raise PolicyError("invalid Git path")
        base = self.policy.relative(cwd)
        combined = requested if base == "." else f"{base}/{requested}"
        target = self.policy.resolve(combined, must_exist=must_exist)
        try:
            target.relative_to(cwd)
        except ValueError as error:
            raise PolicyError("Git path must stay inside cwd") from error
        return target

    def _git(self, arguments: list[str], timeout: int = 30, cwd: Path | None = None) -> dict[str, Any]:
        return self._run(["/usr/bin/git", *arguments], timeout=timeout, cwd=cwd)

    def _run_spec(self, spec: CommandSpec) -> dict[str, Any]:
        cwd = self.policy.resolve(spec.cwd)
        if not cwd.is_dir():
            raise PolicyError("command cwd must be a directory")
        return self._run(
            list(spec.command),
            timeout=spec.timeout_seconds,
            cwd=cwd,
            environment=spec.environment,
            max_output_bytes=spec.max_output_bytes,
        )

    @staticmethod
    def _port_guard(operation: str, port: int) -> dict[str, Any]:
        try:
            port_number = int(port)
        except (TypeError, ValueError) as error:
            raise PolicyError("port must be an integer") from error
        if not 1024 <= port_number <= 65_535:
            raise PolicyError("port must be between 1024 and 65535")
        if operation not in {"inspect", "stop"}:
            raise PolicyError("unsupported port operation")

        request = json.dumps({"operation": operation, "port": port_number}, separators=(",", ":")).encode() + b"\n"
        response = bytearray()
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
                client.settimeout(15)
                client.connect(PORT_GUARD_SOCKET)
                client.sendall(request)
                while len(response) <= 1_048_576:
                    chunk = client.recv(65_536)
                    if not chunk:
                        break
                    response.extend(chunk)
                    if b"\n" in chunk:
                        break
        except OSError as error:
            raise PolicyError("port control service is unavailable") from error
        if len(response) > 1_048_576:
            raise PolicyError("port control response is too large")
        try:
            result = json.loads(bytes(response).split(b"\n", 1)[0])
        except (json.JSONDecodeError, UnicodeDecodeError) as error:
            raise PolicyError("invalid port control response") from error
        if not isinstance(result, dict):
            raise PolicyError("invalid port control response")
        if not result.get("ok"):
            raise PolicyError(str(result.get("error", "port control failed")))
        payload = result.get("result")
        if not isinstance(payload, dict):
            raise PolicyError("invalid port control response")
        return payload

    def _browser_tool(
        self,
        tool: str,
        operation: str,
        arguments: dict[str, Any],
        *,
        audit: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        try:
            result = self._browser_rpc(operation, arguments)
            self._audit(tool, True, audit or {})
            return result
        except Exception as error:
            self._audit(tool, False, audit or {}, error)
            raise

    @staticmethod
    def _browser_rpc(operation: str, arguments: dict[str, Any]) -> dict[str, Any]:
        request = json.dumps(
            {"operation": operation, "arguments": arguments},
            separators=(",", ":"),
        ).encode("utf-8") + b"\n"
        if len(request) > 1_048_576:
            raise PolicyError("browser request exceeds limit")
        response = bytearray()
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
                client.settimeout(100)
                client.connect(BROWSER_SOCKET)
                client.sendall(request)
                while len(response) <= 16_777_216:
                    chunk = client.recv(65_536)
                    if not chunk:
                        break
                    response.extend(chunk)
                    if b"\n" in chunk:
                        break
        except OSError as error:
            raise PolicyError("browser service is unavailable") from error
        if len(response) > 16_777_216:
            raise PolicyError("browser response exceeds limit")
        try:
            envelope = json.loads(bytes(response).split(b"\n", 1)[0])
        except (json.JSONDecodeError, UnicodeDecodeError) as error:
            raise PolicyError("invalid browser service response") from error
        if not isinstance(envelope, dict):
            raise PolicyError("invalid browser service response")
        if not envelope.get("ok"):
            raise OperationError(str(envelope.get("error", "browser operation failed")))
        result = envelope.get("result")
        if not isinstance(result, dict):
            raise PolicyError("invalid browser service response")
        return result

    def _run(
        self,
        command: list[str],
        timeout: int,
        cwd: Path | None = None,
        environment: dict[str, str] | None = None,
        max_output_bytes: int | None = None,
    ) -> dict[str, Any]:
        try:
            completed = subprocess.run(
                command,
                cwd=cwd or self.policy.root,
                env=self._environment(environment),
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                encoding="utf-8",
                errors="replace",
                timeout=timeout,
                shell=False,
                check=False,
            )
            output, truncated = self._truncate(completed.stdout, max_output_bytes)
            return {"exit_code": completed.returncode, "output": output, "truncated": truncated}
        except subprocess.TimeoutExpired as error:
            output = error.stdout or ""
            if isinstance(output, bytes):
                output = output.decode("utf-8", "replace")
            output, truncated = self._truncate(output, max_output_bytes)
            return {"exit_code": 124, "output": output, "truncated": truncated, "timed_out": True}

    def _run_input(
        self,
        command: list[str],
        content: bytes,
        timeout: int,
        cwd: Path | None = None,
    ) -> dict[str, Any]:
        completed = subprocess.run(
            command,
            cwd=cwd or self.policy.root,
            env=self._environment(),
            input=content,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=timeout,
            shell=False,
            check=False,
        )
        raw_output = completed.stdout
        output = raw_output[:self.config.max_output_bytes].decode("utf-8", "replace")
        return {
            "exit_code": completed.returncode,
            "output": output,
            "raw_output": raw_output,
            "truncated": len(raw_output) > self.config.max_output_bytes,
        }

    def _parse_numstat(self, output: bytes) -> list[dict[str, Any]]:
        files: list[dict[str, Any]] = []
        for record in output.split(b"\x00"):
            if not record:
                continue
            fields = record.split(b"\t", 2)
            if len(fields) != 3:
                raise ValueError("unable to parse patch file list")
            added_raw, deleted_raw, path_raw = fields
            if added_raw == b"-" or deleted_raw == b"-":
                raise PolicyError("binary patches are not allowed")
            try:
                path = path_raw.decode("utf-8")
            except UnicodeDecodeError as error:
                raise PolicyError("patch paths must be UTF-8") from error
            files.append({"path": path, "added": int(added_raw), "deleted": int(deleted_raw)})
        return files

    def _truncate(self, output: str, limit: int | None = None) -> tuple[str, bool]:
        maximum = limit or self.config.max_output_bytes
        encoded = output.encode("utf-8")
        if len(encoded) <= maximum:
            return output, False
        clipped = encoded[:maximum].decode("utf-8", "ignore")
        return clipped, True

    @staticmethod
    def _environment(extra: dict[str, str] | None = None) -> dict[str, str]:
        environment = {
            "HOME": "/home/runner",
            "GH_CONFIG_DIR": "/home/runner/.config/gh",
            "TMPDIR": "/tmp",
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "PATH": "/opt/loki-mcp/venv/bin:/usr/bin:/bin",
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_OPTIONAL_LOCKS": "0",
        }
        if extra:
            environment.update(extra)
        return environment

    def _audit(self, tool: str, success: bool, metadata: dict[str, Any], error: Exception | None = None) -> None:
        record: dict[str, Any] = {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "tool": tool,
            "success": success,
            "metadata": metadata,
        }
        if error is not None:
            record["error"] = type(error).__name__
        self.config.audit_log.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        descriptor = os.open(self.config.audit_log, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
        try:
            os.write(descriptor, (json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n").encode("utf-8"))
        finally:
            os.close(descriptor)
