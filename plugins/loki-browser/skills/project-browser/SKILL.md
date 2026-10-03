---
name: project-browser
description: Use when the user explicitly selects Loki Project Browser or asks to use the configured project-host browser connection. Controls official Playwright and Chrome DevTools sessions on that execution host.
---

# Project browser

This connection controls a browser on the configured project execution host.
Its profiles, tabs and login state belong to this MCP connection. Identify it as
**Loki Project Browser** when explaining which browser is being used.

Use this skill when the user selects this connection. Requests about the
desktop app's in-app browser belong to that app's available integration. If the
requested integration is unavailable, report that and offer this project-host
connection; wait for the user's selection before switching browser ownership.

Discover the actual available tools. Playwright and DevTools preserve their
official schemas. With both engines enabled, each has its own tabs and profile;
keep a workflow on its selected engine. Paths and localhost refer to the
execution host. Use the project's existing development-server instructions.

Use `loki_browser_files` to stage uploads and read owned screenshot, video and
download results. Reads can return inline images or resource contents. Transfers
are bounded to 4 MiB per file. Save exports inside the engine's owned output
directory. Sharing is a separate optional tool group.

If startup fails, report the exact stage and bounded public error. Process or
socket presence alone does not establish readiness. Run `loki doctor` on the
configured execution host when a diagnosis is needed. Actual visual behavior is
verified only after successful navigation and observation.
