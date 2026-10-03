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

[Run 37151060313](https://github.com/jinyongp/loki/actions/runs/37151060313)
repeated baseline and optional-capability acceptance on all five native targets
from source `ba9e1d5`; all passed.
[Run 37151058384](https://github.com/jinyongp/loki/actions/runs/37151058384)
prepared and accepted the management-only command on Linux amd64/arm64,
Windows amd64/arm64 and macOS amd64/arm64 from the same source; all six passed.

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
The complete suite and architecture check were repeated successfully at
`24dce9b`, including the modular full execution-contract fixture.

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
The optional fixture also passed on all five native project-host targets in
run 37151060313. Experimental listing/discovery results retain the limitations
above.

## Full runtime progress

A retained local full candidate was installed into an isolated management root
with only workspace enabled. Its service startup exposed a real execution-contract
failure: the shared validator omitted the new `/etc/loki/gitconfig` path.
`24dce9b` admits that exact owned path and adds a test loading the packaged full
contract, including rejection of an ambient replacement path. The isolated
deployment was stopped and its resources removed. After rebuilding the core
program, module archive and OCI images, isolated workspace-only service start,
doctor readiness and owned-resource stop passed. The core archive SHA-256 was
`63a03cbb00aef20df997882f4838140318329eea2d9c05446bb3a1ace5b48fa4`.

Actual frontend MCP connection then exposed a separate publication
failure on Docker 29.4.1. Docker retained the requested loopback mapping in
`HostConfig.PortBindings` but returned no usable `NetworkSettings.Ports` mapping
for the internal-only bridge. The adapter correctly rejected that missing
owned endpoint. This behavior is also described in the
[Moby project's internal-network publication discussion](https://github.com/moby/moby/discussions/53256).
The corrected frontend starts a private stdio adapter in the exact inspected
MCP container by immutable container ID and non-root role. Only that adapter
reads its bearer and connects to container loopback. The internal network stays
private. Upstream request metadata now belongs to each negotiated proxy session,
so a newer downstream protocol does not override the stateful service protocol.
Named volume mounts disable Docker copy-up so image directory metadata cannot
replace bootstrap ownership of an empty workspace volume.

The rebuilt local Linux amd64 candidate passed actual private stdio discovery,
workspace file create/read, immediate cached-call rejection after disabling
workspace, and owned service shutdown. `accept_full_workspace_candidate.py`
checks receipt-bound manager and OCI bytes and executes these checks on both
native Linux full CI targets. This is workspace acceptance; other full modules
retain their required execution gates. Every temporary deployment was stopped
successfully. The current full changes also passed the whole Go suite, the
90-package/245-edge architecture check, and race checks for toolproxy, MCP and
management.

[Full run 37150598895](https://github.com/jinyongp/loki/actions/runs/37150598895)
passed native Linux amd64/arm64 preparation and focused Go/manager checks from
`2f08a0d`. That workflow did not execute the actual full frontend connection and
therefore did not catch the publication failure. Run 37151479175 also passed
native Linux amd64/arm64 preparation and focused checks after the contract fix.
The next native full run includes actual workspace transport acceptance.

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
