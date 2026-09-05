from pathlib import Path
from types import SimpleNamespace
import json
import os
import subprocess

import pytest

import loki_mcp.runtime as runtime
from loki_mcp.runtime import (
    AGENT_UID,
    RuntimeController,
    EncryptedStateStore,
    LokiRuntimeError,
    _allocate_loopback_port,
    _file_lock_is_held,
    inspect_docker_port,
    _action_cwd,
    _selected_secret_names,
    _validate_action,
)


def _action(
    cwd: str, command: list[str], *, dynamic_port: dict | None = None,
    required: list[str] | None = None, local_callback: bool = False,
    public_environment: list[str] | None = None,
) -> dict:
    return {
        "cwd": cwd,
        "command": command,
        "secrets": [],
        "all_secrets": True,
        "required_secrets": required or [],
        "timeout_seconds": 3600,
        "max_output_bytes": 1_048_576,
        "dynamic_port": dynamic_port,
        "singleton": False,
        "lock_probe": None,
        "local_callback": local_callback,
        "public_environment": public_environment or [],
        "docker_access": False,
        "materialize_env_file": None,
        "materialize_env_path": None,
    }


def _frontend_action(cwd: str = "frontend") -> dict:
    return _action(
        cwd, ["pnpm", "dev", "--port", "{LOKI_PORT}"],
        dynamic_port={
            "preferred": 42100,
            "environment": "PORT",
            "origin_environment": "PUBLIC_ORIGIN",
        },
        public_environment=["PUBLIC_BACKEND_URL"],
    )


def _backend_action(cwd: str = "backend") -> dict:
    action = _action(
        cwd, ["just", "agent", "dev", "{LOKI_PORT}"],
        dynamic_port={"preferred": 31180, "environment": "APP_PORT"},
        required=["APP_SECRET"], local_callback=True,
    )
    action["singleton"] = True
    action["lock_probe"] = ".tmp/runtime/.owner.lock"
    return action


def test_registered_action_allocates_port_and_publishes_actual_origin() -> None:
    action = _frontend_action()
    assert action["command"][-1] == "{LOKI_PORT}"
    assert action["dynamic_port"] == {
        "preferred": 42100,
        "environment": "PORT",
        "origin_environment": "PUBLIC_ORIGIN",
    }


def test_dynamic_port_allocator_falls_back_when_preferred_is_occupied() -> None:
    import socket

    runtime._PENDING_PORTS.clear()
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        preferred = int(listener.getsockname()[1])
        allocated = _allocate_loopback_port(preferred)
    assert allocated != preferred
    assert 1024 <= allocated <= 65_535


def test_registered_backend_action_is_worktree_singleton_with_dynamic_port() -> None:
    action = _backend_action()
    assert action["dynamic_port"] == {
        "preferred": 31180, "environment": "APP_PORT",
    }
    assert action["singleton"] is True
    assert action["lock_probe"] == ".tmp/runtime/.owner.lock"


def test_file_lock_probe_distinguishes_held_and_stale_files(tmp_path: Path) -> None:
    import fcntl

    for directory in (tmp_path.parent.parent, tmp_path.parent, tmp_path):
        directory.chmod(0o755)
    lock = tmp_path / ".owner.lock"
    lock.touch()
    os.chown(lock, AGENT_UID, -1)
    assert _file_lock_is_held(lock) is False
    with lock.open("r+b") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert _file_lock_is_held(lock) is True
    assert _file_lock_is_held(lock) is False


def test_dynamic_port_allocator_reserves_concurrent_startup_ports() -> None:
    import socket

    runtime._PENDING_PORTS.clear()
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        preferred = int(listener.getsockname()[1])
    first = _allocate_loopback_port(preferred)
    second = _allocate_loopback_port(preferred)
    assert first == preferred
    assert second != first


