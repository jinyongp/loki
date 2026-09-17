from unittest.mock import Mock, patch
import subprocess
from loki_mcp.processes import ManagedProcess


def managed():
    return ManagedProcess(
        session_id="test", name="test", process=Mock(), started_at=0,
        max_output_bytes=4096, timeout_seconds=86400,
        metadata={"systemd_unit": "loki-action-test.scope"},
    )


def test_oom_is_reported_from_systemd_not_exit_signal():
    item = managed()
    item.process.returncode = -15
    with patch("loki_mcp.processes.subprocess.run") as run:
        run.return_value = subprocess.CompletedProcess([], 0, "ActiveState=failed\nResult=oom-kill\n")
        item.mark_complete()
    assert item.metadata["oom_killed"] is True
    assert item.metadata["termination_reason"] == "oom"
    assert run.call_args_list[-1].args[0][1] == "reset-failed"


def test_sigterm_without_oom_is_not_misclassified():
    item = managed()
    item.process.returncode = -15
    with patch("loki_mcp.processes.subprocess.run") as run:
        run.return_value = subprocess.CompletedProcess([], 0, "ActiveState=inactive\nResult=success\n")
        item.mark_complete()
    assert item.metadata["oom_killed"] is False
    assert "termination_reason" not in item.metadata
    assert run.call_count == 1


def test_missing_systemd_result_does_not_break_completion():
    item = managed()
    with patch("loki_mcp.processes.subprocess.run", side_effect=OSError):
        item.mark_complete()
    assert item.completed_at is not None
    assert "oom_killed" not in item.metadata
