#!/opt/loki-mcp/venv/bin/python
from __future__ import annotations

from pathlib import Path
import argparse
import getpass
import json
import os
import secrets
import subprocess
import sys

from loki_mcp.runtime import (
    INBOX_DIRECTORY,
    MAX_INBOX_BYTES,
    LokiRuntimeError,
    parse_dotenv,
    request_runtime,
)
from loki_mcp.runtime_settings import (
    CONFIG_PATH,
    ACTION_PROCESS_SETTING_NAMES,
    load_action_process_limits,
    update_action_process_limit,
)
from loki_mcp.project_state import HOST_WORKSPACE_ROOT, ProjectStateStore, STATE_ROOT


def _request(operation: str, **values: object) -> dict[str, object]:
    return request_runtime({"operation": operation, **values})


def _dotenv(path: Path) -> dict[str, str]:
    return parse_dotenv(path.read_text(encoding="utf-8"))


def _stage_env(path: Path, delete_source: bool) -> dict[str, object]:
    if os.geteuid() != 0:
        raise ValueError("staging a dotenv file requires sudo")
    source = path.expanduser()
    if source.is_symlink():
        raise ValueError("dotenv source must not be a symbolic link")
    source = source.resolve(strict=True)
    if not source.is_file():
        raise ValueError("dotenv source must be a regular file")
    raw = source.read_bytes()
    if not 0 < len(raw) <= MAX_INBOX_BYTES:
        raise ValueError("dotenv source is empty or too large")
    try:
        values = parse_dotenv(raw.decode("utf-8"))
    except UnicodeDecodeError as error:
        raise ValueError("dotenv source must be UTF-8") from error
    INBOX_DIRECTORY.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(INBOX_DIRECTORY, 0o700)
    import_id = secrets.token_hex(16)
    destination = INBOX_DIRECTORY / f"{import_id}.env"
    descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        written = 0
        while written < len(raw):
            written += os.write(descriptor, raw[written:])
        os.fsync(descriptor)
    except Exception:
        destination.unlink(missing_ok=True)
        raise
    finally:
        os.close(descriptor)
    if delete_source:
        source.unlink()
    return {
        "import_id": import_id,
        "secret_names": sorted(values),
        "count": len(values),
        "source_deleted": delete_source,
    }


def _print(result: object) -> None:
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))


def _pair(value: str, label: str) -> list[str]:
    parts = value.split("/", 1)
    if len(parts) != 2 or not all(parts):
        raise ValueError(f"{label} must use PROFILE/NAME")
    return parts


