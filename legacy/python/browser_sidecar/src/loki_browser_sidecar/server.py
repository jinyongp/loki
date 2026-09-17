from __future__ import annotations

import asyncio
import base64
import ipaddress
import inspect
import json
import logging
import os
from pathlib import Path
import signal
from typing import Any
from urllib.parse import urlsplit

from browser_use import Browser

from .debug import BrowserDebugCollector


SOCKET_PATH = Path("/run/loki/browser/control.sock")
CHROME_PATH = Path("/opt/loki-browser/chromium/chrome-linux64/chrome")
PROFILE_PATH = Path("/var/lib/loki/browser/browser-use-user-data-dir-persistent")
DOWNLOADS_PATH = Path("/downloads")
PROXY_URL = "http://127.0.0.1:8767"
PROTECTED_LOCAL_PORTS = frozenset({8765, 8766, 8767})
MAX_REQUEST_BYTES = 1_048_576
MAX_RESPONSE_BYTES = 16_777_216
MAX_TEXT_CHARS = 65_536
MAX_INTERACTIVE_ELEMENTS = 1_000
MAX_RESPONSE_BODY_BYTES = 1_048_576
MAX_RESPONSE_BODY_CHARS = 262_144
ACTION_TIMEOUT_SECONDS = 90
LOGGER = logging.getLogger("loki-browser")

