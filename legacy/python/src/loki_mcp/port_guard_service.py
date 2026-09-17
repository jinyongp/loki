from __future__ import annotations

from pathlib import Path
import json
import os
import re
import signal
import socketserver
import time


SOCKET_PATH = Path("/run/loki/port-guard/control.sock")
WORKSPACE_ROOT = Path("/srv/workspace/loki").resolve()
SANDBOX_WORKSPACE_ROOT = Path("/workspace")
PROTECTED_PORTS = frozenset({8765, 8766, 8767})
MAX_REQUEST_BYTES = 4096
PID_PATTERN = re.compile(r"^[1-9]\d*$")
MANAGED_SCOPE_PATTERN = re.compile(
    r"^/system\.slice/loki-(?:action|project-bootstrap)-[0-9a-f]{16}\.scope$"
)


class PortGuardError(Exception):
    pass


def _validate_port(value: object) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not 1024 <= value <= 65535:
        raise PortGuardError("port must be an integer between 1024 and 65535")
    if value in PROTECTED_PORTS:
        raise PortGuardError("protected Loki service port")
    return value


def _process_identity(pid: int) -> tuple[int, int, Path, str]:
    proc = Path("/proc") / str(pid)
    status = (proc / "status").read_text(encoding="utf-8")
    uid_line = next((line for line in status.splitlines() if line.startswith("Uid:")), None)
    if uid_line is None:
        raise PortGuardError("unable to determine listener owner")
    uid = int(uid_line.split()[1])
    stat = (proc / "stat").read_text(encoding="utf-8")
    remainder = stat[stat.rfind(")") + 2:].split()
    start_time = int(remainder[19])
    cwd = Path(os.readlink(proc / "cwd")).resolve()
    command = (proc / "comm").read_text(encoding="utf-8").strip()
    return uid, start_time, cwd, command


def _in_loki_cgroup(pid: int) -> bool:
    try:
        cgroups = (Path("/proc") / str(pid) / "cgroup").read_text(encoding="utf-8")
    except OSError:
        return False
    managed_services = ("/loki-mcp.service", "/loki-runtime.service")
    for line in cgroups.splitlines():
        path = line.split(":", 2)[-1].rstrip("/")
        if path.endswith(managed_services) or MANAGED_SCOPE_PATTERN.fullmatch(path):
            return True
    return False


def _inside_workspace(pid: int, path: Path) -> bool:
    try:
        path.relative_to(WORKSPACE_ROOT)
        return True
    except ValueError:
        pass
    try:
        path.relative_to(SANDBOX_WORKSPACE_ROOT)
    except ValueError:
        return False
    return _in_loki_cgroup(pid)


def _listener_is_allowed(pid: int, uid: int, cwd: Path) -> bool:
    try:
        cwd.relative_to(WORKSPACE_ROOT)
    except ValueError:
        pass
    else:
        return uid == os.getuid() or _in_loki_cgroup(pid)

    # bubblewrap's user namespace can expose the sandboxed runner as the
    # overflow/nobody uid to this host-side service. The Loki MCP or runtime
    # cgroup plus a workspace cwd is the durable ownership boundary.
    try:
        cwd.relative_to(SANDBOX_WORKSPACE_ROOT)
    except ValueError:
        return False
    return _inside_workspace(pid, cwd)


def _display_cwd(path: Path) -> str:
    try:
        relative = path.relative_to(WORKSPACE_ROOT)
        return str(SANDBOX_WORKSPACE_ROOT / relative)
    except ValueError:
        return str(path)


