from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
from threading import RLock
from typing import Callable
from urllib.parse import urlsplit, urlunsplit
import asyncio
import re
import secrets
import time

import httpx2
from websockets.asyncio.client import connect as websocket_connect
from websockets.exceptions import ConnectionClosed


HOST_TOKEN_PATTERN = re.compile(r"^loki-([0-9a-f]{32})$")
SHARE_ID_PATTERN = re.compile(r"^[0-9a-f]{16}$")
MAX_REQUEST_BODY_BYTES = 16 * 1024 * 1024
PORT_VALIDATION_TTL_SECONDS = 2.0
HOP_BY_HOP_HEADERS = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}
PRIVATE_EDGE_HEADERS = {
    "cf-access-jwt-assertion",
    "cf-access-authenticated-user-email",
    "cf-connecting-ip",
    "cf-ipcountry",
    "cf-ray",
    "cdn-loop",
}


@dataclass(frozen=True)
class PreviewRoute:
    prefix: str
    port: int


@dataclass(frozen=True)
class Preview:
    share_id: str
    host_token: str
    port: int
    routes: tuple[PreviewRoute, ...]
    cwd: str
    command: str
    created_at: float
    expires_at: float


class PreviewStore:
    def __init__(
        self,
        base_domain: str,
        *,
        max_items: int = 8,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.base_domain = base_domain.rstrip(".").lower()
        self.max_items = max_items
        self.clock = clock
        self._by_host: dict[str, Preview] = {}
        self._by_id: dict[str, Preview] = {}
        self._lock = RLock()

    def publish(self, *, port: int, cwd: str, command: str, ttl_seconds: int) -> dict[str, object]:
        return self.publish_stack(
            routes={"/": port}, cwd=cwd, command=command, ttl_seconds=ttl_seconds,
        )

    def publish_stack(
        self,
        *,
        routes: dict[str, int],
        cwd: str,
        command: str,
        ttl_seconds: int,
    ) -> dict[str, object]:
        normalized = self._normalize_routes(routes)
        now = self.clock()
        with self._lock:
            self._purge(now)
            if len(self._by_id) >= self.max_items:
                raise RuntimeError("temporary preview capacity is full; stop or wait for a preview to expire")
            share_id = secrets.token_hex(8)
            host_token = secrets.token_hex(16)
            preview = Preview(
                share_id=share_id,
                host_token=host_token,
                port=next(route.port for route in normalized if route.prefix == "/"),
                routes=normalized,
                cwd=cwd,
                command=command,
                created_at=now,
                expires_at=now + ttl_seconds,
            )
            self._by_id[share_id] = preview
            self._by_host[host_token] = preview
        return self._serialize(preview)

    @staticmethod
    def _normalize_routes(routes: dict[str, int]) -> tuple[PreviewRoute, ...]:
        if "/" not in routes or not 1 <= len(routes) <= 8:
            raise ValueError("preview routes must include a root route")
        normalized: list[PreviewRoute] = []
        for prefix, port in routes.items():
            if (
                not isinstance(prefix, str)
                or not prefix.startswith("/")
                or (prefix != "/" and prefix.endswith("/"))
                or "//" in prefix
                or ".." in prefix.split("/")
                or not isinstance(port, int)
                or not 1 <= port <= 65_535
            ):
                raise ValueError("preview route is invalid")
            normalized.append(PreviewRoute(prefix=prefix, port=port))
        return tuple(sorted(normalized, key=lambda item: (-len(item.prefix), item.prefix)))

    @staticmethod
    def resolve_route(preview: Preview, path: str) -> tuple[PreviewRoute, str] | None:
        for route in preview.routes:
            if route.prefix == "/":
                if path.startswith("/_loki/"):
                    return None
                return route, path
            if path == route.prefix or path.startswith(route.prefix + "/"):
                upstream_path = path[len(route.prefix):] or "/"
                return route, upstream_path
        return None

    def list(self) -> list[dict[str, object]]:
        now = self.clock()
        with self._lock:
            self._purge(now)
            previews = sorted(self._by_id.values(), key=lambda item: item.created_at)
            return [self._serialize(item) for item in previews]

    def get(self, share_id: str) -> dict[str, object] | None:
        if SHARE_ID_PATTERN.fullmatch(share_id) is None:
            return None
        now = self.clock()
        with self._lock:
            self._purge(now)
            preview = self._by_id.get(share_id)
            return None if preview is None else self._serialize(preview)

    def revoke(self, share_id: str) -> dict[str, object] | None:
        if SHARE_ID_PATTERN.fullmatch(share_id) is None:
            return None
        now = self.clock()
        with self._lock:
            self._purge(now)
            preview = self._by_id.pop(share_id, None)
            if preview is None:
                return None
            self._by_host.pop(preview.host_token, None)
            return self._serialize(preview)

    def resolve_host(self, host: str) -> Preview | None:
        hostname = host.partition(":")[0].rstrip(".").lower()
        suffix = f".{self.base_domain}"
        if not hostname.endswith(suffix):
            return None
        label = hostname.removesuffix(suffix)
        match = HOST_TOKEN_PATTERN.fullmatch(label)
        if match is None:
            return None
        token = match.group(1)
        now = self.clock()
        with self._lock:
            self._purge(now)
            return self._by_host.get(token)

    def clear(self) -> None:
        with self._lock:
            self._by_host.clear()
            self._by_id.clear()

    def _purge(self, now: float) -> None:
        expired = [item for item in self._by_id.values() if item.expires_at <= now]
        for item in expired:
            self._by_id.pop(item.share_id, None)
            self._by_host.pop(item.host_token, None)

    def _serialize(self, preview: Preview) -> dict[str, object]:
        url = f"https://loki-{preview.host_token}.{self.base_domain}"
        return {
            "share_id": preview.share_id,
            "url": url,
            "port": preview.port,
            "routes": {route.prefix: route.port for route in preview.routes},
            "cwd": preview.cwd,
            "command": preview.command,
            "created_at": datetime.fromtimestamp(preview.created_at, timezone.utc).isoformat(),
            "expires_at": datetime.fromtimestamp(preview.expires_at, timezone.utc).isoformat(),
            "display_markdown": f"[Open live preview]({url})",
        }


class PreviewProxy:
    def __init__(
        self,
        store: PreviewStore,
        port_validator: Callable[[int], bool],
        *,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self.store = store
        self.port_validator = port_validator
        self.clock = clock
        self._validation_cache: dict[int, float] = {}
        self._validation_locks: dict[int, asyncio.Lock] = {}

    def resolve(self, scope: dict) -> Preview | None:
        return self.store.resolve_host(self._host(scope))

    async def __call__(self, scope: dict, receive: object, send: object) -> None:
        preview = self.resolve(scope)
        if preview is None:
            await self._reject(scope, send, 404, "preview not found")
            return
        routed = self.store.resolve_route(preview, str(scope.get("path", "/")) or "/")
        if routed is None:
            await self._reject(scope, send, 404, "preview route not found")
            return
        route, upstream_path = routed
        if not await self._port_allowed(route.port):
            await self._reject(scope, send, 410, "preview server is no longer available")
            return
        if scope.get("type") == "http":
            await self._proxy_http(scope, receive, send, route, upstream_path)
        elif scope.get("type") == "websocket":
            await self._proxy_websocket(scope, receive, send, route, upstream_path)
        else:
            await self._reject(scope, send, 404, "preview not found")

    async def _port_allowed(self, port: int) -> bool:
        checked_at = self._validation_cache.get(port)
        if checked_at is not None and self.clock() - checked_at <= PORT_VALIDATION_TTL_SECONDS:
            return True
        lock = self._validation_locks.setdefault(port, asyncio.Lock())
        async with lock:
            checked_at = self._validation_cache.get(port)
            if checked_at is not None and self.clock() - checked_at <= PORT_VALIDATION_TTL_SECONDS:
                return True
            try:
                allowed = await asyncio.to_thread(self.port_validator, port)
            except Exception:
                allowed = False
            if allowed:
                self._validation_cache[port] = self.clock()
            else:
                self._validation_cache.pop(port, None)
            return allowed

    async def _proxy_http(
        self, scope: dict, receive: object, send: object, route: PreviewRoute, upstream_path: str,
    ) -> None:
        method = str(scope.get("method", "GET")).upper()
        if method in {"CONNECT", "TRACE"}:
            await self._reject(scope, send, 405, "method not allowed")
            return
        body = bytearray()
        while True:
            message = await receive()
            if message.get("type") == "http.disconnect":
                return
            if message.get("type") != "http.request":
                continue
            body.extend(message.get("body", b""))
            if len(body) > MAX_REQUEST_BODY_BYTES:
                await self._reject(scope, send, 413, "request body is too large")
                return
            if not message.get("more_body", False):
                break

        public_origin = f"https://{self._host(scope).partition(':')[0]}"
        headers = self._request_headers(scope, route.port, public_origin, route.prefix)
        query = bytes(scope.get("query_string", b"")).decode("ascii")
        upstream_url = f"http://127.0.0.1:{route.port}{upstream_path}"
        if query:
            upstream_url += f"?{query}"
        started = False
        try:
            timeout = httpx2.Timeout(connect=10, read=None, write=30, pool=10)
            async with httpx2.AsyncClient(timeout=timeout, trust_env=False, follow_redirects=False) as client:
                async with client.stream(method, upstream_url, headers=headers, content=bytes(body)) as response:
                    response_headers = self._response_headers(
                        response.headers.multi_items(), route.port, public_origin, route.prefix,
                    )
                    await send({
                        "type": "http.response.start",
                        "status": response.status_code,
                        "headers": response_headers,
                    })
                    started = True
                    async for chunk in response.aiter_raw():
                        await send({"type": "http.response.body", "body": chunk, "more_body": True})
                    await send({"type": "http.response.body", "body": b"", "more_body": False})
        except (httpx2.HTTPError, OSError):
            if started:
                await send({"type": "http.response.body", "body": b"", "more_body": False})
            else:
                await self._reject(scope, send, 502, "preview upstream is unavailable")

    async def _proxy_websocket(
        self, scope: dict, receive: object, send: object, route: PreviewRoute, upstream_path: str,
    ) -> None:
        first = await receive()
        if first.get("type") != "websocket.connect":
            await send({"type": "websocket.close", "code": 1002})
            return
        query = bytes(scope.get("query_string", b"")).decode("ascii")
        upstream_url = f"ws://127.0.0.1:{route.port}{upstream_path}"
        if query:
            upstream_url += f"?{query}"
        public_origin = f"https://{self._host(scope).partition(':')[0]}"
        headers = self._request_headers(
            scope, route.port, public_origin, route.prefix, websocket=True,
        )
        subprotocols = list(scope.get("subprotocols", []))
        try:
            async with websocket_connect(
                upstream_url,
                additional_headers=headers,
                subprotocols=subprotocols or None,
                open_timeout=10,
                close_timeout=5,
                max_size=16 * 1024 * 1024,
            ) as upstream:
                await send({
                    "type": "websocket.accept",
                    "subprotocol": upstream.subprotocol,
                    "headers": [(b"x-robots-tag", b"noindex, nofollow, noarchive")],
                })

                async def downstream_to_upstream() -> None:
                    while True:
                        message = await receive()
                        if message.get("type") == "websocket.disconnect":
                            await upstream.close(code=int(message.get("code", 1000)))
                            return
                        if message.get("type") == "websocket.receive":
                            if message.get("bytes") is not None:
                                await upstream.send(message["bytes"])
                            elif message.get("text") is not None:
                                await upstream.send(message["text"])

                async def upstream_to_downstream() -> None:
                    async for message in upstream:
                        if isinstance(message, bytes):
                            await send({"type": "websocket.send", "bytes": message})
                        else:
                            await send({"type": "websocket.send", "text": message})

                tasks = {
                    asyncio.create_task(downstream_to_upstream()),
                    asyncio.create_task(upstream_to_downstream()),
                }
                done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
                for task in pending:
                    task.cancel()
                await asyncio.gather(*done, *pending, return_exceptions=True)
                await send({"type": "websocket.close", "code": 1000})
        except (ConnectionClosed, OSError, TimeoutError):
            await send({"type": "websocket.close", "code": 1011, "reason": "preview upstream unavailable"})

    @staticmethod
    def _request_headers(
        scope: dict,
        port: int,
        public_origin: str,
        prefix: str = "/",
        *,
        websocket: bool = False,
    ) -> list[tuple[str, str]]:
        headers: list[tuple[str, str]] = []
        for raw_name, raw_value in scope.get("headers", []):
            name = raw_name.decode("latin-1").lower()
            if name in HOP_BY_HOP_HEADERS or name in PRIVATE_EDGE_HEADERS or name == "host":
                continue
            value = raw_value.decode("latin-1")
            if name == "cookie":
                cookies = [
                    item.strip() for item in value.split(";")
                    if not item.strip().lower().startswith("cf_authorization=")
                ]
                if not cookies:
                    continue
                value = "; ".join(cookies)
            if websocket and name.startswith("sec-websocket-"):
                continue
            headers.append((name, value))
        if not websocket:
            headers.append(("host", f"127.0.0.1:{port}"))
        headers.extend([
            ("x-forwarded-host", urlsplit(public_origin).netloc),
            ("x-forwarded-proto", "https"),
        ])
        if prefix != "/":
            headers.append(("x-forwarded-prefix", prefix))
        return headers

    @staticmethod
    def _response_headers(
        headers: list[tuple[str, str]],
        port: int,
        public_origin: str,
        prefix: str = "/",
    ) -> list[tuple[bytes, bytes]]:
        result: list[tuple[bytes, bytes]] = []
        for name, value in headers:
            lowered = name.lower()
            if lowered in HOP_BY_HOP_HEADERS or lowered in {
                "cache-control", "content-length", "x-robots-tag"
            }:
                continue
            if lowered == "location":
                parsed = urlsplit(value)
                if parsed.hostname in {"127.0.0.1", "localhost"} and parsed.port == port:
                    public = urlsplit(public_origin)
                    path = parsed.path if prefix == "/" else prefix + parsed.path
                    value = urlunsplit((public.scheme, public.netloc, path, parsed.query, parsed.fragment))
            result.append((lowered.encode("ascii"), value.encode("latin-1")))
        result.extend([
            (b"cache-control", b"no-store"),
            (b"x-robots-tag", b"noindex, nofollow, noarchive"),
        ])
        return result

    @staticmethod
    def _host(scope: dict) -> str:
        for name, value in scope.get("headers", []):
            if name.lower() == b"host":
                try:
                    return value.decode("ascii").lower()
                except UnicodeDecodeError:
                    return ""
        return ""

    @staticmethod
    async def _reject(scope: dict, send: object, status: int, message: str) -> None:
        if scope.get("type") == "websocket":
            await send({"type": "websocket.close", "code": 4404 if status == 404 else 4410})
            return
        body = message.encode("utf-8")
        await send({
            "type": "http.response.start",
            "status": status,
            "headers": [
                (b"content-type", b"text/plain; charset=utf-8"),
                (b"content-length", str(len(body)).encode("ascii")),
                (b"cache-control", b"no-store"),
                (b"x-content-type-options", b"nosniff"),
                (b"x-robots-tag", b"noindex, nofollow, noarchive"),
            ],
        })
        await send({"type": "http.response.body", "body": body})
