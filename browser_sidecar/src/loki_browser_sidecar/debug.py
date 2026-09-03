from __future__ import annotations

from collections import deque
from datetime import datetime, timezone
import re
from typing import Any
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit


MAX_EVENTS = 1_000
MAX_REQUESTS = 2_000
MAX_TEXT_CHARS = 2_000
SENSITIVE_NAME = re.compile(r"(?:auth|cookie|token|secret|password|passwd|api[-_]?key|signature|credential)", re.I)


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds")


def _text(value: object, limit: int = MAX_TEXT_CHARS) -> str:
    rendered = str(value)
    return rendered if len(rendered) <= limit else rendered[:limit] + "…"


def sanitize_headers(value: object) -> dict[str, str]:
    if not isinstance(value, dict):
        return {}
    result: dict[str, str] = {}
    for key, item in value.items():
        name = _text(key, 200)
        result[name] = "[REDACTED]" if SENSITIVE_NAME.search(name) else _text(item)
    return result


def sanitize_url(value: object) -> str:
    url = _text(value, 4_096)
    try:
        parsed = urlsplit(url)
    except ValueError:
        return url
    query = []
    for key, item in parse_qsl(parsed.query, keep_blank_values=True):
        query.append((key, "[REDACTED]" if SENSITIVE_NAME.search(key) else item))
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path, urlencode(query), parsed.fragment))


def _remote_object(value: object) -> str:
    if not isinstance(value, dict):
        return _text(value)
    if "value" in value:
        return _text(value["value"])
    if value.get("unserializableValue") is not None:
        return _text(value["unserializableValue"])
    description = value.get("description") or value.get("className") or value.get("type") or "object"
    return _text(description)


def _stack(value: object, limit: int = 10) -> list[dict[str, Any]]:
    if not isinstance(value, dict):
        return []
    frames = value.get("callFrames")
    if not isinstance(frames, list):
        return []
    result = []
    for frame in frames[:limit]:
        if not isinstance(frame, dict):
            continue
        result.append({
            "function": _text(frame.get("functionName", ""), 300),
            "url": sanitize_url(frame.get("url", "")),
            "line": frame.get("lineNumber"),
            "column": frame.get("columnNumber"),
        })
    return result


