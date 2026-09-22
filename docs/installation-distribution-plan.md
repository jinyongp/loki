# Loki installation and distribution plan

## Goal

Loki should be installable on a clean Linux or WSL2 host without exposing its internal service layout, fixed container identities, secret storage, or Compose wiring to the operator.

The product goal is not to provide a general-purpose remote shell. Loki exists to give an attached MCP client a stable, tightly constrained execution environment where the client can perform repository and development work through Loki's bounded MCP tools. External network access belongs to the connected client or to explicitly permitted Loki facilities; it is not a reason to broaden the MCP server's host privileges.

Installation scope and runtime behavior are separate concerns. Whether the host-management CLI is installed for one user or system-wide, the MCP runtime must use the same OCI images, Compose contract, workspace boundary, identities, network policy, and tool restrictions.

## Foundational authorization and execution rules

These are product-wide design requirements for every Loki capability, not exceptions for file or Git operations. They apply to dedicated MCP tools, generic command execution, managed processes, project scripts and hooks, toolchain providers, optional components, and external integrations. They describe the target architecture; this plan does not claim that every existing execution path already meets them.

Authorization is determined by the caller, administrator-owned grants, requested operation, and target resources, not by the interface used to request it. Changing from a dedicated tool to a CLI, interpreter, script, or child process must not broaden authority. Components may have different narrowly scoped grants, but all derive them from the same host-owned policy authority. Sharing policy does not mean giving every component the same credentials or privileges.

Host policy and administrative state remain outside the agent-accessible workspace and cannot be changed through an MCP operation, project file, executable, or subprocess. Runtime components consume effective policy; they do not expose host-policy or server-deployment mutation to the runner. Installation must reject a workspace selection that would expose protected management state.

Mandatory restrictions must be enforced at boundaries that every applicable access traverses: filesystem and mount permissions, execution identities and process isolation, network mediation, resource limits, and authenticated brokers for protected operations. Validating only the working directory, top-level command name, command arguments, or shim path is not sufficient. Agent instructions and preferred-tool guidance are not authorization controls. Project code, dependencies, build hooks, and their descendants remain untrusted and must stay within the execution context's grants.

Toolchain approval governs managed provisioning and supported execution entrypoints; it is not proof that project code cannot implement equivalent behavior through another approved interpreter. Any stronger restriction must have an enforceable resource or execution boundary and cross-path tests, rather than relying on a command-name denylist.

Generic execution remains a first-class development interface within those bounds. Dedicated tools are retained where they add meaningful value such as typed inputs, structured output, concurrency checks, atomic operations, recovery, or narrowly brokered access. They are not maintained merely to wrap an existing CLI. Brokered capabilities expose only the authorized operation, not the broker's raw privileges, credentials, or administrative state.

Operation-specific safeguards must be described accurately. A hash check, revision snapshot, or other protection offered by one tool is not a system-wide guarantee while another permitted path can omit it. If a protection is mandatory for all equivalent operations, the underlying authority must be mediated so every path enforces it; otherwise it remains a documented tool-level safeguard. Secret injection similarly grants the receiving process access to the injected value; suppressing default output alone is not a confidentiality boundary.

Errors must distinguish authorization denials, failed operation preconditions, unsupported inputs, and implementation failures. A policy denial must not suggest retrying through generic execution. An alternative supported interface may be used for a tool limitation only within the same authorization and any mandatory operation preconditions.

Acceptance must test the same prohibited resource access through every applicable direct and indirect route, including generic commands, project code, child processes, hooks, and broker requests. Tests must also prove that permitted development work succeeds, policy cannot be widened by workspace changes, and enabling a component does not leak its authority to unrelated execution paths. Adding a capability requires reviewing these boundaries, not just adding a tool handler.

## Canonical deployment

Docker Compose is the canonical installation model for ordinary Loki users.

The core installation contains only the roles required for safe MCP work: initialization, the MCP gateway, authorization/control, isolated job execution, and required network mediation. Their process/container decomposition follows the architecture improvement plan. Existing Compose service names are not a constraint that requires untrusted jobs to share the gateway or credential-controller context.

