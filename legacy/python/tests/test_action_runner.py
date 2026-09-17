from pathlib import Path
from importlib.machinery import SourceFileLoader
from types import ModuleType


def test_docker_proxy_environment_is_forced_after_secret_injection() -> None:
    source = (
        Path(__file__).parents[1] / "scripts" / "loki-action-runner.py"
    ).read_text(encoding="utf-8")
    secret_injection = source.index("for name in args.env:")
    access_mode = source.index(
        'environment["DOCKER_ACCESS_MODE"] = "restricted-proxy"'
    )
    docker_host = source.index('environment["DOCKER_HOST"] = f"unix://{docker_socket}"')

    assert secret_injection < access_mode < docker_host


def test_docker_actions_execute_from_the_host_visible_workspace_path() -> None:
    source = (
        Path(__file__).parents[1] / "scripts" / "loki-action-runner.py"
    ).read_text(encoding="utf-8")

    assert 'docker_host_cwd = Path("/srv/workspace/loki", *relative_cwd.parts)' in source
    assert "execution_cwd = docker_host_cwd" in source
    assert '"--bind", str(docker_host_cwd), str(execution_cwd)' in source
    assert '"--chdir", str(execution_cwd)' in source


def test_docker_materialization_uses_the_host_visible_namespace() -> None:
    source = (
        Path(__file__).parents[1] / "scripts" / "loki-action-runner.py"
    ).read_text(encoding="utf-8")

    assert "environment[args.materialize_env_file] = str(placeholder)" in source
    assert '"--ro-bind", str(session_directory / "runtime.env"), str(placeholder)' in source


def test_materialization_marker_recovers_after_crash(
    tmp_path: Path,
) -> None:
    source = Path(__file__).parents[1] / "scripts" / "loki-action-runner.py"
    module = ModuleType("loki_secret_runner_test")
    SourceFileLoader(module.__name__, str(source)).exec_module(module)
    module.MATERIALIZATION_ROOT = tmp_path / "materializations"
    module.MATERIALIZATION_ROOT.mkdir()
    target = tmp_path / ".env.local"

    marker = module._claim_materialization(target)
    target.write_text("SECRET=value\n", encoding="utf-8")
    target.chmod(0o600)
    marker.write_text(
        '{"pid":99999999,"target":' + __import__("json").dumps(str(target)) + '}\n',
        encoding="utf-8",
    )
    module._pid_alive = lambda _: False
    module._recover_materialization(target, marker)

    assert not target.exists()
    assert not marker.exists()
