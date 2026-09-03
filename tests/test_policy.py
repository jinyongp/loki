from pathlib import Path
import os

import pytest

from loki_mcp.policy import PolicyError, WorkspacePolicy, atomic_write


def test_rejects_absolute_parent_and_denied_paths(tmp_path: Path) -> None:
    policy = WorkspacePolicy(tmp_path)
    for requested in ("/etc/passwd", "../escape", ".git/config", ".env", "secret.pem"):
        with pytest.raises(PolicyError):
            policy.resolve(requested, must_exist=False)


def test_resolve_cwd_accepts_only_sandbox_workspace_absolute_paths(tmp_path: Path) -> None:
    project = tmp_path / "project"
    project.mkdir()
    policy = WorkspacePolicy(tmp_path)
    assert policy.resolve_cwd("project") == project
    assert policy.resolve_cwd("/workspace/project") == project
    with pytest.raises(PolicyError, match="absolute cwd must be within /workspace"):
        policy.resolve_cwd("/srv/workspace/loki/project")


def test_rejects_symlink_escape(tmp_path: Path) -> None:
    outside = tmp_path.parent / "outside.txt"
    outside.write_text("secret", encoding="utf-8")
    os.symlink(outside, tmp_path / "link")
    policy = WorkspacePolicy(tmp_path)
    with pytest.raises(PolicyError):
        policy.resolve("link")


def test_atomic_write_replaces_regular_file(tmp_path: Path) -> None:
    target = tmp_path / "note.txt"
    target.write_text("old", encoding="utf-8")
    atomic_write(target, "new")
    assert target.read_text(encoding="utf-8") == "new"


def test_atomic_write_can_protect_existing_file(tmp_path: Path) -> None:
    target = tmp_path / "existing.txt"
    target.write_text("old", encoding="utf-8")
    with pytest.raises(FileExistsError):
        atomic_write(target, "new", overwrite=False)
    assert target.read_text(encoding="utf-8") == "old"