Optional components are not enabled during the base installation. Browser control and isolated Git signing remain opt-in capabilities that can be enabled later without reinstalling Loki.

The existing native systemd/Go-candidate deployment remains an advanced and maintainer-oriented path for release engineering, migration validation, and specialized native deployments. It is not the primary first-install experience.

## Supported hosts

The initial clean-host acceptance targets for the new installer are:

- Ubuntu 24.04 amd64
- WSL2 running Ubuntu 24.04 amd64

Other Linux distributions may run Loki when a compatible Docker Engine and Docker Compose v2 are already available. Loki does not initially automate package-manager changes on those distributions.

## License and public distribution

Loki is distributed under the Apache License 2.0.

Public installation artifacts must be readable without requiring a GitHub login or another bootstrap credential. The intended distribution channels are:

- signed TUF metadata and consistent-snapshot targets: `jinyongp.dev/loki/tuf/`
- immutable public release assets and acceptance evidence: GitHub Releases
- OCI images: `ghcr.io/jinyongp/loki`
- reserved post-acceptance installer frontend: `jinyongp.dev/loki/install.sh`

The stable installer frontend is reserved but is not published or advertised while A13/A14 acceptance remains open. During release validation, first install starts from the authenticated `loki-bootstrap` binary artifact described in [First install](first-install.md). Before A14 evidence is assembled, the configured TUF signing system must finish a signed repository version. Loki verifies that repository through its production Go TUF client, proves that the accepted bootstrap embeds `https://jinyongp.dev/loki/tuf/` and the same trusted root, and binds the deterministic repository archive into the candidate evidence. The final publication pipeline then has an A14 caller supply that accepted candidate bundle: `releaseway/actions` publishes the exact public asset set as an immutable GitHub Release, and only after that succeeds does the Loki project Pages deployment update both `jinyongp.dev/loki/install.sh` and `jinyongp.dev/loki/tuf/`. The Pages installer is release-bound to one exact Git tag and the exact SHA-256 of the public `loki-bootstrap-linux-amd64` artifact; it does not resolve a mutable bootstrap at install time. The artifact backend may change later without changing the reserved frontend.

Released OCI images must preserve the licenses and required notices of bundled third-party software such as Chromium and toolchains. Loki's Apache-2.0 license does not replace third-party licenses.

## Bootstrap architecture

The source-free bootstrap is deliberately small. It:

1. Uses an embedded initial TUF root and authenticated metadata repository URL.
2. Resolves the requested or current Loki release for the supported host.
3. Downloads and verifies the matching host binary and release manifest.
4. Stages the verified inputs privately.
5. Hands installation to `loki host install`.

The reserved public shell frontend is a release-rendered thin downloader. It detects the supported host, downloads one exact `loki-bootstrap` artifact from one immutable GitHub Release tag, verifies the SHA-256 embedded into that rendered installer, and executes the bootstrap with the caller's arguments. It contains no lifecycle logic. Installation, update, rollback, diagnostics, and optional-component management belong in the Go host-management CLI so they share one implementation and one safety model.

The canonical pre-release first-install procedure is [docs/first-install.md](first-install.md). The source and publication workflow for the stable frontend are implemented, but no public one-line shell command is documented as live until A14 release acceptance passes and the Pages deployment succeeds.

## Installation scope

User-scoped host management is the recommended default because the operator-facing CLI, configuration and lifecycle ownership belong to the developer rather than a machine-wide administrative interface. This scope does not imply that every enforcement helper runs as that user: if the verified sandbox backend requires a root-owned launcher or other minimal system component, installation presents that component and its exact privileged changes for explicit approval.

A system-wide host-management installation remains available explicitly. Both scopes run the same workload, policy and isolation contract and therefore expose the same MCP capabilities and restrictions. Scope changes who owns host-management state and commands; it must not weaken or broaden project execution authority.

During A13/A14 validation, both scopes are invoked through the verified `loki-bootstrap` artifact rather than a public installer URL. The bootstrap forwards installation options, including `--system`, to `loki host install`.

The exact host paths are an implementation detail of the selected scope. They must not alter the container runtime contract or MCP authorization boundary.

## Workspace selection

