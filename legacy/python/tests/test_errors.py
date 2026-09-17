import subprocess

import pytest
from mcp.server.mcpserver.exceptions import ToolError

from loki_mcp.errors import tool_error_boundary


@pytest.mark.parametrize(
    ("error", "message"),
    [
        (FileNotFoundError("/host/secret"), "requested path or required executable was not found"),
        (FileExistsError("/host/secret"), "destination already exists"),
        (PermissionError("/host/secret"), "workspace permissions denied"),
        (TimeoutError("details"), "operation timed out"),
        (ProcessLookupError("details"), "target process no longer exists"),
        (ConnectionError("details"), "required local service or remote endpoint is unavailable"),
        (UnicodeDecodeError("utf-8", b"x", 0, 1, "details"), "content encoding is invalid"),
        (subprocess.CalledProcessError(1, ["secret-command"]), "external command failed"),
        (OSError("/host/secret"), "operating system could not complete"),
    ],
)
def test_expected_errors_become_safe_tool_errors(error: Exception, message: str) -> None:
    @tool_error_boundary
    def fail() -> None:
        raise error

    with pytest.raises(ToolError, match=message) as captured:
        fail()
    assert "/host/secret" not in str(captured.value)
    assert "secret-command" not in str(captured.value)


def test_value_error_keeps_bounded_actionable_context() -> None:
    @tool_error_boundary
    def fail() -> None:
        raise ValueError("expected 2 replacements, found 1")

    with pytest.raises(ToolError, match="invalid request: expected 2 replacements, found 1"):
        fail()


def test_unexpected_programming_error_remains_redacted_by_mcp() -> None:
    @tool_error_boundary
    def fail() -> None:
        raise AttributeError("sensitive internal detail")

    with pytest.raises(AttributeError, match="sensitive internal detail"):
        fail()
