from __future__ import annotations

from pathlib import Path
import sys


BROWSER_SOURCE = Path(__file__).parents[1] / "browser_sidecar" / "src"
sys.path.insert(0, str(BROWSER_SOURCE))

from loki_browser_sidecar.debug import BrowserDebugCollector, MAX_EVENTS, sanitize_headers, sanitize_url


def test_sensitive_headers_and_query_values_are_redacted() -> None:
    headers = sanitize_headers({"Authorization": "Bearer secret", "X-Api-Key": "key", "Accept": "text/html"})
    assert headers == {
        "Authorization": "[REDACTED]",
        "X-Api-Key": "[REDACTED]",
        "Accept": "text/html",
    }
    assert sanitize_url("https://example.com/a?token=secret&page=2") == (
        "https://example.com/a?token=%5BREDACTED%5D&page=2"
    )


def test_console_is_bounded_and_remote_objects_do_not_expose_object_ids() -> None:
    collector = BrowserDebugCollector()
    for index in range(MAX_EVENTS + 5):
        collector.console_called(
            {
                "type": "log",
                "args": [{"type": "object", "description": f"Object {index}", "objectId": f"secret-{index}"}],
            },
            "session",
        )
    result = collector.console_result(None, 0, MAX_EVENTS)
    assert len(result["events"]) == MAX_EVENTS
    assert result["events"][0]["sequence"] == 6
    assert "secret-" not in str(result)


def test_network_details_are_redacted_filterable_and_incremental() -> None:
    collector = BrowserDebugCollector()
    collector.request_will_be_sent(
        {
            "requestId": "req-1",
            "type": "Script",
            "request": {
                "url": "https://example.com/app.js?signature=hidden&v=1",
                "method": "GET",
                "headers": {"Cookie": "session=hidden", "Accept": "*/*"},
            },
        },
        "session",
    )
    sequence = collector.sequence
    collector.response_received(
        {
            "requestId": "req-1",
            "type": "Script",
            "response": {
                "url": "https://example.com/app.js?signature=hidden&v=1",
                "status": 503,
                "statusText": "Unavailable",
                "mimeType": "text/javascript",
                "headers": {"Set-Cookie": "session=hidden", "Content-Type": "text/javascript"},
            },
        },
        "session",
    )
    collector.loading_finished({"requestId": "req-1", "encodedDataLength": 123}, "session")

    result = collector.network_result(500, True, "script", sequence, 10)
    assert len(result["requests"]) == 1
    request = result["requests"][0]
    assert request["request_headers"]["Cookie"] == "[REDACTED]"
    assert request["response_headers"]["Set-Cookie"] == "[REDACTED]"
    assert "hidden" not in request["url"]


def test_websocket_frames_expose_metadata_not_payload() -> None:
    collector = BrowserDebugCollector()
    collector.websocket_created({"requestId": "ws-1", "url": "wss://example.com/hmr?token=hidden"}, "session")
    collector.websocket_frame(
        {"requestId": "ws-1", "response": {"opcode": 1, "mask": False, "payloadData": "private payload"}},
        "session",
        "received",
    )
    result = collector.websocket_result(0, 10)
    assert result["events"][0]["url"].endswith("token=%5BREDACTED%5D")
    assert result["events"][1]["payload_bytes"] == len("private payload")
    assert "private payload" not in str(result)


def test_diagnostics_collects_page_and_network_failures() -> None:
    collector = BrowserDebugCollector()
    collector.exception_thrown(
        {
            "exceptionDetails": {
                "text": "Uncaught",
                "url": "https://example.com/app.js",
                "lineNumber": 10,
                "exception": {"description": "Error: boom"},
            }
        },
        "session",
    )
    collector.request_will_be_sent(
        {"requestId": "bad", "request": {"url": "https://example.com/missing", "method": "GET"}},
        "session",
    )
    collector.loading_failed({"requestId": "bad", "type": "Fetch", "errorText": "net::ERR_FAILED"}, "session")
    result = collector.diagnostics_result(0, 10)
    assert result["summary"]["page_errors"] == 1
    assert result["summary"]["failed_requests"] == 1
    assert result["recent_failed_requests"][0]["error"] == "net::ERR_FAILED"
