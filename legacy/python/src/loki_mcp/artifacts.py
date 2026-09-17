from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
from threading import RLock
from typing import Callable
from urllib.parse import quote
import re
import secrets
import time


TOKEN_PATTERN = re.compile(r"^[A-Za-z0-9_-]{43}$")


@dataclass(frozen=True)
class Artifact:
    data: bytes
    filename: str
    mime_type: str
    sha256: str
    expires_at: float
    disposition: str


class ArtifactStore:
    def __init__(
        self,
        base_url: str,
        allowed_hosts: set[str],
        *,
        max_items: int = 16,
        max_total_bytes: int = 64 * 1024 * 1024,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.allowed_hosts = {host.lower() for host in allowed_hosts}
        self.max_items = max_items
        self.max_total_bytes = max_total_bytes
        self.clock = clock
        self._items: dict[str, Artifact] = {}
        self._lock = RLock()

    def publish(
        self,
        *,
        data: bytes,
        filename: str,
        mime_type: str,
        sha256: str,
        ttl_seconds: int,
        disposition: str = "inline",
    ) -> dict[str, object]:
        if disposition not in {"inline", "attachment"}:
            raise ValueError("artifact disposition must be inline or attachment")
        now = self.clock()
        expires_at = now + ttl_seconds
        with self._lock:
            self._purge(now)
            total_bytes = sum(len(item.data) for item in self._items.values())
            if len(self._items) >= self.max_items or total_bytes + len(data) > self.max_total_bytes:
                raise RuntimeError("temporary artifact capacity is full; wait for links to expire and retry")
            token = secrets.token_urlsafe(32)
            self._items[token] = Artifact(
                data=bytes(data),
                filename=filename,
                mime_type=mime_type,
                sha256=sha256,
                expires_at=expires_at,
                disposition=disposition,
            )
        return {
            "share_id": token,
            "url": f"{self.base_url}/{token}",
            "expires_at": datetime.fromtimestamp(expires_at, timezone.utc).isoformat(),
        }

    def list(self) -> list[dict[str, object]]:
        now = self.clock()
        with self._lock:
            self._purge(now)
            return [
                {
                    "share_id": token,
                    "url": f"{self.base_url}/{token}",
                    "filename": item.filename,
                    "mime_type": item.mime_type,
                    "bytes": len(item.data),
                    "sha256": item.sha256,
                    "disposition": item.disposition,
                    "expires_at": datetime.fromtimestamp(item.expires_at, timezone.utc).isoformat(),
                }
                for token, item in self._items.items()
            ]

    def revoke(self, share_id: str) -> dict[str, object] | None:
        if TOKEN_PATTERN.fullmatch(share_id) is None:
            return None
        now = self.clock()
        with self._lock:
            self._purge(now)
            item = self._items.pop(share_id, None)
        if item is None:
            return None
        return {
            "share_id": share_id,
            "filename": item.filename,
            "bytes": len(item.data),
            "sha256": item.sha256,
        }

    def clear(self) -> None:
        with self._lock:
            self._items.clear()

    async def __call__(self, scope: dict, receive: object, send: object) -> None:
        if scope.get("type") != "http":
            await self._respond(send, 404, b"not found")
            return
        if self._host(scope) not in self.allowed_hosts:
            await self._respond(send, 421, b"misdirected request")
            return
        method = str(scope.get("method", "GET")).upper()
        if method not in {"GET", "HEAD"}:
            await self._respond(send, 405, b"method not allowed", [(b"allow", b"GET, HEAD")])
            return
        path = str(scope.get("path", ""))
        token = path.removeprefix("/artifacts/")
        if path != f"/artifacts/{token}" or TOKEN_PATTERN.fullmatch(token) is None:
            await self._respond(send, 404, b"not found")
            return
        now = self.clock()
        with self._lock:
            self._purge(now)
            artifact = self._items.get(token)
        if artifact is None:
            await self._respond(send, 404, b"not found")
            return

        encoded_name = quote(artifact.filename, safe="")
        headers = [
            (b"content-type", artifact.mime_type.encode("ascii")),
            (b"content-length", str(len(artifact.data)).encode("ascii")),
            (
                b"content-disposition",
                f"{artifact.disposition}; filename=\"artifact\"; filename*=UTF-8''{encoded_name}".encode("ascii"),
            ),
            (b"cache-control", b"no-store"),
            (b"x-content-type-options", b"nosniff"),
            (b"content-security-policy", b"default-src 'none'; sandbox"),
            (b"cross-origin-resource-policy", b"cross-origin"),
            (b"referrer-policy", b"no-referrer"),
        ]
        await send({"type": "http.response.start", "status": 200, "headers": headers})
        await send({
            "type": "http.response.body",
            "body": b"" if method == "HEAD" else artifact.data,
        })

    def _purge(self, now: float) -> None:
        expired = [token for token, item in self._items.items() if item.expires_at <= now]
        for token in expired:
            self._items.pop(token, None)

    @staticmethod
    def _host(scope: dict) -> str:
        for key, value in scope.get("headers", []):
            if key.lower() == b"host":
                try:
                    return value.decode("ascii").lower()
                except UnicodeDecodeError:
                    return ""
        return ""

    @staticmethod
    async def _respond(
        send: object,
        status: int,
        body: bytes,
        extra_headers: list[tuple[bytes, bytes]] | None = None,
    ) -> None:
        headers = [
            (b"content-type", b"text/plain; charset=utf-8"),
            (b"content-length", str(len(body)).encode("ascii")),
            (b"cache-control", b"no-store"),
            *(extra_headers or []),
        ]
        await send({"type": "http.response.start", "status": status, "headers": headers})
        await send({"type": "http.response.body", "body": body})
