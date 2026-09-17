#!/usr/bin/python3
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile


LOCAL_DEPLOY_ROOT = Path("/var/lib/loki/local-deploy")
MATERIALIZATION_ROOT = LOCAL_DEPLOY_ROOT / "materializations"


def _marker_path(target: Path) -> Path:
    digest = hashlib.sha256(str(target).encode("utf-8")).hexdigest()
    return MATERIALIZATION_ROOT / f"{digest}.json"


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def _recover_materialization(target: Path, marker: Path) -> None:
    if not marker.exists():
        return
    if marker.is_symlink():
        raise SystemExit("materialized secret marker is unsafe")
    try:
        record = json.loads(marker.read_text(encoding="utf-8"))
        owner_pid = int(record["pid"])
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as error:
        raise SystemExit("materialized secret marker is invalid") from error
    if record.get("target") != str(target):
        raise SystemExit("materialized secret marker target mismatch")
    if owner_pid != os.getpid() and _pid_alive(owner_pid):
        raise SystemExit("materialized secret target is in use")
    if target.exists() or target.is_symlink():
        metadata = target.lstat()
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or stat.S_IMODE(metadata.st_mode) != 0o600
            or metadata.st_nlink != 1
        ):
            raise SystemExit("materialized secret target is unsafe to recover")
        target.unlink()
    marker.unlink(missing_ok=True)


def _claim_materialization(target: Path) -> Path:
    marker = _marker_path(target)
    _recover_materialization(target, marker)
    if target.exists() or target.is_symlink():
        raise SystemExit("materialized secret target already exists")
    record = json.dumps({"pid": os.getpid(), "target": str(target)}, separators=(",", ":"))
    marker.write_text(record + "\n", encoding="utf-8")
    marker.chmod(0o600)
    try:
        target.touch(mode=0o600, exist_ok=False)
        target.chmod(0o600)
    except Exception:
        marker.unlink(missing_ok=True)
        raise
    return marker


def _dotenv_value(value: str) -> str:
    if value and all(character not in value for character in " \t\r\n#'\""):
        return value
    escaped = (
        value.replace("\\", "\\\\")
        .replace('"', '\\"')
        .replace("\r", "\\r")
        .replace("\n", "\\n")
        .replace("\t", "\\t")
    )
    return f'"{escaped}"'


