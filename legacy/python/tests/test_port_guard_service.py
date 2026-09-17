from pathlib import Path

import pytest

from loki_mcp import port_guard_service


def test_port_validation_protects_system_and_loki_ports() -> None:
    assert port_guard_service._validate_port(5173) == 5173
    with pytest.raises(port_guard_service.PortGuardError):
        port_guard_service._validate_port(80)
    with pytest.raises(port_guard_service.PortGuardError):
        port_guard_service._validate_port(8765)


def test_inspect_port_hides_internal_process_identity(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(port_guard_service, "_listeners", lambda port: [{
        "pid": 123,
        "start_time": 456,
        "cwd": Path("/srv/workspace/loki/project"),
        "command": "node",
        "local_address": "127.0.0.1:5173",
    }])
    result = port_guard_service.inspect_port(5173)
    assert result["in_use"] is True
    assert result["listeners"][0]["pid"] == 123
    assert "start_time" not in result["listeners"][0]


def test_workspace_accepts_host_paths_and_sandbox_paths_only_in_loki_cgroup(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    assert port_guard_service._inside_workspace(123, Path("/srv/workspace/loki/project")) is True

    original_read_text = Path.read_text

    def loki_cgroup(path: Path, *args: object, **kwargs: object) -> str:
        if path == Path("/proc/123/cgroup"):
            return "0::/system.slice/loki-mcp.service\n"
        return original_read_text(path, *args, **kwargs)

    monkeypatch.setattr(Path, "read_text", loki_cgroup)
    assert port_guard_service._inside_workspace(123, Path("/workspace/project")) is True
    assert port_guard_service._inside_workspace(123, Path("/tmp/project")) is False


def test_runtime_cgroup_is_managed(monkeypatch: pytest.MonkeyPatch) -> None:
    original_read_text = Path.read_text

    def secret_broker_cgroup(path: Path, *args: object, **kwargs: object) -> str:
        if path == Path("/proc/321/cgroup"):
            return "0::/system.slice/loki-runtime.service\n"
        return original_read_text(path, *args, **kwargs)

    monkeypatch.setattr(Path, "read_text", secret_broker_cgroup)
    assert port_guard_service._in_loki_cgroup(321) is True
    assert port_guard_service._listener_is_allowed(
        321, 65534, Path("/srv/workspace/loki/stamp.is-web")
    ) is True


def test_action_scope_is_managed(monkeypatch: pytest.MonkeyPatch) -> None:
    original_read_text = Path.read_text

    def action_scope(path: Path, *args: object, **kwargs: object) -> str:
        if path == Path("/proc/654/cgroup"):
            return "0::/system.slice/loki-action-cfeaa8bdbedf353e.scope\n"
        return original_read_text(path, *args, **kwargs)

    monkeypatch.setattr(Path, "read_text", action_scope)
    assert port_guard_service._in_loki_cgroup(654) is True
    assert port_guard_service._listener_is_allowed(
        654, 65534, Path("/workspace/stamp.is-web/apps/storefront")
    ) is True


def test_similarly_named_unmanaged_scope_is_rejected(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    original_read_text = Path.read_text

    def unmanaged_scope(path: Path, *args: object, **kwargs: object) -> str:
        if path == Path("/proc/655/cgroup"):
            return "0::/user.slice/loki-action-not-a-token.scope\n"
        return original_read_text(path, *args, **kwargs)

    monkeypatch.setattr(Path, "read_text", unmanaged_scope)
    assert port_guard_service._in_loki_cgroup(655) is False


def test_listener_owner_uses_uid_for_host_and_cgroup_for_user_namespace(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    runner_uid = 1000
    monkeypatch.setattr(port_guard_service.os, "getuid", lambda: runner_uid)
    monkeypatch.setattr(
        port_guard_service,
        "_inside_workspace",
        lambda pid, cwd: pid == 123 and cwd == Path("/workspace/project"),
    )
    monkeypatch.setattr(port_guard_service, "_in_loki_cgroup", lambda pid: pid == 123)

    assert port_guard_service._listener_is_allowed(123, runner_uid, Path("/srv/workspace/loki/project")) is True
    assert port_guard_service._listener_is_allowed(123, 65534, Path("/srv/workspace/loki/project")) is True
    assert port_guard_service._listener_is_allowed(456, 65534, Path("/srv/workspace/loki/project")) is False
    assert port_guard_service._listener_is_allowed(123, 65534, Path("/workspace/project")) is True
    assert port_guard_service._listener_is_allowed(456, 65534, Path("/workspace/project")) is False


def test_listener_inspection_retries_transient_proc_race(monkeypatch: pytest.MonkeyPatch) -> None:
    attempts = 0

    def inspect(_: int) -> list[dict[str, object]]:
        nonlocal attempts
        attempts += 1
        if attempts < 3:
            raise port_guard_service.PortGuardError(
                "listener details are unavailable or owned by another user (0/1 sockets matched)"
            )
        return [{"pid": 123}]

    monkeypatch.setattr(port_guard_service, "_listeners_once", inspect)
    monkeypatch.setattr(port_guard_service.time, "sleep", lambda _: None)
    assert port_guard_service._listeners(5173) == [{"pid": 123}]
    assert attempts == 3
