from __future__ import annotations

from collections.abc import Awaitable, Callable
from functools import wraps
from typing import Any, ParamSpec, TypeVar, cast
import errno
import inspect
import re
import subprocess

from mcp.server.mcpserver.exceptions import ToolError


P = ParamSpec("P")
R = TypeVar("R")
MAX_ERROR_DETAIL = 500


class OperationError(ToolError):
    """An anticipated operational failure with a safe, actionable message."""


def _bounded_detail(error: ValueError) -> str:
    detail = re.sub(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]+", " ", str(error))
    detail = " ".join(detail.split())
    return detail[:MAX_ERROR_DETAIL] or "invalid request"


def expected_tool_error(error: Exception) -> ToolError | None:
    """Translate anticipated runtime failures without exposing host details."""
    if isinstance(error, ToolError):
        return error
    if isinstance(error, FileNotFoundError):
        return ToolError("requested path or required executable was not found; verify the path and installed tool")
    if isinstance(error, FileExistsError):
        return ToolError("destination already exists; choose another path or explicitly allow overwrite")
    if isinstance(error, NotADirectoryError):
        return ToolError("a directory was required at the requested path")
    if isinstance(error, IsADirectoryError):
        return ToolError("a regular file was required at the requested path")
    if isinstance(error, PermissionError):
        return ToolError("workspace permissions denied the operation; verify ownership and writable scope")
    if isinstance(error, (subprocess.TimeoutExpired, TimeoutError)):
        return ToolError("operation timed out; narrow the request or increase its timeout")
    if isinstance(error, ProcessLookupError):
        return ToolError("the target process no longer exists; refresh process or port state")
    if isinstance(error, ConnectionError):
        return ToolError("a required local service or remote endpoint is unavailable; retry after checking connectivity")
    if isinstance(error, UnicodeError):
        return ToolError("content encoding is invalid; use valid UTF-8 text or a supported binary tool")
    if isinstance(error, subprocess.CalledProcessError):
        return ToolError("an external command failed before Loki could return its normal exit result")
    if isinstance(error, ValueError):
        return ToolError(f"invalid request: {_bounded_detail(error)}")
    if isinstance(error, OSError):
        messages = {
            errno.ENOSPC: "workspace storage is full; free space and retry",
            errno.EROFS: "the target is read-only; write inside the workspace",
            errno.EBUSY: "the requested resource is busy; stop the using process and retry",
            errno.ENAMETOOLONG: "the requested path or filename is too long",
            errno.EMFILE: "the process has too many open files; close sessions and retry",
        }
        return ToolError(messages.get(error.errno, "the operating system could not complete the operation; check workspace state and retry"))
    return None


def tool_error_boundary(function: Callable[P, R]) -> Callable[P, R]:
    """Expose anticipated failures as MCP ToolError while retaining crash redaction."""
    if inspect.iscoroutinefunction(function):
        @wraps(function)
        async def async_wrapper(*args: P.args, **kwargs: P.kwargs) -> Any:
            try:
                return await cast(Callable[P, Awaitable[Any]], function)(*args, **kwargs)
            except Exception as error:
                translated = expected_tool_error(error)
                if translated is None:
                    raise
                raise translated from error

        return cast(Callable[P, R], async_wrapper)

    @wraps(function)
    def wrapper(*args: P.args, **kwargs: P.kwargs) -> R:
        try:
            return function(*args, **kwargs)
        except Exception as error:
            translated = expected_tool_error(error)
            if translated is None:
                raise
            raise translated from error

    return wrapper