def main() -> None:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--cwd", required=True)
    parser.add_argument("--env", action="append", default=[])
    parser.add_argument("--materialize-env-file")
    parser.add_argument("--materialize-env-path")
    parser.add_argument("--docker-socket")
    parser.add_argument("argv", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    argv = args.argv[1:] if args.argv[:1] == ["--"] else args.argv
    if not argv:
        raise SystemExit("missing command")
    environment = {
        "HOME": "/home/runner",
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "PATH": "/home/linuxbrew/.linuxbrew/bin:/opt/loki-mcp/venv/bin:/usr/local/bin:/usr/bin:/bin",
        "CI": "1",
        "COREPACK_HOME": "/workspace/.loki/corepack",
        "PNPM_HOME": "/workspace/.loki/corepack",
        "TMPDIR": "/tmp",
        "GIT_CONFIG_GLOBAL": "/etc/loki/gitconfig",
        "SSH_AUTH_SOCK": "/run/loki/signing/agent.sock",
        "GOMODCACHE": "/workspace/.loki/go/pkg/mod",
        "GOPATH": "/workspace/.loki/go",
        "GOPROXY": "https://proxy.golang.org",
        "CARGO_HOME": "/workspace/.loki/cargo",
        "RUSTUP_HOME": "/workspace/.loki/rustup",
        "CARGO_REGISTRIES_CRATES_IO_PROTOCOL": "sparse",
        "CARGO_NET_GIT_FETCH_WITH_CLI": "true",
        "NPM_CONFIG_REGISTRY": "https://registry.npmjs.org",
        "npm_config_store_dir": "/workspace/.loki/pnpm-store",
        "HTTP_PROXY": "http://127.0.0.1:8766",
        "HTTPS_PROXY": "http://127.0.0.1:8766",
        "http_proxy": "http://127.0.0.1:8766",
        "https_proxy": "http://127.0.0.1:8766",
        "NO_PROXY": "127.0.0.1,localhost",
        "no_proxy": "127.0.0.1,localhost",
        "NODE_USE_ENV_PROXY": "1",
    }
    for name in args.env:
        if name not in os.environ:
            raise SystemExit(f"missing injected secret: {name}")
        environment[name] = os.environ[name]
    session_directory: Path | None = None
    placeholder: Path | None = None
    materialization_marker: Path | None = None
    sandbox_env_file: Path | None = None
    if args.materialize_env_file:
        session_directory = Path(tempfile.mkdtemp(prefix="session-", dir=LOCAL_DEPLOY_ROOT / "sessions"))
        env_file = session_directory / "runtime.env"
        with env_file.open("x", encoding="utf-8", newline="\n") as handle:
            for name in sorted(args.env):
                handle.write(f"{name}={_dotenv_value(environment[name])}\n")
        env_file.chmod(0o600)
        sandbox_cwd = Path(args.cwd)
        try:
            relative_cwd = sandbox_cwd.relative_to("/workspace")
        except ValueError as error:
            raise SystemExit("actions with materialized secrets require a workspace cwd") from error
        host_cwd = Path("/srv/workspace/loki", *relative_cwd.parts)
        if args.materialize_env_path:
            relative_env_path = Path(args.materialize_env_path)
            if relative_env_path.is_absolute() or ".." in relative_env_path.parts:
                raise SystemExit("materialized env path must be workspace-relative")
            placeholder = host_cwd.joinpath(*relative_env_path.parts)
            placeholder_directory = placeholder.parent.resolve()
            resolved_host_cwd = host_cwd.resolve()
            if placeholder_directory != resolved_host_cwd and resolved_host_cwd not in placeholder_directory.parents:
                raise SystemExit("materialized env path escapes the workspace cwd")
            sandbox_env_file = sandbox_cwd.joinpath(*relative_env_path.parts)
        else:
            placeholder_directory = host_cwd / ".tmp"
            placeholder = placeholder_directory / f".loki-action-{session_directory.name}.env"
            sandbox_env_file = sandbox_cwd / ".tmp" / placeholder.name
        if not placeholder_directory.is_dir() or placeholder_directory.is_symlink():
            raise SystemExit("actions with materialized secrets require an existing target directory")
        materialization_marker = _claim_materialization(placeholder)
        environment[args.materialize_env_file] = str(sandbox_env_file)
        environment["TMPDIR"] = str(LOCAL_DEPLOY_ROOT / "snapshots")
    if args.docker_socket:
        docker_socket = Path(args.docker_socket)
        if not docker_socket.is_socket() or docker_socket.parent.parent != Path("/run/loki"):
            raise SystemExit("transient docker proxy socket is unavailable")
        environment["DOCKER_ACCESS_MODE"] = "restricted-proxy"
        environment["DOCKER_HOST"] = f"unix://{docker_socket}"
    execution_cwd = Path(args.cwd)
    docker_host_cwd: Path | None = None
    if args.docker_socket:
        try:
            relative_cwd = execution_cwd.relative_to("/workspace")
        except ValueError as error:
            raise SystemExit("docker actions require a workspace cwd") from error
        docker_host_cwd = Path("/srv/workspace/loki", *relative_cwd.parts)
        execution_cwd = docker_host_cwd
        if args.materialize_env_file and placeholder is not None:
            environment[args.materialize_env_file] = str(placeholder)
    sandbox = [
        "/usr/bin/bwrap", "--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid",
        "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
    ]
    sandbox.extend([
        "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/sbin", "/sbin",
        "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev",
        "--tmpfs", "/tmp", "--dir", "/home", "--dir", "/home/runner", "--dir", "/home/runner/.ssh",
        "--ro-bind", "/etc/loki/signing_key.pub", "/home/runner/.ssh/id_ed25519.pub",
        "--dir", "/home/runner/.local",
        "--dir", "/home/runner/.local/share",
        "--ro-bind", "/home/runner/.local/share/fnm", "/home/runner/.local/share/fnm",
        "--dir", "/home/linuxbrew",
        "--ro-bind", "/home/linuxbrew/.linuxbrew", "/home/linuxbrew/.linuxbrew", "--dir", "/etc",
        "--ro-bind", "/etc/passwd", "/etc/passwd", "--ro-bind", "/etc/group", "/etc/group",
        "--ro-bind", "/etc/hosts", "/etc/hosts",
        "--ro-bind", "/etc/alternatives", "/etc/alternatives",
        "--dir", "/etc/ssl", "--ro-bind", "/etc/ssl/certs", "/etc/ssl/certs", "--dir", "/etc/loki",
        "--ro-bind", "/etc/loki/gitconfig", "/etc/loki/gitconfig", "--dir", "/run",
        "--ro-bind", "/run/loki/signing", "/run/loki/signing", "--dir", "/workspace",
        "--bind", "/srv/workspace/loki", "/workspace", "--chdir", str(execution_cwd), "--", *argv,
    ])
    dynamic_mounts: list[str] = []
    if session_directory is not None and sandbox_env_file is not None:
        dynamic_mounts.extend([
            "--dir", "/var/lib", "--dir", str(LOCAL_DEPLOY_ROOT),
            "--bind", str(LOCAL_DEPLOY_ROOT / "snapshots"), str(LOCAL_DEPLOY_ROOT / "snapshots"),
            "--ro-bind", str(session_directory / "runtime.env"), str(sandbox_env_file),
        ])
    if args.docker_socket:
        assert docker_host_cwd is not None
        dynamic_mounts.extend([
            "--dir", "/srv", "--dir", "/srv/workspace", "--dir", "/srv/workspace/loki",
            "--bind", str(docker_host_cwd), str(execution_cwd),
            "--dir", "/run/loki",
            "--dir", str(Path(args.docker_socket).parent),
            "--ro-bind", args.docker_socket, args.docker_socket,
        ])
        if session_directory is not None and placeholder is not None:
            dynamic_mounts.extend([
                "--ro-bind", str(session_directory / "runtime.env"), str(placeholder),
            ])
    sandbox[sandbox.index("--chdir"):sandbox.index("--chdir")] = dynamic_mounts
    try:
        completed = subprocess.run(sandbox, env=environment, check=False)
        raise SystemExit(completed.returncode)
    finally:
        if placeholder is not None:
            placeholder.unlink(missing_ok=True)
        if materialization_marker is not None:
            materialization_marker.unlink(missing_ok=True)
        if session_directory is not None:
            shutil.rmtree(session_directory)


if __name__ == "__main__":
    main()
