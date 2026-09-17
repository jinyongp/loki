import asyncio
from urllib.parse import urlsplit

import pytest

from loki_mcp.artifacts import ArtifactStore


def call_artifact(
    store: ArtifactStore,
    path: str,
    *,
    method: str = "GET",
    host: str = "mcp.example.com",
) -> list[dict]:
    messages: list[dict] = []

    async def receive() -> dict:
        return {"type": "http.request", "body": b"", "more_body": False}

    async def send(message: dict) -> None:
        messages.append(message)

    asyncio.run(store({
        "type": "http",
        "method": method,
        "path": path,
        "headers": [(b"host", host.encode("ascii"))],
    }, receive, send))
    return messages


def test_serves_published_artifact_and_head_without_body() -> None:
    store = ArtifactStore("https://mcp.example.com/artifacts", {"mcp.example.com"})
    published = store.publish(
        data=b"png-data",
        filename="shot.png",
        mime_type="image/png",
        sha256="a" * 64,
        ttl_seconds=900,
    )
    path = urlsplit(str(published["url"])).path
    messages = call_artifact(store, path)
    assert messages[0]["status"] == 200
    assert messages[1]["body"] == b"png-data"
    headers = dict(messages[0]["headers"])
    assert headers[b"content-type"] == b"image/png"
    assert headers[b"cache-control"] == b"no-store"
    assert headers[b"content-disposition"].startswith(b"inline;")

    head = call_artifact(store, path, method="HEAD")
    assert head[0]["status"] == 200
    assert head[1]["body"] == b""


def test_rejects_expired_unknown_wrong_host_and_wrong_method() -> None:
    now = [1_000.0]
    store = ArtifactStore(
        "https://mcp.example.com/artifacts",
        {"mcp.example.com"},
        clock=lambda: now[0],
    )
    published = store.publish(
        data=b"png-data",
        filename="shot.png",
        mime_type="image/png",
        sha256="a" * 64,
        ttl_seconds=60,
    )
    path = urlsplit(str(published["url"])).path
    assert call_artifact(store, path, host="other.example.com")[0]["status"] == 421
    assert call_artifact(store, path, method="POST")[0]["status"] == 405
    assert call_artifact(store, "/artifacts/not-a-token")[0]["status"] == 404
    now[0] += 61
    assert call_artifact(store, path)[0]["status"] == 404


def test_enforces_artifact_capacity() -> None:
    store = ArtifactStore(
        "https://mcp.example.com/artifacts",
        {"mcp.example.com"},
        max_items=1,
    )
    values = {
        "data": b"png-data",
        "filename": "shot.png",
        "mime_type": "image/png",
        "sha256": "a" * 64,
        "ttl_seconds": 900,
    }
    store.publish(**values)
    with pytest.raises(RuntimeError):
        store.publish(**values)


def test_lists_revokes_and_serves_downloadable_attachment() -> None:
    store = ArtifactStore("https://mcp.example.com/artifacts", {"mcp.example.com"})
    published = store.publish(
        data=b"archive-data",
        filename="result.zip",
        mime_type="application/zip",
        sha256="b" * 64,
        ttl_seconds=900,
        disposition="attachment",
    )
    listed = store.list()
    assert listed[0]["share_id"] == published["share_id"]
    assert listed[0]["disposition"] == "attachment"
    path = urlsplit(str(published["url"])).path
    headers = dict(call_artifact(store, path)[0]["headers"])
    assert headers[b"content-disposition"].startswith(b"attachment;")
    assert store.revoke(str(published["share_id"]))["filename"] == "result.zip"
    assert call_artifact(store, path)[0]["status"] == 404