def test_action_cwd_accepts_only_same_repository_worktree(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    repository = workspace / "stamp.is-web"
    other = workspace / "other"
    worktree = workspace / "stamp.is-web-feature"
    repository.mkdir(parents=True)
    other.mkdir()
    for path in (repository, other):
        subprocess.run(["git", "init", "-q", str(path)], check=True)
        subprocess.run(["git", "-C", str(path), "config", "user.name", "Test"], check=True)
        subprocess.run([
            "git", "-C", str(path), "config", "user.email", "test@example.invalid",
        ], check=True)
        (path / "tracked.txt").write_text("base\n", encoding="utf-8")
        subprocess.run(["git", "-C", str(path), "add", "tracked.txt"], check=True)
        subprocess.run([
            "git", "-C", str(path), "commit", "-qm", "initial",
        ], check=True)
    subprocess.run([
        "git", "-C", str(repository), "worktree", "add", "-q", "-b", "feature",
        str(worktree),
    ], check=True)
    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    action = {"cwd": "stamp.is-web"}

    assert _action_cwd(action, "stamp.is-web-feature") == worktree
    with pytest.raises(LokiRuntimeError, match="worktree of the registered repository"):
        _action_cwd(action, "other")
    with pytest.raises(LokiRuntimeError, match="workspace-relative"):
        _action_cwd(action, str(worktree))

    # Git can return the MCP sandbox's absolute common-dir path to the runtime.
    original = subprocess.run
    def sandbox_paths(*args, **kwargs):
        result = original(*args, **kwargs)
        if "rev-parse" in args[0] and result.returncode == 0:
            result.stdout = result.stdout.replace(str(workspace), "/workspace")
        return result
    monkeypatch.setattr(subprocess, "run", sandbox_paths)
    assert _action_cwd(action, "stamp.is-web-feature") == worktree
    with pytest.raises(LokiRuntimeError, match="worktree of the registered repository"):
        _action_cwd(action, "other")


def test_dynamic_port_action_returns_actual_endpoint_and_overrides_origin(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    cwd = workspace / "stamp.is-web"
    cwd.mkdir(parents=True)
    (cwd / ".node-version").write_text("24.20.0\n", encoding="utf-8")
    action = _frontend_action("stamp.is-web")

    class Store:
        def load(self) -> dict:
                return {"profiles": {"web": {"secrets": {
                    "PUBLIC_ORIGIN": "http://127.0.0.1:42100",
            }, "actions": {"storefront": action}}}}

    captured: dict = {}

    class Processes:
        def start_command(self, **kwargs: object) -> dict:
            captured.update(kwargs)
            return {"session_id": "session123", "status": "running", "output": ""}

    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    monkeypatch.setattr(runtime, "_allocate_loopback_port", lambda preferred: 43210)
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = Store()
    broker.processes = Processes()
    broker._scrubbers = {}
    broker._action_launches = {}

    result = broker.op_run_action({"profile": "web", "action_name": "storefront"})

    assert "43210" in captured["command"]
    assert "{LOKI_PORT}" not in captured["command"]
    assert captured["command"][:4] == [
        "/usr/bin/systemd-run", "--scope", "--quiet", "--collect",
    ]
    assert captured["environment"]["PORT"] == "43210"
    assert captured["environment"]["PUBLIC_ORIGIN"] == (
        "http://127.0.0.1:43210"
    )
    assert result["port"] == 43210
    assert result["local_url"] == "http://127.0.0.1:43210"
    assert result["cwd"] == "/workspace/stamp.is-web"
    assert captured["group"] == "web"
    assert captured["max_group_processes"] == 6


def test_dynamic_api_action_can_bind_fixed_local_callback(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    cwd = workspace / "stamp.is-api"
    cwd.mkdir(parents=True)
    (cwd / ".node-version").write_text("24.20.0\n", encoding="utf-8")
    action = _backend_action("stamp.is-api")

    class Store:
        def load(self) -> dict:
            return {"profiles": {"api": {"secrets": {
                "APP_SECRET": "configured",
            }, "actions": {"api": action}}}}

    class Processes:
        def find_running(self, _: str) -> None:
            return None

        def start_command(self, **_: object) -> dict:
            return {"session_id": "api-session", "status": "running", "output": ""}

    class Callback:
        def bind(self, session_id: str) -> dict:
            assert session_id == "api-session"
            return {
                "bound": True, "listening": True,
                "origin": "http://127.0.0.1:41800",
                "session_id": session_id, "target_port": 43211,
            }

    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    monkeypatch.setattr(runtime, "_allocate_loopback_port", lambda preferred: 43211)
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = Store()
    broker.processes = Processes()
    broker.local_callback = Callback()
    broker._scrubbers = {}
    broker._action_launches = {}

    result = broker.op_run_action({
        "profile": "api", "action_name": "api", "bind_local_callback": True,
    })

    assert result["local_callback"] == {
        "bound": True, "listening": True,
        "origin": "http://127.0.0.1:41800",
        "session_id": "api-session", "target_port": 43211,
    }


def test_local_callback_binding_rejects_non_api_action(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    cwd = workspace / "stamp.is-web"
    cwd.mkdir(parents=True)
    (cwd / ".node-version").write_text("24.20.0\n", encoding="utf-8")
    action = _frontend_action("stamp.is-web")

    class Store:
        def load(self) -> dict:
            return {"profiles": {"web": {"secrets": {
                "PUBLIC_STOREFRONT_ORIGIN": "http://127.0.0.1:42100",
            }, "actions": {"storefront": action}}}}

    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = Store()

    with pytest.raises(LokiRuntimeError, match="not approved as a local callback target"):
        broker.op_run_action({
            "profile": "web", "action_name": "storefront",
            "bind_local_callback": True,
        })


def test_prepared_dynamic_port_is_consumed_by_matching_action(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    cwd = workspace / "stamp.is-web"
    cwd.mkdir(parents=True)
    (cwd / ".node-version").write_text("24.20.0\n", encoding="utf-8")
    action = _frontend_action("stamp.is-web")

    class Store:
        def load(self) -> dict:
            return {"profiles": {"web": {"secrets": {}, "actions": {"storefront": action}}}}

    captured: dict = {}

    class Processes:
        def start_command(self, **kwargs: object) -> dict:
            captured.update(kwargs)
            return {"session_id": "session123", "status": "running", "output": ""}

    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    monkeypatch.setattr(runtime, "_allocate_loopback_port", lambda preferred: 43210)
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = Store()
    broker.processes = Processes()
    broker._scrubbers = {}
    broker._action_launches = {}

    prepared = broker.op_prepare_action({"profile": "web", "action_name": "storefront"})
    result = broker.op_run_action({
        "profile": "web", "action_name": "storefront",
        "launch_token": prepared["launch_token"],
        "public_environment": {"PUBLIC_ORIGIN": "https://loki-" + "a" * 32 + ".streamliner.im"},
    })

    assert prepared["port"] == 43210
    assert result["port"] == 43210
    assert "43210" in captured["command"]
    assert prepared["launch_token"] not in broker._action_launches


def test_inspect_docker_port_accepts_only_loopback_workspace_compose(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    container_id = "a" * 12
    labels = {
        "com.docker.compose.project": "stamp-is-api-local-deploy",
        "com.docker.compose.service": "api",
        "com.docker.compose.project.working_dir": "/var/lib/loki/local-deploy/snapshots/run/deploy/local",
        "com.docker.compose.project.config_files": "/var/lib/loki/local-deploy/snapshots/run/deploy/local/compose.yml",
    }

    def docker(*arguments: str) -> str:
        if arguments[:2] == ("container", "ls"):
            return f"{container_id}\n"
        return "true\t" + json.dumps({
            "31080/tcp": [{"HostIp": "127.0.0.1", "HostPort": "41280"}],
        }) + "\t" + json.dumps(labels) + "\n"

    monkeypatch.setattr(runtime, "_docker", docker)
    result = inspect_docker_port(41280)
    assert result["in_use"] is True
    assert result["listeners"] == [{
        "container_id": container_id,
        "cwd": "/workspace",
        "command": "docker-compose:stamp-is-api-local-deploy/api",
        "local_address": "127.0.0.1:41280",
    }]


@pytest.mark.parametrize(
    ("host_ip", "working_dir", "config_file"),
    [
        ("0.0.0.0", "/workspace/app", "/workspace/app/compose.yml"),
        ("127.0.0.1", "/tmp/app", "/tmp/app/compose.yml"),
        ("127.0.0.1", "/workspace/app", "/workspace/app/../outside/compose.yml"),
    ],
)
def test_inspect_docker_port_rejects_untrusted_publish(
    monkeypatch: pytest.MonkeyPatch, host_ip: str, working_dir: str, config_file: str,
) -> None:
    labels = {
        "com.docker.compose.project": "example",
        "com.docker.compose.service": "api",
        "com.docker.compose.project.working_dir": working_dir,
        "com.docker.compose.project.config_files": config_file,
    }

    def docker(*arguments: str) -> str:
        if arguments[:2] == ("container", "ls"):
            return f"{'b' * 12}\n"
        return "true\t" + json.dumps({
            "80/tcp": [{"HostIp": host_ip, "HostPort": "41280"}],
        }) + "\t" + json.dumps(labels) + "\n"

    monkeypatch.setattr(runtime, "_docker", docker)
    assert inspect_docker_port(41280) == {
        "port": 41280, "in_use": False, "listeners": [],
    }


def test_agent_manages_opaque_staged_secret_imports(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    inbox = tmp_path / "inbox"
    inbox.mkdir(mode=0o700)
    monkeypatch.setattr(runtime, "INBOX_DIRECTORY", inbox)
    monkeypatch.setattr(runtime, "AUDIT_PATH", tmp_path / "audit.jsonl")
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store
    broker.processes = None
    broker._scrubbers = {}

    created = broker.dispatch(
        {"operation": "profile_create", "profile": "agent-local"}, AGENT_UID,
    )
    assert created == {"profile": "agent-local", "created": True}

    import_id = "a" * 32
    staged = inbox / f"{import_id}.env"
    staged.write_text("DATABASE_URL=postgres://local\nEMPTY=\n", encoding="utf-8")
    staged.chmod(0o600)
    listed = broker.dispatch({"operation": "list_imports"}, AGENT_UID)
    assert [item["import_id"] for item in listed["imports"]] == [import_id]

    imported = broker.dispatch({
        "operation": "import_staged_env",
        "profile": "agent-local",
        "import_id": import_id,
    }, AGENT_UID)
    assert imported["imported"] == ["DATABASE_URL", "EMPTY"]
    assert imported["source_deleted"] is True
    assert not staged.exists()
    metadata = broker.dispatch(
        {"operation": "get_profile", "profile": "agent-local"}, AGENT_UID,
    )
    assert metadata["secret_names"] == ["DATABASE_URL", "EMPTY"]
    assert metadata["configured_secret_count"] == 1
    assert metadata["empty_secret_names"] == ["EMPTY"]
    assert "postgres://local" not in json.dumps(metadata)

    broker.dispatch({
        "operation": "secret_remove", "profile": "agent-local", "secret": "EMPTY",
    }, AGENT_UID)
    broker.dispatch(
        {"operation": "profile_remove", "profile": "agent-local"}, AGENT_UID,
    )
    with pytest.raises(LokiRuntimeError, match="requires the Loki agent user"):
        broker.dispatch(
            {"operation": "profile_create", "profile": "denied"}, AGENT_UID + 1,
        )
    with pytest.raises(LokiRuntimeError, match="require root"):
        broker.dispatch({
            "operation": "secret_set",
            "profile": "agent-local",
            "secret": "TOKEN",
            "value": "must-not-pass",
        }, AGENT_UID)


def test_mcp_service_peer_can_perform_administrative_operations(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    broker = runtime.RuntimeController(
        EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json"),
    )
    monkeypatch.setattr(runtime, "_peer_is_mcp_service", lambda uid, pid: True)
    monkeypatch.setattr(
        broker, "op_project_register", lambda request: {"created": True, "cwd": request["cwd"]},
    )

    result = broker.dispatch(
        {"operation": "project_register", "cwd": "sample"}, AGENT_UID, 1234,
    )

    assert result == {"created": True, "cwd": "sample"}


def test_mcp_peer_identity_requires_exact_service_cgroup(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        Path, "read_text",
        lambda self, **kwargs: "0::/system.slice/loki-mcp.service\n",
    )
    assert runtime._peer_is_mcp_service(AGENT_UID, 1234) is True
    assert runtime._peer_is_mcp_service(AGENT_UID + 1, 1234) is False

    monkeypatch.setattr(
        Path, "read_text",
        lambda self, **kwargs: "0::/system.slice/loki-action-example.scope\n",
    )
    assert runtime._peer_is_mcp_service(AGENT_UID, 1234) is False


def test_agent_sets_public_configuration_without_echo_or_audit_value(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    audit_path = tmp_path / "audit.jsonl"
    monkeypatch.setattr(runtime, "AUDIT_PATH", audit_path)
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    store.update(lambda document: document["profiles"].update({
        "agent-local": {"secrets": {}, "actions": {}},
    }))
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store

    result = broker.dispatch({
        "operation": "public_value_set",
        "profile": "agent-local",
        "secret": "FEATURE_ENABLED",
        "value": "public-config-marker",
    }, AGENT_UID)

    assert result == {
        "profile": "agent-local", "secret": "FEATURE_ENABLED", "stored": True,
    }
    assert (
        store.load()["profiles"]["agent-local"]["secrets"]["FEATURE_ENABLED"]
        == "public-config-marker"
    )
    assert "public-config-marker" not in json.dumps(result)
    assert "public-config-marker" not in audit_path.read_text(encoding="utf-8")


def test_public_configuration_rejects_unsafe_or_oversized_values(tmp_path: Path) -> None:
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    store.update(lambda document: document["profiles"].update({
        "agent-local": {"secrets": {}, "actions": {}},
    }))
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store

    with pytest.raises(LokiRuntimeError, match="contains NUL"):
        broker.op_public_value_set({
            "profile": "agent-local", "secret": "VALUE", "value": "a\x00b",
        })
    with pytest.raises(LokiRuntimeError, match="exceeds 65536 bytes"):
        broker.op_public_value_set({
            "profile": "agent-local", "secret": "VALUE", "value": "가" * 21_846,
        })


def test_staged_secret_import_rejects_unsafe_file_permissions(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    inbox = tmp_path / "inbox"
    inbox.mkdir(mode=0o700)
    monkeypatch.setattr(runtime, "INBOX_DIRECTORY", inbox)
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    store.update(lambda document: document["profiles"].update({
        "local": {"secrets": {}, "actions": {}},
    }))
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store

    import_id = "b" * 32
    staged = inbox / f"{import_id}.env"
    staged.write_text("TOKEN=value\n", encoding="utf-8")
    staged.chmod(0o644)
    with pytest.raises(LokiRuntimeError, match="unsafe ownership or permissions"):
        broker.op_import_staged_env({
            "profile": "local", "import_id": import_id,
        })
    assert staged.exists()


def test_encrypted_store_never_persists_plaintext(tmp_path: Path) -> None:
    key_path = tmp_path / "master.key"
    store_path = tmp_path / "store.json"
    store = EncryptedStateStore(key_path, store_path)
    assert store.initialize() is True
    assert store.initialize() is False
    value = "not-in-the-envelope-8b3f59"

    def update(document: dict) -> None:
        document["profiles"]["local"] = {
            "secrets": {"API_TOKEN": value},
            "actions": {},
        }

    store.update(update)
    assert store.load()["profiles"]["local"]["secrets"]["API_TOKEN"] == value
    assert value.encode() not in store_path.read_bytes()
    assert os.stat(key_path).st_mode & 0o777 == 0o600
    assert os.stat(store_path).st_mode & 0o777 == 0o600
    envelope = json.loads(store_path.read_text(encoding="utf-8"))
    assert set(envelope) == {"version", "nonce", "ciphertext"}


def test_action_policy_requires_registered_secret_and_safe_command() -> None:
    action = {
        "cwd": "stamp.is-api",
        "command": ["pnpm", "dev"],
        "secrets": ["DATABASE_URL"],
        "required_secrets": ["DATABASE_URL"],
        "timeout_seconds": 3600,
        "max_output_bytes": 65_536,
    }
    _validate_action(action, {"DATABASE_URL"})
    with pytest.raises(LokiRuntimeError, match="unknown"):
        _validate_action({**action, "secrets": ["MISSING"]}, {"DATABASE_URL"})
    with pytest.raises(LokiRuntimeError, match="unknown or duplicate required"):
        _validate_action({**action, "required_secrets": ["MISSING"]}, {"DATABASE_URL"})
    with pytest.raises(LokiRuntimeError, match="not allowed"):
        _validate_action({**action, "command": ["sh", "-c", "env"]}, {"DATABASE_URL"})
    with pytest.raises(LokiRuntimeError, match="workspace-relative"):
        _validate_action({**action, "cwd": "../outside"}, {"DATABASE_URL"})


def test_action_policy_supports_explicit_dynamic_all_secrets() -> None:
    action = {
        "cwd": "stamp.is-api",
        "command": ["go", "run", "./cmd/api"],
        "secrets": [],
        "all_secrets": True,
        "timeout_seconds": 3600,
        "max_output_bytes": 65_536,
    }
    _validate_action(action, {"DATABASE_URL", "JWT_SECRET"})
    assert _selected_secret_names(action, {"JWT_SECRET": "two", "DATABASE_URL": "one"}) == [
        "DATABASE_URL",
        "JWT_SECRET",
    ]
    with pytest.raises(LokiRuntimeError, match="cannot combine"):
        _validate_action({**action, "secrets": ["DATABASE_URL"]}, {"DATABASE_URL"})
    with pytest.raises(LokiRuntimeError, match="must select"):
        _validate_action({**action, "all_secrets": False}, {"DATABASE_URL"})


def test_legacy_empty_secret_selection_remains_all_secrets() -> None:
    action = {
        "cwd": "stamp.is-api",
        "command": ["go", "run", "./cmd/api"],
        "secrets": [],
        "timeout_seconds": 3600,
        "max_output_bytes": 65_536,
    }
    _validate_action(action, {"DATABASE_URL"})
    assert _selected_secret_names(action, {"DATABASE_URL": "one"}) == ["DATABASE_URL"]


def test_required_secret_preflight_and_opaque_generation(tmp_path: Path) -> None:
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    action = {
        "cwd": ".",
        "command": ["printenv", "RATE_LIMIT_SCOPE_SECRET"],
        "secrets": [],
        "all_secrets": True,
        "required_secrets": ["RATE_LIMIT_SCOPE_SECRET"],
        "timeout_seconds": 30,
        "max_output_bytes": 65_536,
    }
    store.update(lambda document: document["profiles"].update({
        "local": {
            "secrets": {"RATE_LIMIT_SCOPE_SECRET": ""},
            "actions": {"api": action},
        },
    }))
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store
    broker.processes = None
    broker._scrubbers = {}

    metadata = broker.op_get_profile({"profile": "local"})
    assert metadata["action_policies"]["api"]["ready"] is False
    assert metadata["action_policies"]["api"]["missing_required_secrets"] == [
        "RATE_LIMIT_SCOPE_SECRET",
    ]
    with pytest.raises(LokiRuntimeError, match="RATE_LIMIT_SCOPE_SECRET"):
        broker.op_run_action({"profile": "local", "action_name": "api"})

    generated = broker.dispatch({
        "operation": "secret_generate",
        "profile": "local",
        "secret": "RATE_LIMIT_SCOPE_SECRET",
        "bytes": 32,
    }, AGENT_UID)
    assert generated == {
        "profile": "local",
        "secret": "RATE_LIMIT_SCOPE_SECRET",
        "generated": True,
        "bytes": 32,
    }
    stored = store.load()["profiles"]["local"]["secrets"]["RATE_LIMIT_SCOPE_SECRET"]
    assert stored and stored not in json.dumps(generated)
    with pytest.raises(LokiRuntimeError, match="already configured"):
        broker.op_secret_generate({
            "profile": "local", "secret": "RATE_LIMIT_SCOPE_SECRET",
        })


def test_project_bootstrap_runs_registered_workflow(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    repository = workspace / "sample"
    repository.mkdir(parents=True)
    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    action = _action("sample", ["just", "--version"], required=["APP_SECRET"])
    store.update(lambda document: (
        document["profiles"].update({
            "development": {
                "secrets": {"APP_SECRET": "configured"},
                "actions": {"setup": action},
            },
        }),
        document["projects"].update({
            "a" * 32: {
                "name": "sample", "repository": "sample",
                "workflows": {"development": {
                    "steps": [["development", "setup"]],
                    "required_secrets": {"development": ["APP_SECRET"]},
                    "timeout_seconds": 3600,
                }},
            },
        }),
    ))
    captured: dict = {}

    class Processes:
        def start_command(self, **kwargs: object) -> dict:
            captured.update(kwargs)
            return {"session_id": "bootstrap-session", "status": "running", "output": ""}

    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store
    broker.processes = Processes()
    broker._scrubbers = {}
    broker.project_state = SimpleNamespace(resolve=lambda _: SimpleNamespace(
        project_id="a" * 32, worktree_id="b" * 32, repository_root=repository,
    ))
    result = broker.op_bootstrap_project({"cwd": "sample"})

    assert result["configuration_ready"] is True
    assert result["accepted"] is True
    assert result["status"] == "running"
    assert result["outcome"] == "running"
    assert result["session_id"] == "bootstrap-session"
    assert "/usr/local/libexec/loki-project-bootstrap" in captured["command"]
    assert captured["command"][:4] == [
        "/usr/bin/systemd-run", "--scope", "--quiet", "--collect",
    ]
    assert captured["command"][-2:] == ["sample", "development"]
    assert result["workflow"] == "development"


def test_project_bootstrap_selects_explicit_workflow(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    repository = workspace / "sample"
    repository.mkdir(parents=True)
    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    action = _action("sample", ["just", "--version"])
    store.update(lambda document: (
        document["profiles"].update({
            "deployment": {"secrets": {}, "actions": {"setup": action}},
        }),
        document["projects"].update({
            "a" * 32: {
                "name": "sample", "repository": "sample",
                "workflows": {"isolated": {
                    "steps": [["deployment", "setup"]],
                    "required_secrets": {}, "timeout_seconds": 3600,
                }},
            },
        }),
    ))

    class Processes:
        def start_command(self, **_: object) -> dict:
            return {"session_id": "deploy-session", "status": "running", "output": ""}

    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store
    broker.processes = Processes()
    broker._scrubbers = {}
    broker.project_state = SimpleNamespace(resolve=lambda _: SimpleNamespace(
        project_id="a" * 32, worktree_id="b" * 32, repository_root=repository,
    ))
    result = broker.op_bootstrap_project({"cwd": "sample", "workflow": "isolated"})

    assert result["workflow"] == "isolated"
    assert result["accepted"] is True


def test_project_bootstrap_reports_missing_secrets_without_starting(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    repository = workspace / "sample"
    repository.mkdir(parents=True)
    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    store = EncryptedStateStore(tmp_path / "master.key", tmp_path / "store.json")
    store.initialize()
    action = _action("sample", ["just", "--version"], required=["APP_SECRET"])
    store.update(lambda document: (
        document["profiles"].update({
            "development": {
                "secrets": {"APP_SECRET": ""}, "actions": {"setup": action},
            },
        }),
        document["projects"].update({
            "a" * 32: {
                "name": "sample", "repository": "sample",
                "workflows": {"development": {
                    "steps": [["development", "setup"]],
                    "required_secrets": {"development": ["APP_SECRET"]},
                    "timeout_seconds": 3600,
                }},
            },
        }),
    ))
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = store
    broker.processes = None
    broker._scrubbers = {}
    broker.project_state = SimpleNamespace(resolve=lambda _: SimpleNamespace(
        project_id="a" * 32, worktree_id="b" * 32, repository_root=repository,
    ))

    result = broker.op_bootstrap_project({"cwd": "sample"})

    assert result["configuration_ready"] is False
    assert result["accepted"] is False
    assert result["status"] == "blocked"
    assert result["missing_required_secrets"] == {"development": ["APP_SECRET"]}


def test_scrub_snapshot_reports_terminal_outcome() -> None:
    broker = RuntimeController.__new__(RuntimeController)
    broker._scrubbers = {}

    succeeded = broker._scrub_snapshot({
        "session_id": "success", "status": "exited", "exit_code": 0, "output": "ok",
    })
    failed = broker._scrub_snapshot({
        "session_id": "failure", "status": "exited", "exit_code": 1, "output": "bad",
    })

    assert succeeded["outcome"] == "succeeded"
    assert failed["outcome"] == "failed"


def test_stop_process_reports_intentional_stop() -> None:
    class Processes:
        def stop(self, session_id: str) -> dict:
            return {
                "session_id": session_id, "status": "exited", "exit_code": -15,
                "timed_out": False, "output": "",
            }

    broker = RuntimeController.__new__(RuntimeController)
    broker.processes = Processes()
    broker._scrubbers = {}

    result = broker.op_stop_process({"session_id": "stopped-session"})

    assert result["outcome"] == "stopped"


def test_project_bootstrap_rejects_unknown_project(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    (workspace / "other").mkdir(parents=True)
    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    broker = RuntimeController.__new__(RuntimeController)
    broker.project_state = SimpleNamespace(resolve=lambda _: SimpleNamespace(
        project_id="a" * 32, worktree_id="b" * 32,
    ))
    broker.store = SimpleNamespace(load=lambda: {"profiles": {}, "projects": {}})
    with pytest.raises(LokiRuntimeError, match="project is not registered"):
        broker.op_bootstrap_project({"cwd": "other"})


def test_secret_runner_mounts_signing_public_key() -> None:
    runner = (
        Path(__file__).parents[1] / "scripts" / "loki-action-runner.py"
    ).read_text(encoding="utf-8")
    assert '"--dir", "/home/runner/.ssh"' in runner
    assert (
        '"--ro-bind", "/etc/loki/signing_key.pub", '
        '"/home/runner/.ssh/id_ed25519.pub"'
    ) in runner


def test_docker_access_accepts_generic_registered_commands() -> None:
    action = {
        "cwd": "stamp.is-api",
        "command": ["just", "local", "deploy"],
        "secrets": [],
        "all_secrets": True,
        "materialize_env_file": "LOCAL_DEPLOY_RUNTIME_ENV",
        "materialize_env_path": "deploy/local/.env.local",
        "docker_access": True,
        "timeout_seconds": 3600,
        "max_output_bytes": 65_536,
    }
    _validate_action(action, {"DATABASE_URL"})
    _validate_action(
        {
            **action,
            "command": ["just", "agent", "infra-reset"],
            "materialize_env_file": None,
            "materialize_env_path": None,
        },
        {"DATABASE_URL"},
    )
    with pytest.raises(LokiRuntimeError, match="not allowed"):
        _validate_action({**action, "command": ["docker", "run", "alpine"]}, {"DATABASE_URL"})
    _validate_action({**action, "command": ["just", "custom", "workflow"]}, {"DATABASE_URL"})
    with pytest.raises(LokiRuntimeError, match="workspace-relative"):
        _validate_action({**action, "materialize_env_path": "../.env"}, {"DATABASE_URL"})


def test_docker_action_uses_restricted_transient_proxy_wrapper(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    (workspace / "stamp.is-api").mkdir(parents=True)
    (workspace / "stamp.is-api" / ".node-version").write_text("24.20.0\n", encoding="utf-8")
    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    action = {
        "cwd": "stamp.is-api",
        "command": ["just", "local", "deploy"],
        "secrets": [],
        "all_secrets": True,
        "materialize_env_file": "LOCAL_DEPLOY_RUNTIME_ENV",
        "materialize_env_path": "deploy/local/.env.local",
        "docker_access": True,
        "public_environment": ["PUBLIC_BACKEND_URL"],
        "timeout_seconds": 3600,
        "max_output_bytes": 65_536,
    }

    class Store:
        def load(self) -> dict:
            return {"profiles": {"local": {"secrets": {"DATABASE_URL": "value"}, "actions": {"deploy": action}}}}

    captured: dict = {}

    class Processes:
        def start_command(self, **kwargs: object) -> dict:
            captured.update(kwargs)
            return {"session_id": "session123", "status": "running", "output": ""}

    broker = RuntimeController.__new__(RuntimeController)
    broker.store = Store()
    broker.processes = Processes()
    broker._scrubbers = {}
    preview_api = "https://loki-0123456789abcdef0123456789abcdef.streamliner.im/_loki/backend"
    result = broker.op_run_action({
        "profile": "local",
        "action_name": "deploy",
        "public_environment": {"PUBLIC_BACKEND_URL": preview_api},
    })

    command = captured["command"]
    assert command[:4] == [
        "/usr/bin/systemd-run", "--scope", "--quiet", "--collect",
    ]
    assert any(item.startswith("--unit=loki-action-") for item in command)
    assert "/usr/local/libexec/loki-docker-action-runner" in command
    assert captured["environment"]["PUBLIC_BACKEND_URL"] == preview_api
    assert command.count("{LOKI_DOCKER_PROXY_SOCKET}") == 1
    docker_option = command.index("--docker-socket")
    assert command[docker_option:docker_option + 2] == [
        "--docker-socket", "{LOKI_DOCKER_PROXY_SOCKET}",
    ]
    env_path_option = command.index("--materialize-env-path")
    assert command[env_path_option:env_path_option + 2] == [
        "--materialize-env-path", "deploy/local/.env.local",
    ]
    assert result["session_id"] == "session123"
    with pytest.raises(LokiRuntimeError, match="public preview environment is invalid"):
        broker.op_run_action({
            "profile": "local",
            "action_name": "deploy",
            "public_environment": {"DATABASE_URL": "https://example.com"},
        })


def test_clear_action_materialization_removes_only_registered_private_file(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    target = workspace / "app/deploy/local/.env.local"
    target.parent.mkdir(parents=True)
    target.write_text("SECRET=value\n", encoding="utf-8")
    target.chmod(0o600)
    action = {
        "cwd": "app",
        "materialize_env_file": "LOCAL_DEPLOY_RUNTIME_ENV",
        "materialize_env_path": "deploy/local/.env.local",
    }

    class Store:
        def load(self) -> dict:
            return {"profiles": {"local": {"secrets": {}, "actions": {"deploy": action}}}}

    class Processes:
        def list(self) -> dict:
            return {"processes": []}

    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    monkeypatch.setattr(runtime, "AGENT_UID", target.stat().st_uid)
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = Store()
    broker.processes = Processes()
    broker._scrubbers = {}

    assert broker.op_clear_action_materialization({
        "profile": "local", "action_name": "deploy",
    }) == {"profile": "local", "action": "deploy", "cleared": True}
    assert not target.exists()


def test_clear_action_materialization_rejects_symlink(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    workspace = tmp_path / "workspace"
    target = workspace / "app/deploy/local/.env.local"
    target.parent.mkdir(parents=True)
    outside = tmp_path / "outside"
    outside.write_text("keep\n", encoding="utf-8")
    target.symlink_to(outside)
    action = {
        "cwd": "app",
        "materialize_env_file": "LOCAL_DEPLOY_RUNTIME_ENV",
        "materialize_env_path": "deploy/local/.env.local",
    }

    class Store:
        def load(self) -> dict:
            return {"profiles": {"local": {"secrets": {}, "actions": {"deploy": action}}}}

    class Processes:
        def list(self) -> dict:
            return {"processes": []}

    monkeypatch.setattr(runtime, "WORKSPACE_ROOT", workspace)
    broker = RuntimeController.__new__(RuntimeController)
    broker.store = Store()
    broker.processes = Processes()
    broker._scrubbers = {}
    with pytest.raises(LokiRuntimeError, match="unsafe"):
        broker.op_clear_action_materialization({
            "profile": "local", "action_name": "deploy",
        })
    assert outside.read_text(encoding="utf-8") == "keep\n"


def test_store_rejects_partial_initialization(tmp_path: Path) -> None:
    key_path = tmp_path / "master.key"
    store_path = tmp_path / "store.json"
    key_path.write_bytes(b"partial")
    store = EncryptedStateStore(key_path, store_path)
    with pytest.raises(LokiRuntimeError, match="partially initialized"):
        store.initialize()


def test_runtime_service_can_stop_runner_processes() -> None:
    service = (
        Path(__file__).parents[1] / "systemd" / "loki-runtime.service"
    ).read_text(encoding="utf-8")
    assert "CapabilityBoundingSet=CAP_SETUID CAP_SETGID CAP_KILL" in service
    assert "AmbientCapabilities=CAP_SETUID CAP_SETGID CAP_KILL" in service