KEYS = {
    "Enter": {"key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13},
    "Tab": {"key": "Tab", "code": "Tab", "windowsVirtualKeyCode": 9},
    "Escape": {"key": "Escape", "code": "Escape", "windowsVirtualKeyCode": 27},
    "Backspace": {"key": "Backspace", "code": "Backspace", "windowsVirtualKeyCode": 8},
    "Delete": {"key": "Delete", "code": "Delete", "windowsVirtualKeyCode": 46},
    "ArrowUp": {"key": "ArrowUp", "code": "ArrowUp", "windowsVirtualKeyCode": 38},
    "ArrowDown": {"key": "ArrowDown", "code": "ArrowDown", "windowsVirtualKeyCode": 40},
    "ArrowLeft": {"key": "ArrowLeft", "code": "ArrowLeft", "windowsVirtualKeyCode": 37},
    "ArrowRight": {"key": "ArrowRight", "code": "ArrowRight", "windowsVirtualKeyCode": 39},
    "PageUp": {"key": "PageUp", "code": "PageUp", "windowsVirtualKeyCode": 33},
    "PageDown": {"key": "PageDown", "code": "PageDown", "windowsVirtualKeyCode": 34},
    "Home": {"key": "Home", "code": "Home", "windowsVirtualKeyCode": 36},
    "End": {"key": "End", "code": "End", "windowsVirtualKeyCode": 35},
    "Space": {"key": " ", "code": "Space", "windowsVirtualKeyCode": 32},
}


class BrowserRequestError(Exception):
    pass


def validate_url(value: object) -> str:
    if not isinstance(value, str) or not value or len(value) > 4_096 or "\x00" in value:
        raise BrowserRequestError("url must be a non-empty HTTP or HTTPS URL")
    parsed = urlsplit(value)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
        raise BrowserRequestError("only credential-free HTTP and HTTPS URLs are allowed")
    hostname = parsed.hostname.rstrip(".").lower()
    if hostname == "127.0.0.1":
        port = parsed.port or 80
        if not 1024 <= port <= 65_535 or port in PROTECTED_LOCAL_PORTS:
            raise BrowserRequestError("local development port is not allowed")
        return value
    if hostname == "localhost" or hostname.endswith(".localhost"):
        raise BrowserRequestError("local and private destinations are blocked")
    try:
        address = ipaddress.ip_address(hostname)
    except ValueError:
        pass
    else:
        if not address.is_global:
            raise BrowserRequestError("local and private destinations are blocked")
    if parsed.port is not None and parsed.port not in {80, 443}:
        raise BrowserRequestError("only ports 80 and 443 are allowed")
    return value


def _integer(value: object, name: str, minimum: int, maximum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or not minimum <= value <= maximum:
        raise BrowserRequestError(f"{name} must be an integer between {minimum} and {maximum}")
    return value


class BrowserController:
    def __init__(self) -> None:
        self.browser: Browser | None = None
        self.lock = asyncio.Lock()
        self.debug = BrowserDebugCollector()
        self._debug_registered_clients: set[int] = set()
        self._debug_enabled_sessions: set[str] = set()
        self._owned_target_id: str | None = None

    async def close(self) -> None:
        async with self.lock:
            if self.browser is not None:
                await self.browser.stop()
                self.browser = None
                self._owned_target_id = None

    async def dispatch(self, operation: str, arguments: dict[str, Any]) -> dict[str, Any]:
        async with self.lock:
            handlers: dict[str, Any] = {
                "start": self._start,
                "navigate": self._navigate,
                "state": self._state,
                "click": self._click,
                "type": self._type,
                "press": self._press,
                "scroll": self._scroll,
                "back": self._back,
                "list_tabs": self._list_tabs,
                "switch_tab": self._switch_tab,
                "close_tab": self._close_tab,
                "screenshot": self._screenshot,
                "console": self._console,
                "network": self._network,
                "request": self._request,
                "websockets": self._websockets,
                "page_errors": self._page_errors,
                "debug_diagnostics": self._debug_diagnostics,
                "stop": self._stop,
            }
            handler = handlers.get(operation)
            if handler is None:
                raise BrowserRequestError("unknown browser operation")
            if self.browser is not None and operation not in {
                "start",
                "stop",
                "list_tabs",
                "switch_tab",
                "close_tab",
            }:
                await self._focus_owned_tab()
            result = handler(arguments)
            if inspect.isawaitable(result):
                return await asyncio.wait_for(result, timeout=ACTION_TIMEOUT_SECONDS)
            return result

    def _require_browser(self) -> Browser:
        if self.browser is None:
            raise BrowserRequestError("browser is not running; call browser_start first")
        return self.browser

    async def _focus_owned_tab(self) -> Any:
        browser = self._require_browser()
        target_id = self._owned_target_id
        try:
            session = await browser.get_or_create_cdp_session(target_id=target_id, focus=True)
        except ValueError:
            # The owned tab may have been closed by the page or Chromium. Recover
            # to browser-use's current valid page and make that the new owner.
            self._owned_target_id = None
            session = await browser.get_or_create_cdp_session(target_id=None, focus=True)
        if session is None:
            raise BrowserRequestError("no active browser tab")
        self._owned_target_id = session.target_id
        return session

    async def _ensure_debug_session(self) -> Any:
        browser = self._require_browser()
        session = await self._focus_owned_tab()
        client = session.cdp_client
        client_id = id(client)
        if client_id not in self._debug_registered_clients:
            client.register.Runtime.consoleAPICalled(self.debug.console_called)
            client.register.Runtime.exceptionThrown(self.debug.exception_thrown)
            client.register.Log.entryAdded(self.debug.log_entry)
            client.register.Network.requestWillBeSent(self.debug.request_will_be_sent)
            client.register.Network.responseReceived(self.debug.response_received)
            client.register.Network.loadingFinished(self.debug.loading_finished)
            client.register.Network.loadingFailed(self.debug.loading_failed)
            client.register.Network.webSocketCreated(self.debug.websocket_created)
            client.register.Network.webSocketWillSendHandshakeRequest(
                lambda event, session_id: self.debug.websocket_handshake(event, session_id, "request")
            )
            client.register.Network.webSocketHandshakeResponseReceived(
                lambda event, session_id: self.debug.websocket_handshake(event, session_id, "response")
            )
            client.register.Network.webSocketFrameSent(
                lambda event, session_id: self.debug.websocket_frame(event, session_id, "sent")
            )
            client.register.Network.webSocketFrameReceived(
                lambda event, session_id: self.debug.websocket_frame(event, session_id, "received")
            )
            client.register.Network.webSocketClosed(self.debug.websocket_closed)
            client.register.Network.webSocketFrameError(self.debug.websocket_error)
            self._debug_registered_clients.add(client_id)
        if session.session_id not in self._debug_enabled_sessions:
            await client.send.Runtime.enable(session_id=session.session_id)
            await client.send.Log.enable(session_id=session.session_id)
            await client.send.Network.enable(
                params={
                    "maxTotalBufferSize": 10_485_760,
                    "maxResourceBufferSize": MAX_RESPONSE_BODY_BYTES,
                    "maxPostDataSize": 0,
                },
                session_id=session.session_id,
            )
            self._debug_enabled_sessions.add(session.session_id)
        return session

    async def _start(self, _: dict[str, Any]) -> dict[str, Any]:
        if self.browser is not None:
            session = await self._focus_owned_tab()
            return {"status": "running", "active_tab_id": session.target_id[-4:]}
        PROFILE_PATH.mkdir(parents=True, exist_ok=True, mode=0o700)
        DOWNLOADS_PATH.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.browser = Browser(
            executable_path=CHROME_PATH,
            headless=True,
            chromium_sandbox=False,
            user_data_dir=PROFILE_PATH,
            downloads_path=DOWNLOADS_PATH,
            accept_downloads=True,
            enable_default_extensions=False,
            keep_alive=True,
            args=[
                f"--proxy-server={PROXY_URL}",
                "--proxy-bypass-list=<-loopback>",
                "--disable-quic",
                "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
                "--disable-background-networking",
            ],
        )
        try:
            await self.browser.start()
            self.debug.reset()
            self._debug_registered_clients.clear()
            self._debug_enabled_sessions.clear()
            self._owned_target_id = None
            session = await self._ensure_debug_session()
        except Exception:
            self.browser = None
            raise
        return {"status": "running", "active_tab_id": session.target_id[-4:]}

    async def _stop(self, _: dict[str, Any]) -> dict[str, Any]:
        if self.browser is None:
            return {"status": "stopped"}
        await self.browser.stop()
        self.browser = None
        self._owned_target_id = None
        self.debug.reset()
        self._debug_registered_clients.clear()
        self._debug_enabled_sessions.clear()
        return {"status": "stopped"}

    async def _navigate(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        url = validate_url(arguments.get("url"))
        new_tab = bool(arguments.get("new_tab", False))
        try:
            await browser.navigate_to(url, new_tab=new_tab)
        except RuntimeError as error:
            active_url = "unknown"
            try:
                active_url = (await browser.get_browser_state_summary(include_screenshot=False)).url
            except Exception:
                pass
            raise BrowserRequestError(
                f"navigation to {url} failed (active URL: {active_url}): {error}"
            ) from error
        state = await browser.get_browser_state_summary(include_screenshot=False)
        self._owned_target_id = browser.agent_focus_target_id
        await self._ensure_debug_session()
        return {
            "url": state.url,
            "title": state.title,
            "new_tab": new_tab,
            "active_tab_id": self._owned_target_id[-4:] if self._owned_target_id else None,
        }

    async def _state(self, _: dict[str, Any]) -> dict[str, Any]:
        state = await self._require_browser().get_browser_state_summary(include_screenshot=False)
        elements: list[dict[str, Any]] = []
        for index, element in list(state.dom_state.selector_map.items())[:MAX_INTERACTIVE_ELEMENTS]:
            item: dict[str, Any] = {
                "index": index,
                "tag": element.tag_name,
                "text": element.get_all_children_text(max_depth=2)[:200],
            }
            for attribute in ("aria-label", "href", "name", "placeholder", "role", "type", "value"):
                value = element.attributes.get(attribute)
                if value:
                    item[attribute.replace("-", "_")] = value[:500]
            elements.append(item)
        result: dict[str, Any] = {
            "url": state.url,
            "title": state.title,
            "active_tab_id": self._owned_target_id[-4:] if self._owned_target_id else None,
            "tabs": [
                {
                    "tab_id": tab.target_id[-4:],
                    "url": tab.url,
                    "title": tab.title or "",
                    "active": tab.target_id == self._owned_target_id,
                }
                for tab in state.tabs
            ],
            "interactive_elements": elements,
            "pixels_above": state.pixels_above,
            "pixels_below": state.pixels_below,
        }
        if state.page_info:
            result["viewport"] = {
                "width": state.page_info.viewport_width,
                "height": state.page_info.viewport_height,
                "scroll_x": state.page_info.scroll_x,
                "scroll_y": state.page_info.scroll_y,
                "page_width": state.page_info.page_width,
                "page_height": state.page_info.page_height,
            }
        return result

    async def _click(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        index = arguments.get("index")
        x = arguments.get("x")
        y = arguments.get("y")
        new_tab = bool(arguments.get("new_tab", False))
        if index is not None and (x is not None or y is not None):
            raise BrowserRequestError("provide an element index or coordinates, not both")
        if index is not None:
            index = _integer(index, "index", 0, 1_000_000)
            element = await browser.get_dom_element_by_index(index)
            if element is None:
                raise BrowserRequestError("element index was not found; refresh browser_state")
            if new_tab and element.attributes.get("href"):
                from urllib.parse import urljoin

                state = await browser.get_browser_state_summary(include_screenshot=False)
                url = validate_url(urljoin(state.url, element.attributes["href"]))
                await browser.navigate_to(url, new_tab=True)
            else:
                from browser_use.browser.events import ClickElementEvent

                await browser.event_bus.dispatch(ClickElementEvent(node=element))
            if browser.agent_focus_target_id is not None:
                self._owned_target_id = browser.agent_focus_target_id
            return {"clicked": {"index": index}, "new_tab": new_tab}
        if x is None or y is None:
            raise BrowserRequestError("provide index or both x and y")
        x = _integer(x, "x", 0, 16_384)
        y = _integer(y, "y", 0, 16_384)
        from browser_use.browser.events import ClickCoordinateEvent

        await browser.event_bus.dispatch(ClickCoordinateEvent(coordinate_x=x, coordinate_y=y))
        return {"clicked": {"x": x, "y": y}, "new_tab": False}

    async def _type(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        index = _integer(arguments.get("index"), "index", 0, 1_000_000)
        text = arguments.get("text")
        if not isinstance(text, str) or len(text) > MAX_TEXT_CHARS or "\x00" in text:
            raise BrowserRequestError("text must be a string of at most 65536 characters")
        element = await browser.get_dom_element_by_index(index)
        if element is None:
            raise BrowserRequestError("element index was not found; refresh browser_state")
        from browser_use.browser.events import TypeTextEvent

        await browser.event_bus.dispatch(TypeTextEvent(node=element, text=text, is_sensitive=False))
        return {"typed": True, "index": index, "characters": len(text)}

    async def _press(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        key = arguments.get("key")
        if key not in KEYS:
            raise BrowserRequestError(f"unsupported key; choose one of: {', '.join(KEYS)}")
        session = await self._focus_owned_tab()
        params = KEYS[key]
        await session.cdp_client.send.Input.dispatchKeyEvent(
            params={"type": "keyDown", **params}, session_id=session.session_id
        )
        await session.cdp_client.send.Input.dispatchKeyEvent(
            params={"type": "keyUp", **params}, session_id=session.session_id
        )
        return {"pressed": key}

    async def _scroll(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        direction = arguments.get("direction", "down")
        if direction not in {"up", "down"}:
            raise BrowserRequestError("direction must be up or down")
        amount = _integer(arguments.get("amount", 500), "amount", 1, 10_000)
        from browser_use.browser.events import ScrollEvent

        await browser.event_bus.dispatch(ScrollEvent(direction=direction, amount=amount))
        return {"direction": direction, "amount": amount}

    async def _back(self, _: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        from browser_use.browser.events import GoBackEvent

        await browser.event_bus.dispatch(GoBackEvent())
        state = await browser.get_browser_state_summary(include_screenshot=False)
        return {"url": state.url, "title": state.title}

    async def _list_tabs(self, _: dict[str, Any]) -> dict[str, Any]:
        tabs = await self._require_browser().get_tabs()
        return {
            "active_tab_id": self._owned_target_id[-4:] if self._owned_target_id else None,
            "tabs": [
                {
                    "tab_id": tab.target_id[-4:],
                    "url": tab.url,
                    "title": tab.title or "",
                    "active": tab.target_id == self._owned_target_id,
                }
                for tab in tabs
            ],
        }

    async def _switch_tab(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        tab_id = arguments.get("tab_id")
        if not isinstance(tab_id, str) or len(tab_id) != 4:
            raise BrowserRequestError("tab_id must be the four-character id from browser_list_tabs")
        from browser_use.browser.events import SwitchTabEvent

        target_id = await browser.get_target_id_from_tab_id(tab_id)
        await browser.event_bus.dispatch(SwitchTabEvent(target_id=target_id))
        self._owned_target_id = target_id
        state = await browser.get_browser_state_summary(include_screenshot=False)
        await self._ensure_debug_session()
        return {"tab_id": tab_id, "url": state.url, "title": state.title}

    async def _close_tab(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        tab_id = arguments.get("tab_id")
        if not isinstance(tab_id, str) or len(tab_id) != 4:
            raise BrowserRequestError("tab_id must be the four-character id from browser_list_tabs")
        from browser_use.browser.events import CloseTabEvent

        target_id = await browser.get_target_id_from_tab_id(tab_id)
        await browser.event_bus.dispatch(CloseTabEvent(target_id=target_id))
        if target_id == self._owned_target_id:
            self._owned_target_id = None
            remaining = await browser.get_tabs()
            if remaining:
                session = await browser.get_or_create_cdp_session(target_id=None, focus=True)
                self._owned_target_id = session.target_id
        return {
            "closed": tab_id,
            "active_tab_id": self._owned_target_id[-4:] if self._owned_target_id else None,
        }

    async def _screenshot(self, arguments: dict[str, Any]) -> dict[str, Any]:
        browser = self._require_browser()
        data = await browser.take_screenshot(full_page=bool(arguments.get("full_page", False)))
        if len(data) > 10_485_760:
            raise BrowserRequestError("screenshot exceeds 10 MiB")
        return {"mime_type": "image/png", "bytes": len(data), "data_base64": base64.b64encode(data).decode("ascii")}

    async def _console(self, arguments: dict[str, Any]) -> dict[str, Any]:
        await self._ensure_debug_session()
        level = arguments.get("level")
        if level is not None and (not isinstance(level, str) or len(level) > 50):
            raise BrowserRequestError("level must be a string of at most 50 characters")
        since = _integer(arguments.get("since_sequence", 0), "since_sequence", 0, 2_147_483_647)
        limit = _integer(arguments.get("limit", 100), "limit", 1, 500)
        return self.debug.console_result(level, since, limit)

    async def _network(self, arguments: dict[str, Any]) -> dict[str, Any]:
        await self._ensure_debug_session()
        status_min = arguments.get("status_min")
        if status_min is not None:
            status_min = _integer(status_min, "status_min", 100, 599)
        resource_type = arguments.get("resource_type")
        if resource_type is not None and (not isinstance(resource_type, str) or len(resource_type) > 50):
            raise BrowserRequestError("resource_type must be a string of at most 50 characters")
        since = _integer(arguments.get("since_sequence", 0), "since_sequence", 0, 2_147_483_647)
        limit = _integer(arguments.get("limit", 100), "limit", 1, 500)
        return self.debug.network_result(
            status_min,
            bool(arguments.get("failed_only", False)),
            resource_type,
            since,
            limit,
        )

    async def _request(self, arguments: dict[str, Any]) -> dict[str, Any]:
        session = await self._ensure_debug_session()
        request_id = arguments.get("request_id")
        if not isinstance(request_id, str) or not request_id or len(request_id) > 300:
            raise BrowserRequestError("request_id must be a non-empty string returned by browser_network")
        detail = self.debug.request_result(request_id)
        if detail is None:
            raise BrowserRequestError("request_id is not retained; call browser_network for current ids")
        include_body = bool(arguments.get("include_body", False))
        if not include_body:
            return detail
        maximum = _integer(arguments.get("max_body_chars", 65_536), "max_body_chars", 1, MAX_RESPONSE_BODY_CHARS)
        mime_type = str(detail.get("mime_type") or "").lower()
        text_like = (
            mime_type.startswith("text/")
            or any(marker in mime_type for marker in ("json", "javascript", "xml", "svg", "x-www-form-urlencoded"))
        )
        if not text_like:
            raise BrowserRequestError("response body is available only for retained text-like responses")
        encoded_bytes = detail.get("encoded_bytes")
        if not isinstance(encoded_bytes, (int, float)) or encoded_bytes > MAX_RESPONSE_BODY_BYTES:
            raise BrowserRequestError("response body exceeds the 1 MiB retrieval limit or has not finished")
        try:
            body_result = await session.cdp_client.send.Network.getResponseBody(
                params={"requestId": request_id},
                session_id=detail.get("session_id") or session.session_id,
            )
        except Exception as error:
            raise BrowserRequestError(f"response body is no longer available: {error}") from error
        body = body_result.get("body", "")
        if body_result.get("base64Encoded"):
            try:
                body = base64.b64decode(body, validate=True).decode("utf-8", errors="replace")
            except (ValueError, UnicodeDecodeError) as error:
                raise BrowserRequestError("response body encoding is invalid") from error
        detail["body"] = str(body)[:maximum]
        detail["body_truncated"] = len(str(body)) > maximum
        return detail

    async def _websockets(self, arguments: dict[str, Any]) -> dict[str, Any]:
        await self._ensure_debug_session()
        since = _integer(arguments.get("since_sequence", 0), "since_sequence", 0, 2_147_483_647)
        limit = _integer(arguments.get("limit", 100), "limit", 1, 500)
        return self.debug.websocket_result(since, limit)

    async def _page_errors(self, arguments: dict[str, Any]) -> dict[str, Any]:
        await self._ensure_debug_session()
        since = _integer(arguments.get("since_sequence", 0), "since_sequence", 0, 2_147_483_647)
        limit = _integer(arguments.get("limit", 100), "limit", 1, 500)
        return self.debug.page_errors_result(since, limit)

    async def _debug_diagnostics(self, arguments: dict[str, Any]) -> dict[str, Any]:
        await self._ensure_debug_session()
        since = _integer(arguments.get("since_sequence", 0), "since_sequence", 0, 2_147_483_647)
        limit = _integer(arguments.get("limit", 50), "limit", 1, 200)
        result = self.debug.diagnostics_result(since, limit)
        state = await self._require_browser().get_browser_state_summary(include_screenshot=False)
        result["page"] = {"url": state.url, "title": state.title}
        return result


async def serve() -> None:
    controller = BrowserController()
    stop_event = asyncio.Event()

    async def handle(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            raw = await reader.readline()
            if not raw or len(raw) > MAX_REQUEST_BYTES:
                raise BrowserRequestError("invalid or oversized request")
            request = json.loads(raw.decode("utf-8"))
            if not isinstance(request, dict) or not isinstance(request.get("operation"), str):
                raise BrowserRequestError("request must include an operation")
            arguments = request.get("arguments", {})
            if not isinstance(arguments, dict):
                raise BrowserRequestError("arguments must be an object")
            result = await controller.dispatch(request["operation"], arguments)
            response = {"ok": True, "result": result}
        except (BrowserRequestError, asyncio.TimeoutError, json.JSONDecodeError, UnicodeDecodeError) as error:
            response = {"ok": False, "error": str(error) or "browser request failed"}
        except Exception:
            LOGGER.exception("Unexpected browser sidecar failure")
            response = {"ok": False, "error": "unexpected browser sidecar failure"}
        encoded = (json.dumps(response, separators=(",", ":")) + "\n").encode("utf-8")
        if len(encoded) > MAX_RESPONSE_BYTES:
            encoded = b'{"ok":false,"error":"browser response exceeds limit"}\n'
        writer.write(encoded)
        await writer.drain()
        writer.close()
        await writer.wait_closed()

    SOCKET_PATH.parent.mkdir(parents=True, exist_ok=True)
    SOCKET_PATH.unlink(missing_ok=True)
    server = await asyncio.start_unix_server(handle, path=SOCKET_PATH, limit=MAX_REQUEST_BYTES + 1)
    os.chmod(SOCKET_PATH, 0o660)
    loop = asyncio.get_running_loop()
    for signum in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(signum, stop_event.set)
    try:
        async with server:
            await stop_event.wait()
    finally:
        await controller.close()
        SOCKET_PATH.unlink(missing_ok=True)


def main() -> None:
    logging.basicConfig(level=logging.INFO)
    asyncio.run(serve())


if __name__ == "__main__":
    main()
