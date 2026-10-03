# 0.2 implementation prerequisite dossier

Recorded 2026-10-03 against source `6a99bc1a8b6cb44e2c73c1c06e7b16a47d2fb336` with the existing uncommitted contract slice. This task investigates and verifies prerequisites; it does not implement the module extraction or install the 0.2 product. The specification and plan incorporate the findings.

## What was verified

| Check | Measured result | Evidence |
| --- | --- | --- |
| Repository baseline, `go test -json ./...` | 2,360 passing test events in 76 packages; no failures in the baseline run; environment-gated tests listed separately | [go-baseline.json](evidence/go-baseline.json) |
| Existing manifest/state contracts | Race check passed; architecture check: 81 packages, 208 production edges, no baseline exceptions | [source-checks.json](evidence/source-checks.json) |
| Official Playwright MCP, full profile | 71 discovered tools; 30 fixture calls passed, including expected invalid-call errors | [playwright.json](evidence/playwright.json) |
| Official Chrome DevTools MCP, full profile | 55 discovered tools; 25 fixture calls passed, including expected invalid-call errors | [devtools.json](evidence/devtools.json) |
| Optional DevTools features | 16 fixture calls passed: extension lifecycle, PWA lifecycle, actual WebMCP and third-party tool execution | [optional-browser.json](evidence/optional-browser.json) |
| Stdio protocol | Initialization, roots request/response, discovery change notification, unknown-method error and post-cancellation responsiveness observed | [protocol.json](evidence/protocol.json) |
| Existing Chrome/CDP implementation | All five previously gated real-browser tests passed using the temporary Chrome and library fixture | [legacy-browser.json](evidence/legacy-browser.json) |
| Existing OCI sandbox primitives | All six real tests passed with a freshly built source fixture, including network/endpoint preview authority and cleanup | [oci-current-source.json](evidence/oci-current-source.json), [log](evidence/oci-current-source.log) |
| Privileged Linux primitives | Three synthetic root/delegated-identity fixtures passed in an isolated container: vault access denial, foreign signing socket ownership and delegated GitHub CLI startup | [privileged-linux.json](evidence/privileged-linux.json), [log](evidence/privileged-linux.log) |
| Existing project E2E | Actual Node/pnpm/devtools and temporary Chrome fixture passed installation/offline cache, concurrent project install and managed process/secret cleanup contract | [project-e2e.json](evidence/project-e2e.json) |
| Real coordination adapter | Failed against installed devtools 0.23.0 / protocol 6: existing adapter requires protocol 5 | [failures.json](evidence/failures.json) |
| Platform compilation | Contract test binaries compile for Windows/macOS x64/arm64; existing Windows frontend builds; existing Linux host command fails to build for macOS | [platform-builds.json](evidence/platform-builds.json) |

These are upstream/local/primitives results. They do not satisfy the 0.2 packaged-browser, actual desktop-app SSH, management artifact or full-runtime product acceptance criteria. Commands and fixture preparation are in [the maintainer recipe](../../scripts/maintainer/v02-preflight/README.md); external acceptance is in [external-checks.md](external-checks.md).

## Exact dependencies and acquisition

The actual host is Ubuntu 24.04, Linux x64 on WSL, glibc 2.39, Node 22.22.2, npm 10.9.7 and Go 1.27.1. Host metadata, all four npm package integrities and notice hashes are recorded in [metadata.json](evidence/metadata.json). The checked-in [lockfile](../../scripts/maintainer/v02-preflight/package-lock.json) was installed by npm with exact versions and integrity verification.

| Dependency | Tested pin | Effective requirement / artifact detail |
| --- | --- | --- |
| `@playwright/mcp` | `0.0.83` | Top-level package says Node >=18, but its exact Playwright dependencies require >=20 |
| `playwright`, `playwright-core` | `1.64.0-alpha-1790635538000` | A prerelease transitive runtime; do not describe the entire dependency closure as stable Playwright |
| `chrome-devtools-mcp` | `1.10.1` | Node `^20.19.0 || ^22.12.0 || >=23`; tested Node 22.22.2 satisfies both engines |
| Chrome for Testing | `154.0.8037.92`, `linux64` | Official vendor download; archive 196,202,491 bytes |
| Playwright video encoder | FFmpeg revision `1011` | Required for video, not for initialization or ordinary screenshots; installed in the temporary browser cache |

Chrome archive SHA256 is `ff43322f335e436b2f4dcdfeeec5db032299e335a7e8c1c618b326e100ce8732`. This is a measured download digest, not an independently published vendor signature. The product release must bind acquisition to trusted, reviewed release metadata. npm lock integrity likewise identifies pinned bytes; release provenance is a separate responsibility.

