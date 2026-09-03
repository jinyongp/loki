from pathlib import Path
import subprocess

import pytest

from loki_mcp.policy import PolicyError
from loki_mcp.project_state import ProjectStateStore


def _repository(tmp_path: Path) -> tuple[Path, Path]:
    workspace = tmp_path / "workspace"
    repository = workspace / "project"
    worktree = workspace / "feature"
    repository.mkdir(parents=True)
    subprocess.run(["git", "init", "-q", str(repository)], check=True)
    subprocess.run(["git", "-C", str(repository), "config", "user.name", "Test"], check=True)
    subprocess.run([
        "git", "-C", str(repository), "config", "user.email", "test@example.invalid",
    ], check=True)
    (repository / "tracked.txt").write_text("base\n", encoding="utf-8")
    subprocess.run(["git", "-C", str(repository), "add", "tracked.txt"], check=True)
    subprocess.run(["git", "-C", str(repository), "commit", "-qm", "initial"], check=True)
    subprocess.run([
        "git", "-C", str(repository), "worktree", "add", "-q", "-b", "feature",
        str(worktree),
    ], check=True)
    return repository, worktree


def test_worktrees_share_project_state_but_keep_separate_bindings(tmp_path: Path) -> None:
    repository, worktree = _repository(tmp_path)
    store = ProjectStateStore(repository.parent, tmp_path / "state")
    initialized = store.initialize_workstream(
        repository,
        goal="Share project state safely across worktrees",
        slug_base="share-project-state",
        slug=None,
        depth="standard",
        intent_source_kind="plan-local",
        intent_source=None,
    )
    slug = initialized["slug"]

    assert store.status(repository)["project_id"] == store.status(worktree)["project_id"]
    assert store.status(repository)["active_workstream"] == slug
    assert store.status(worktree)["active_workstream"] is None
    store.bind_workstream(worktree, slug)
    assert store.status(worktree)["active_workstream"] == slug


def test_artifact_updates_require_current_revision(tmp_path: Path) -> None:
    repository, _ = _repository(tmp_path)
    store = ProjectStateStore(repository.parent, tmp_path / "state")
    initialized = store.initialize_workstream(
        repository, goal="Protect concurrent artifact updates",
        slug_base="protect-artifact-updates", slug=None, depth="standard",
        intent_source_kind="plan-local", intent_source=None,
    )
    slug = initialized["slug"]
    created = store.write_artifact(repository, slug, "plan.md", "first\n", None)
    store.write_artifact(
        repository, slug, "plan.md", "second\n", created["sha256"],
    )
    with pytest.raises(PolicyError, match="changed since"):
        store.write_artifact(
            repository, slug, "plan.md", "stale\n", created["sha256"],
        )


def test_legacy_migration_preserves_tasks_and_workstreams(tmp_path: Path) -> None:
    repository, worktree = _repository(tmp_path)
    legacy = repository / ".tasks"
    (legacy / "data").mkdir(parents=True)
    (legacy / "data" / "taskchampion.sqlite3").write_bytes(b"task-data")
    (legacy / "items" / "existing-workstream").mkdir(parents=True)
    (legacy / "items" / "existing-workstream" / "manifest.json").write_text(
        '{"slug":"existing-workstream"}\n', encoding="utf-8",
    )
    (legacy / "taskrc").write_text(
        "data.location=.tasks/data\nconfirmation=1\n", encoding="utf-8",
    )
    state_root = tmp_path / "state"
    store = ProjectStateStore(repository.parent, state_root)

    result = store.migrate_legacy(repository)

    assert result["legacy_removed"] is True
    assert not legacy.exists()
    assert store.status(worktree)["initialized"] is True
    destination = state_root / "projects" / result["project_id"]
    assert (destination / "taskwarrior" / "data" / "taskchampion.sqlite3").read_bytes() == b"task-data"
    assert (destination / "workstreams" / "existing-workstream" / "manifest.json").is_file()
    assert f"data.location={destination}/taskwarrior/data" in (
        destination / "taskwarrior" / "taskrc"
    ).read_text(encoding="utf-8")


def test_legacy_migration_retains_repository_local_outputs(tmp_path: Path) -> None:
    repository, _ = _repository(tmp_path)
    legacy = repository / ".tasks"
    (legacy / "data").mkdir(parents=True)
    (legacy / "items").mkdir()
    (legacy / "verification").mkdir()
    evidence = legacy / "verification" / "latest.json"
    evidence.write_text('{"status":"passed"}\n', encoding="utf-8")
    store = ProjectStateStore(repository.parent, tmp_path / "state")

    result = store.migrate_legacy(repository)

    assert result["legacy_removed"] is False
    assert result["worktree_local_entries_retained"] == ["verification"]
    assert evidence.read_text(encoding="utf-8") == '{"status":"passed"}\n'
    assert not (legacy / "data").exists()
    assert not (legacy / "items").exists()