Loki does not silently invent a default workspace directory. Installation requires the operator to choose the host directory that Loki is allowed to access.

Interactive installation asks for the workspace path. A non-interactive installation accepts an explicit workspace option.

When the selected path does not exist, Loki shows the exact directory-creation command and asks for approval before creating it. When the path already exists, Loki checks whether the Compose runtime can safely read, write, and traverse it.

Loki must not apply broad recursive permission changes such as `chmod -R`. If a permission adjustment is required, Loki presents the smallest exact command, explains its effect, and runs it only after explicit approval.

The selected workspace remains the operator-approved host data root. The gateway and each workload receive only the views required by their role; a job may receive a narrower repository/workspace mount than the full selected root. Selecting a workspace never implies that control-plane state, credentials, Docker/launcher sockets, or host-management files become visible to project execution.

## Docker prerequisite policy

Loki first checks whether Docker Engine and Docker Compose v2 already work for the current operator. If they do, installation does not alter Docker configuration.

On the fully supported Ubuntu hosts, Loki may offer to install the minimum Docker/Compose prerequisites when they are missing. Before making any package-manager or service change, Loki prints the exact commands it proposes to run and asks for explicit approval.

Non-interactive execution must not silently elevate privileges or install host packages. It requires an explicit option authorizing the known prerequisite commands.

If Docker is installed but the current user cannot access the Docker socket, Loki does not automatically add the user to the `docker` group because membership grants effectively root-equivalent Docker access. One-shot lifecycle work may use explicitly approved `sudo` Docker commands, but ongoing job launches must not depend on an interactive sudo prompt. When the selected sandbox backend needs privileged Docker access, Loki installs or configures only the dedicated launcher boundary required for that purpose and exposes a finite validated launch protocol instead of giving the agent or gateway the raw socket.

The installer may offer direct Docker-group membership only as an explicit operator alternative, not the default architecture. It must show the exact command and security consequence, and the workload isolation contract remains identical either way.

On WSL2, Loki first accepts any already-working Docker integration. If Loki would need a local Docker Engine and WSL systemd is disabled, it explains the required `/etc/wsl.conf` change and `wsl.exe --shutdown` restart rather than forcibly shutting down the running WSL instance.

## Host CLI

Host lifecycle operations live under a dedicated namespace so they remain separate from Loki runtime and administrator commands.

The planned surface includes:

```text
loki host install
loki host status
loki host doctor
loki host connection
loki host update ...
loki host rollback
loki host backup
loki host restore
loki host enable ...
loki host disable ...
loki host uninstall
```

The host manager materializes the versioned Compose and configuration assets it needs. Ordinary installation must not require cloning the Loki source repository.

`loki host connection` reports generic MCP connection information such as endpoint, authentication mode, and token-file location. Loki does not configure ChatGPT, Codex, or any other specific MCP client; client configuration is outside Loki's responsibility.

## Toolchain management

Loki manages development toolchains independently from the workspace. Runtime families such as Node.js, pnpm, Python, uv, Rust, and Go are administrator-approved managed toolchains: approval controls provisioning and supported execution availability, not the underlying sandbox authority. Project files may select a version of an approved family, but they cannot change host policy, mounts, credentials, network grants, or other execution authority.

Loki does not depend on external version managers such as fnm, nvm, pyenv, or rustup for its runtime contract. Instead, the runtime exposes thin Loki-owned shims at the front of `PATH`. A shim resolves the requested toolchain version for the current working directory and then executes the matching binary from a Loki-managed store.

The managed store lives outside the workspace and is writable only through Loki's toolchain management boundary. Ordinary runner processes may read and execute installed toolchains but cannot replace or mutate them. Toolchains are downloaded only from Loki-defined trusted sources, verified against authenticated metadata and checksums, and installed atomically.

Projects use ecosystem-standard version declarations where practical. Examples include `.node-version` or `.nvmrc` for Node.js, the `packageManager` field for pnpm, `.python-version` for Python, uv's project version requirements, `rust-toolchain.toml` for Rust, and Go module/toolchain declarations. Loki-specific project policy files are not used to grant execution or network permissions.

