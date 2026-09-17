from __future__ import annotations


DEVELOPER_VIEWER_URI = "ui://loki/developer-output-v1.html"
DEVELOPER_VIEWER_MIME_TYPE = "text/html;profile=mcp-app"


def developer_viewer_html() -> str:
    return r"""<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    :root {
      color-scheme: light dark;
      font-family: ui-sans-serif, system-ui, sans-serif;
      --surface: Canvas;
      --ink: CanvasText;
      --muted: color-mix(in srgb, CanvasText 62%, Canvas);
      --line: color-mix(in srgb, CanvasText 16%, transparent);
      --soft: color-mix(in srgb, CanvasText 5%, Canvas);
      --green: light-dark(#08783e, #79dfa6);
      --red: light-dark(#b42318, #ff9b91);
      --blue: light-dark(#175cd3, #89b4ff);
      --amber: light-dark(#9a6700, #e8c76a);
    }
    * { box-sizing: border-box; }
    ::selection { background: color-mix(in srgb, var(--blue) 28%, transparent); }
    body { margin: 0; padding: 8px; background: transparent; color: var(--ink); }
    [hidden] { display: none !important; }
    main { overflow: hidden; border: 1px solid var(--line); border-radius: 14px; background: var(--surface); box-shadow: 0 10px 28px color-mix(in srgb, CanvasText 8%, transparent); }
    header { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 12px; padding: 14px 16px 12px; border-bottom: 1px solid var(--line); }
    h1 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 15px; line-height: 1.35; letter-spacing: -0.015em; }
    #subtitle { margin-top: 3px; overflow: hidden; color: var(--muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
    nav { display: flex; align-items: center; gap: 2px; }
    button { border: 0; border-radius: 8px; padding: 6px 9px; color: var(--muted); background: transparent; font: 600 12px/1 ui-sans-serif, system-ui, sans-serif; cursor: pointer; }
    button:hover { color: var(--ink); background: var(--soft); }
    button:focus-visible { outline: 2px solid var(--blue); outline-offset: 2px; }
    #stats { display: flex; flex-wrap: wrap; gap: 6px; padding: 9px 16px; border-bottom: 1px solid var(--line); background: var(--soft); }
    .stat { color: var(--muted); font: 600 11px/1.4 ui-sans-serif, system-ui, sans-serif; font-variant-numeric: tabular-nums; }
    .stat strong { color: var(--ink); }
    #notice { padding: 8px 16px; color: var(--amber); border-bottom: 1px solid var(--line); font-size: 12px; }
    #viewport { height: min(64vh, 680px); min-height: 300px; overflow: auto; scrollbar-color: var(--muted) transparent; scrollbar-width: thin; }
    #viewport::-webkit-scrollbar { width: 10px; height: 10px; }
    #viewport::-webkit-scrollbar-thumb { border: 3px solid transparent; border-radius: 999px; background: var(--muted); background-clip: padding-box; }
    ol { min-width: 100%; width: max-content; margin: 0; padding: 12px 0; counter-reset: line; font: 12px/1.55 ui-monospace, SFMono-Regular, Consolas, monospace; tab-size: 2; }
    li { display: grid; grid-template-columns: 5ch minmax(0, 1fr); min-height: 1.55em; padding-right: 16px; white-space: pre; }
    li::before { counter-increment: line; content: counter(line); padding-right: 12px; color: var(--muted); text-align: right; user-select: none; }
    li.add { color: var(--green); background: color-mix(in srgb, var(--green) 8%, transparent); }
    li.del { color: var(--red); background: color-mix(in srgb, var(--red) 8%, transparent); }
    li.hunk, li.meta { color: var(--blue); }
    li.fail { color: var(--red); font-weight: 600; }
    li.pass { color: var(--green); }
    li.warn { color: var(--amber); }
    main.wrap li { width: 100%; white-space: pre-wrap; overflow-wrap: anywhere; }
    #empty { padding: 48px 20px; color: var(--muted); text-align: center; }
    #status { padding: 20px; color: var(--muted); }
  </style>
</head>
<body>
  <div id="status">Waiting for developer output…</div>
  <main hidden>
    <header>
      <div><h1 id="title">Developer output</h1><div id="subtitle"></div></div>
      <nav aria-label="Viewer controls">
        <button id="wrap" type="button" aria-pressed="false">Wrap</button>
        <button id="copy" type="button">Copy</button>
        <button id="fullscreen" type="button">Fullscreen</button>
      </nav>
    </header>
    <div id="stats"></div>
    <div id="notice" hidden>Output was truncated. Open the source tool again with a narrower range.</div>
    <div id="viewport"><ol id="lines"></ol><div id="empty" hidden>No output.</div></div>
  </main>
  <script>
    const main = document.querySelector("main");
    const status = document.getElementById("status");
    const title = document.getElementById("title");
    const subtitle = document.getElementById("subtitle");
    const stats = document.getElementById("stats");
    const lines = document.getElementById("lines");
    const empty = document.getElementById("empty");
    const notice = document.getElementById("notice");
    let current = null;
    let lastReportedHeight = 0;

    function reportHeight() {
      const height = Math.ceil(document.documentElement.scrollHeight);
      if (height === lastReportedHeight) return;
      lastReportedHeight = height;
      window.openai?.notifyIntrinsicHeight?.(height);
    }

    function lineClass(text, kind) {
      if (kind === "diff") {
        if (text.startsWith("+") && !text.startsWith("+++")) return "add";
        if (text.startsWith("-") && !text.startsWith("---")) return "del";
        if (text.startsWith("@@")) return "hunk";
        if (/^(diff --git|index |--- |\+\+\+ )/.test(text)) return "meta";
      }
      if (/\b(fail(?:ed|ure)?|error|not ok)\b/i.test(text)) return "fail";
      if (/\b(pass(?:ed)?|ok|success)\b/i.test(text)) return "pass";
      if (/\b(warn(?:ing)?|skipped|todo)\b/i.test(text)) return "warn";
      return "";
    }

    function render(value) {
      const result = value?.structuredContent ?? value;
      if (!result || typeof result.content !== "string") return;
      current = result;
      title.textContent = typeof result.title === "string" ? result.title : "Developer output";
      subtitle.textContent = typeof result.subtitle === "string" ? result.subtitle : "";
      stats.replaceChildren();
      for (const [key, value] of Object.entries(result.stats ?? {})) {
        if (value === null || value === undefined) continue;
        const item = document.createElement("span");
        item.className = "stat";
        const strong = document.createElement("strong");
        strong.textContent = String(value);
        item.append(document.createTextNode(`${key} `), strong);
        stats.append(item);
      }
      lines.replaceChildren();
      const rows = result.content ? result.content.replace(/\r\n/g, "\n").split("\n") : [];
      if (rows.at(-1) === "") rows.pop();
      for (const text of rows) {
        const row = document.createElement("li");
        row.className = lineClass(text, result.kind);
        const content = document.createElement("span");
        content.textContent = text || " ";
        row.append(content);
        lines.append(row);
      }
      empty.hidden = rows.length > 0;
      notice.hidden = result.truncated !== true;
      status.hidden = true;
      main.hidden = false;
      requestAnimationFrame(reportHeight);
    }

    document.getElementById("wrap").addEventListener("click", event => {
      const enabled = main.classList.toggle("wrap");
      event.currentTarget.setAttribute("aria-pressed", String(enabled));
    });
    document.getElementById("copy").addEventListener("click", async event => {
      if (!current) return;
      await navigator.clipboard.writeText(current.content);
      event.currentTarget.textContent = "Copied";
      setTimeout(() => { event.currentTarget.textContent = "Copy"; }, 1200);
    });
    document.getElementById("fullscreen").addEventListener("click", () => {
      window.openai?.requestDisplayMode?.({ mode: "fullscreen" });
    });
    window.addEventListener("message", event => {
      if (event.source !== window.parent) return;
      const message = event.data;
      if (message?.jsonrpc === "2.0" && message.method === "ui/notifications/tool-result") render(message.params);
    }, { passive: true });
    window.addEventListener("openai:set_globals", event => render(event.detail?.globals?.toolOutput), { passive: true });
    render(window.openai?.toolOutput);
    new ResizeObserver(reportHeight).observe(document.body);
  </script>
</body>
</html>"""
