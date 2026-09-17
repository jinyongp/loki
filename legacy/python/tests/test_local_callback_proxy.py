from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread

import httpx2

from loki_mcp.local_callback_proxy import LocalCallbackProxy


class _UpstreamHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        body = f"{self.path}|{self.headers['Host']}".encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_: object) -> None:
        return


def test_fixed_callback_proxy_forwards_and_clears_selected_session() -> None:
    upstream = ThreadingHTTPServer(("127.0.0.1", 0), _UpstreamHandler)
    upstream_thread = Thread(target=upstream.serve_forever, daemon=True)
    upstream_thread.start()
    targets = {"api-session": int(upstream.server_address[1])}
    proxy = LocalCallbackProxy(lambda session_id: targets.get(session_id), port=0)
    try:
        binding = proxy.bind("api-session")
        assert binding["bound"] is True
        assert binding["target_port"] == upstream.server_address[1]
        response = httpx2.get(
            f"{binding['origin']}/v1/auth/oauth/google/callback?code=test",
            timeout=5,
        )
        assert response.status_code == 200
        assert response.text == (
            "/v1/auth/oauth/google/callback?code=test|"
            + str(binding["origin"]).removeprefix("http://")
        )

        proxy.clear("api-session")
        unavailable = httpx2.get(f"{binding['origin']}/healthz", timeout=5)
        assert unavailable.status_code == 503
    finally:
        proxy.close()
        upstream.shutdown()
        upstream.server_close()
        upstream_thread.join(timeout=2)
