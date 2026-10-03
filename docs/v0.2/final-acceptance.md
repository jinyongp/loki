# Final acceptance progress

This report records executed checks. It does not publish a release, switch an
existing installation, or establish acceptance for checks still pending.

## Completed native browser checks

[Native run 37150298732](https://github.com/jinyongp/loki/actions/runs/37150298732)
prepared and accepted the project-host browser on Linux amd64/arm64, Windows
amd64 and macOS amd64/arm64 from source `2b6991c`. Every target passed both
official engines, synthetic page snapshots, inline PNG bytes, owned PNG resource
readback, staged file/resource round trips, disable gates, shutdown and removal
with the project fixture preserved. Chrome's sandbox remained enabled.

The preceding failed cycles exposed several separate issues: the manager's
manifest copy lost explicitly empty arrays; the DevTools fixture omitted its
required page ID; a named Playwright screenshot used workspace-relative semantics
instead of the owned output directory; and the readiness capture ran before
painted frames. The final run includes these corrections. macOS candidate
signature and path-alias preparation corrections are recorded separately in
[candidate-preparation.md](candidate-preparation.md).

Linux CI used a scoped temporary AppArmor profile for its owned Chrome path.
Local WSL acceptance used a receipt-bound temporary native library closure.
Neither result establishes that a user's native dependencies are already installed.

## Completed source checks

The complete `go test ./... -timeout 90s` suite and `go run ./tools/archcheck`
passed after the 0.2 fixture and SDK ownership corrections. The architecture
report covers 90 packages and 244 production edges without baseline exceptions.
`go test -race ./internal/app/mcp ./internal/transport/toolproxy -timeout 90s`
also passed. Rejected initialization now cancels its owned browser engines
immediately. Distinct accepted HTTP sessions retain distinct engine ownership.

The protected service transport belongs to the MCP wire adapter. Its Unix peer
credential dependency is Linux-only; standalone Windows/macOS managers use
the portable adapter. Cross-builds caught and corrected the accidental spread
of that Linux dependency. Current-source native candidates must be rebuilt after
these corrections; cross-builds alone do not establish native acceptance.

## Optional browser acceptance

Isolated WSL execution discovered 129 combined tools with all fourteen explicit
capabilities selected. `accept_browser_capabilities.py` passed vision pointer
actions, PDF bytes, synthetic storage, assertions, intercepted network responses,
trace event logs, video bytes, configuration access, explicit unsafe code and
immediate capability revocation. Extension, WebMCP and third-party listing calls
also passed. These checks use synthetic pages and owned files.

Interactive annotation, extension installation, PWA installation, WebMCP
invocation, third-party invocation and memory snapshot analysis remain separate
feature checks. Discovery is not proof of those end-to-end workflows.
The optional fixture is now included in native project-host acceptance; its
cross-platform results remain pending until that workflow finishes.

## Remaining required gates

- Actual Windows Codex desktop → SSH → WSL navigation, host location and rendered
  image acceptance. The isolated temporary server is prepared; this session
  cannot observe the user's desktop conversation.
- Current-source native candidate rebuilds and affected final checks after the
  transport and session corrections.
- Real full-runtime authority, job/network/signing/publication cleanup and
  representative combined selections. Native full preparation and focused
  tests are separate from executing those complete runtime workflows.
- The remaining optional features above and final documentation/artifact
  evidence reconciliation.

The workstream remains open until its required gates have current evidence.
