from __future__ import annotations


LIVE_PREVIEW_URI = "ui://loki/live-preview-v1.html"
LIVE_PREVIEW_MIME_TYPE = "text/html;profile=mcp-app"


def live_preview_html() -> str:
    return """<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    :root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, sans-serif; }
    body { margin: 0; padding: 8px; }
    [hidden] { display: none !important; }
    main { display: grid; gap: 8px; }
    header { display: flex; align-items: center; gap: 8px; min-width: 0; }
    #title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
    #meta { color: GrayText; font-size: 12px; }
    button, a { color: LinkText; background: transparent; border: 0; padding: 4px; font: inherit; cursor: pointer; }
    #frame-wrap { position: relative; height: min(70vh, 720px); min-height: 420px; overflow: hidden; border: 1px solid color-mix(in srgb, CanvasText 18%, transparent); border-radius: 12px; background: Canvas; }
    iframe { display: block; width: 100%; height: 100%; border: 0; background: white; }
    #status { position: absolute; inset: 0; display: grid; place-items: center; color: GrayText; background: Canvas; }
    #error { padding: 16px; color: CanvasText; }
  </style>
</head>
<body>
  <div id="error">Waiting for a live preview…</div>
  <main hidden>
    <header>
      <span id="title">Loki live preview</span>
      <span id="meta"></span>
      <button id="reload" type="button">Reload</button>
      <button id="fullscreen" type="button">Fullscreen</button>
      <a id="open" target="_blank" rel="noopener noreferrer">Open in new tab</a>
    </header>
    <div id="frame-wrap">
      <div id="status">Loading preview…</div>
      <iframe id="preview" title="Loki live development preview" sandbox="allow-scripts allow-same-origin allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads allow-pointer-lock" allow="clipboard-read; clipboard-write"></iframe>
    </div>
  </main>
  <script>
    const error = document.getElementById("error");
    const main = document.querySelector("main");
    const frame = document.getElementById("preview");
    const status = document.getElementById("status");
    const title = document.getElementById("title");
    const meta = document.getElementById("meta");
    const open = document.getElementById("open");
    let currentUrl = "";
    let lastReportedHeight = 0;

    function reportHeight() {
      const height = Math.ceil(document.documentElement.scrollHeight);
      if (height === lastReportedHeight) return;
      lastReportedHeight = height;
      window.openai?.notifyIntrinsicHeight?.(height);
    }

    function render(value) {
      const result = value?.structuredContent ?? value;
      if (!result || typeof result.url !== "string") return;
      try {
        const url = new URL(result.url);
        if (url.protocol !== "https:" || !/^loki-[0-9a-f]{32}\\./.test(url.hostname)) {
          throw new Error("unsupported preview URL");
        }
        currentUrl = url.href;
        frame.src = currentUrl;
        open.href = currentUrl;
        title.textContent = typeof result.cwd === "string" ? result.cwd : "Loki live preview";
        const expiry = typeof result.expires_at === "string" ? new Date(result.expires_at) : null;
        const routeCount = result.routes && typeof result.routes === "object"
          ? Object.keys(result.routes).length
          : 0;
        const target = routeCount > 1 ? `${routeCount} routes` : `port ${result.port}`;
        meta.textContent = expiry && !Number.isNaN(expiry.valueOf())
          ? `${target} · expires ${expiry.toLocaleTimeString()}`
          : target;
        error.hidden = true;
        main.hidden = false;
        status.hidden = false;
        requestAnimationFrame(reportHeight);
      } catch (_) {
        main.hidden = true;
        error.hidden = false;
        error.textContent = "The live preview URL is invalid.";
      }
    }

    frame.addEventListener("load", () => {
      status.hidden = true;
      requestAnimationFrame(reportHeight);
    });
    document.getElementById("reload").addEventListener("click", () => {
      if (!currentUrl) return;
      status.hidden = false;
      frame.src = currentUrl;
    });
    document.getElementById("fullscreen").addEventListener("click", () => {
      window.openai?.requestDisplayMode?.({ mode: "fullscreen" });
    });
    window.addEventListener("message", (event) => {
      if (event.source !== window.parent) return;
      const message = event.data;
      if (!message || message.jsonrpc !== "2.0") return;
      if (message.method === "ui/notifications/tool-result") render(message.params);
    }, { passive: true });
    window.addEventListener("openai:set_globals", (event) => {
      render(event.detail?.globals?.toolOutput);
    }, { passive: true });
    render(window.openai?.toolOutput);
    new ResizeObserver(reportHeight).observe(document.body);
  </script>
</body>
</html>"""
