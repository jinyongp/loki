from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
import os
import re
import tempfile
import tomllib


CONFIG_PATH = Path(os.environ.get("LOKI_MCP_CONFIG", "/etc/loki/config.toml"))
DEFAULT_MAX_ACTION_PROCESSES = 12
DEFAULT_MAX_ACTION_PROCESSES_PER_PROFILE = 6
MIN_ACTION_PROCESSES = 1
MAX_ACTION_PROCESSES = 32
ACTION_PROCESS_SETTING_NAMES = {
    "max-action-processes": "max_action_processes",
    "max-action-processes-per-profile": "max_action_processes_per_profile",
}


@dataclass(frozen=True)
class ActionProcessLimits:
    total: int
    per_profile: int


def load_action_process_limits(path: Path = CONFIG_PATH) -> ActionProcessLimits:
    try:
        with path.open("rb") as handle:
            raw = tomllib.load(handle)
    except FileNotFoundError:
        raw = {}
    total = _bounded_integer(
        raw.get("max_action_processes", DEFAULT_MAX_ACTION_PROCESSES),
        "max_action_processes",
    )
    per_profile = _bounded_integer(
        raw.get("max_action_processes_per_profile", DEFAULT_MAX_ACTION_PROCESSES_PER_PROFILE),
        "max_action_processes_per_profile",
    )
    if per_profile > total:
        raise ValueError("max_action_processes_per_profile must not exceed max_action_processes")
    return ActionProcessLimits(total=total, per_profile=per_profile)


def update_action_process_limit(path: Path, cli_name: str, value: int) -> ActionProcessLimits:
    key = ACTION_PROCESS_SETTING_NAMES.get(cli_name)
    if key is None:
        raise ValueError("unknown action process setting")
    _bounded_integer(value, key)
    original = path.read_text(encoding="utf-8")
    line = f"{key} = {value}"
    pattern = re.compile(rf"(?m)^{re.escape(key)}\s*=.*$")
    updated = pattern.sub(line, original, count=1)
    if updated == original:
        lines = original.splitlines(keepends=True)
        table_index = next(
            (index for index, current in enumerate(lines) if current.lstrip().startswith("[")),
            len(lines),
        )
        lines.insert(table_index, f"{line}\n")
        updated = "".join(lines)
    parsed = tomllib.loads(updated)
    limits = ActionProcessLimits(
        total=_bounded_integer(
            parsed.get("max_action_processes", DEFAULT_MAX_ACTION_PROCESSES),
            "max_action_processes",
        ),
        per_profile=_bounded_integer(
            parsed.get(
                "max_action_processes_per_profile",
                DEFAULT_MAX_ACTION_PROCESSES_PER_PROFILE,
            ),
            "max_action_processes_per_profile",
        ),
    )
    if limits.per_profile > limits.total:
        raise ValueError("max_action_processes_per_profile must not exceed max_action_processes")
    metadata = path.stat()
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as handle:
            handle.write(updated)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, metadata.st_mode & 0o777)
        if hasattr(os, "chown"):
            os.chown(temporary, metadata.st_uid, metadata.st_gid)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)
    return limits


def _bounded_integer(value: object, name: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"{name} must be an integer")
    if not MIN_ACTION_PROCESSES <= value <= MAX_ACTION_PROCESSES:
        raise ValueError(
            f"{name} must be between {MIN_ACTION_PROCESSES} and {MAX_ACTION_PROCESSES}"
        )
    return value
