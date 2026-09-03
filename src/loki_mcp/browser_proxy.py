from __future__ import annotations

import ipaddress
import json
import logging
import select
import socket
import socketserver
from urllib.parse import urlsplit


LISTEN_ADDRESS = ("127.0.0.1", 8767)
MAX_HEADER_BYTES = 65_536
MAX_LINE_BYTES = 8_192
CONNECT_TIMEOUT_SECONDS = 15
ALLOWED_PORTS = frozenset({80, 443})
PORT_GUARD_SOCKET = "/run/loki/port-guard/control.sock"
MAX_PORT_GUARD_RESPONSE_BYTES = 1_048_576
LOGGER = logging.getLogger("loki-browser-proxy")


class ProxyError(Exception):
    pass


def _public_addresses(hostname: str, port: int) -> list[tuple[int, int, int, str, tuple]]:
    normalized = hostname.rstrip(".").lower()
    if normalized == "localhost" or normalized.endswith(".localhost"):
        raise ProxyError("private destination")
    try:
        records = socket.getaddrinfo(normalized, port, type=socket.SOCK_STREAM)
    except socket.gaierror as error:
        raise ProxyError("host resolution failed") from error
    public: list[tuple[int, int, int, str, tuple]] = []
    for record in records:
        address = ipaddress.ip_address(record[4][0])
        if not address.is_global:
            raise ProxyError("private destination")
        if record not in public:
            public.append(record)
    if not public:
        raise ProxyError("host has no public address")
    return public


def _connect_public(hostname: str, port: int) -> socket.socket:
    if port not in ALLOWED_PORTS:
        raise ProxyError("destination port is blocked")
    last_error: OSError | None = None
    for family, socktype, protocol, _, sockaddr in _public_addresses(hostname, port):
        upstream = socket.socket(family, socktype, protocol)
        upstream.settimeout(CONNECT_TIMEOUT_SECONDS)
        try:
            upstream.connect(sockaddr)
            return upstream
        except OSError as error:
            last_error = error
            upstream.close()
    raise ProxyError("upstream connection failed") from last_error


def _workspace_port_is_allowed(port: int) -> bool:
    if not 1024 <= port <= 65_535:
        return False
    request = json.dumps(
        {"operation": "inspect", "port": port}, separators=(",", ":")
    ).encode("utf-8") + b"\n"
    response = bytearray()
    try:
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
            client.settimeout(CONNECT_TIMEOUT_SECONDS)
            client.connect(PORT_GUARD_SOCKET)
            client.sendall(request)
            while len(response) <= MAX_PORT_GUARD_RESPONSE_BYTES:
                chunk = client.recv(65_536)
                if not chunk:
                    break
                response.extend(chunk)
                if b"\n" in chunk:
                    break
    except OSError as error:
        raise ProxyError("workspace port validation unavailable") from error
    if len(response) > MAX_PORT_GUARD_RESPONSE_BYTES:
        raise ProxyError("workspace port validation response too large")
    try:
        envelope = json.loads(bytes(response).split(b"\n", 1)[0])
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise ProxyError("invalid workspace port validation response") from error
    if not isinstance(envelope, dict):
        raise ProxyError("invalid workspace port validation envelope")
    if not envelope.get("ok"):
        detail = envelope.get("error")
        if not isinstance(detail, str) or not detail:
            detail = "unspecified validation failure"
        raise ProxyError(f"workspace port validation failed: {detail}")
    result = envelope.get("result")
    return bool(
        isinstance(result, dict)
        and result.get("in_use") is True
        and isinstance(result.get("listeners"), list)
        and result["listeners"]
    )


def _target_addresses(hostname: str, port: int) -> list[tuple[int, int, int, str, tuple]]:
    normalized = hostname.rstrip(".").lower()
    if normalized == "127.0.0.1":
        if not _workspace_port_is_allowed(port):
            raise ProxyError("workspace development port is not allowed")
        return [(socket.AF_INET, socket.SOCK_STREAM, socket.IPPROTO_TCP, "", (normalized, port))]
    if port not in ALLOWED_PORTS:
        raise ProxyError("destination port is blocked")
    return _public_addresses(normalized, port)


def _connect_target(hostname: str, port: int) -> socket.socket:
    last_error: OSError | None = None
    for family, socktype, protocol, _, sockaddr in _target_addresses(hostname, port):
        upstream = socket.socket(family, socktype, protocol)
        upstream.settimeout(CONNECT_TIMEOUT_SECONDS)
        try:
            upstream.connect(sockaddr)
            # The timeout limits only connection establishment.  Keeping it on
            # the connected socket breaks idle HTTP keep-alive and Vite HMR
            # WebSocket connections after 15 seconds.
            upstream.settimeout(None)
            return upstream
        except OSError as error:
            last_error = error
            upstream.close()
    raise ProxyError("upstream connection failed") from last_error