def _required_secret_map(values: list[str]) -> dict[str, list[str]]:
    result: dict[str, list[str]] = {}
    for value in values:
        profile, name = _pair(value, "required secret")
        result.setdefault(profile, []).append(name)
    return result


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="loki")
    parser.add_argument("--version", action="store_true")
    commands = parser.add_subparsers(dest="command")
    bootstrap = commands.add_parser("bootstrap", help="run one registered project workflow")
    bootstrap.add_argument("cwd")
    bootstrap.add_argument("workflow", nargs="?", default="development")
    config = commands.add_parser("config", help="inspect or change Loki runtime settings")
    config_commands = config.add_subparsers(dest="config_command", required=True)
    config_commands.add_parser("show")
    config_set = config_commands.add_parser("set")
    config_set.add_argument("name", choices=tuple(ACTION_PROCESS_SETTING_NAMES))
    config_set.add_argument("value", type=int)
    state = commands.add_parser("state", help="inspect or migrate shared project state")
    state_commands = state.add_subparsers(dest="state_command", required=True)
    for operation in ("status", "migrate"):
        item = state_commands.add_parser(operation)
        item.add_argument("cwd")
    project = commands.add_parser("project", help="manage central project workflows")
    project_commands = project.add_subparsers(dest="project_command", required=True)
    project_register = project_commands.add_parser("register")
    project_register.add_argument("cwd")
    project_register.add_argument("--name")
    project_unregister = project_commands.add_parser("unregister")
    project_unregister.add_argument("cwd")
    project_status = project_commands.add_parser("status")
    project_status.add_argument("cwd")
    workflow = project_commands.add_parser("workflow")
    workflow_commands = workflow.add_subparsers(dest="workflow_command", required=True)
    workflow_set = workflow_commands.add_parser("set")
    workflow_set.add_argument("cwd")
    workflow_set.add_argument("name")
    workflow_set.add_argument("--step", action="append", required=True, metavar="PROFILE/ACTION")
    workflow_set.add_argument("--require", action="append", default=[], metavar="PROFILE/SECRET")
    workflow_set.add_argument("--timeout-seconds", type=int, default=3600)
    workflow_remove = workflow_commands.add_parser("remove")
    workflow_remove.add_argument("cwd")
    workflow_remove.add_argument("name")
    secret = commands.add_parser("secret", help="manage encrypted Loki secret profiles")
    secret_commands = secret.add_subparsers(dest="secret_command", required=True)
    secret_commands.add_parser("init")
    secret_commands.add_parser("status")
    secret_commands.add_parser("list")

    profile = secret_commands.add_parser("profile")
    profile_commands = profile.add_subparsers(dest="profile_command", required=True)
    for operation in ("create", "remove", "show"):
        item = profile_commands.add_parser(operation)
        item.add_argument("profile")

    import_env = secret_commands.add_parser("import-env")
    import_env.add_argument("profile")
    import_env.add_argument("file", type=Path)
    import_env.add_argument("--delete-source", action="store_true")

    stage_env = secret_commands.add_parser("stage-env")
    stage_env.add_argument("file", type=Path)
    stage_env.add_argument("--delete-source", action="store_true")

    set_secret = secret_commands.add_parser("set")
    set_secret.add_argument("profile")
    set_secret.add_argument("name")
    generate_secret = secret_commands.add_parser("generate")
    generate_secret.add_argument("profile")
    generate_secret.add_argument("name")
    generate_secret.add_argument("--bytes", type=int, default=32)
    remove_secret = secret_commands.add_parser("remove")
    remove_secret.add_argument("profile")
    remove_secret.add_argument("name")

    secret_commands.add_parser("audit").add_argument("--limit", type=int, default=50)

    action = commands.add_parser("action", help="manage and run approved actions")
    action_commands = action.add_subparsers(dest="action_command", required=True)
    action_list = action_commands.add_parser("list")
    action_list.add_argument("profile")
    action_remove = action_commands.add_parser("remove")
    action_remove.add_argument("profile")
    action_remove.add_argument("name")
    action_clear = action_commands.add_parser("clear-materialization")
    action_clear.add_argument("profile")
    action_clear.add_argument("name")
    action_set = action_commands.add_parser("set")
    action_set.add_argument("profile")
    action_set.add_argument("name")
    action_set.add_argument("--cwd", required=True)
    secret_selection = action_set.add_mutually_exclusive_group(required=True)
    secret_selection.add_argument("--all-secrets", action="store_true")
    secret_selection.add_argument("--secret", action="append", default=[])
    action_set.add_argument("--require-secret", action="append", default=[])
    action_set.add_argument("--timeout-seconds", type=int, default=3600)
    action_set.add_argument("--max-output-bytes", type=int, default=1_048_576)
    action_set.add_argument("--materialize-env-file", metavar="ENV_NAME")
    action_set.add_argument("--materialize-env-path", metavar="RELATIVE_PATH")
    action_set.add_argument("--docker", action="store_true")
    action_set.add_argument("--preferred-port", type=int)
    action_set.add_argument("--port-env")
    action_set.add_argument("--origin-env")
    action_set.add_argument("--singleton", action="store_true")
    action_set.add_argument("--lock-probe")
    action_set.add_argument("--local-callback", action="store_true")
    action_set.add_argument("--public-env", action="append", default=[])
    action_set.add_argument("argv", nargs="+")

    run = action_commands.add_parser("run")
    run.add_argument("profile")
    run.add_argument("action")
    run.add_argument("--cwd")
    run.add_argument("--bind-local-callback", action="store_true")
    read = action_commands.add_parser("read")
    read.add_argument("session_id")
    read.add_argument("--offset", type=int)
    read.add_argument("--limit", type=int, default=65_536)
    stop = action_commands.add_parser("stop")
    stop.add_argument("session_id")
    action_commands.add_parser("processes")
    return parser


