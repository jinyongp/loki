from pathlib import Path
import subprocess
import sys
import time

import pytest
from mcp.server.mcpserver.exceptions import ToolError

import loki_mcp.skills as skills_module
from loki_mcp.artifacts import ArtifactStore
from loki_mcp.config import CommandSpec, ServerConfig
from loki_mcp.policy import PolicyError
from loki_mcp.previews import PreviewStore
from loki_mcp.tools import WorkspaceTools


def command_spec(command: list[str], *, timeout: int = 30, output: int = 65_536) -> CommandSpec:
    return CommandSpec(tuple(command), ".", timeout, output, {})


def make_tools(
    tmp_path: Path,
    *,
    checks: dict[str, CommandSpec] | None = None,
    processes: dict[str, CommandSpec] | None = None,
    artifact_store: ArtifactStore | None = None,
    preview_store: PreviewStore | None = None,
) -> WorkspaceTools:
    return WorkspaceTools(ServerConfig(
        root=tmp_path,
        audit_log=tmp_path / "audit.jsonl",
        host="127.0.0.1",
        port=8765,
        public_hosts=(),
        cloudflare_access_team_domain=None,
        cloudflare_access_audience=None,
        max_file_bytes=1024 * 1024,
        max_write_bytes=1024 * 1024,
        max_output_bytes=64 * 1024,
        max_list_entries=100,
        max_search_results=20,
        max_read_lines=100,
        max_patch_bytes=64 * 1024,
        max_patch_files=10,
        max_processes=2,
        process_retention_seconds=60,
        checks=checks or {},
        processes=processes or {},
    ), artifact_store=artifact_store, preview_store=preview_store)