Version declarations are selectors rather than authorization grants. Resolution follows these rules:

- An exact selector such as `26.9.0` selects that exact version.
- A partial selector such as `22` or `22.23` selects the highest installed matching version.
- If no installed version matches, Loki resolves the latest matching version from its trusted toolchain index, verifies it, installs it into the managed store, and then executes it.
- The existence of a newer matching version does not replace an already-installed matching version during ordinary command execution. Toolchain updates are explicit operations.
- A selector cannot escape the administrator-owned provisioning and version policy for that toolchain family.

For example, a project that declares Node.js `26` uses the highest installed `26.x` release. If no `26.x` release is installed, Loki installs the latest trusted `26.x` release. Updating an existing `26.x` installation to a newer matching release is a separate explicit toolchain update rather than a side effect of running `node`.

Project dependencies remain project state rather than host toolchains. Once a runtime family and its package manager are provisioned for the workload, operations such as `pnpm install`, `uv sync`, `cargo update`, and their project-local executables run under that job's filesystem, credential, resource and network grants. Project packages such as ESLint, Ruff, pytest, or individual crates do not become host-managed toolchains merely because a project uses them, and command names are not treated as a substitute for the underlying sandbox policy.

Derived OCI images remain available for comparatively static native or operating-system extensions that cannot be expressed as a managed language toolchain. Routine Node.js, Python, uv, Rust, pnpm, or Go version changes do not require rebuilding the Loki runtime image or restarting the MCP server.

## Optional components

Browser control and isolated Git signing are disabled by default.

They must be independently enableable and disableable after installation, for example:

```sh
loki host enable browser
loki host disable browser

loki host enable signing
loki host disable signing
```

Enabling an optional component performs only the additional image, state, configuration, and health work required by that component. It does not reinstall or broaden the base runtime.

The signing capability remains provider-neutral. Loki may expose the generated public signing key, but registering that key with a particular Git provider is a separate integration concern.

## Update model

A Loki update must not immediately mutate a running MCP server merely because a newer release exists or has been downloaded.

The public update lifecycle is separated into three phases:

```text
loki host update status
loki host update prepare
loki host update apply
```

### Status

`update status` is read-only. It reports the installed release, the available release, immutable image digests, release notes, compatibility requirements, migration requirements, optional-component impact, restart impact, and rollback compatibility.

It performs no service restart and changes no installed runtime state.

### Prepare

`update prepare` downloads and verifies the target host binary, release manifest, and OCI images. It performs compatibility and migration preflight checks but does not switch the running Loki release.

A successfully prepared update can therefore be inspected before the operator accepts any runtime interruption.

### Apply

`update apply` is the explicit mutation boundary. Before changing services it shows the concrete impact and asks for confirmation in interactive mode.

Application creates the required recovery snapshot, switches the pinned release assets, performs any declared state migration, recreates the affected Compose services, and runs health and MCP smoke checks. If validation fails, Loki automatically attempts to restore the prior known-good release and matching state.

Automatic background updates, automatic update application, and silent self-update are out of scope for the initial distribution model.

## Release metadata

A release manifest must contain enough information for installation and update planning without trusting mutable tags alone. At minimum it should carry:

- Loki version
- release timestamp
- host binary URL, checksum, and authenticity information
- immutable core OCI digest
- immutable digests for optional component images
- minimum Docker and Compose requirements
- supported host information
- state-schema compatibility information
- required migration information
- rollback compatibility information
- release-notes location

Runtime application must pin OCI images by immutable digest rather than relying on a mutable tag.

## Safety invariants

All Loki capabilities, including installation and lifecycle work, must preserve the foundational authorization and execution rules above and these constraints:

