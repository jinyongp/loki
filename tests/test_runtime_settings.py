from pathlib import Path

import pytest

from loki_mcp.runtime_settings import (
    ActionProcessLimits,
    load_action_process_limits,
    update_action_process_limit,
)


def test_action_process_limits_default_to_twelve_and_six(tmp_path: Path) -> None:
    assert load_action_process_limits(tmp_path / "missing.toml") == ActionProcessLimits(
        total=12,
        per_profile=6,
    )


def test_action_process_limit_update_preserves_toml_tables(tmp_path: Path) -> None:
    path = tmp_path / "config.toml"
    path.write_text('root = "/workspace"\n\n[executables]\nnode = "/usr/bin/node"\n')

    limits = update_action_process_limit(path, "max-action-processes", 16)
    limits = update_action_process_limit(
        path, "max-action-processes-per-profile", 8,
    )

    assert limits == ActionProcessLimits(total=16, per_profile=8)
    assert load_action_process_limits(path) == limits
    assert path.read_text().index("max_action_processes = 16") < path.read_text().index(
        "[executables]"
    )


def test_action_process_limits_reject_invalid_relationship(tmp_path: Path) -> None:
    path = tmp_path / "config.toml"
    path.write_text(
        "max_action_processes = 12\nmax_action_processes_per_profile = 6\n"
    )
    with pytest.raises(ValueError, match="must not exceed"):
        update_action_process_limit(path, "max-action-processes", 5)
    assert load_action_process_limits(path) == ActionProcessLimits(total=12, per_profile=6)


@pytest.mark.parametrize("value", [0, 33, True])
def test_action_process_limits_reject_out_of_range_values(
    tmp_path: Path, value: object,
) -> None:
    path = tmp_path / "config.toml"
    path.write_text(
        f"max_action_processes = {str(value).lower()}\n"
        "max_action_processes_per_profile = 6\n"
    )
    with pytest.raises(ValueError, match="must be"):
        load_action_process_limits(path)
