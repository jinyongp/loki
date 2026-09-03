import asyncio

from loki_mcp.previews import PreviewProxy, PreviewStore


def test_preview_store_resolves_lists_revokes_and_expires() -> None:
    now = [1_000.0]
    store = PreviewStore("preview.example.com", clock=lambda: now[0])
    published = store.publish(port=5173, cwd="/workspace/app", command="node", ttl_seconds=60)
    host = str(published["url"]).removeprefix("https://")
    resolved = store.resolve_host(host)
    assert resolved is not None
    assert resolved.port == 5173
    assert store.list()[0]["share_id"] == published["share_id"]
    assert store.resolve_host("other.example.com") is None

    revoked = store.revoke(str(published["share_id"]))
    assert revoked is not None
    assert store.resolve_host(host) is None

    second = store.publish(port=4173, cwd="/workspace/app", command="node", ttl_seconds=60)
    now[0] += 61
    assert store.resolve_host(str(second["url"]).removeprefix("https://")) is None
    assert store.list() == []


def test_preview_store_routes_stack_paths_and_reserves_internal_namespace() -> None:
    store = PreviewStore("preview.example.com")
    published = store.publish_stack(
        routes={"/": 42100, "/_loki/api": 41280, "/_loki/ops": 41281},
        cwd="/workspace/stamp.is-web",
        command="pnpm dev",
        ttl_seconds=60,
    )
    preview = store.resolve_host(str(published["url"]).removeprefix("https://"))
    assert preview is not None
    assert store.resolve_route(preview, "/products") == (preview.routes[-1], "/products")
    api_route = next(route for route in preview.routes if route.prefix == "/_loki/api")
    assert store.resolve_route(preview, "/_loki/api/v1/session") == (api_route, "/v1/session")
    assert store.resolve_route(preview, "/_loki/unknown") is None
    assert published["routes"] == {
        "/": 42100,
        "/_loki/api": 41280,
        "/_loki/ops": 41281,
    }


def test_preview_proxy_forwards_http_to_fixed_local_port() -> None:
    async def verify() -> None:
        captured = bytearray()

        async def upstream(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
            while b"\r\n\r\n" not in captured:
                captured.extend(await reader.read(4096))
            writer.write(
                b"HTTP/1.1 200 OK\r\n"
                b"Content-Type: text/plain\r\n"
                b"Content-Length: 2\r\n"
                b"Connection: close\r\n\r\nok"
            )
            await writer.drain()
            writer.close()
            await writer.wait_closed()

        server = await asyncio.start_server(upstream, "127.0.0.1", 0)
        port = int(server.sockets[0].getsockname()[1])
        store = PreviewStore("preview.example.com")
        published = store.publish(port=port, cwd="/workspace/app", command="node", ttl_seconds=60)
        host = str(published["url"]).removeprefix("https://")
        proxy = PreviewProxy(store, lambda candidate: candidate == port)
        messages: list[dict] = []
        requests = [{"type": "http.request", "body": b"", "more_body": False}]

        async def receive() -> dict:
            return requests.pop(0)

        async def send(message: dict) -> None:
            messages.append(message)

        try:
            await proxy({
                "type": "http",
                "method": "GET",
                "path": "/hello",
                "query_string": b"name=loki",
                "headers": [
                    (b"host", host.encode("ascii")),
                    (b"cf-access-jwt-assertion", b"private-edge-token"),
                    (b"cookie", b"CF_Authorization=edge; app=value"),
                ],
            }, receive, send)
        finally:
            server.close()
            await server.wait_closed()

        assert messages[0]["status"] == 200
        assert b"".join(item.get("body", b"") for item in messages[1:]) == b"ok"
        request_text = captured.decode("latin-1").lower()
        assert "get /hello?name=loki http/1.1" in request_text
        assert f"host: 127.0.0.1:{port}" in request_text
        assert "cf-access-jwt-assertion" not in request_text
        assert "cf_authorization" not in request_text
        assert "cookie: app=value" in request_text

    asyncio.run(verify())


def test_preview_proxy_strips_stack_prefix_and_forwards_it() -> None:
    async def verify() -> None:
        captured = bytearray()

        async def upstream(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
            while b"\r\n\r\n" not in captured:
                captured.extend(await reader.read(4096))
            writer.write(
                b"HTTP/1.1 302 Found\r\n"
                + f"Location: http://127.0.0.1:{port}/login\r\n".encode()
                + b"Content-Length: 0\r\nConnection: close\r\n\r\n"
            )
            await writer.drain()
            writer.close()
            await writer.wait_closed()

        server = await asyncio.start_server(upstream, "127.0.0.1", 0)
        port = int(server.sockets[0].getsockname()[1])
        store = PreviewStore("preview.example.com")
        published = store.publish_stack(
            routes={"/": 42100, "/_loki/api": port},
            cwd="/workspace/app", command="node", ttl_seconds=60,
        )
        host = str(published["url"]).removeprefix("https://")
        proxy = PreviewProxy(store, lambda candidate: candidate == port)
        messages: list[dict] = []
        requests = [{"type": "http.request", "body": b"", "more_body": False}]

        async def receive() -> dict:
            return requests.pop(0)

        async def send(message: dict) -> None:
            messages.append(message)

        try:
            await proxy({
                "type": "http", "method": "GET", "path": "/_loki/api/v1/session",
                "query_string": b"", "headers": [(b"host", host.encode("ascii"))],
            }, receive, send)
        finally:
            server.close()
            await server.wait_closed()

        request_text = captured.decode("latin-1").lower()
        assert "get /v1/session http/1.1" in request_text
        assert "x-forwarded-prefix: /_loki/api" in request_text
        response_headers = dict(messages[0]["headers"])
        assert response_headers[b"location"] == (
            f"https://{host}/_loki/api/login".encode("latin-1")
        )

    asyncio.run(verify())


def test_preview_proxy_rejects_stopped_upstream() -> None:
    async def verify() -> None:
        store = PreviewStore("preview.example.com")
        published = store.publish(port=5173, cwd="/workspace/app", command="node", ttl_seconds=60)
        host = str(published["url"]).removeprefix("https://")
        proxy = PreviewProxy(store, lambda _: False)
        messages: list[dict] = []

        async def receive() -> dict:
            return {"type": "http.request", "body": b"", "more_body": False}

        async def send(message: dict) -> None:
            messages.append(message)

        await proxy({
            "type": "http",
            "method": "GET",
            "path": "/",
            "query_string": b"",
            "headers": [(b"host", host.encode("ascii"))],
        }, receive, send)
        assert messages[0]["status"] == 410

    asyncio.run(verify())


def test_preview_proxy_coalesces_concurrent_port_validation() -> None:
    async def verify() -> None:
        calls = 0

        def validate(_: int) -> bool:
            nonlocal calls
            calls += 1
            return True

        store = PreviewStore("preview.example.com")
        proxy = PreviewProxy(store, validate)
        results = await asyncio.gather(*(proxy._port_allowed(5173) for _ in range(32)))
        assert all(results)
        assert calls == 1

    asyncio.run(verify())