- A host-management installation scope must not change the MCP runtime's authorization model.
- Loki must never broaden workspace access implicitly.
- Workspace contents may select versions of administrator-approved managed toolchains but cannot alter host policy, filesystem/mount authority, credential grants, resource limits, or network/egress authority.
- Managed toolchains and their shims are owned outside the workspace; ordinary runner processes cannot mutate installed toolchain binaries or trusted-source metadata.
- Host package installation, permission changes, and privileged commands are shown before execution and require approval unless the operator has explicitly authorized that exact class of non-interactive change.
- Secrets are generated or imported through Loki's protected state boundary and are not printed by default.
- Optional capabilities remain disabled until explicitly enabled.
- Downloading an update does not alter the running MCP server.
- Runtime changes occur only at an explicit apply boundary and retain a rollback path.
- The install path does not configure or assume a particular MCP client.
- Ordinary installation does not require the Loki source repository or a local Go, Node, or Python toolchain.

## Retained Python deployment

The previous Python server and all Python-deployment-only source, package metadata, browser sidecar, tests, installers, host launchers, systemd units, configuration, and live verification assets are isolated in `legacy/python/`. Its [maintenance and removal guide](../legacy/python/README.md) defines the retirement unit. There are no old-path forwarding wrappers at the repository root.

Keep the archive until the Go replacement has actually been deployed, required MCP workflows and enabled integrations pass, restart and protected-state recovery have been verified, and the operator has retired the Python rollback path. This repository reorganization is not a deployment or a removal of installed Python services.

After that gate, delete `legacy/python/` and remove its navigation references from this plan and the root README. Shared `bundled_skills/`, `config/gitconfig`, current Go packaging, and Go migration/compatibility fixtures remain outside the retirement unit. Go build and release tests must not depend on the Python archive being present.

## Implementation sequence

The [repository-wide Go review](go-readiness-review.md) records reproduced defects and integration gaps at the reviewed baseline. The [architecture improvement plan](architecture-improvement-plan.md) is the authoritative remediation sequence, with work units A01-A14 and explicit acceptance criteria. Compatibility with the Python or current Go API/configuration is not required; preserving operator data and the separate recovery/cutover boundary is required.

Implementation proceeds through enforcement and usefulness before installation polish:

1. Add regressions and implement strict policy, credential separation, isolated execution, complete job lifecycle and workload-specific networking (A01-A06).
2. Provide a usable MCP-only execution workflow and correct file/repository operations through that boundary (A07-A08).
3. Implement safe toolchain provisioning/shims (A09). After its dependencies are ready, optional runtime integrations (A10) and the transactional core host lifecycle (A11) may progress independently rather than blocking one another.
4. Complete remaining providers, resource/retention budgets, diagnostics and package cleanup (A12), including each finished optional integration's lifecycle hooks and acceptance.
5. Prepare authenticated artifacts and the thin bootstrap, then run source-checkout-free Ubuntu/WSL acceptance against actual distinct release images (A13-A14). Advertise the public one-line installation only after those required gates pass.

The host CLI still owns workspace selection, explicit prerequisite approval, embedded/versioned Compose assets, optional-component management, and generic connection reporting. Runtime findings must not be bypassed by making the installer silently grant broader privileges. Production deployment, acceptance of cutover, and legacy retirement remain separately authorized operations; no review or implementation step implicitly restarts the existing Python service.

## Deferred implementation details

These details do not block the trusted execution or MCP-only development milestones. They are resolved inside the work unit that owns them, under the contracts already fixed above rather than as separate architecture decisions:

- A10 chooses the exact interactive flags/presentation for browser and signing, while preserving explicit opt-in/disable semantics and zero additional authority when disabled.
- A11 uninstall preserves the workspace unconditionally and retains Loki-owned state/backups by default. Purging Loki-owned state is a separate destructive action with an explicit preview and confirmation; uninstall never recursively deletes the operator-selected workspace.
- A10/A13 may improve GitHub App onboarding, but provider onboarding remains optional and cannot become a prerequisite for core installation or generic MCP use.
- Public-tunnel integration is not part of the initial general-install release. If added later, it is an optional provider integration with its own authority and exposure review, never a silent installer side effect.
- A13 selects the concrete release/toolchain metadata authentication technology against the required freshness, rollback-protection, trust-root and key-rotation properties in the architecture plan.
- A13 may choose the concrete third-party notice/provenance file format, but release acceptance must verify that required notices and provenance inputs are complete and shipped with the relevant artifacts.

None of these implementation details may weaken the foundational authorization, data-preservation or explicit-apply rules in this document.
