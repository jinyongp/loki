from __future__ import annotations


IMAGE_VIEWER_URI = "ui://loki/image-viewer-v3.html"
IMAGE_VIEWER_MIME_TYPE = "text/html;profile=mcp-app"


def image_viewer_html() -> str:
    return """<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    :root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, sans-serif; }
    body { margin: 0; padding: 8px; }
    [hidden] { display: none !important; }
    figure { margin: 0; display: grid; gap: 8px; }
    img { display: block; width: 100%; height: auto; max-height: 720px; object-fit: contain; border-radius: 10px; background: Canvas; cursor: zoom-in; }
    figcaption { display: flex; gap: 8px; align-items: center; color: GrayText; font-size: 12px; overflow-wrap: anywhere; }
    a { color: LinkText; }
    #status { padding: 12px; color: GrayText; }
  </style>
</head>
<body>
  <div id="status">Loading image…</div>
  <figure hidden>
    <img id="image" alt="Image shared from the Loki workspace">
    <figcaption><span id="name"></span><a id="open" target="_blank" rel="noopener noreferrer">Open original</a></figcaption>
  </figure>
  <script>
    const status = document.getElementById("status");
    const figure = document.querySelector("figure");
    const image = document.getElementById("image");
    const name = document.getElementById("name");
    const open = document.getElementById("open");
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
        if (url.protocol !== "https:") throw new Error("unsupported image URL");
        image.src = url.href;
        open.href = url.href;
        name.textContent = typeof result.path === "string" ? result.path : "Loki image";
        status.hidden = true;
        figure.hidden = false;
        requestAnimationFrame(reportHeight);
      } catch (_) {
        status.textContent = "The image URL is invalid.";
      }
    }

    image.addEventListener("load", () => {
      requestAnimationFrame(reportHeight);
    });

    image.addEventListener("error", () => {
      figure.hidden = true;
      status.hidden = false;
      status.textContent = "The image could not be loaded. The temporary link may have expired.";
      requestAnimationFrame(reportHeight);
    });

    image.addEventListener("click", () => {
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
