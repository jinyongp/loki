#!/usr/bin/python3
from __future__ import annotations

import argparse
import ctypes
import grp
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading


RUNTIME_ROOT = Path("/run/loki")
UPSTREAM_SOCKET = "/run/docker.sock"
SOCKET_SENTINEL = "{LOKI_DOCKER_PROXY_SOCKET}"
RUNNER_PREFIX = [
    "/usr/sbin/runuser", "-u", "runner", "-m", "--",
    "/usr/local/libexec/loki-action-runner",
]
ACL_TYPE_ACCESS = 0x8000


def _set_access_acl(path: Path, value: str) -> None:
    library = ctypes.CDLL("libacl.so.1", use_errno=True)
    library.acl_from_text.argtypes = [ctypes.c_char_p]
    library.acl_from_text.restype = ctypes.c_void_p
    library.acl_set_file.argtypes = [ctypes.c_char_p, ctypes.c_int, ctypes.c_void_p]
    library.acl_set_file.restype = ctypes.c_int
    library.acl_free.argtypes = [ctypes.c_void_p]
    acl = library.acl_from_text(value.encode())
    if not acl:
        raise OSError(ctypes.get_errno(), f"cannot create ACL for {path}")
    try:
        if library.acl_set_file(os.fsencode(path), ACL_TYPE_ACCESS, acl) != 0:
            raise OSError(ctypes.get_errno(), f"cannot apply ACL to {path}")
    finally:
        library.acl_free(acl)


def _copy(source: socket.socket, destination: socket.socket) -> None:
    try:
        while data := source.recv(64 * 1024):
            destination.sendall(data)
    except OSError:
        pass
    finally:
        try:
            destination.shutdown(socket.SHUT_WR)
        except OSError:
            pass


def _proxy(listener: socket.socket, stop: threading.Event, connections: list[socket.socket]) -> None:
    listener.settimeout(0.2)
    while not stop.is_set():
        try:
            client, _ = listener.accept()
        except TimeoutError:
            continue
        except OSError:
            break
        upstream = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            upstream.connect(UPSTREAM_SOCKET)
        except OSError:
            client.close()
            upstream.close()
            continue
        connections.extend([client, upstream])
        threading.Thread(target=_copy, args=(client, upstream), daemon=True).start()
        threading.Thread(target=_copy, args=(upstream, client), daemon=True).start()


def main() -> None:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("argv", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    argv = args.argv[1:] if args.argv[:1] == ["--"] else args.argv
    if os.geteuid() != 0:
        raise SystemExit("restricted docker action runner requires root")
    if argv[:len(RUNNER_PREFIX)] != RUNNER_PREFIX or argv.count(SOCKET_SENTINEL) != 1:
        raise SystemExit("invalid restricted docker action command")
    if not Path(UPSTREAM_SOCKET).is_socket():
        raise SystemExit("docker socket is unavailable")

    runtime_directory = Path(tempfile.mkdtemp(prefix="docker-", dir=RUNTIME_ROOT))
    proxy_socket = runtime_directory / "docker.sock"
    listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    stop = threading.Event()
    connections: list[socket.socket] = []
    proxy_thread: threading.Thread | None = None
    try:
        runtime_directory.chmod(0o700)
        _set_access_acl(runtime_directory, "u::rwx,u:runner:r-x,g::---,m::r-x,o::---")
        os.setgroups([grp.getgrnam("docker").gr_gid])
        listener.bind(str(proxy_socket))
        proxy_socket.chmod(0o600)
        _set_access_acl(proxy_socket, "u::rw-,u:runner:rw-,g::---,m::rw-,o::---")
        listener.listen(32)
        proxy_thread = threading.Thread(target=_proxy, args=(listener, stop, connections), daemon=True)
        proxy_thread.start()
        command = [str(proxy_socket) if item == SOCKET_SENTINEL else item for item in argv]
        completed = subprocess.run(command, env=os.environ, check=False)
        raise SystemExit(completed.returncode)
    finally:
        stop.set()
        listener.close()
        for connection in connections:
            connection.close()
        if proxy_thread is not None:
            proxy_thread.join(timeout=1)
        shutil.rmtree(runtime_directory, ignore_errors=True)


if __name__ == "__main__":
    main()
