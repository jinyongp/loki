from pathlib import Path
from types import SimpleNamespace
import os

import pytest

from loki_mcp.policy import PolicyError, WorkspacePolicy
from loki_mcp.processes import ProcessManager


pytestmark = pytest.mark.skipif(os.name == "nt", reason="process groups require POSIX")


def _manager(tmp_path: Path, total: int) -> ProcessManager:
    config = SimpleNamespace(
        max_processes=total,
        process_retention_seconds=60,
        max_output_bytes=65_536,
        processes={},
        checks={},
    )
    return ProcessManager(config, WorkspacePolicy(tmp_path))  # type: ignore[arg-type]


def _start(manager: ProcessManager, tmp_path: Path, group: str, per_group: int) -> None:
    manager.start_command(
        name=f"{group}/test",
        command=["/bin/sh", "-c", "sleep 30"],
        cwd=tmp_path,
        environment={"PATH": "/usr/bin:/bin"},
        timeout_seconds=60,
        max_output_bytes=65_536,
        group=group,
        max_group_processes=per_group,
    )


def test_singleton_process_reuses_running_session(tmp_path: Path) -> None:
    manager = _manager(tmp_path, 8)
    first = manager.start_command(
        name="api", command=["/bin/sh", "-c", "sleep 30"], cwd=tmp_path,
        environment={}, timeout_seconds=60, max_output_bytes=4096,
        instance_key="worktree-api", metadata={"port": 32180},
    )
    try:
        second = manager.start_command(
            name="api", command=["/bin/sh", "-c", "sleep 30"], cwd=tmp_path,
            environment={}, timeout_seconds=60, max_output_bytes=4096,
            instance_key="worktree-api", metadata={"port": 32181},
        )
        assert second["session_id"] == first["session_id"]
        assert second["reused"] is True
        assert second["port"] == 32180
    finally:
        manager.stop(str(first["session_id"]))


def test_profile_limit_is_atomic_and_does_not_stop_existing_processes(tmp_path: Path) -> None:
    manager = _manager(tmp_path, total=4)
    try:
        _start(manager, tmp_path, "web", 2)
        _start(manager, tmp_path, "web", 2)
        with pytest.raises(PolicyError, match=r"web \(2/2; total 2/4\)"):
            _start(manager, tmp_path, "web", 2)
        assert manager.usage() == {"total": 2, "groups": {"web": 2}}
    finally:
        manager.close()


def test_total_limit_reports_total_and_profile_usage(tmp_path: Path) -> None:
    manager = _manager(tmp_path, total=2)
    try:
        _start(manager, tmp_path, "api", 2)
        _start(manager, tmp_path, "web", 2)
        with pytest.raises(PolicyError, match=r"total 2/2, profile web 1/2"):
            _start(manager, tmp_path, "web", 2)
        assert manager.usage() == {"total": 2, "groups": {"api": 1, "web": 1}}
    finally:
        manager.close()