def _forward_headers(headers: list[bytes]) -> list[bytes]:
    names = {line.split(b":", 1)[0].strip().lower() for line in headers}
    is_upgrade = b"upgrade" in names
    blocked = {b"proxy-authorization", b"proxy-connection"}
    if not is_upgrade:
        # A subsequent request on a proxy connection contains another absolute
        # URI and could even name a different destination.  Keep ordinary HTTP
        # requests one-per-connection.  WebSocket upgrades remain a fixed-target
        # raw relay after the 101 response.
        blocked.update({b"connection", b"keep-alive"})
    forwarded = [
        line for line in headers
        if line.split(b":", 1)[0].strip().lower() not in blocked
    ]
    if not is_upgrade:
        forwarded.append(b"Connection: close\r\n")
    return forwarded


def _authority(value: str, default_port: int) -> tuple[str, int]:
    parsed = urlsplit(f"//{value}")
    if not parsed.hostname or parsed.username or parsed.password:
        raise ProxyError("invalid authority")
    try:
        port = parsed.port or default_port
    except ValueError as error:
        raise ProxyError("invalid authority") from error
    return parsed.hostname, port


class PublicProxyHandler(socketserver.StreamRequestHandler):
    def handle(self) -> None:
        self.connection.settimeout(CONNECT_TIMEOUT_SECONDS)
        destination = "unparsed destination"
        try:
            request_line = self.rfile.readline(MAX_LINE_BYTES + 1)
            if not request_line or len(request_line) > MAX_LINE_BYTES:
                raise ProxyError("invalid request line")
            method, target, version = request_line.decode("ascii").strip().split(" ", 2)
            headers = self._headers(len(request_line))
            if method == "CONNECT":
                host, port = _authority(target, 443)
                destination = f"{host}:{port}"
                upstream = _connect_target(host, port)
                try:
                    self.connection.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
                    self._relay(upstream)
                finally:
                    upstream.close()
                return
            if method not in {"GET", "HEAD", "OPTIONS"}:
                raise ProxyError("method is blocked")
            parsed = urlsplit(target)
            if parsed.scheme != "http" or not parsed.hostname or parsed.username or parsed.password:
                raise ProxyError("only absolute HTTP proxy requests are allowed")
            try:
                port = parsed.port or 80
            except ValueError as error:
                raise ProxyError("invalid destination port") from error
            destination = f"{parsed.hostname}:{port}"
            upstream = _connect_target(parsed.hostname, port)
            try:
                path = parsed.path or "/"
                if parsed.query:
                    path += f"?{parsed.query}"
                filtered = _forward_headers(headers)
                upstream.sendall(f"{method} {path} {version}\r\n".encode("ascii"))
                for line in filtered:
                    upstream.sendall(line)
                upstream.sendall(b"\r\n")
                self._relay(upstream)
            finally:
                upstream.close()
        except (ProxyError, UnicodeDecodeError, ValueError) as error:
            LOGGER.warning("Blocked browser proxy request to %s: %s", destination, error)
            self._respond(403, "Forbidden")
        except OSError as error:
            LOGGER.warning("Browser proxy upstream failure for %s: %s", destination, error)
            self._respond(502, "Bad Gateway")

    def _headers(self, initial_size: int) -> list[bytes]:
        total = initial_size
        headers: list[bytes] = []
        while True:
            line = self.rfile.readline(MAX_LINE_BYTES + 1)
            total += len(line)
            if len(line) > MAX_LINE_BYTES or total > MAX_HEADER_BYTES:
                raise ProxyError("request headers exceed limit")
            if line in {b"\r\n", b"\n", b""}:
                return headers
            if b":" not in line:
                raise ProxyError("invalid request header")
            headers.append(line)

    def _relay(self, upstream: socket.socket) -> None:
        peers = {self.connection: upstream, upstream: self.connection}
        while True:
            readable, _, _ = select.select(list(peers), [], [], 300)
            if not readable:
                return
            for source in readable:
                data = source.recv(65_536)
                if not data:
                    return
                peers[source].sendall(data)

    def _respond(self, status: int, reason: str) -> None:
        try:
            self.connection.sendall(
                f"HTTP/1.1 {status} {reason}\r\nConnection: close\r\nContent-Length: 0\r\n\r\n".encode("ascii")
            )
        except OSError:
            pass


class ThreadedServer(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


def main() -> None:
    logging.basicConfig(level=logging.INFO)
    with ThreadedServer(LISTEN_ADDRESS, PublicProxyHandler) as server:
        server.serve_forever()


if __name__ == "__main__":
    main()