def _listeners_once(port: int) -> list[dict[str, object]]:
    socket_inodes: set[str] = set()
    expected_port = f"{port:04X}"
    for table_name in ("tcp", "tcp6"):
        try:
            lines = (Path("/proc/net") / table_name).read_text(encoding="ascii").splitlines()[1:]
        except OSError as error:
            raise PortGuardError("unable to inspect listening sockets") from error
        for line in lines:
            fields = line.split()
            if len(fields) > 9 and fields[3] == "0A" and fields[1].rsplit(":", 1)[-1] == expected_port:
                socket_inodes.add(fields[9])
    if not socket_inodes:
        return []

    listeners: list[dict[str, object]] = []
    seen: set[int] = set()
    matched_inodes: set[str] = set()
    for proc in Path("/proc").iterdir():
        if not PID_PATTERN.fullmatch(proc.name):
            continue
        pid = int(proc.name)
        try:
            uid, start_time, cwd, command = _process_identity(pid)
            if not _listener_is_allowed(pid, uid, cwd):
                continue
            owned_inodes = {
                target[8:-1]
                for fd in (proc / "fd").iterdir()
                if (target := os.readlink(fd)).startswith("socket:[") and target.endswith("]")
            } & socket_inodes
            if not owned_inodes:
                continue
            listeners.append({
                "pid": pid,
                "start_time": start_time,
                "cwd": _display_cwd(cwd),
                "command": command,
                "local_address": f"*:{port}",
            })
            seen.add(pid)
            matched_inodes.update(owned_inodes)
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue
    if matched_inodes != socket_inodes:
        raise PortGuardError(
            f"listener details are unavailable or owned by another user "
            f"({len(matched_inodes)}/{len(socket_inodes)} sockets matched)"
        )
    return listeners


def _listeners(port: int) -> list[dict[str, object]]:
    last_error: PortGuardError | None = None
    for attempt in range(5):
        try:
            return _listeners_once(port)
        except PortGuardError as error:
            if not str(error).startswith("listener details are unavailable"):
                raise
            last_error = error
            if attempt < 4:
                time.sleep(0.025 * (attempt + 1))
    if last_error is not None:
        raise last_error
    return []


def inspect_port(port: int) -> dict[str, object]:
    listeners = _listeners(_validate_port(port))
    return {
        "port": port,
        "in_use": bool(listeners),
        "listeners": [
            {key: value for key, value in listener.items() if key != "start_time"}
            for listener in listeners
        ],
    }


def stop_port(port: int) -> dict[str, object]:
    listeners = _listeners(_validate_port(port))
    if not listeners:
        return {"port": port, "stopped": False, "terminated_pids": []}
    identities = {(int(item["pid"]), int(item["start_time"])) for item in listeners}
    for pid, start_time in identities:
        _, current_start, cwd, _ = _process_identity(pid)
        if current_start != start_time or not _inside_workspace(pid, cwd):
            raise PortGuardError("listener changed during validation")
        os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 5
    remaining = set(identities)
    while remaining and time.monotonic() < deadline:
        remaining = {(pid, started) for pid, started in remaining if (Path("/proc") / str(pid)).exists()}
        if remaining:
            time.sleep(0.1)
    for pid, started in remaining:
        try:
            _, current_start, cwd, _ = _process_identity(pid)
            if current_start == started and _inside_workspace(pid, cwd):
                os.kill(pid, signal.SIGKILL)
        except (FileNotFoundError, ProcessLookupError):
            pass
    return {
        "port": port,
        "stopped": True,
        "terminated_pids": sorted(pid for pid, _ in identities),
    }


class RequestHandler(socketserver.StreamRequestHandler):
    def handle(self) -> None:
        raw = self.rfile.readline(MAX_REQUEST_BYTES + 1)
        if len(raw) > MAX_REQUEST_BYTES:
            response = {"ok": False, "error": "request too large"}
        else:
            try:
                request = json.loads(raw.decode("utf-8"))
                if not isinstance(request, dict):
                    raise PortGuardError("request must be an object")
                operation = request.get("operation")
                port = _validate_port(request.get("port"))
                if operation == "inspect":
                    result = inspect_port(port)
                elif operation == "stop":
                    result = stop_port(port)
                else:
                    raise PortGuardError("unknown operation")
                response = {"ok": True, "result": result}
            except (json.JSONDecodeError, UnicodeDecodeError, KeyError, OSError, PortGuardError) as error:
                response = {"ok": False, "error": str(error)}
        self.wfile.write((json.dumps(response, separators=(",", ":")) + "\n").encode("utf-8"))


class ThreadingUnixServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def main() -> None:
    SOCKET_PATH.parent.mkdir(parents=True, exist_ok=True)
    SOCKET_PATH.unlink(missing_ok=True)
    with ThreadingUnixServer(str(SOCKET_PATH), RequestHandler) as server:
        os.chmod(SOCKET_PATH, 0o660)
        server.serve_forever(poll_interval=0.5)


if __name__ == "__main__":
    main()
