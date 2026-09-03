from __future__ import annotations

from collections.abc import Callable
from threading import RLock, Thread
import select
import socket
import socketserver


CALLBACK_HOST = "127.0.0.1"
CALLBACK_PORT = 41_800
BUFFER_SIZE = 65_536


class LocalCallbackProxyError(RuntimeError):
    pass


class _CallbackHandler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        proxy = self.server.proxy  # type: ignore[attr-defined]
        target_port = proxy.resolve_target()
        if target_port is None:
            self.request.sendall(
                b"HTTP/1.1 503 Service Unavailable\r\n"
                b"Connection: close\r\n"
                b"Content-Type: text/plain; charset=utf-8\r\n"
                b"Content-Length: 36\r\n\r\n"
                b"local callback target is unavailable"
            )
            return
        try:
            upstream = socket.create_connection((CALLBACK_HOST, target_port), timeout=5)
        except OSError:
            self.request.sendall(
                b"HTTP/1.1 502 Bad Gateway\r\n"
                b"Connection: close\r\n"
                b"Content-Type: text/plain; charset=utf-8\r\n"
                b"Content-Length: 38\r\n\r\n"
                b"local callback upstream is unavailable"
            )
            return
        try:
            sockets = (self.request, upstream)
            while True:
                readable, _, _ = select.select(sockets, (), (), 30)
                if not readable:
                    continue
                for source in readable:
                    data = source.recv(BUFFER_SIZE)
                    if not data:
                        return
                    destination = upstream if source is self.request else self.request
                    destination.sendall(data)
        finally:
            upstream.close()


class _CallbackServer(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = False

    def __init__(self, address: tuple[str, int], proxy: "LocalCallbackProxy") -> None:
        self.proxy = proxy
        super().__init__(address, _CallbackHandler)


class LocalCallbackProxy:
    """Loopback-only fixed entry point for one explicitly selected managed API session."""

    def __init__(
        self,
        resolver: Callable[[str], int | None],
        *,
        host: str = CALLBACK_HOST,
        port: int = CALLBACK_PORT,
    ) -> None:
        if host not in {"127.0.0.1", "localhost"}:
            raise ValueError("local callback proxy must bind to loopback")
        if not 0 <= port <= 65_535:
            raise ValueError("local callback proxy port is invalid")
        self.resolver = resolver
        self.host = host
        self.port = port
        self._session_id: str | None = None
        self._server: _CallbackServer | None = None
        self._thread: Thread | None = None
        self._lock = RLock()

    def bind(self, session_id: str) -> dict[str, object]:
        target_port = self.resolver(session_id)
        if target_port is None:
            raise LocalCallbackProxyError("local callback target must be a running managed API session")
        with self._lock:
            if self._server is None:
                try:
                    server = _CallbackServer((self.host, self.port), self)
                except OSError as error:
                    raise LocalCallbackProxyError(
                        f"local callback port {self.port} is unavailable"
                    ) from error
                self._server = server
                self.port = int(server.server_address[1])
                self._thread = Thread(target=server.serve_forever, daemon=True)
                self._thread.start()
            self._session_id = session_id
        return self.status()

    def resolve_target(self) -> int | None:
        with self._lock:
            session_id = self._session_id
        return None if session_id is None else self.resolver(session_id)

    def clear(self, session_id: str | None = None) -> None:
        with self._lock:
            if session_id is None or self._session_id == session_id:
                self._session_id = None

    def status(self) -> dict[str, object]:
        with self._lock:
            session_id = self._session_id
            listening = self._server is not None
        target_port = None if session_id is None else self.resolver(session_id)
        return {
            "bound": target_port is not None,
            "listening": listening,
            "origin": f"http://{self.host}:{self.port}",
            "session_id": session_id,
            "target_port": target_port,
        }

    def close(self) -> None:
        with self._lock:
            server = self._server
            thread = self._thread
            self._server = None
            self._thread = None
            self._session_id = None
        if server is not None:
            server.shutdown()
            server.server_close()
        if thread is not None:
            thread.join(timeout=2)