class BrowserDebugCollector:
    def __init__(self) -> None:
        self.sequence = 0
        self.console: deque[dict[str, Any]] = deque(maxlen=MAX_EVENTS)
        self.page_errors: deque[dict[str, Any]] = deque(maxlen=MAX_EVENTS)
        self.network_events: deque[dict[str, Any]] = deque(maxlen=MAX_EVENTS)
        self.websocket_events: deque[dict[str, Any]] = deque(maxlen=MAX_EVENTS)
        self.requests: dict[str, dict[str, Any]] = {}
        self.request_order: deque[str] = deque()

    def reset(self) -> None:
        self.sequence = 0
        self.console.clear()
        self.page_errors.clear()
        self.network_events.clear()
        self.websocket_events.clear()
        self.requests.clear()
        self.request_order.clear()

    def _record(self, target: deque[dict[str, Any]], kind: str, session_id: str | None, **data: Any) -> dict[str, Any]:
        self.sequence += 1
        item = {
            "sequence": self.sequence,
            "captured_at": _now(),
            "kind": kind,
            "session_id": session_id,
            **data,
        }
        target.append(item)
        return item

    def _remember_request(self, request_id: str, item: dict[str, Any]) -> None:
        if request_id not in self.requests:
            self.request_order.append(request_id)
        self.requests[request_id] = item
        while len(self.requests) > MAX_REQUESTS:
            oldest = self.request_order.popleft()
            self.requests.pop(oldest, None)

    def console_called(self, event: dict[str, Any], session_id: str | None) -> None:
        args = event.get("args") if isinstance(event.get("args"), list) else []
        self._record(
            self.console,
            "console",
            session_id,
            level=_text(event.get("type", "log"), 50),
            text=" ".join(_remote_object(item) for item in args)[:MAX_TEXT_CHARS],
            stack=_stack(event.get("stackTrace")),
        )

    def log_entry(self, event: dict[str, Any], session_id: str | None) -> None:
        entry = event.get("entry") if isinstance(event.get("entry"), dict) else {}
        self._record(
            self.console,
            "browser_log",
            session_id,
            level=_text(entry.get("level", "info"), 50),
            source=_text(entry.get("source", ""), 100),
            text=_text(entry.get("text", "")),
            url=sanitize_url(entry.get("url", "")),
            line=entry.get("lineNumber"),
            stack=_stack(entry.get("stackTrace")),
        )

    def exception_thrown(self, event: dict[str, Any], session_id: str | None) -> None:
        details = event.get("exceptionDetails") if isinstance(event.get("exceptionDetails"), dict) else {}
        exception = details.get("exception") if isinstance(details.get("exception"), dict) else {}
        self._record(
            self.page_errors,
            "exception",
            session_id,
            text=_text(exception.get("description") or exception.get("value") or details.get("text", "Unknown exception")),
            url=sanitize_url(details.get("url", "")),
            line=details.get("lineNumber"),
            column=details.get("columnNumber"),
            stack=_stack(details.get("stackTrace")),
        )

    def request_will_be_sent(self, event: dict[str, Any], session_id: str | None) -> None:
        request_id = _text(event.get("requestId", ""), 300)
        if not request_id:
            return
        request = event.get("request") if isinstance(event.get("request"), dict) else {}
        item = {
            "request_id": request_id,
            "session_id": session_id,
            "url": sanitize_url(request.get("url", "")),
            "method": _text(request.get("method", "GET"), 30),
            "resource_type": _text(event.get("type", "Other"), 50),
            "request_headers": sanitize_headers(request.get("headers")),
            "status": None,
            "failed": False,
            "finished": False,
            "encoded_bytes": None,
            "mime_type": None,
        }
        self._remember_request(request_id, item)
        self._record(self.network_events, "request", session_id, **{k: item[k] for k in ("request_id", "url", "method", "resource_type")})

    def response_received(self, event: dict[str, Any], session_id: str | None) -> None:
        request_id = _text(event.get("requestId", ""), 300)
        response = event.get("response") if isinstance(event.get("response"), dict) else {}
        item = self.requests.get(request_id, {"request_id": request_id, "session_id": session_id})
        item.update({
            "session_id": session_id,
            "url": sanitize_url(response.get("url", item.get("url", ""))),
            "status": response.get("status"),
            "status_text": _text(response.get("statusText", ""), 300),
            "mime_type": _text(response.get("mimeType", ""), 200),
            "protocol": _text(response.get("protocol", ""), 100),
            "remote_ip": _text(response.get("remoteIPAddress", ""), 100),
            "from_disk_cache": bool(response.get("fromDiskCache", False)),
            "response_headers": sanitize_headers(response.get("headers")),
        })
        self._remember_request(request_id, item)
        self._record(
            self.network_events,
            "response",
            session_id,
            request_id=request_id,
            url=item.get("url", ""),
            status=item.get("status"),
            mime_type=item.get("mime_type"),
            resource_type=_text(event.get("type", item.get("resource_type", "Other")), 50),
        )

    def loading_finished(self, event: dict[str, Any], session_id: str | None) -> None:
        request_id = _text(event.get("requestId", ""), 300)
        item = self.requests.get(request_id, {"request_id": request_id, "session_id": session_id})
        item["finished"] = True
        item["encoded_bytes"] = event.get("encodedDataLength")
        self._remember_request(request_id, item)

    def loading_failed(self, event: dict[str, Any], session_id: str | None) -> None:
        request_id = _text(event.get("requestId", ""), 300)
        item = self.requests.get(request_id, {"request_id": request_id, "session_id": session_id})
        item.update({
            "failed": True,
            "finished": True,
            "error": _text(event.get("errorText", "Network request failed")),
            "blocked_reason": _text(event.get("blockedReason", ""), 200),
            "canceled": bool(event.get("canceled", False)),
        })
        self._remember_request(request_id, item)
        self._record(
            self.network_events,
            "failure",
            session_id,
            request_id=request_id,
            url=item.get("url", ""),
            resource_type=_text(event.get("type", item.get("resource_type", "Other")), 50),
            error=item["error"],
            blocked_reason=item["blocked_reason"],
            canceled=item["canceled"],
        )

    def websocket_created(self, event: dict[str, Any], session_id: str | None) -> None:
        self._record(
            self.websocket_events,
            "created",
            session_id,
            request_id=_text(event.get("requestId", ""), 300),
            url=sanitize_url(event.get("url", "")),
        )

    def websocket_handshake(self, event: dict[str, Any], session_id: str | None, direction: str) -> None:
        detail = event.get("request") if direction == "request" else event.get("response")
        detail = detail if isinstance(detail, dict) else {}
        self._record(
            self.websocket_events,
            f"handshake_{direction}",
            session_id,
            request_id=_text(event.get("requestId", ""), 300),
            status=detail.get("status"),
            headers=sanitize_headers(detail.get("headers")),
        )

    def websocket_frame(self, event: dict[str, Any], session_id: str | None, direction: str) -> None:
        frame = event.get("response") if isinstance(event.get("response"), dict) else {}
        payload = frame.get("payloadData", "")
        self._record(
            self.websocket_events,
            f"frame_{direction}",
            session_id,
            request_id=_text(event.get("requestId", ""), 300),
            opcode=frame.get("opcode"),
            masked=bool(frame.get("mask", False)),
            payload_bytes=len(str(payload).encode("utf-8", errors="replace")),
        )

    def websocket_closed(self, event: dict[str, Any], session_id: str | None) -> None:
        self._record(self.websocket_events, "closed", session_id, request_id=_text(event.get("requestId", ""), 300))

    def websocket_error(self, event: dict[str, Any], session_id: str | None) -> None:
        self._record(
            self.websocket_events,
            "error",
            session_id,
            request_id=_text(event.get("requestId", ""), 300),
            error=_text(event.get("errorMessage", "WebSocket error")),
        )

    @staticmethod
    def _page(items: deque[dict[str, Any]], since_sequence: int, limit: int) -> list[dict[str, Any]]:
        matching = [item for item in items if item["sequence"] > since_sequence]
        return matching[-limit:]

    def console_result(self, level: str | None, since_sequence: int, limit: int) -> dict[str, Any]:
        items = self._page(self.console, since_sequence, MAX_EVENTS)
        if level:
            items = [item for item in items if str(item.get("level", "")).lower() == level.lower()]
        return {"events": items[-limit:], "latest_sequence": self.sequence, "retained": len(self.console)}

    def network_result(
        self,
        status_min: int | None,
        failed_only: bool,
        resource_type: str | None,
        since_sequence: int,
        limit: int,
    ) -> dict[str, Any]:
        values = list(self.requests.values())
        if status_min is not None:
            values = [item for item in values if isinstance(item.get("status"), (int, float)) and item["status"] >= status_min]
        if failed_only:
            values = [item for item in values if item.get("failed") or (isinstance(item.get("status"), (int, float)) and item["status"] >= 400)]
        if resource_type:
            values = [item for item in values if str(item.get("resource_type", "")).lower() == resource_type.lower()]
        if since_sequence:
            ids = {item.get("request_id") for item in self._page(self.network_events, since_sequence, MAX_EVENTS)}
            values = [item for item in values if item.get("request_id") in ids]
        return {"requests": values[-limit:], "latest_sequence": self.sequence, "retained": len(self.requests)}

    def request_result(self, request_id: str) -> dict[str, Any] | None:
        item = self.requests.get(request_id)
        return dict(item) if item is not None else None

    def websocket_result(self, since_sequence: int, limit: int) -> dict[str, Any]:
        return {
            "events": self._page(self.websocket_events, since_sequence, limit),
            "latest_sequence": self.sequence,
            "retained": len(self.websocket_events),
        }

    def page_errors_result(self, since_sequence: int, limit: int) -> dict[str, Any]:
        return {
            "errors": self._page(self.page_errors, since_sequence, limit),
            "latest_sequence": self.sequence,
            "retained": len(self.page_errors),
        }

    def diagnostics_result(self, since_sequence: int, limit: int) -> dict[str, Any]:
        failed = [
            item for item in self.requests.values()
            if item.get("failed") or (isinstance(item.get("status"), (int, float)) and item["status"] >= 400)
        ]
        return {
            "summary": {
                "console_events": len(self.console),
                "page_errors": len(self.page_errors),
                "network_requests": len(self.requests),
                "failed_requests": len(failed),
                "websocket_events": len(self.websocket_events),
                "latest_sequence": self.sequence,
            },
            "recent_console": self._page(self.console, since_sequence, limit),
            "recent_page_errors": self._page(self.page_errors, since_sequence, limit),
            "recent_failed_requests": failed[-limit:],
            "recent_websockets": self._page(self.websocket_events, since_sequence, limit),
        }
