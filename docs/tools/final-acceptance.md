# Final acceptance progress

This report records executed checks and the published release. Pending checks
retain their actual validation status.

## Finding current release evidence

Start with the [current release](https://github.com/jinyongp/loki/releases/latest)
and its `loki-release-lock.json` asset. The sections below retain historical
execution records; their version numbers and labels such as `Unreleased` describe
the stage at which those checks ran, not the latest release's support status.

The lock's `release` and `source_commit` identify the release source. Each entry
in `units` records its target, immutable artifact or image identity, and
`acceptance`: the source commit, OS/architecture/mode, validation fingerprint,
`passed`, and GitHub Actions `run_id`/`run_attempt`. Follow the referenced run's
job logs for the exact checks and results. Reused artifacts retain their own
release and URL; a newer catalog does not imply every module was rebuilt.

The [release workflow](../../.github/workflows/release.yml) separates source
checks from manager, browser and full-runtime acceptance. A successful push run
does not establish packaged acceptance. Publication, installer Pages deployment
and public installation verification are separate jobs; check their actual
conclusions before treating a release as complete. A native build, browser tool
execution, desktop image rendering and live external integration establish
different capabilities. Historical gaps below require current evidence rather
than assumptions that they are either still open or already resolved.

## Published 0.2.11

[Loki 0.2.11](https://github.com/jinyongp/loki/releases/tag/v0.2.11) was
published from `5fe3159f0b8646539b70aad699d959b94de72813` by
[run 38402517897](https://github.com/jinyongp/loki/actions/runs/38402517897).
All required native checks, release publication, Pages deployment and anonymous
public verification passed.
See [release-0.2.11.json](evidence/release-0.2.11.json) for the accepted source,
native job conclusions and independently compared public installer checksums.

The run accepted six native CLI targets, five independent browser targets,
Linux amd64/arm64 full compositions with generated test signing keys, and
minimal Ubuntu 22/24 and Debian 12/13 browser installation/recovery. The full
composition exercised both browser engines, owned screenshot resources/PNG
bytes, both job network profiles, retry-safe starts, signed commit verification,
partial staging through isolated stdin, secrets, coordination and cached access
revocation. Public verification also installed the released Linux CLI and
verified its self-upgrade, installer bytes and anonymous OCI manifest access.

These results resolve the new candidates' native OS/CPU gates listed below.
Live GitHub App operations, public sharing configuration/external routing,
desktop SSH image rendering, upgrade from an older nonempty tool composition,
and remaining experimental browser workflows are still separate checks.

Update with `loki upgrade`, then `loki tools update`. Run `loki setup` afterward
to prepare missing browser libraries and inspect runtime readiness.

## Installed 0.2.11 extended browser checks

The actual Windows Codex-configured MCP connection to WSL Ubuntu passed video
recording start/stop and WebM resource readback. The observed user selection was
`devtools` only. Further checks reused the installed browser bundle with all
fourteen capabilities in a disposable WSL activation, profile and output root;
the user's activation was retained.

PWA installation, OS state, browser-mode launch, removal and subsequent state
rejection passed. WebMCP registration through `document.modelContext`, discovery
and parameterized execution returned the expected synthetic value. Third-party
provider discovery and execution also returned the expected value. Memory
snapshot capture, known retained fixture objects, summary/aggregate analysis and
closing the loaded snapshot passed.

Extension installation, enabled listing, reload, content-script DOM changes,
removal and subsequent absence passed. The action command returned success, but
its expected DOM change did not appear, including with `devtools` excluded. The
underlying cause is unresolved, so extension action behavior is not accepted.
Annotation failed: its dashboard searches for a system Chromium channel rather
than the bundled Chrome executable and reports no browser found. An isolated
server-only dashboard start succeeded, but no annotated image was returned.

These checks cover Linux amd64 in WSL, not every native platform, individual
tool or option combination. They resolve several previously unexecuted feature
checks without establishing that all browser functions work. Owned processes
exited and the disposable root was removed. See
[browser-extended-0.2.11.json](evidence/browser-extended-0.2.11.json).

## Unreleased extension and annotation corrections

Working-tree corrections resolve both failures above. Extension startup resumes
the new worker's initial debugger wait, reload waits for its replacement rather
than the old target, and actions activate the selected MCP page before invoking
Chrome. Actual DOM effects passed immediately after installation and after reload.

Annotations now use a one-time loopback screen with the existing headless browser
session. Drawing a labelled rectangle, submitting feedback, retrieving the result
and reading its owned PNG passed without a system browser or graphical display
on WSL. Cancellation, malformed PNG rejection and cross-origin rejection also
passed. The screen expires after five minutes; SSH/container deployments need
forwarding to its runtime loopback port.

The final WSL run also passed PWA installation/launch/removal, WebMCP and
third-party execution, memory analysis and the existing optional-capability
acceptance suite. The actual installed Windows frontend relaying to the candidate
WSL CLI passed extension actions, annotations and PNG return. Full Go tests, vet,
architecture checks and Windows compilation passed. Native macOS/Windows browser
execution and every option/site combination are outside these WSL checks.
User activation was retained and disposable processes/profiles were removed.
These changes are not published; see
[browser-adapter-fixes.json](evidence/browser-adapter-fixes.json) for source and
candidate fingerprints.

## Windows short-path annotation correction

The first 0.2.12 release candidate passed Linux and macOS acceptance but failed
Windows annotation startup. Native Windows reproduction confirmed that short
paths and canonical long paths loaded separate CommonJS coreBundle instances.
Resolving the bundle through the upstream CLI entry now patches the same tool
instance. Native Windows short-path discovery, annotation screen submission and
PNG return passed. Packaged capability acceptance also checks that the headless
annotation descriptor is loaded before invoking it. See
[browser-windows-short-path.json](evidence/browser-windows-short-path.json).

## Local candidate validation — 2026-10-13

These checks ran against working-tree fixes based on 0.2.10 before 0.2.11
publication. They do not establish that published 0.2.10 contains the corrections.

Executed on Linux amd64, native Windows amd64, and Windows-to-WSL:

- CLI-only Linux and PowerShell IEX installation, repeated installation,
  configuration preservation and corrupt-download rejection.
- CLI upgrade cancellation, verified downgrade/return, backup/restore integrity
  and recovery. Upgrade with a nonempty older tool composition remains pending.
- Disposable WSL import, automatic CLI preparation, selection/switch, restart,
  missing/deleted host handling and retained selection on a failed switch.
- Native Windows browser discovery, both engines, owned PNG return and cached
  access revocation. Windows desktop app rendering remains unobserved.
- Minimal Ubuntu 22/24 and Debian 12/13 browser installation, automatic native
  dependencies, real sandboxed screenshot/video, damage detection and lifecycle.
- Fresh Linux full composition: workspace, execution, Git with generated test
  signing keys, secrets, coordination and both browser engines. Real jobs with
  `none` and `dependency-install`, retry-safe admission, signed commits and
  signature verification, partial staging through isolated stdin, screenshot
  resource/PNG return and cached access revocation passed together.

Failures reproduced and corrected include the missing full-browser `libatomic1`
library, writable browser home, Chrome sandbox `chroot` permission with container
capabilities dropped, the combined engines' PID/thread limit, Docker named-volume
job mounts, readable public signing inputs and proxy resource protocol metadata.
Sharing now reports the absence of a public artifact/preview origin rather than
claiming readiness from private services alone.

The full composition check is included in the release workflow for native Linux
amd64/arm64. Its updated GitHub Actions execution is still pending; local Linux
execution does not certify macOS, Windows arm64 or Linux arm64. Live GitHub App
authorization, external endpoint/sharing routes, desktop SSH image rendering and
the remaining experimental browser workflows retain separate acceptance gates.
Local evidence is summarized in [local-acceptance-2026-10-13.json](evidence/local-acceptance-2026-10-13.json).

## Historical published 0.2.3

[Loki 0.2.3](https://github.com/jinyongp/loki/releases/tag/v0.2.3)
was published by
[run 37201234614](https://github.com/jinyongp/loki/actions/runs/37201234614)
from exact source `eabf41d4791f10e9546c2decdcc50019d0f86314`.

All same-source native acceptance runs passed:

- [Manager 37200278215](https://github.com/jinyongp/loki/actions/runs/37200278215):
  Linux, macOS and Windows amd64/arm64, CLI-only concise installation, Windows
  PowerShell IEX, descriptive help, empty-state upgrade and actual CLI
  self-upgrade to official 0.2.2 followed by return to the accepted candidate.
- [Browser 37200281594](https://github.com/jinyongp/loki/actions/runs/37200281594):
  Linux/macOS amd64/arm64 and Windows amd64, both official engines, optional
  capabilities, Codex preservation/idempotence and owned image
  return/transfer/revocation.
- [Full workspace 37200279823](https://github.com/jinyongp/loki/actions/runs/37200279823):
  Linux amd64/arm64, private stdio, selected discovery, create/read, cached
  selection revocation and owned stop.

The publisher verified original accepted bytes before publication. Both public
installers match their immutable release attachments. Anonymous access verified
all ten exact OCI manifests. An actual public native management installation,
descriptive help, latest-release lookup and verified forced CLI reinstall passed.
Earlier pinned input receipt attachments remain available for acquisition.

The public installer installs the native CLI. Configure the execution host,
add and enable tool groups, and connect the MCP client afterward using
`loki tools`. Full tools require an accessible Docker Engine on Linux/WSL.
CLI versions 0.2.2 and earlier use this installer once to obtain `loki upgrade`.

## Published CLI self-upgrade and help

`loki upgrade` shows current and target stable versions before a confirmation.
Enter or EOF cancels. `--version` selects a stable release, `--yes` / `-y` skips
confirmation, `--check` only reports versions, `--force` allows reinstall or
downgrade and `--timeout` bounds lookup/download/installation. Official manager
ZIP bytes are verified by SHA-256 and executable version before the current
publisher replaces the owned CLI. Tool selections and settings are retained;
an empty installation follows the target CLI release.

All six native targets verified cancellation without mutation, actual official
previous-release publication, target ownership/digest, preserved user data,
return to 0.2.3 and backup cleanup. Native Windows tests also cover replacement
of a running executable, restoration on failure and held-reader state conflicts.
Public Linux checks exercised release lookup and forced reinstall of 0.2.3.
See [self-upgrade.json](evidence/self-upgrade.json).

Bare `loki`, `tools` and `integrations` show descriptive help. Public commands
accept contextual `--help`, `-h` and `help COMMAND`, with descriptions, options
and examples. Help is resolved before state, credentials or remote access.
Unknown commands and missing inputs point to relevant help. See
[cli-help.json](evidence/cli-help.json).

The Go suite passes. Actual Windows desktop SSH image rendering, other full
job/network/signing/endpoints/sharing combinations and experimental browser
workflows retain their separate final acceptance gates.

## Historical 0.2.1 bootstrap acceptance

The following records describe independently accepted corrections published
before 0.2.2. Their original source bases and recorded hashes remain historical
evidence; current publication is recorded above.

## Concise installer output

[Run 37190369288](https://github.com/jinyongp/loki/actions/runs/37190369288)
accepted the output correction from source
`f575792581e1caecc6ec4b8f9cf396d4d59a9c56`. All six native OS/CPU targets
passed exact three-line success output, CLI-only state, repeated installation,
and unchanged Codex/project files. Windows PowerShell IEX passed on both CPU
architectures. Corrupt downloads were rejected, and nested installation errors
retained the underlying ownership conflict while leaving user files intact.
Successful nested setup logs are captured; installation no longer prints an
automatic diagnostic report. Public bootstrap hashes and publication are
recorded in [installer-output.json](evidence/installer-output.json).

## CLI-only initial installation

[Run 37189867621](https://github.com/jinyongp/loki/actions/runs/37189867621)
accepted and deployed source `790df6cff38ccaf45523fbe508ad4526db37d01b`.
Native CLI bootstraps passed on Linux, macOS and Windows amd64/arm64. Installation
and repeated installation completed with an empty installed/selected tool set,
unchanged Codex settings and project files, and corrupt manager downloads
rejected. Windows PowerShell 5.1 also accepted the literal zero-argument
`irm https://jinyongp.dev/loki/install.ps1 | iex` entry without prompts or WSL
configuration, even with unrelated tool/host variables in the caller's scope.

Both public installers install only the CLI. The manager/catalog release bytes
and module acceptance results retain their original source basis. Tool setup
and MCP configuration are explicit post-install operations; native browser
installer acceptance fixtures now exercise those later CLI steps separately.
Deployment follows six-target acceptance and is checked against accepted
bootstrap bytes. Details are recorded in
[cli-installation.json](evidence/cli-installation.json).

## Earlier PowerShell bootstrap correction

[Run 37189109648](https://github.com/jinyongp/loki/actions/runs/37189109648)
accepted and deployed source `a4b9581415b1d5e18c3d72bea53bce7ccd1ef39f`.
Windows PowerShell 5.1 reproduced `ValidateSetFailure` in the original
zero-argument `irm https://jinyongp.dev/loki/install.ps1 | iex` entry. Optional
`Tools` and `HostKind` declaration validators rejected their initial empty
values before interactive selection; explicit body validation now handles them.
The earlier native installer checks used `-File` with explicit parameters and
did not cover this interactive entry.

The corrected literal pipeline passed management-only and native browser/Codex
setup with fixture-controlled downloads of verified release assets. Existing
Codex configuration was preserved; invalid selections and corrupt manager bytes
were rejected. Publication required this Windows acceptance before Pages
deployment. Public verification checked the deployed bootstrap, all ten OCI
manifests and an actual Linux management install. An independent fetch of the
bare public URLs matched the accepted bytes. Immutable 0.2.1 release attachments
were retained. Details and hashes are in
[bootstrap-correction.json](evidence/bootstrap-correction.json).

The checks below retain their historical prerequisite scope; they do not replace
these exact-source release acceptance results.

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
The subsequent full run 37153576451 encountered a classic Docker image-store
OCI import failure, and browser run 37153578467 encountered the short Windows
painted-frame deadline. The published-source runs above passed after those fixes.
The common transport correction passed
a local independent Playwright/DevTools navigation, transfer, owned/inline PNG
and immediate browser-disable regression check.

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
