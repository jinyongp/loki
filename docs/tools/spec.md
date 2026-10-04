# Loki 0.2.x modular tools specification

## Product contract

Loki 0.2.x manages a collection of development tools in one repository. Install the host management CLI first, then select tool bundles and the prerequisites they actually require. Each tool has a defined ownership boundary, a declared support matrix, and a discoverable installation, activation, readiness, and failure state. A full installation composes these same implementations.

The first concrete workflow is Windows Codex desktop connected over SSH to a project in an existing WSL Ubuntu environment. Install and run the browser tool in that project environment. Browser-only operation does not require the Loki appliance, GitHub authentication, Git, application secrets, devtools task coordination, or a general job executor.

The 0.2.x public CLI, MCP tools, configuration, persisted state, and distribution contracts are designed afresh. Implementation may reuse verified primitives. Release engineering targets 0.2.0 as the first candidate in the 0.2.x line. Source redesign does not authorize erasing existing installations, user workspaces, credentials, or running services.

## Tool ownership

All tool source roots live at the same modules/<tool> level. Collection names are plural; individual tool names identify capabilities. The public management group is tools. Integrations are provider bindings owned by the relevant tool.

| Tool | Owned behavior | Composition relationship |
| --- | --- | --- |
| browser | Official Playwright MCP and Chrome DevTools MCP packages, launchers, profiles, screenshots, upload/download staging, health and capability descriptions | Independent project-host operation; optional sharing adapter |
| git | Local repository operations, checkpoints, restore, Git signing, signing keys and agent lifecycle | Scoped filesystem access and declared execution backend; signing is a Git option |
| github | GitHub App configuration, installations, token issuance, API/gh operations, Issues, PRs and Projects | Own protected provider credentials and outbound API access; local Git is not required for API-only use |
| workspace | Scoped files, search, bounded edits and file history | Shared path-access primitives; explicit adapters for operations that need repository knowledge |
| execution | Isolated jobs, toolchains, output, cancellation, resource limits and owned service endpoints | Platform-specific sandbox backend; optional application-secret provider |
| secrets | Application-secret profiles, protected storage and explicit delivery grants | Values delivered through narrow execution adapters; provider keys remain with their owners |
| sharing | Artifact/image publication and live previews | Scoped content source for artifacts; verified endpoint ownership for previews |
| coordination | Existing external devtools task/workstream adapter | Own external CLI contract and task context handling |

Git includes signing as an internal feature. Runtime process and credential isolation may remain separate inside a tool. Product/module separation does not imply one process, Go module, container or independent release version per feature.

## Shared and composition layers

Shared primitives cover scoped path access, identity/policy values, bounded process and protocol handling, safe publication, and audit/error types. They do not require every tool to initialize an appliance or share every credential. Dependency direction is tool -> narrow shared contract; host/composition wires tool adapters. Sibling private implementations are not imported.

The host/composition layer resolves the selected tools and required prerequisites, installs verified artifacts, owns operation state and recovery, registers actual available tools, prevents duplicate bindings, and reports per-tool status/doctor results. Project context and combined views consume interfaces from selected tools. An absent tool is not initialized and its credentials are not requested.

A module can need a private execution service while exposing only its own public tool group. Selecting Git does not grant a general-purpose job tool automatically. Modules declare implementation prerequisites separately from public capabilities.

## Browser contract

Use exact official package versions and package integrity, retaining upstream licenses and notices. Keep upstream tool schemas, content blocks and feature semantics; keep engine-specific identities distinct. Preserve advertised MCP capabilities, discovery changes, errors, cancellation, roots and file/image results across the adapter. Forward progress when emitted and provide launcher/operation progress independently; do not invent upstream resources, prompts or progress support. Six legacy action wrappers do not define the 0.2 browser API.

The browser plugin and server descriptions identify a project-host browser with separate tabs and login state. Once the user selects this plugin for browser work, route browser checks to it. Generic in-app browser guidance must not accidentally select an unrelated Loki endpoint. A disabled tool group is absent from discovery and cached calls are rejected.

Record browser support by pinned engine version, capability, OS, transport and runtime mode. Separate upstream engines are supported without requiring them to share one browser. Session sharing is a capability with its own acceptance check. Engine-specific profiles prevent lock conflicts and cross-project state mixing. Optional upstream code-execution features require an explicit capability and runtime boundary; schema-level filtering alone does not establish confinement.

The prerequisite probes in [preflight.md](preflight.md) pin actual engines and record their native catalogs. Playwright's tested default catalog includes a server-process code-execution tool. The product must exclude that tool from discovery and reject cached calls until the explicit unsafe capability is enabled; upstream defaults do not define Loki defaults. Experimental features are separately activated and verified, including Chrome feature flags. File results require an owned local artifact adapter even when sharing is absent.

Readiness includes compatible Node/transitive package engines, Chrome availability, native libraries, sandboxed browser launch and dependencies for enabled features such as video. A successful MCP handshake alone is insufficient. Keep Chrome's sandbox enabled; browser-process sandboxing does not confine the Node MCP server. Cancellation may suppress a response without proving browser actions stopped, so operation timeouts and owned process cleanup remain host responsibilities.