All four npm packages declare Apache-2.0. Retain their license/NOTICE files and the DevTools bundled `THIRD_PARTY_NOTICES`; four npm package entries are not a complete inventory of vendored dependencies. The FFmpeg artifact carries `COPYING.LGPLv2.1`, with its digest recorded. Chrome has separate vendor distribution terms/notices. Prefer owned installation caches populated from pinned upstream artifacts rather than an unreviewed redistribution of every browser binary. Release packaging must inventory the actual artifacts and accompanying notices.

The observed official Chrome for Testing manifest includes Linux x64/arm64, macOS x64/arm64 and Windows x86/x64 artifacts. The Linux arm64 archive returned HTTP 200 on an explicit HEAD request, recorded in [chrome-linux-arm64.json](evidence/chrome-linux-arm64.json); it was not executed on this x64 host. The static upstream README platform list lagged the actual manifest, so acquisition must inspect pinned metadata. No native Windows arm64 artifact appeared. Download availability and Node/Go management support do not establish browser support. Chrome for Testing is versioned and does not auto-update; Loki must manage acquisition and updates explicitly. [Official Chrome for Testing distribution](https://developer.chrome.com/docs/automation-and-testing/chrome-for-testing), [download metadata](https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json).

The DevTools engine officially targets Google Chrome / Chrome for Testing. Do not claim arbitrary Chromium distributions are equivalent supported artifacts. Pin engine/runtime/browser combinations and preserve installed artifacts for deterministic restart and rollback. [Official DevTools MCP](https://github.com/ChromeDevTools/chrome-devtools-mcp).

## Real browser behavior and readiness

Both engines initialized before Chrome could launch. First navigation failed because the host lacked NSPR/NSS/ALSA libraries. Playwright reported the missing loader library; DevTools returned a less specific target-closed error. Temporary copies of Ubuntu packages were downloaded and extracted under `/tmp`, without installing system packages. With those libraries, Chrome launched with its sandbox enabled in both engines. Exact native packages and hashes are in metadata.

Video initially failed because FFmpeg was absent. After the isolated encoder install it produced a nonempty WebM file. These failures are retained in [failures.json](evidence/failures.json). A browser doctor must validate actual launch and the enabled feature's prerequisites, retain stderr/exit causes and provide phase progress. It must not fall back to disabling the sandbox when a dependency is missing.

The local fixture binds an ephemeral IPv4 loopback port and contains only synthetic data. Fresh per-run engine roots avoid reusing old output. The probes use separate profiles and XDG directories, strip ambient provider variables, and close owned process groups. They neither connect to the user's logged-in browser nor initialize Loki's appliance.

| Feature | Actual coverage / limitation |
| --- | --- |
| Navigation, accessibility, forms and page JavaScript | Both engines: real local page, changed input/counter values checked |
| Screenshots | Inline image bytes decoded and hashed; DevTools also writes a screenshot file |
| Files | Both upload a synthetic file; Playwright downloads a known file and writes storage state, PDF, trace and video; DevTools writes performance trace, heap snapshot and Lighthouse artifacts |
| Storage / network mocking | Playwright cookie/localStorage, persisted state and mocked fetch response verified |
| Network / console / emulation / CSS | Both record observations; DevTools emulation and CSS inspection exercised |
| Performance / memory | DevTools trace start/stop, heap snapshot and summary, Lighthouse snapshot mode exercised |
| Extensions | Real temporary unpacked extension installed, listed, reloaded, action triggered and uninstalled in headless Chrome |
| PWA | An installable local fixture with icon/service worker is installed, queried, launched and uninstalled; OS integration/UI on Windows/macOS remains separate |
| WebMCP | Requires `--enable-features=WebMCP` in this pin; `document.modelContext` registers the fixture tool and execution returns the expected value |
| Third-party developer tools | A real `devtoolstooldiscovery` listener exposes a fixture tool and execution returns the expected value |
| Experimental DevTools UI | A combined fixture timed out at a file screenshot; underlying cause was not isolated. The standard profile passes. This feature stays separately gated |
| Shared browser sessions / existing login | Not exercised; neither is a prerequisite for separate-engine operation |

The default catalogs are Playwright 25 tools and DevTools 30 tools. Full profiles reveal 71/55 tools. This captures complete native schemas for those profiles, not successful semantic execution of every tool and every option. The evidence index lists discovered tools without a direct call. Such features need capability-specific acceptance before a supported-product claim. Upstream main-branch documentation can differ from the pinned npm catalog; the latter controls the packaged API. [Playwright capabilities](https://playwright.dev/mcp/capabilities), [DevTools configuration](https://github.com/ChromeDevTools/chrome-devtools-mcp/blob/main/docs/configuration.md), [Chrome WebMCP](https://developer.chrome.com/docs/ai/webmcp/).

Snapshots can return file links instead of inline text. Images carry actual MCP image content; some artifacts are filesystem paths. Standalone browser must own file/image staging without requiring public sharing. Full deployment needs an explicit mapping across the engine filesystem, MCP host and client; a remote path alone does not prove the desktop app can display it. Tests must validate bytes, ownership, size limits, cleanup and error propagation through the packaged adapter.

## Protocol, transport and authority

The probes negotiated MCP `2025-06-18` and exchanged newline-delimited JSON over stdio. Both engines advertise tools/listChanged; DevTools also advertises logging. Neither tested package advertises prompts or resources. Playwright requested `roots/list` and emitted a tool-list change notification. Preserve these actual messages and capabilities; do not freeze a startup inventory when tools can change.

The cancellation probe sent `notifications/cancelled` during a 15-second wait. No response to that canceled request arrived within three seconds; a subsequent snapshot completed. The cancellation specification allows an omitted response, so this is not itself a protocol failure and does not establish whether the original browser action stopped. No progress notifications were emitted despite a progress token. Host-side deadlines, owned child cleanup and launcher progress still need implementation. Unknown methods return JSON-RPC -32601; invalid/unknown tool calls preserve native errors. Stdout must remain protocol-only, with logs on stderr. [MCP transports](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports), [cancellation](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/cancellation), [progress](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/progress).

The upstream Playwright default catalog already includes `browser_run_code_unsafe`, explicitly described as server-process arbitrary JavaScript. The synthetic probe only queried the fixture page title. Loki must gate discovery and calls for this capability; browser sandboxing does not sandbox Node code. Roots, origin/file allowlists and tool filtering are guardrails, not a process-level security boundary. Standalone engines run with the selected user's host authority. Protected full deployment must supply enforced process, filesystem, credential and network boundaries around the same upstream engines. [Official Playwright MCP](https://github.com/microsoft/playwright-mcp), [code-execution contract](https://playwright.dev/mcp/tools/code-execution).

Stdio in the selected project host is the first connection path. Browser-only installation does not need an HTTP listener, general tunnel or OpenAI account authorization. If full composition uses HTTP, test its own authentication, origins, sessions, capabilities and reconnect behavior; local stdio results cannot establish those properties.

## Windows desktop app and SSH

The official remote documentation describes a remote app-server process launched through the remote user's login shell. MCP configuration documents the experimental `experimental_environment = "remote"` setting for stdio when a remote executor is available. Availability depends on the actual desktop app connection, not just the existence of SSH or a CLI on the WSL host. [Remote connections](https://learn.chatgpt.com/docs/remote-connections), [MCP configuration](https://learn.chatgpt.com/docs/extend/mcp?surface=cli).

Local Codex CLI is 0.160.0. A read-only `mcp get` configuration override accepted the sample stdio configuration but omitted `experimental_environment` from its output. This does not prove it understands or executes that field. The generated app-server schema exposes MCP status/reload/tool-call APIs; a real tool call requires a server, tool and thread identifier. No actual Windows app session/control handle was available to this test harness. WSL interop was not usable here. User configuration was not changed.

The actual Windows app -> SSH -> WSL call and screenshot display therefore remain pending. App version/settings information was requested to locate that external path, but it is not necessary to finish local prerequisite research. The external runbook gives the exact required evidence rather than treating a CLI inventory check as a pass.

## Extraction and artifact implications

The actual Go import inventory is in [source-imports.json](evidence/source-imports.json); the specification's extraction table identifies implementation ownership. Important dependencies are behavioral:

| Current coupling | Required 0.2 boundary |
| --- | --- |
| MCP startup constructs runtime/jobs/toolchains and repository context before individual groups | Selected module composition with optional project-context interfaces; browser-only startup with those services absent |
| Workspace repository and patch operations call Git/job runners | Git owns repository operations; workspace file APIs use scoped I/O, and Git-dependent edits declare an adapter |
| Signing uses private sockets, peer UID checks and a restricted public agent | Git owns the feature and keys; private service isolation remains enforced within that module |
| GitHub key loading/token broker and application vault have different owners | Provider credentials stay in GitHub; application secrets use explicit delivery grants, independent of Git signing |
| Browser upload receives workspace Files and full-runtime identity/proxy inputs | Owned staging and a mode-specific execution boundary without requiring public workspace or general jobs |
| Preview publication checks active endpoint/port ownership | Sharing accepts a narrow verified endpoint contract, never a general arbitrary proxy target |
| Current devtools adapter hard-codes protocol 5 | Pin supported CLI/protocol/catalog and test the real binary; installed protocol 6 currently fails before secret-delivery behavior is exercised |
| Linux command imports openat2/Renameat2/SO_PEERCRED implementations | Portable management command and platform implementations; preserve safe path/peer guarantees instead of removing checks to obtain a build |

Windows/macOS contract cross-compilation proves the pure contracts are portable, not that installers, signing, process lifetime, diagnostics or browser artifacts run on those hosts. macOS distribution requires an actual signing/notarization and installation decision; Git SSH signing is unrelated to Developer ID distribution. [Apple Developer ID](https://developer.apple.com/developer-id/).

Installation resolves a tool-specific artifact closure: launcher, exact Node/packages, optional managed Chrome, native requirements, and FFmpeg only for enabled video. Secrets and general jobs are not browser dependencies. Each artifact needs identity, integrity, notices, target/mode and ownership metadata. Download/verify/unpack/publish operations must be bounded, atomic and retryable; interrupted stages and concurrent installs need recoverable operation records. Disabling must remove discovery and reject stale calls. Removal must stop only owned processes and preserve user projects/credentials. These are implementation/acceptance cases, not behavior of the temporary probes.

## GitHub Projects prerequisite correction

Repository Projects uses the owning user/organization's ProjectV2 and repository linking; it is not a separate repo-owned ProjectV2 namespace. Preserve the verified repository-project adapter and provider authorization intent when extracting it. Personal Projects authorization stays opt-in; ordinary repository operations must not automatically require Device Flow.

For an installation-token `createProjectV2` request that supplies `repositoryId`, GitHub also requires the repository's Contents permission. Diagnose the owner, project permission, Contents permission and repository link independently rather than assuming a generic Projects 403 calls for Admin. [Official Projects API authentication](https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-api-to-manage-projects).

An organization-update API does exist: `PATCH /orgs/{org}` supports `has_organization_projects`. It requires Organization Administration write access, separately from Organization Projects read/write. Report disabled Projects distinctly and link to the organization's Projects settings. Do not silently add Administration permission or change an organization-wide setting as part of ordinary setup. Actual repository-linked ProjectV2 permissions and mutations still require current provider acceptance; this turn made no GitHub account mutations. [Official organization update API](https://docs.github.com/en/rest/orgs/orgs#update-an-organization).

## Remaining acceptance gates

| Gate | Why it remains open | Owning task |
| --- | --- | --- |
| Actual Windows app SSH browser calls and usable screenshot/file results | No real desktop app connection was exercised | `browser_probe`, `browser_package`, `acceptance` |
| 0.2 packaged standalone engine/feature matrix, call gates, dynamic discovery and owned cleanup | Product launcher/adapter/installer is not implemented | `browser_package`, `composition` |
| Windows/macOS/Linux fresh management install/lifecycle/recovery | Cross-builds are insufficient; macOS legacy command does not build | `host_lifecycle`, `acceptance` |
| 0.2 full deployment authority and credential isolation | Existing primitives passed, but new composition and artifacts do not exist | `runtime_adapters`, `composition`, `acceptance` |
| Actual pinned coordination adapter | Protocol 6 failure is retained; fix belongs to module extraction | `runtime_adapters` |
| Published release transaction and OCI archive content tests | Require exact release manifests/artifacts and produced archive, which are not part of the prerequisite task; other baseline test skips were exercised or retained as the real coordination adapter failure | `acceptance`, `candidate` |
| Chrome package provenance/notices, macOS signing and immutable 0.2 candidate closure | Measured downloads and primitive tests are not a distributable product | `candidate` |

The prerequisite task can complete with this evidence and executable remaining gates. `A_REMOTE`, `A_HOST`, `A_ACCEPTANCE` and `A_CANDIDATE` remain required; no external or product test is waived.

The privileged fixtures used static current-source test binaries in the installed immutable 0.1 image, with only synthetic temporary state and no network or host Docker socket. Root capabilities were limited to CHOWN, SETUID, SETGID and DAC_OVERRIDE (required for the test parent's readback). Host sudo required a password and was not used. Container-root results verify these Linux primitives; they do not validate the new production deployment or host service installation.

The project E2E called devtools directly, so its success does not override the old Loki adapter's protocol rejection. Its Playwright fixture deliberately disables Chrome sandboxing; the independent upstream probes above verify sandbox-enabled launch. Keep those two scopes distinct.