def main() -> None:
    parser = _parser()
    args = parser.parse_args()
    if args.version:
        from importlib.metadata import version
        print(version("loki-mcp"))
        return
    if args.command == "bootstrap":
        _print(_request("bootstrap_project", cwd=args.cwd, workflow=args.workflow))
        return
    if args.command == "config":
        if args.config_command == "show":
            limits = load_action_process_limits(CONFIG_PATH)
        else:
            if os.geteuid() != 0:
                raise ValueError("changing Loki configuration requires sudo")
            limits = update_action_process_limit(CONFIG_PATH, args.name, args.value)
            subprocess.run(
                ["/usr/bin/systemctl", "restart", "loki-runtime.service"],
                check=True,
            )
        _print({
            "max_action_processes": limits.total,
            "max_action_processes_per_profile": limits.per_profile,
        })
        return
    if args.command == "state":
        if args.state_command == "migrate":
            if os.geteuid() != 0:
                raise ValueError("migrating project state requires sudo")
            store = ProjectStateStore(HOST_WORKSPACE_ROOT, STATE_ROOT)
            cwd = (HOST_WORKSPACE_ROOT / args.cwd).resolve()
            result = store.migrate_legacy(cwd)
        else:
            result = _request("project_state", action="status", cwd=args.cwd)
        _print(result)
        return
    if args.command == "project":
        if args.project_command == "register":
            result = _request("project_register", cwd=args.cwd, name=args.name)
        elif args.project_command == "unregister":
            result = _request("project_unregister", cwd=args.cwd)
        elif args.project_command == "status":
            result = _request("project_status", cwd=args.cwd)
        elif args.workflow_command == "set":
            result = _request(
                "project_set_workflow",
                cwd=args.cwd,
                workflow=args.name,
                steps=[_pair(value, "workflow step") for value in args.step],
                required_secrets=_required_secret_map(args.require),
                timeout_seconds=args.timeout_seconds,
            )
        else:
            result = _request(
                "project_remove_workflow", cwd=args.cwd, workflow=args.name,
            )
        _print(result)
        return
    if args.command == "action":
        command = args.action_command
        if command == "list":
            result = _request("get_profile", profile=args.profile)
        elif command == "remove":
            result = _request("action_remove", profile=args.profile, action_name=args.name)
        elif command == "clear-materialization":
            result = _request(
                "clear_action_materialization",
                profile=args.profile,
                action_name=args.name,
            )
        elif command == "set":
            argv = args.argv[1:] if args.argv[:1] == ["--"] else args.argv
            if not argv:
                raise ValueError("action command is required after --")
            if (args.preferred_port is None) != (args.port_env is None):
                raise ValueError("--preferred-port and --port-env must be used together")
            dynamic_port = None
            if args.preferred_port is not None:
                dynamic_port = {
                    "preferred": args.preferred_port,
                    "environment": args.port_env,
                }
                if args.origin_env is not None:
                    dynamic_port["origin_environment"] = args.origin_env
            elif args.origin_env is not None:
                raise ValueError("--origin-env requires a dynamic port")
            result = _request("action_set", profile=args.profile, action_name=args.name, action={
                "cwd": args.cwd, "command": argv, "secrets": args.secret,
                "required_secrets": args.require_secret,
                "all_secrets": args.all_secrets,
                "materialize_env_file": args.materialize_env_file,
                "materialize_env_path": args.materialize_env_path,
                "docker_access": args.docker,
                "dynamic_port": dynamic_port,
                "singleton": args.singleton,
                "lock_probe": args.lock_probe,
                "local_callback": args.local_callback,
                "public_environment": args.public_env,
                "timeout_seconds": args.timeout_seconds,
                "max_output_bytes": args.max_output_bytes,
            })
        elif command == "run":
            result = _request(
                "run_action", profile=args.profile, action_name=args.action,
                cwd=args.cwd, bind_local_callback=args.bind_local_callback,
            )
        elif command == "read":
            result = _request(
                "read_process", session_id=args.session_id,
                offset=args.offset, limit=args.limit,
            )
        elif command == "stop":
            result = _request("stop_process", session_id=args.session_id)
        else:
            result = _request("list_processes")
        _print(result)
        return
    if args.command != "secret":
        parser.print_help()
        return
    command = args.secret_command
    if command == "init":
        result = _request("init")
    elif command == "status":
        result = _request("status")
    elif command == "list":
        result = _request("list_profiles")
    elif command == "profile":
        operation = {"create": "profile_create", "remove": "profile_remove", "show": "get_profile"}[args.profile_command]
        result = _request(operation, profile=args.profile)
    elif command == "import-env":
        path = args.file.expanduser().resolve(strict=True)
        if not path.is_file() or path.is_symlink():
            raise ValueError("dotenv source must be a regular file")
        result = _request("import_env", profile=args.profile, values=_dotenv(path))
        if args.delete_source:
            path.unlink()
            result["source_deleted"] = True
    elif command == "stage-env":
        result = _stage_env(args.file, args.delete_source)
    elif command == "set":
        value = getpass.getpass("Secret value: ")
        result = _request("secret_set", profile=args.profile, secret=args.name, value=value)
    elif command == "generate":
        result = _request(
            "secret_generate", profile=args.profile, secret=args.name, bytes=args.bytes,
        )
    elif command == "remove":
        result = _request("secret_remove", profile=args.profile, secret=args.name)
    else:
        result = _request("audit", limit=args.limit)
    _print(result)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, LokiRuntimeError) as error:
        print(f"loki: {error}", file=sys.stderr)
        raise SystemExit(1)