Standalone project-host execution and the full appliance deployment have separately documented authority and network boundaries. Full-mode adapters preserve the protected service identities, credentials and network controls. A browser-only screenshot/file workflow does not require public sharing or an external tunnel.

## Host and distribution contract

Management CLI targets Windows, Linux and macOS. Each tool declares which OS/runtime combinations it supports. Linux-only workload isolation is not silently advertised as native macOS/Windows support. Windows project-host selection distinguishes native Windows, an existing WSL distribution, and an SSH host. Localhost and filesystem paths refer to the selected execution host.

Host installation installs management only. Tool installation resolves exact artifacts and prerequisites for selected tools. Installation and activation are separate states. Removal owns only managed tool resources and preserves user work/data. Terminal/browser launching behavior is deliberate, progress is visible during slow operations, and errors retain their actual cause across platform and transport hops.

Initially keep one root Go module and one release train; Node workspaces manage official browser dependencies. Modules have separate artifact dependency closures. Full-bundle metadata pins exact included artifacts. macOS acquisition, signing/distribution requirements and supported architectures are established by the platform task and real acceptance; they are not current supported-product claims.

The observed Chrome for Testing manifest provides Linux x64/arm64, macOS x64/arm64 and Windows x86/x64 artifacts, but no native Windows arm64 artifact. The Linux arm64 archive's availability was checked, not its execution. A management target or downloadable browser does not imply tested browser support. Package acquisition records independently trusted release integrity, complete bundled notices and feature-specific dependencies. Mutable `latest`, auto-installing `npx` launches and an unversioned system Node are not artifact contracts. The current Linux host CLI does not cross-build for macOS; management needs a portable entrypoint and platform filesystem/process backends rather than importing the appliance command graph.

## Repository rollout

Create modules/<tool>, thin entrypoints, shared primitives and host composition as each coherent slice is implemented. Existing source is an implementation reference; 0.2 contracts are authoritative. Keep the current main branch and existing installations reviewable while preparing source and candidate artifacts. Publication and installation cutover are separate operations.

## Implementation map

This specification describes the target product. The inspected 0.1 implementation still binds the following features together; these are extraction points, not completed 0.2 support claims.

| Current source | Observed coupling | Target ownership / task |
| --- | --- | --- |
| `internal/app/mcp/app.go` | MCP startup requires runtime, port guard, general jobs, Git jobs and toolchains; registration builds most groups together | Selected module composition / `contracts`, `composition` |
| `internal/work/workspace/repository.go`, `internal/work/workspace/git/runner.go` | Repository operations live under workspace and execute through a Job runner | Git owns repository operations and declares its private execution adapter / `workspace_git` |
| `internal/work/workspace/search_patch.go` | File patching invokes the configured Git runner | Workspace owns bounded edits, with an explicit repository adapter where needed / `workspace_git` |
| `internal/agentcontext/provider.go` | Project guidance requires a repository resolver | Composition supplies optional repository context to scoped project guidance / `composition` |
| `internal/integrations/signing/agent.go` | A private SSH agent and restricted public proxy protect the signing key | Git owns signing configuration and isolated internal services / `workspace_git` |
| `internal/integrations/github` | GitHub provider configuration and tokens have their own implementation but bind through the main runtime | GitHub owns API authorization independently of local Git / `providers` |
| `cmd/loki/browser.go`, `internal/transport/mcp/browser_upload.go` | Browser startup requires service identity, execution contract and proxy; upload staging uses workspace Files | Official browser engines plus host-mode-specific launch and staging adapters / `browser_probe`, `browser_package` |
| `internal/transport/mcp/previews.go` | Preview routes depend on owned ports or active Job endpoint leases | Sharing consumes a verified endpoint ownership contract / `runtime_adapters` |
| `internal/devtools` | External task coordination is wired through the full runtime | Optional coordination adapter / `runtime_adapters` |
| `cmd/loki-windows`, `.github/workflows/release.yml` | Windows frontend and release pipeline assume the appliance distribution | Management-only host entrypoints and selected artifact closures / `host_lifecycle`, `candidate` |

The source layout initially uses `modules/<tool>` for tool ownership, `internal` for narrowly shared and host/composition implementations, and thin `cmd` entrypoints. Existing root `tools` contains maintainer tooling; the user-facing `tools` CLI group does not imply that source folder owns product modules.

The installed devtools 0.23.0 speaks protocol 6; the current adapter accepts protocol 5 and fails its real-process test with that binary. Coordination must declare its exact supported CLI/protocol/catalog and test the real pinned binary. GitHub repository Projects uses provider-owned installation authorization by default. Personal Projects authorization stays optional. Disabled organization Projects is a distinct readiness cause with an activation link; enabling it through the organization update API requires separately authorized Organization Administration write access and must not silently expand default permissions.

## Acceptance evidence

Every requirement is linked to an acceptance criterion, task and required validation in the devtools workstream. Definitions and plans are updated before scope-dependent implementation. Each source task is claimed and checkpointed with implementation decisions, remaining work and next action. Candidate preparation follows implementation; all remaining product validation procedures run in the final validation phase against the current definition/code basis. Historical investigation evidence remains an implementation input. Delivery and verified acceptance are reported separately, and the workstream stays active until every required final check passes.
