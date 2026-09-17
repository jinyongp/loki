#!/usr/bin/env python3
"""Expose only identity lookup and signing from a root-owned SSH agent."""

from __future__ import annotations

import grp
import os
import pwd
import socket
import socketserver
import struct
import sys


MAX_MESSAGE = 1024 * 1024
ALLOWED_REQUESTS = {11, 13}  # identities, sign request
FAILURE = struct.pack(">I", 1) + b"\x05"


def read_exact(sock: socket.socket, size: int) -> bytes:
    chunks: list[bytes] = []
    remaining = size
    while remaining:
        chunk = sock.recv(remaining)
        if not chunk:
            raise EOFError
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


class Handler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        _, uid, _ = struct.unpack("3i", self.request.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        if uid != self.server.runner_uid:
            return
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as upstream:
            upstream.connect(self.server.private_socket)
            while True:
                try:
                    header = read_exact(self.request, 4)
                except EOFError:
                    return
                length = struct.unpack(">I", header)[0]
                if length < 1 or length > MAX_MESSAGE:
                    return
                payload = read_exact(self.request, length)
                if payload[0] not in ALLOWED_REQUESTS:
                    self.request.sendall(FAILURE)
                    continue
                upstream.sendall(header + payload)
                response_header = read_exact(upstream, 4)
                response_length = struct.unpack(">I", response_header)[0]
                if response_length > MAX_MESSAGE:
                    return
                response = read_exact(upstream, response_length)
                self.request.sendall(response_header + response)


class Server(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True
    allow_reuse_address = False

    def __init__(self, private_socket: str, public_socket: str) -> None:
        self.private_socket = private_socket
        self.runner_uid = pwd.getpwnam("runner").pw_uid
        super().__init__(public_socket, Handler)
        os.chown(public_socket, 0, grp.getgrnam("workspace").gr_gid)
        os.chmod(public_socket, 0o660)


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: loki-signing-agent-proxy PRIVATE_SOCKET PUBLIC_SOCKET")
    private_socket, public_socket = sys.argv[1:]
    try:
        os.unlink(public_socket)
    except FileNotFoundError:
        pass
    with Server(private_socket, public_socket) as server:
        server.serve_forever(poll_interval=0.2)


if __name__ == "__main__":
    main()
