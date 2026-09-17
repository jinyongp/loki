import socket

import pytest

from loki_mcp.browser_proxy import (
    ProxyError,
    _authority,
    _connect_target,
    _forward_headers,
    _public_addresses,
    _target_addresses,
)


def test_proxy_rejects_private_and_loopback_addresses(monkeypatch: pytest.MonkeyPatch) -> None:
    for address in ("127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1"):
        monkeypatch.setattr(
            socket,
            "getaddrinfo",
            lambda host, port, type: [(socket.AF_INET6 if ":" in address else socket.AF_INET, socket.SOCK_STREAM, 6, "", (address, port))],
        )
        with pytest.raises(ProxyError):
            _public_addresses("example.test", 443)


def test_proxy_accepts_only_when_every_resolved_address_is_public(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(
        socket,
        "getaddrinfo",
        lambda host, port, type: [(socket.AF_INET, socket.SOCK_STREAM, 6, "", ("93.184.216.34", port))],
    )
    assert _public_addresses("example.com", 443)[0][4][0] == "93.184.216.34"

    monkeypatch.setattr(
        socket,
        "getaddrinfo",
        lambda host, port, type: [
            (socket.AF_INET, socket.SOCK_STREAM, 6, "", ("93.184.216.34", port)),
            (socket.AF_INET, socket.SOCK_STREAM, 6, "", ("127.0.0.1", port)),
        ],
    )
    with pytest.raises(ProxyError):
        _public_addresses("rebind.test", 443)


def test_proxy_authority_rejects_credentials_and_parses_ipv6() -> None:
    assert _authority("example.com:443", 443) == ("example.com", 443)
    assert _authority("[2001:4860:4860::8888]:443", 443) == ("2001:4860:4860::8888", 443)
    with pytest.raises(ProxyError):
        _authority("user:password@example.com:443", 443)


def test_proxy_allows_only_validated_workspace_loopback_port(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr("loki_mcp.browser_proxy._workspace_port_is_allowed", lambda port: port == 5173)
    assert _target_addresses("127.0.0.1", 5173)[0][4] == ("127.0.0.1", 5173)
    with pytest.raises(ProxyError):
        _target_addresses("127.0.0.1", 3000)


def test_proxy_keeps_other_private_destinations_blocked(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(
        socket,
        "getaddrinfo",
        lambda host, port, type: [(socket.AF_INET, socket.SOCK_STREAM, 6, "", ("127.0.0.1", port))],
    )
    with pytest.raises(ProxyError):
        _target_addresses("localhost", 5173)


def test_proxy_connection_timeout_does_not_apply_to_relay(monkeypatch: pytest.MonkeyPatch) -> None:
    class FakeSocket:
        def __init__(self, *_: object) -> None:
            self.timeouts: list[float | None] = []

        def settimeout(self, value: float | None) -> None:
            self.timeouts.append(value)

        def connect(self, _: tuple[str, int]) -> None:
            pass

        def close(self) -> None:
            pass

    created = FakeSocket()
    monkeypatch.setattr(
        "loki_mcp.browser_proxy._target_addresses",
        lambda host, port: [(socket.AF_INET, socket.SOCK_STREAM, socket.IPPROTO_TCP, "", (host, port))],
    )
    monkeypatch.setattr(socket, "socket", lambda *_: created)

    assert _connect_target("127.0.0.1", 5173) is created
    assert created.timeouts == [15, None]


def test_proxy_closes_ordinary_http_but_preserves_websocket_upgrade() -> None:
    ordinary = _forward_headers(
        [b"Host: 127.0.0.1:5173\r\n", b"Connection: keep-alive\r\n", b"Proxy-Connection: keep-alive\r\n"]
    )
    assert ordinary == [b"Host: 127.0.0.1:5173\r\n", b"Connection: close\r\n"]

    websocket = _forward_headers(
        [b"Host: 127.0.0.1:5173\r\n", b"Connection: Upgrade\r\n", b"Upgrade: websocket\r\n"]
    )
    assert b"Connection: Upgrade\r\n" in websocket
    assert b"Upgrade: websocket\r\n" in websocket
    assert b"Connection: close\r\n" not in websocket