def test_write_chunk_read_search_replace_move(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    tools.write_file("docs/note.txt", "alpha\nbeta\ngamma\n")
    chunk = tools.read_file("docs/note.txt", offset=1, limit=1)
    assert chunk["content"] == "beta\n"
    assert chunk["next_offset"] == 2
    assert chunk["eof"] is False
    match = tools.search_text("gamma")["matches"][0]
    assert match["line"] == 3
    before = tools.read_file("docs/note.txt")
    replaced = tools.replace_text("docs/note.txt", "beta", "delta", before["sha256"])
    revisions = tools.list_file_revisions("docs/note.txt")["revisions"]
    assert revisions[0]["revision"] == replaced["previous_revision"]
    assert "-beta" in tools.show_file_revision_diff("docs/note.txt", replaced["previous_revision"])["diff"]
    tools.move_path("docs/note.txt", "docs/moved.txt")
    assert (tmp_path / "docs/moved.txt").read_text(encoding="utf-8") == "alpha\ndelta\ngamma\n"
    tools.write_file("docs/alias.txt", "alias\n")
    assert (tmp_path / "docs/alias.txt").read_text(encoding="utf-8") == "alias\n"


def test_write_file_never_overwrites_existing_file(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    tools.write_file("dirty.txt", "user change\n")
    with pytest.raises(FileExistsError):
        tools.write_file("dirty.txt", "agent replacement\n")
    assert (tmp_path / "dirty.txt").read_text(encoding="utf-8") == "user change\n"


def test_replace_and_restore_preserve_dirty_file_exactly(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    target = tmp_path / "dirty.sh"
    target.write_text("#!/bin/sh\necho user-dirty\n", encoding="utf-8")
    target.chmod(0o755)
    before = tools.read_file("dirty.sh")
    changed = tools.replace_text("dirty.sh", "user-dirty", "agent-change", before["sha256"])
    assert target.stat().st_mode & 0o777 == 0o755
    with pytest.raises(PolicyError, match="changed since"):
        tools.replace_text("dirty.sh", "agent-change", "stale", before["sha256"])
    current = tools.read_file("dirty.sh")
    restored = tools.restore_file_revision("dirty.sh", changed["previous_revision"], current["sha256"])
    assert target.read_bytes() == b"#!/bin/sh\necho user-dirty\n"
    assert target.stat().st_mode & 0o777 == 0o755
    assert restored["undo_revision"]


def test_apply_patch_captures_preimage_and_move_can_restore_missing_source(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    tools.write_file("dirty.txt", "before\n")
    applied = tools.apply_patch("""--- a/dirty.txt
+++ b/dirty.txt
@@ -1 +1 @@
-before
+after
""")
    revision = applied["previous_revisions"]["dirty.txt"]
    assert (tmp_path / "dirty.txt").read_text(encoding="utf-8") == "after\n"
    moved = tools.move_path("dirty.txt", "moved.txt")
    assert moved["previous_revision"]
    restored = tools.restore_file_revision("dirty.txt", revision, "missing")
    assert restored["undo_revision"] is None
    assert (tmp_path / "dirty.txt").read_text(encoding="utf-8") == "before\n"


def test_server_info_and_diagnostics_are_non_sensitive(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    tools.set_tool_catalog(["server_info", "diagnostics", "read_file"])
    info = tools.server_info()
    assert info["schema_revision"] == "2026-09-04.1"
    assert info["tool_catalog"]["revision"] == "2026-09-04.4"
    assert info["tool_catalog"]["count"] == 3
    assert info["tool_catalog"]["tools"] == ["server_info", "diagnostics", "read_file"]
    assert len(info["tool_catalog"]["sha256"]) == 64
    assert info["capabilities"]["agent_skills"]["dynamic_catalog"] is True
    assert info["capabilities"]["agent_skills"]["precedence"] == [
        "project", "shared", "builtin",
    ]
    assert info["capabilities"]["git_partial_staging"] is True
    assert info["capabilities"]["file_revisions"] is True
    assert info["capabilities"]["secret_management"] == {
        "opaque_staged_imports": True,
        "agent_profile_lifecycle": True,
        "direct_value_access": False,
        "action_registration": "root-only",
    }
    assert info["capabilities"]["signed_git_commits"] is True
    assert info["capabilities"]["browser_tool_catalog"]["count"] == 6
    assert "browser_observe" in info["capabilities"]["browser_tool_catalog"]["tools"]
    assert info["name"] == "loki"
    diagnostics = tools.diagnostics()
    assert diagnostics["workspace"]["writable"] is True
    assert set(diagnostics["git_signing"]) == {
        "identity_configured", "format", "commit_signing_required",
        "public_key_available", "agent_socket_available",
    }
    assert diagnostics["browser"]["expected_tool_count"] == 6
    assert diagnostics["browser"]["catalog_revision"] == "2026-09-03.1"
    assert diagnostics["tool_catalog"] == info["tool_catalog"]
    assert "token" not in str(diagnostics).lower()


def test_tool_catalog_rejects_duplicate_names(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    with pytest.raises(ValueError, match="duplicate"):
        tools.set_tool_catalog(["server_info", "server_info"])


def test_diagnostics_skips_unreadable_repository_candidates(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = make_tools(tmp_path)
    cache = tmp_path / ".pnpm-store"
    cache.mkdir()
    original_exists = Path.exists

    def guarded_exists(path: Path) -> bool:
        if path == cache / ".git":
            raise PermissionError("intentionally unreadable")
        return original_exists(path)

    monkeypatch.setattr(Path, "exists", guarded_exists)
    diagnostics = tools.diagnostics()
    assert diagnostics["repositories"] == []


def test_read_rejects_binary(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    (tmp_path / "binary.bin").write_bytes(b"abc\x00def")
    with pytest.raises(PolicyError):
        tools.read_file("binary.bin")


def test_read_image_accepts_supported_signature_and_rejects_mismatch(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    target = tmp_path / "result.png"
    target.write_bytes(b"\x89PNG\r\n\x1a\n" + b"data")
    result = tools.read_image("result.png")
    image = result.content[0]
    assert image.mime_type == "image/png"
    assert image.data.startswith("iVBORw0KGgo"[:10])
    assert result.structured_content == {
        "path": "result.png",
        "mime_type": "image/png",
        "bytes": 12,
        "sha256": "0a8658df1c970c052938fa0e8fe369a6351fb74b51b9f399a72397ed8c94b3ba",
    }

    (tmp_path / "fake.png").write_bytes(b"not an image")
    with pytest.raises(PolicyError):
        tools.read_image("fake.png")


def test_share_image_returns_widget_data_without_file_attachment(tmp_path: Path) -> None:
    store = ArtifactStore("https://mcp.example.com/artifacts", {"mcp.example.com"})
    tools = make_tools(tmp_path, artifact_store=store)
    target = tmp_path / "result.png"
    target.write_bytes(b"\x89PNG\r\n\x1a\n" + b"data")
    result = tools.share_image("result.png", ttl_seconds=120)
    assert result.structured_content["url"].startswith("https://mcp.example.com/artifacts/")
    assert result.structured_content["mime_type"] == "image/png"
    assert result.structured_content["display_markdown"] == (
        f"![Loki image]({result.structured_content['url']})"
    )
    assert result.content[0].type == "text"
    assert len(result.content) == 1

    with pytest.raises(PolicyError):
        tools.share_image("result.png", ttl_seconds=59)


def test_share_file_bundle_list_and_revoke(tmp_path: Path) -> None:
    import zipfile

    store = ArtifactStore("https://mcp.example.com/artifacts", {"mcp.example.com"})
    tools = make_tools(tmp_path, artifact_store=store)
    (tmp_path / "docs").mkdir()
    (tmp_path / "docs/a.txt").write_text("alpha\n", encoding="utf-8")
    (tmp_path / "docs/b.txt").write_text("beta\n", encoding="utf-8")
    shared = tools.share_file("docs/a.txt", ttl_seconds=120)
    assert shared.structured_content["filename"] == "a.txt"
    assert shared.content[1].type == "resource_link"
    assert shared.content[1].mime_type == "text/plain"

    bundled = tools.workspace_bundle(["docs"], filename="docs.zip", ttl_seconds=120)
    bundle_data = next(item.data for item in store._items.values() if item.filename == "docs.zip")
    with zipfile.ZipFile(__import__("io").BytesIO(bundle_data)) as archive:
        assert archive.namelist() == ["docs/a.txt", "docs/b.txt"]
    assert bundled.structured_content["file_count"] == 2
    listed = tools.list_shared_files()
    assert len(listed["artifacts"]) == 2
    share_id = shared.structured_content["share_id"]
    assert tools.revoke_shared_file(share_id)["revoked"] is True
    assert len(tools.list_shared_files()["artifacts"]) == 1


def test_workspace_bundle_excludes_denied_nested_files(tmp_path: Path) -> None:
    store = ArtifactStore("https://mcp.example.com/artifacts", {"mcp.example.com"})
    tools = make_tools(tmp_path, artifact_store=store)
    (tmp_path / "project/.git").mkdir(parents=True)
    (tmp_path / "project/.git/config").write_text("secret-ish", encoding="utf-8")
    (tmp_path / "project/ok.txt").write_text("ok", encoding="utf-8")
    result = tools.workspace_bundle(["project"])
    assert result.structured_content["file_count"] == 1
    assert result.structured_content["excluded_entries"] == 1


def test_write_image_saves_valid_base64_and_rejects_mismatch(tmp_path: Path) -> None:
    import base64

    tools = make_tools(tmp_path)
    content = b"\x89PNG\r\n\x1a\n" + b"generated"
    result = tools.write_image("art/result.png", base64.b64encode(content).decode(), "image/png")
    assert result["bytes"] == len(content)
    assert (tmp_path / "art/result.png").read_bytes() == content
    with pytest.raises(PolicyError):
        tools.write_image("art/fake.jpg", base64.b64encode(content).decode(), "image/jpeg")


def test_apply_patch_modifies_text_and_rejects_delete(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    (tmp_path / "note.txt").write_text("alpha\nbeta\n", encoding="utf-8")
    patch = """diff --git a/note.txt b/note.txt
--- a/note.txt
+++ b/note.txt
@@ -1,2 +1,2 @@
 alpha
-beta
+gamma
"""
    result = tools.apply_patch(patch)
    assert result["files"] == [{"path": "note.txt", "added": 1, "deleted": 1}]
    assert (tmp_path / "note.txt").read_text(encoding="utf-8") == "alpha\ngamma\n"

    delete_patch = """diff --git a/note.txt b/note.txt
deleted file mode 100644
--- a/note.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-alpha
-gamma
"""
    with pytest.raises(PolicyError):
        tools.apply_patch(delete_patch)


def test_npm_fnm_and_just_use_allowlisted_tools(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = make_tools(tmp_path)
    (tmp_path / ".loki/fnm/node-versions/v24.20.0").mkdir(parents=True)
    captured: list[list[str]] = []

    def fake_run(command: list[str], **kwargs: object) -> dict[str, object]:
        captured.append(command)
        return {"exit_code": 0, "output": "ok", "truncated": False}

    monkeypatch.setattr(tools, "_run", fake_run)
    assert tools.npm(["ci"])["exit_code"] == 0
    assert captured[-1][-2:] == ["npm", "ci"]
    assert tools.fnm(["list"])["exit_code"] == 0
    assert captured[-1][-1] == "list"
    assert tools.just(["test"])["exit_code"] == 0
    assert captured[-1][-2:] == ["/home/linuxbrew/.linuxbrew/bin/just", "test"]

    with pytest.raises(PolicyError):
        tools.npm(["publish"])
    with pytest.raises(PolicyError):
        tools.fnm(["completions"])


def test_run_pnpm_passes_native_argv_through_corepack(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = make_tools(tmp_path)
    (tmp_path / ".loki/fnm/node-versions/v24.20.0").mkdir(parents=True)
    captured: list[list[str]] = []

    def fake_run(command: list[str], **kwargs: object) -> dict[str, object]:
        captured.append(command)
        return {"exit_code": 0, "output": "11.24.0", "truncated": False}

    monkeypatch.setattr(tools, "_run", fake_run)
    result = tools.run_pnpm(["-C", "docs", "--filter", "@sectile/core", "test"])
    assert result["exit_code"] == 0
    assert captured[-1][-6:] == [
        "pnpm",
        "-C",
        "docs",
        "--filter",
        "@sectile/core",
        "test",
    ]


def test_start_pnpm_passes_native_argv_and_returns_session(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = make_tools(tmp_path)
    (tmp_path / ".loki/fnm/node-versions/v24.20.0").mkdir(parents=True)
    captured: dict[str, object] = {}

    def fake_start_command(**kwargs: object) -> dict[str, object]:
        captured.update(kwargs)
        return {"session_id": "session", "status": "running"}

    monkeypatch.setattr(tools.processes, "start_command", fake_start_command)
    result = tools.start_pnpm(["-C", "docs", "--filter", "app", "dev"])
    assert result["session_id"] == "session"
    assert captured["command"][-6:] == ["pnpm", "-C", "docs", "--filter", "app", "dev"]


def test_browser_tools_use_structured_sidecar_rpc(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = make_tools(tmp_path)
    calls: list[tuple[str, dict[str, object]]] = []

    def fake_rpc(operation: str, arguments: dict[str, object]) -> dict[str, object]:
        calls.append((operation, arguments))
        if operation == "state":
            return {"url": "https://example.com", "interactive_elements": []}
        return {"status": "ok"}

    monkeypatch.setattr(tools, "_browser_rpc", fake_rpc)
    assert tools.browser_start()["status"] == "ok"
    assert tools.browser_navigate("https://example.com", new_tab=True)["status"] == "ok"
    assert tools.browser_state()["url"] == "https://example.com"
    assert tools.browser_console(level="error", since_sequence=2, limit=5)["status"] == "ok"
    assert tools.browser_network(status_min=400, failed_only=True, resource_type="Script", limit=5)["status"] == "ok"
    assert tools.browser_request("request-1", include_body=True, max_body_chars=123)["status"] == "ok"
    assert tools.browser_websockets(since_sequence=3, limit=5)["status"] == "ok"
    assert tools.browser_page_errors(since_sequence=4, limit=5)["status"] == "ok"
    assert tools.browser_diagnostics(since_sequence=5, limit=5)["status"] == "ok"
    assert tools.browser_click(index=3)["status"] == "ok"
    assert tools.browser_type(4, "private text")["status"] == "ok"
    assert tools.browser_press("Enter")["status"] == "ok"
    assert tools.browser_scroll("down", 800)["status"] == "ok"
    assert tools.browser_back()["status"] == "ok"
    assert tools.browser_list_tabs()["status"] == "ok"
    assert tools.browser_switch_tab("abcd")["status"] == "ok"
    assert tools.browser_close_tab("abcd")["status"] == "ok"
    assert tools.browser_stop()["status"] == "ok"
    assert calls[1] == ("navigate", {"url": "https://example.com", "new_tab": True})
    assert calls[3] == ("console", {"level": "error", "since_sequence": 2, "limit": 5})
    assert calls[5] == (
        "request",
        {"request_id": "request-1", "include_body": True, "max_body_chars": 123},
    )
    audit = (tmp_path / "audit.jsonl").read_text(encoding="utf-8")
    assert "private text" not in audit


def test_browser_screenshot_validates_png(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    import base64

    tools = make_tools(tmp_path)
    png = b"\x89PNG\r\n\x1a\ncontent"
    monkeypatch.setattr(
        tools,
        "_browser_rpc",
        lambda operation, arguments: {"data_base64": base64.b64encode(png).decode("ascii")},
    )
    result = tools.browser_screenshot()
    assert result.to_image_content().mime_type == "image/png"

    monkeypatch.setattr(tools, "_browser_rpc", lambda operation, arguments: {"data_base64": "bm90LXBuZw=="})
    with pytest.raises(PolicyError):
        tools.browser_screenshot()


def test_browser_save_screenshot_writes_png_and_protects_existing_file(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import base64

    tools = make_tools(tmp_path)
    png = b"\x89PNG\r\n\x1a\ncontent"
    monkeypatch.setattr(
        tools,
        "_browser_rpc",
        lambda operation, arguments: {"data_base64": base64.b64encode(png).decode("ascii")},
    )
    result = tools.browser_save_screenshot("artifacts/page.png", full_page=True)
    assert result["path"] == "artifacts/page.png"
    assert result["mime_type"] == "image/png"
    assert result["full_page"] is True
    assert (tmp_path / "artifacts/page.png").read_bytes() == png

    with pytest.raises(FileExistsError):
        tools.browser_save_screenshot("artifacts/page.png")
    tools.browser_save_screenshot(
        "artifacts/page.png", overwrite=True, expected_sha256=result["sha256"],
    )

    with pytest.raises(PolicyError):
        tools.browser_save_screenshot("artifacts/page.jpg")


def test_browser_share_screenshot_publishes_exact_capture_without_attachment(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    import base64

    store = ArtifactStore("https://mcp.example.com/artifacts", {"mcp.example.com"})
    tools = make_tools(tmp_path, artifact_store=store)
    png = b"\x89PNG\r\n\x1a\ncurrent-tab"
    monkeypatch.setattr(
        tools,
        "_browser_rpc",
        lambda operation, arguments: {"data_base64": base64.b64encode(png).decode("ascii")},
    )
    result = tools.browser_share_screenshot(full_page=True, ttl_seconds=120)
    assert result.structured_content["path"] == "browser://active-tab"
    assert result.structured_content["full_page"] is True
    assert result.structured_content["sha256"] == __import__("hashlib").sha256(png).hexdigest()
    assert result.structured_content["url"].startswith("https://mcp.example.com/artifacts/")
    assert len(result.content) == 1
    assert result.content[0].type == "text"


def test_share_dev_server_requires_owned_port_and_can_be_revoked(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    store = PreviewStore("preview.example.com")
    tools = make_tools(tmp_path, preview_store=store)
    monkeypatch.setattr(tools, "_port_guard", lambda operation, port: {
        "port": port,
        "in_use": True,
        "listeners": [{"pid": 12, "cwd": "/workspace/app", "command": "node"}],
    })
    shared = tools.share_dev_server(5173, ttl_seconds=120)
    assert shared["url"].endswith(".preview.example.com")
    assert shared["port"] == 5173
    assert tools.list_shared_servers()["previews"][0]["share_id"] == shared["share_id"]
    stopped = tools.stop_shared_server(str(shared["share_id"]))
    assert stopped == {"revoked": True, "share_id": shared["share_id"], "port": 5173}
    assert tools.list_shared_servers()["previews"] == []

    day_long = tools.share_dev_server(5173, ttl_seconds=86_400)
    assert day_long["expires_at"]
    tools.stop_shared_server(str(day_long["share_id"]))

    with pytest.raises(PolicyError, match="between 60 and 86400 seconds"):
        tools.share_dev_server(5173, ttl_seconds=86_401)

    monkeypatch.setattr(tools, "_port_guard", lambda operation, port: {
        "port": port, "in_use": False, "listeners": [],
    })
    with pytest.raises(PolicyError):
        tools.share_dev_server(5173)


def test_preview_port_falls_back_to_trusted_docker_compose(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    tools = make_tools(tmp_path)
    monkeypatch.setattr(
        tools, "_port_guard",
        lambda operation, port: (_ for _ in ()).throw(PolicyError("unowned listener")),
    )
    monkeypatch.setattr(
        tools, "_runtime_request",
        lambda operation, **values: {
            "port": values["port"],
            "in_use": True,
            "listeners": [{
                "container_id": "a" * 12,
                "cwd": "/workspace",
                "command": "docker-compose:example/api",
                "local_address": f"127.0.0.1:{values['port']}",
            }],
        },
    )
    assert tools.port_info(41280)["listeners"][0]["command"] == "docker-compose:example/api"
    assert tools.preview_port_allowed(41280) is True


def test_share_dev_stack_accepts_arbitrary_routes(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    store = PreviewStore("streamliner.im")
    tools = make_tools(tmp_path, preview_store=store)
    monkeypatch.setattr(tools, "_port_guard", lambda operation, port: {
        "port": port,
        "in_use": True,
        "listeners": [{"pid": port, "cwd": "/workspace/app", "command": "node"}],
    })

    shared = tools.share_dev_stack({
        "/": 42100, "/_loki/backend": 41280, "/_loki/events": 41281,
    }, ttl_seconds=86_400)
    assert shared["routes"] == {
        "/": 42100, "/_loki/backend": 41280, "/_loki/events": 41281,
    }

def test_run_preview_action_uses_prepared_port_and_injects_environment(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    store = PreviewStore("streamliner.im")
    tools = make_tools(tmp_path, preview_store=store)
    monkeypatch.setattr(tools, "_inspect_preview_port", lambda port: {
        "port": port, "in_use": True,
        "listeners": [{"cwd": "/workspace/api", "command": "api"}],
    })
    calls: list[dict[str, object]] = []

    def request(operation: str, **values: object) -> dict[str, object]:
        calls.append({"operation": operation, **values})
        if operation == "prepare_action":
            return {
                "launch_token": "a" * 32, "port": 50331,
                "local_url": "http://127.0.0.1:50331",
            }
        return {"session_id": "preview-session", "status": "running", "output": ""}

    monkeypatch.setattr(tools, "_runtime_request", request)
    result = tools.run_preview_action(
        "sample-local", "serve",
        {"/_loki/backend": 41280, "/_loki/events": 41281},
        {
            "PUBLIC_BACKEND_URL": "/_loki/backend",
            "PUBLIC_EVENTS_URL": "/_loki/events",
        },
        300, "worktrees/sample-a",
    )
    assert result["session_id"] == "preview-session"
    assert result["routes"] == {
        "/": 50331, "/_loki/backend": 41280, "/_loki/events": 41281,
    }
    assert calls[0] == {
        "operation": "prepare_action", "profile": "sample-local",
        "action_name": "serve", "cwd": "worktrees/sample-a",
    }
    assert calls[1]["launch_token"] == "a" * 32
    assert calls[1]["cwd"] == "worktrees/sample-a"
    assert calls[1]["public_environment"] == {
        "PUBLIC_BACKEND_URL": f"{result['url']}/_loki/backend",
        "PUBLIC_EVENTS_URL": f"{result['url']}/_loki/events",
    }


def test_bootstrap_project_uses_registered_runtime_workflow(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    tools = make_tools(tmp_path)
    captured: dict[str, object] = {}

    def request(operation: str, **values: object) -> dict[str, object]:
        captured.update({"operation": operation, **values})
        return {
            "cwd": values["cwd"], "workflow": values["workflow"], "accepted": True,
            "status": "running", "session_id": "bootstrap-session",
        }

    monkeypatch.setattr(tools, "_runtime_request", request)
    result = tools.bootstrap_project("sample", "isolated")
    assert result["session_id"] == "bootstrap-session"
    assert captured == {
        "operation": "bootstrap_project", "cwd": "sample",
        "workflow": "isolated",
    }


def test_exec_command_uses_argv_and_blocks_destructive_forms(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    assert tools.exec_command("pwd")["output"] == f"{tmp_path}\n"
    assert tools.exec_command("pwd", cwd="/workspace")["output"] == f"{tmp_path}\n"
    assert tools.exec_command("git", ["status", "--short"])["exit_code"] != 0

    for executable, arguments in (
        ("rm", ["-rf", "."]),
        ("git", ["clean", "-fdx"]),
        ("git", ["reset", "--hard"]),
        ("git", ["push", "--force"]),
        ("git", ["commit", "--no-gpg-sign", "-m", "unsigned"]),
        ("git", ["tag", "--no-sign", "v1.0.0"]),
        ("git", ["commit-tree", "HEAD^{tree}"]),
        ("git", ["fast-import"]),
        ("git", ["mktag"]),
        ("gh", ["auth", "token"]),
        ("gh", ["api", "/user"]),
        ("node", ["-e", "process.exit()"]),
        ("python", ["-c", "print('unsafe')"]),
        ("find", [".", "-delete"]),
        ("fd", [".", "--exec", "sh", "-c", "echo unsafe"]),
        ("actionlint", ["-shellcheck", "sh"]),
        ("hyperfine", ["rm -rf ."]),
        ("hyperfine", ["--shell=none", "rm -rf ."]),
        ("task", ["sync"]),
        ("task", ["synchronize"]),
        ("task", ["execute", "echo", "unsafe"]),
        ("task", ["rc.hooks=on", "list"]),
    ):
        with pytest.raises(PolicyError):
            tools.exec_command(executable, arguments)

    tools._validate_exec_policy("hyperfine", ["--shell=none", "fd --type file ."])
    tools._validate_exec_policy("hyperfine", ["--version"])
    tools._validate_exec_policy("task", ["project:test", "+PENDING", "list"])


def test_actions_up_runs_through_fnm(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    (tmp_path / ".loki/fnm/node-versions/v24.20.0").mkdir(parents=True)
    command, version = tools._command_argv("actions-up", ["--dry-run"], tmp_path, None)
    assert version == "v24.20.0"
    assert command[-2:] == ["/home/linuxbrew/.linuxbrew/bin/actions-up", "--dry-run"]


def test_exec_command_checkpoints_dirty_repository(tmp_path: Path) -> None:
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    subprocess.run(["git", "-C", str(tmp_path), "config", "user.name", "Loki Test"], check=True)
    subprocess.run(["git", "-C", str(tmp_path), "config", "user.email", "loki@example.invalid"], check=True)
    target = tmp_path / "tracked.txt"
    target.write_text("before\n", encoding="utf-8")
    subprocess.run(["git", "-C", str(tmp_path), "add", "tracked.txt"], check=True)
    subprocess.run(["git", "-C", str(tmp_path), "commit", "-qm", "initial"], check=True)
    target.write_text("after\n", encoding="utf-8")
    (tmp_path / "untracked.txt").write_text("new\n", encoding="utf-8")

    tools = make_tools(tmp_path)
    result = tools.exec_command("pwd")
    assert result["checkpoint"] is not None
    checkpoint = tmp_path / "checkpoints" / f"{result['checkpoint']}.patch"
    assert b"-before" in checkpoint.read_bytes()
    assert b"+after" in checkpoint.read_bytes()


def test_exec_command_checkpoints_unborn_repository(tmp_path: Path) -> None:
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    target = tmp_path / "first.txt"
    target.write_text("first\n", encoding="utf-8")
    subprocess.run(["git", "-C", str(tmp_path), "add", "first.txt"], check=True)

    tools = make_tools(tmp_path)
    result = tools.exec_command("pwd")
    checkpoint = tmp_path / "checkpoints" / f"{result['checkpoint']}.patch"
    assert b"+first" in checkpoint.read_bytes()


def test_apply_patch_rejects_escape_and_symlink(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    escape_patch = """diff --git a/../escape.txt b/../escape.txt
new file mode 100644
--- /dev/null
+++ b/../escape.txt
@@ -0,0 +1 @@
+escape
"""
    with pytest.raises((PolicyError, ValueError)):
        tools.apply_patch(escape_patch)

    symlink_patch = """diff --git a/link b/link
new file mode 120000
--- /dev/null
+++ b/link
@@ -0,0 +1 @@
+/etc/passwd
"""
    with pytest.raises(PolicyError):
        tools.apply_patch(symlink_patch)


def test_remove_requires_git_tracking(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    tools.write_file("untracked.txt", "data")
    with pytest.raises(PolicyError, match=r"\.tmp/loki-quarantine/"):
        tools.remove_tracked_file("untracked.txt")
    assert (tmp_path / "untracked.txt").exists()


def test_remove_allows_git_tracked_file(tmp_path: Path) -> None:
    subprocess.run(["git", "init", "-q", str(tmp_path)], check=True)
    target = tmp_path / "tracked.txt"
    target.write_text("data", encoding="utf-8")
    subprocess.run(["git", "-C", str(tmp_path), "add", "tracked.txt"], check=True)
    tools = make_tools(tmp_path)
    assert tools.remove_tracked_file("tracked.txt")["removed"] is True
    assert not target.exists()


def test_git_tools_support_nested_repository_cwd(tmp_path: Path) -> None:
    repository = tmp_path / "project"
    repository.mkdir()
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    target = repository / "tracked.txt"
    target.write_text("before\n", encoding="utf-8")
    subprocess.run(["git", "-C", str(repository), "add", "tracked.txt"], check=True)
    subprocess.run(["git", "-C", str(repository), "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial"], check=True)
    target.write_text("after\n", encoding="utf-8")
    tools = make_tools(tmp_path)
    assert "tracked.txt" in tools.git_status(cwd="project")["output"]
    assert "+after" in tools.git_diff(cwd="project", path="tracked.txt")["output"]
    viewed = tools.view_git_diff(cwd="project", path="tracked.txt")
    assert viewed.structured_content["kind"] == "diff"
    assert viewed.structured_content["stats"]["additions"] == 1


def test_git_commit_context_reads_workspace_template_and_reports_origin(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    repository = tmp_path / "project"
    repository.mkdir()
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    template = repository / "commit-template.txt"
    template.write_text("type(scope): subject\n", encoding="utf-8")
    subprocess.run([
        "git", "-C", str(repository), "config", "commit.template",
        "commit-template.txt",
    ], check=True)

    result = tools.git_commit_context("project")

    assert result["configured"] is True
    assert result["template"]["content"] == "type(scope): subject\n"
    assert len(result["template"]["sha256"]) == 64
    assert result["origins"][0]["source"].startswith("file:")


def test_git_commit_context_rejects_sensitive_workspace_template(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    repository = tmp_path / "project"
    repository.mkdir()
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    (repository / ".env").write_text("TOKEN=secret\n", encoding="utf-8")
    subprocess.run([
        "git", "-C", str(repository), "config", "commit.template", ".env",
    ], check=True)

    with pytest.raises(PolicyError, match="access denied"):
        tools.git_commit_context("project")


def test_git_config_policy_allows_only_safe_reads() -> None:
    WorkspaceTools._validate_exec_policy(
        "git", ["config", "--show-origin", "--get-all", "commit.template"],
    )
    with pytest.raises(PolicyError, match="allowlisted"):
        WorkspaceTools._validate_exec_policy("git", ["config", "user.name", "Attacker"])
    with pytest.raises(PolicyError, match="allowlisted"):
        WorkspaceTools._validate_exec_policy(
            "git", ["config", "--get", "http.extraHeader"],
        )
    with pytest.raises(PolicyError, match="injection"):
        WorkspaceTools._validate_exec_policy(
            "git", ["--git-dir", "/tmp/repo", "config", "--get", "commit.template"],
        )


def test_test_report_viewer_parses_junit_summary(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    report = tmp_path / "report.xml"
    report.write_text(
        '<testsuite tests="4" failures="1" errors="0" skipped="1"><testcase name="ok"/></testsuite>',
        encoding="utf-8",
    )
    viewed = tools.view_test_report("report.xml")
    assert viewed.structured_content["kind"] == "test"
    assert viewed.structured_content["stats"] == {
        "format": "junit", "tests": 4, "failures": 1, "errors": 0, "skipped": 1,
    }


def test_agent_skills_shared_project_override_and_context(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    repository = tmp_path / "project"
    repository.mkdir()
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    (tmp_path / "AGENTS.md").write_text("root rules\n", encoding="utf-8")
    (repository / "AGENTS.md").write_text("project rules\n", encoding="utf-8")

    shared = tools.create_skill("shared", "review", "Review shared code.", "Follow shared rules.")
    assert shared["scope"] == "shared"
    project = tools.create_skill("project", "review", "Review project code.", "Follow project rules.", cwd="project")
    assert project["scope"] == "project"

    catalog = tools.list_skills("project")
    review_records = [item for item in catalog["skills"] if item["name"] == "review"]
    assert len(review_records) == 2
    assert [item["scope"] for item in review_records if item["selected"]] == ["project"]
    active = tools.activate_skill("review", "project")
    assert active["scope"] == "project"
    assert "Follow project rules" in active["instructions"]
    context = tools.agent_context("project")
    assert [item["content"] for item in context["agents"]] == ["root rules\n", "project rules\n"]
    assert next(item for item in context["skills"] if item["name"] == "review")["scope"] == "project"

    absolute = repository.as_posix()
    assert tools.agent_context(absolute)["cwd"] == "project"
    assert tools.list_skills(absolute)["project_root"] == "project"
    assert tools.activate_skill("review", absolute)["scope"] == "project"
    with pytest.raises(PolicyError, match="absolute cwd must be within the workspace"):
        tools.agent_context((tmp_path.parent / "outside").as_posix())


def test_agent_skills_builtin_shared_project_precedence(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    builtin_root = tmp_path.parent / f"{tmp_path.name}-builtin-skills"
    builtin = builtin_root / "review"
    builtin.mkdir(parents=True)
    builtin_file = builtin / "SKILL.md"
    builtin_file.write_text(
        "---\nname: review\ndescription: Built-in review.\n---\n\nFollow built-in rules.\n",
        encoding="utf-8",
    )
    monkeypatch.setattr(skills_module, "BUILTIN_SKILL_ROOT", builtin_root)
    tools = make_tools(tmp_path)

    active = tools.activate_skill("review")
    assert active["scope"] == "builtin"
    assert active["source"] == "builtin/review"
    with pytest.raises(PolicyError, match="built-in skills are read-only"):
        tools.edit_skill("review", "invalid", active["sha256"])

    tools.create_skill("shared", "review", "Shared review.", "Follow shared rules.")
    assert tools.activate_skill("review")["scope"] == "shared"

    repository = tmp_path / "project"
    repository.mkdir()
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    tools.create_skill("project", "review", "Project review.", "Follow project rules.", cwd="project")
    assert tools.activate_skill("review", "project")["scope"] == "project"


def test_bundled_agent_skills_are_valid_and_mcp_adapted(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    builtin_root = Path(__file__).resolve().parents[1] / "bundled_skills"
    monkeypatch.setattr(skills_module, "BUILTIN_SKILL_ROOT", builtin_root)
    tools = make_tools(tmp_path)
    expected = {"devtools"}

    catalog = tools.list_skills()
    selected = {item["name"] for item in catalog["skills"] if item["selected"]}
    assert selected == expected
    assert catalog["errors"] == []
    assert "solidjs-development" not in selected
    assert "astro-solid-integration" not in selected

    assert tools.validate_skill("devtools")["valid"] is True
    activated = tools.activate_skill("devtools")
    assert activated["scope"] == "builtin"
    assert "devtools schema task claim" in activated["instructions"]


def test_agent_skill_edit_resources_validation_and_revision_guard(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    created = tools.create_skill("shared", "release", "Prepare a release.", "Use the checklist.")
    resource = tools.write_skill_resource(
        "release", "references/checklist.md", "# Checklist\n", overwrite=False,
    )
    assert resource["bytes"] == 12
    read = tools.read_skill_resource("release", "references/checklist.md")
    assert read["content"] == "# Checklist\n"
    assert tools.validate_skill("release")["resource_count"] == 1

    patch = """--- a/SKILL.md
+++ b/SKILL.md
@@ -6,1 +6,1 @@
-Use the checklist.
+Use the complete checklist.
"""
    edited = tools.edit_skill("release", patch, created["sha256"])
    assert edited["sha256"] != created["sha256"]
    assert "complete checklist" in tools.activate_skill("release")["instructions"]
    with pytest.raises(PolicyError):
        tools.edit_skill("release", patch, created["sha256"])
    with pytest.raises(PolicyError):
        tools.write_skill_resource("release", "../escape.md", "bad")


def test_git_partial_staging_preserves_unrelated_index_and_supports_reverse(tmp_path: Path) -> None:
    repository = tmp_path / "project"
    repository.mkdir()
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    subprocess.run(["git", "-C", str(repository), "config", "user.name", "Test"], check=True)
    subprocess.run(["git", "-C", str(repository), "config", "user.email", "test@example.invalid"], check=True)
    target = repository / "tracked.txt"
    target.write_text("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n", encoding="utf-8")
    unrelated = repository / "unrelated.txt"
    unrelated.write_text("base\n", encoding="utf-8")
    subprocess.run(["git", "-C", str(repository), "add", "."], check=True)
    subprocess.run(["git", "-C", str(repository), "commit", "-qm", "initial"], check=True)
    unrelated.write_text("staged unrelated\n", encoding="utf-8")
    subprocess.run(["git", "-C", str(repository), "add", "unrelated.txt"], check=True)
    target.write_text("ONE\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN\n", encoding="utf-8")
    tools = make_tools(tmp_path)
    before = tools.git_index_state("project")["index_sha256"]
    patch = """diff --git a/tracked.txt b/tracked.txt
--- a/tracked.txt
+++ b/tracked.txt
@@ -1,4 +1,4 @@
-one
+ONE
 two
 three
 four
"""
    staged = tools.git_stage_patch(patch, cwd="project", expected_index_sha256=before)
    cached = tools.git_diff(staged=True, cwd="project")["output"]
    unstaged = tools.git_diff(cwd="project")["output"]
    assert "+ONE" in cached and "+staged unrelated" in cached
    assert "+TEN" in unstaged and "+TEN" not in cached
    with pytest.raises(PolicyError):
        tools.git_stage_patch(patch, cwd="project", expected_index_sha256=before)

    reversed_result = tools.git_stage_patch(
        patch, cwd="project", reverse=True, expected_index_sha256=staged["index_sha256"],
    )
    cached_after = tools.git_diff(staged=True, cwd="project")["output"]
    assert "+ONE" not in cached_after and "+staged unrelated" in cached_after
    assert reversed_result["index_sha256"] != staged["index_sha256"]


def test_git_stage_and_unstage_paths_preserve_worktree(tmp_path: Path) -> None:
    repository = tmp_path / "project"
    repository.mkdir()
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    target = repository / "new.txt"
    target.write_text("content\n", encoding="utf-8")
    tools = make_tools(tmp_path)
    before = tools.git_index_state("project")["index_sha256"]
    staged = tools.git_stage_paths(["new.txt"], "project", before)
    assert "new.txt" in tools.git_status("project")["output"]
    tools.git_unstage_paths(["new.txt"], "project", staged["index_sha256"])
    assert target.read_text(encoding="utf-8") == "content\n"
    assert "?? new.txt" in tools.git_status("project")["output"]


def test_denied_file_is_not_listed(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    (tmp_path / ".env").write_text("TOKEN=secret", encoding="utf-8")
    (tmp_path / "visible.txt").write_text("ok", encoding="utf-8")
    paths = {entry["path"] for entry in tools.list_files()["entries"]}
    assert "visible.txt" in paths
    assert ".env" not in paths


def test_allowlisted_check_and_process_lifecycle(tmp_path: Path) -> None:
    checks = {"hello": command_spec([sys.executable, "-c", "print('check-ok')"])}
    processes = {
        "worker": command_spec([
            sys.executable,
            "-c",
            "import time; print('ready', flush=True); time.sleep(30)",
        ])
    }
    tools = make_tools(tmp_path, checks=checks, processes=processes)
    assert tools.run_check("hello")["output"] == "check-ok\n"

    started = tools.start_process("worker")
    session_id = started["session_id"]
    deadline = time.monotonic() + 3
    output = ""
    while time.monotonic() < deadline:
        output = tools.read_process(session_id)["output"]
        if "ready" in output:
            break
        time.sleep(0.05)
    assert "ready" in output
    viewed = tools.view_process_log(session_id)
    assert viewed.structured_content["kind"] == "log"
    assert "ready" in viewed.structured_content["content"]
    stopped = tools.stop_process(session_id)
    assert stopped["status"] == "exited"
    assert tools.list_processes()["processes"][0]["session_id"] == session_id
    tools.close()


def test_start_check_runs_configured_check_as_managed_process(tmp_path: Path) -> None:
    checks = {"slow": command_spec([
        sys.executable,
        "-c",
        "import time; print('checking', flush=True); time.sleep(0.1)",
    ])}
    tools = make_tools(tmp_path, checks=checks)

    started = tools.start_check("slow")
    assert started["name"] == "slow"
    session_id = started["session_id"]
    deadline = time.monotonic() + 3
    result = tools.read_process(session_id)
    while result["status"] == "running" and time.monotonic() < deadline:
        time.sleep(0.05)
        result = tools.read_process(session_id)

    assert result["status"] == "exited"
    assert result["exit_code"] == 0
    assert "checking" in result["output"]
    tools.close()


def test_unknown_commands_are_rejected(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    with pytest.raises(PolicyError):
        tools.run_check("shell")
    with pytest.raises(PolicyError):
        tools.start_check("shell")
    with pytest.raises(PolicyError):
        tools.start_process("shell")
    assert issubclass(PolicyError, ToolError)


def test_go_is_allowlisted_with_workspace_caches(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    command, node_version = tools._command_argv("go", ["version"], tmp_path, None)
    assert command == ["/home/linuxbrew/.linuxbrew/bin/go", "version"]
    assert node_version is None
    environment = tools._tool_environment()
    assert environment["GOCACHE"] == "/workspace/.loki/go/build-cache"
    assert environment["GOMODCACHE"] == "/workspace/.loki/go/pkg/mod"
    assert environment["HTTP_PROXY"] == "http://127.0.0.1:8766"
    assert environment["NODE_USE_ENV_PROXY"] == "1"
    assert environment["SSH_AUTH_SOCK"] == "/run/loki/signing/agent.sock"
    assert environment["GIT_CONFIG_GLOBAL"] == "/etc/loki/gitconfig"
    with pytest.raises(PolicyError):
        tools._command_argv("go", ["env", "-w", "GOPROXY=off"], tmp_path, None)


def test_rust_tools_are_allowlisted_with_workspace_homes(tmp_path: Path) -> None:
    tools = make_tools(tmp_path)
    cargo, node_version = tools._command_argv("cargo", ["check"], tmp_path, None)
    assert cargo == ["/workspace/.loki/cargo/bin/cargo", "check"]
    assert node_version is None
    environment = tools._tool_environment()
    assert environment["CARGO_HOME"] == "/workspace/.loki/cargo"
    assert environment["RUSTUP_HOME"] == "/workspace/.loki/rustup"
    with pytest.raises(PolicyError):
        tools._command_argv("rustup", ["run", "stable", "bash"], tmp_path, None)


def test_port_tools_use_guard_and_validate_port(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    tools = make_tools(tmp_path)
    calls: list[tuple[str, int]] = []

    def fake_guard(operation: str, port: int) -> dict[str, object]:
        calls.append((operation, port))
        if operation == "inspect":
            return {"port": port, "in_use": True, "listeners": []}
        return {"port": port, "stopped": True, "terminated_pids": [123]}

    monkeypatch.setattr(tools, "_port_guard", fake_guard)
    assert tools.port_info(5173)["in_use"] is True
    assert tools.stop_port(5173)["terminated_pids"] == [123]
    assert calls == [("inspect", 5173), ("stop", 5173)]
    with pytest.raises(PolicyError):
        WorkspaceTools._port_guard("inspect", 80)


def test_process_timeout_is_enforced(tmp_path: Path) -> None:
    processes = {
        "timeout": command_spec([
            sys.executable,
            "-c",
            "import time; time.sleep(30)",
        ], timeout=1)
    }
    tools = make_tools(tmp_path, processes=processes)
    session_id = tools.start_process("timeout")["session_id"]
    deadline = time.monotonic() + 4
    result = tools.read_process(session_id)
    while result["status"] == "running" and time.monotonic() < deadline:
        time.sleep(0.05)
        result = tools.read_process(session_id)
    assert result["status"] == "exited"
    assert result["timed_out"] is True
    tools.close()
