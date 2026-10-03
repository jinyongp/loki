# Required external acceptance procedures

These gates remain open after local prerequisite research. Record the actual client version, OS/architecture, source/artifact hashes, command, exit status and observed behavior. Use synthetic fixtures and managed profiles. No production credentials or destructive installation migration is required.

## Windows Codex desktop -> SSH -> WSL

Prerequisites: the real desktop SSH project, a remote MCP executor supported by that client, and the isolated artifacts from [the probe recipe](../../scripts/maintainer/v02-preflight/README.md). The official [MCP configuration](https://learn.chatgpt.com/docs/extend/mcp?surface=cli) documents `experimental_environment = "remote"` conditionally; this dossier has not verified it in the installed Windows app. Do not assume a parsed CLI setting proves the execution location.

1. Record the desktop app version and how that SSH project's MCP configuration is applied. Scope the temporary server to that project and avoid replacing existing global configuration. Copy the proposed stanza below with absolute paths valid on the WSL host. The Node path is the actual persistent installation tested here; re-resolve it on a different host.
2. Reconnect/reload using the real client's supported mechanism. Verify the temporary server is ready and native browser tools are discoverable. Record failure stderr and the actual host on which Node launches. Do not substitute the existing Loki `browser_session` endpoint or the in-app browser.
3. Call navigation on a synthetic data page, take an accessibility snapshot and take a screenshot with `scale: "css"`. Verify the screenshot renders in the actual desktop conversation; a path printed in the tool result is insufficient.
4. Start a synthetic HTTP fixture on WSL loopback, call navigation on its printed URL, change an input/counter and assert the resulting snapshot. This proves localhost means the WSL project host. Confirm no Windows localhost forwarding/tunnel is needed for that path.
5. Upload/download a synthetic file and check result bytes through the client. Exercise a structured invalid tool call and startup failure (an owned temporary missing executable path), verifying actionable causes instead of EOF/generic INVALID_ARGUMENT.
6. Reconnect once, verify owned profile recovery, close the engine and remove only the temporary server stanza. Keep the real project's configuration and other servers intact.

Proposed upstream smoke-test stanza; this is not the future Loki launcher or safe product capability profile. Use only the synthetic fixture because the upstream catalog includes unsafe host code execution:

```toml
[mcp_servers.loki_browser_playwright_preflight]
command = "/home/jinyongp/.local/share/fnm/node-versions/v22.22.2/installation/bin/node"
args = ["/tmp/loki-v02-preflight/node_modules/@playwright/mcp/cli.js", "--headless", "--isolated", "--sandbox", "--executable-path", "/tmp/loki-v02-preflight/chrome-linux64/chrome", "--output-dir", "/tmp/loki-v02-preflight/desktop-output", "--image-responses", "allow"]
cwd = "/tmp/loki-v02-preflight"
experimental_environment = "remote"

[mcp_servers.loki_browser_playwright_preflight.env]
LD_LIBRARY_PATH = "/tmp/loki-v02-preflight/native-deps/root/usr/lib/x86_64-linux-gnu"
PLAYWRIGHT_BROWSERS_PATH = "/tmp/loki-v02-preflight/pw-browsers"
```

For a WSL loopback fixture, run the following in a temporary WSL terminal. It prints an ephemeral port, serves only synthetic HTML, and stops with Ctrl+C:

```sh
python3 - <<'PY'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
class Fixture(BaseHTTPRequestHandler):
    def do_GET(self):
        body=b'<title>Loki remote fixture</title><label>Name<input id="name"></label><button onclick="this.textContent=\"Clicked\"">Click fixture</button>'
        self.send_response(200)
        self.send_header('Content-Type', 'text/html')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
server=ThreadingHTTPServer(('127.0.0.1',0),Fixture)
print('http://127.0.0.1:%d/' % server.server_port,flush=True)
try: server.serve_forever()
except KeyboardInterrupt: pass
finally: server.server_close()
PY
```

Pass requires a real native tool call, fixture state change and visible image/file result in the actual Windows app. Local `tools/list`, `codex mcp get`, a successful SSH login and a headless screenshot saved on WSL are supporting observations only. This gate belongs to `A_REMOTE`; it cannot be marked passed from local upstream probes.

## Native management and browser artifacts

Use actual 0.2 candidate artifacts once built, in a clean user or owned VM on Linux x64, Windows x64 and macOS x64/arm64. Execute the eventual CLI help rather than treating planned command names as current features.

1. Install management only; verify no appliance/general executor/provider setup is required. Validate artifact identity/integrity and retained notices.
2. Install browser only; resolve the pinned Node/engine/browser/feature closure. Test setup/status/doctor with missing native libraries or feature dependencies and verify accurate causes/progress.
3. Activate and call each advertised capability on synthetic fixtures. Test images/files, separate profiles, concurrent projects, stdout purity and reconnect. Native OS extension/PWA UI and optional shared-session behavior require separate results.
4. Interrupt acquisition/unpack/publication in owned staging directories; retry and verify atomic recovery. Exercise install twice, disable/enable, cached calls after disable and removal while a managed process is alive.
5. Confirm removal stops owned processes, releases owned artifacts and preserves project files, unrelated tool installations and user credentials. Check Windows background process/window lifetime; macOS distribution signing/notarization/install behavior must be exercised on macOS.
6. For unsupported browser targets (currently no pinned native CfT Windows arm64), verify clear support errors or an explicitly selected supported execution host. The pinned Linux arm64 archive is available but requires actual runtime acceptance before a support claim. Do not silently advertise emulation as a tested native artifact.

A cross-built Go contract binary satisfies none of these installation/lifecycle checks. `A_HOST` stays open until real product artifacts pass.

## Protected full composition and release

Reuse `scripts/verify/accept-oci-jobs.sh` to validate current source primitives; it passed locally. After extraction, rerun with exact candidate artifacts and add browser-only, Git/signing on/off, GitHub API without Git, execution with/without explicit secret grants, endpoint publication and coordination combinations.

Three privileged synthetic fixtures for execution UID/vault separation, signing socket ownership and delegated `gh` startup passed locally inside an isolated root container. Existing project E2E also passed using actual Node/pnpm/devtools and the temporary Chrome wrapper. Rerun against the new exact production runtime; those primitive results are not new service-deployment acceptance. Published-release transactions need two exact release manifests/artifacts; archive acceptance needs the exact produced OCI archive. Follow the environment contracts in the existing test files/scripts rather than inventing fixture inputs.

For GitHub Projects, record owner type, active installation permission set, repository Contents permission and ProjectV2 link before mutation. Organization disabled-state diagnostics and personal authorization opt-in behavior need separate acceptance. Use previously authorized test repositories and clean up only the objects created by the test.

Candidate publication requires exact source/build basis, immutable artifact closure, checksums/provenance, notices, platform support results and required workstream validation. No local probe is a waiver for missing `A_ACCEPTANCE` or `A_CANDIDATE` evidence.
