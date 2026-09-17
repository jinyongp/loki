from __future__ import annotations

from importlib.util import module_from_spec, spec_from_file_location
from pathlib import Path

def _cli_module():
    script = Path(__file__).parents[1] / "scripts" / "loki.py"
    spec = spec_from_file_location("loki_cli", script)
    assert spec is not None and spec.loader is not None
    module = module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_action_set_accepts_options_after_action_name() -> None:
    args = _cli_module()._parser().parse_args([
        "action", "set", "sample-local", "serve",
        "--cwd", "sample", "--all-secrets",
        "--materialize-env-file", "LOCAL_DEPLOY_RUNTIME_ENV",
        "--materialize-env-path", "deploy/local/.env.local", "--docker",
        "--public-env", "PUBLIC_BACKEND_URL",
        "--", "just", "local", "deploy",
    ])
    assert args.profile == "sample-local"
    assert args.name == "serve"
    assert args.cwd == "sample"
    assert args.all_secrets is True
    assert args.materialize_env_file == "LOCAL_DEPLOY_RUNTIME_ENV"
    assert args.materialize_env_path == "deploy/local/.env.local"
    assert args.docker is True
    assert args.public_env == ["PUBLIC_BACKEND_URL"]
    assert args.argv == ["just", "local", "deploy"]


def test_bootstrap_accepts_arbitrary_registered_project_workflow() -> None:
    parser = _cli_module()._parser()
    default = parser.parse_args(["bootstrap", "sample"])
    explicit = parser.parse_args(["bootstrap", "worktrees/sample-a", "isolated"])
    assert (default.cwd, default.workflow) == ("sample", "development")
    assert (explicit.cwd, explicit.workflow) == ("worktrees/sample-a", "isolated")


def test_action_run_accepts_workspace_relative_worktree() -> None:
    args = _cli_module()._parser().parse_args([
        "action", "run", "sample-local", "serve",
        "--cwd", "worktrees/sample-feature",
    ])
    assert args.cwd == "worktrees/sample-feature"


def test_config_set_accepts_action_process_limits() -> None:
    parser = _cli_module()._parser()
    total = parser.parse_args(["config", "set", "max-action-processes", "12"])
    profile = parser.parse_args([
        "config", "set", "max-action-processes-per-profile", "6",
    ])
    assert (total.name, total.value) == ("max-action-processes", 12)
    assert (profile.name, profile.value) == (
        "max-action-processes-per-profile", 6,
    )


def test_state_cli_accepts_workspace_relative_project() -> None:
    parser = _cli_module()._parser()
    assert parser.parse_args(["state", "status", "sample"]).cwd == "sample"
    assert parser.parse_args(["state", "migrate", "sample"]).state_command == "migrate"


def test_stage_env_creates_opaque_root_only_import(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    module = _cli_module()
    inbox = tmp_path / "inbox"
    source = tmp_path / "secrets.env"
    source.write_text("TOKEN=value\nEMPTY=\n", encoding="utf-8")
    monkeypatch.setattr(module, "INBOX_DIRECTORY", inbox)
    monkeypatch.setattr(module.os, "geteuid", lambda: 0)

    result = module._stage_env(source, True)

    staged = inbox / f"{result['import_id']}.env"
    assert result["secret_names"] == ["EMPTY", "TOKEN"]
    assert result["source_deleted"] is True
    assert not source.exists()
    assert staged.read_text(encoding="utf-8") == "TOKEN=value\nEMPTY=\n"
    assert staged.stat().st_mode & 0o777 == 0o600
