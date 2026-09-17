# Loki installation and distribution plan

## Goal

Loki should be installable on a clean Linux or WSL2 host without exposing its internal service layout, fixed container identities, secret storage, or Compose wiring to the operator.

The product goal is not to provide a general-purpose remote shell. Loki exists to give an attached MCP client a stable, tightly constrained execution environment where the client can perform repository and development work through Loki's bounded MCP tools. External network access belongs to the connected client or to explicitly permitted Loki facilities; it is not a reason to broaden the MCP server's host privileges.

Installation scope and runtime behavior are separate concerns. Whether the host-management CLI is installed for one user or system-wide, the MCP runtime must use the same OCI images, Compose contract, workspace boundary, identities, network policy, and tool restrictions.

## Canonical deployment

Docker Compose is the canonical installation model for ordinary Loki users.

The core installation starts only the services required for the MCP runtime:

- prepare
- egress
- runtime
- mcp

Optional components are not enabled during the base installation. Browser control and isolated Git signing remain opt-in capabilities that can be enabled later without reinstalling Loki.

The existing native systemd/Go-candidate deployment remains an advanced and maintainer-oriented path for release engineering, migration validation, and specialized native deployments. It is not the primary first-install experience.

## Supported hosts

The first fully automated and verified hosts are:

- Ubuntu 24.04 amd64
- WSL2 running Ubuntu 24.04 amd64

Other Linux distributions may run Loki when a compatible Docker Engine and Docker Compose v2 are already available. Loki does not initially automate package-manager changes on those distributions.

## License and public distribution

Loki is distributed under the Apache License 2.0.

Public installation artifacts should be readable without requiring a GitHub login or another bootstrap credential. The intended distribution channels are:

- stable installer frontend: `https://jinyongp.dev/loki/install.sh`
- host CLI binaries and release metadata: GitHub Releases
- OCI images: `ghcr.io/jinyongp/loki`

`jinyongp.dev` is the stable user-facing installation URL. The artifact backend may change without changing the documented install command.

Released OCI images must preserve the licenses and required notices of bundled third-party software such as Chromium and toolchains. Loki's Apache-2.0 license does not replace third-party licenses.

## Bootstrap architecture

The public shell installer must remain deliberately small. Its responsibilities are limited to:

1. Detect the supported operating system and CPU architecture.
2. Fetch signed or otherwise authenticated release metadata.
3. Download the matching Loki host-management binary.
4. Verify its checksum and release authenticity.
5. Start `loki host install`.

The shell bootstrap must not contain the Compose lifecycle implementation. Installation, update, rollback, diagnostics, and optional-component management belong in the Go host-management CLI so they share one implementation and one safety model.

The intended first-install command is:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer should use `/dev/tty` for interactive approval when standard input is occupied by the pipe.

## Installation scope

User-scoped installation is the recommended default because it limits host-wide changes and is appropriate for a developer connecting their own MCP clients.

A system-wide installation remains available explicitly. Both scopes run the same Compose runtime contract and therefore expose the same MCP capabilities and restrictions.

The intended entry points are:

```sh
# Recommended default: user-scoped host manager
curl -fsSL https://jinyongp.dev/loki/install.sh | sh

# Explicit system-wide host manager
curl -fsSL https://jinyongp.dev/loki/install.sh | sudo sh -s -- --system
```

The exact host paths are an implementation detail of the selected scope. They must not alter the container runtime contract or MCP authorization boundary.

## Workspace selection

Loki does not silently invent a default workspace directory. Installation requires the operator to choose the host directory that Loki is allowed to access.

Interactive installation asks for the workspace path. A non-interactive installation accepts an explicit workspace option.

When the selected path does not exist, Loki shows the exact directory-creation command and asks for approval before creating it. When the path already exists, Loki checks whether the Compose runtime can safely read, write, and traverse it.

Loki must not apply broad recursive permission changes such as `chmod -R`. If a permission adjustment is required, Loki presents the smallest exact command, explains its effect, and runs it only after explicit approval.

The workspace remains a host bind mount and is the primary filesystem boundary visible to the MCP runtime.

## Docker prerequisite policy

Loki first checks whether Docker Engine and Docker Compose v2 already work for the current operator. If they do, installation does not alter Docker configuration.

On the fully supported Ubuntu hosts, Loki may offer to install the minimum Docker/Compose prerequisites when they are missing. Before making any package-manager or service change, Loki prints the exact commands it proposes to run and asks for explicit approval.

Non-interactive execution must not silently elevate privileges or install host packages. It requires an explicit option authorizing the known prerequisite commands.

If Docker is installed but the current user cannot access the Docker socket, the default recommendation is to use `sudo` only for the required Docker management operations. Loki does not automatically add the user to the `docker` group because membership grants effectively root-equivalent Docker access.

The installer may offer `docker` group membership as an explicit alternative. It must show the exact command and the security consequence before the operator accepts it.

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

Loki manages development toolchains independently from the workspace. Runtime families such as Node.js, pnpm, Python, uv, Rust, and Go are host-approved capabilities. Project files may select a version of an already-approved family, but they cannot grant a new capability or modify the host policy.

Loki does not depend on external version managers such as fnm, nvm, pyenv, or rustup for its runtime contract. Instead, the runtime exposes thin Loki-owned shims at the front of `PATH`. A shim resolves the requested toolchain version for the current working directory and then executes the matching binary from a Loki-managed store.

The managed store lives outside the workspace and is writable only through Loki's toolchain management boundary. Ordinary runner processes may read and execute installed toolchains but cannot replace or mutate them. Toolchains are downloaded only from Loki-defined trusted sources, verified against authenticated metadata and checksums, and installed atomically.

Projects use ecosystem-standard version declarations where practical. Examples include `.node-version` or `.nvmrc` for Node.js, the `packageManager` field for pnpm, `.python-version` for Python, uv's project version requirements, `rust-toolchain.toml` for Rust, and Go module/toolchain declarations. Loki-specific project policy files are not used to grant execution or network permissions.

Version declarations are selectors rather than authorization grants. Resolution follows these rules:

- An exact selector such as `22.23.2` selects that exact version.
- A partial selector such as `22` or `22.23` selects the highest installed matching version.
- If no installed version matches, Loki resolves the latest matching version from its trusted toolchain index, verifies it, installs it into the managed store, and then executes it.
- The existence of a newer matching version does not replace an already-installed matching version during ordinary command execution. Toolchain updates are explicit operations.
- A selector cannot escape the administrator-owned capability and version policy for that toolchain family.

For example, a project that declares Node.js `22` uses the highest installed `22.x` release. If no `22.x` release is installed, Loki installs the latest trusted `22.x` release. Updating an existing `22.x` installation to a newer matching release is a separate explicit toolchain update rather than a side effect of running `node`.

Project dependencies remain project state rather than host toolchains. Once a runtime family and its package manager are approved, operations such as `pnpm install`, `uv sync`, `cargo update`, and their project-local executables operate inside the workspace under the existing filesystem and egress policy. Loki does not promote packages such as ESLint, Ruff, pytest, or individual crates into the host command allowlist merely because a project uses them.

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

The installation and lifecycle work must preserve these constraints:

- A host-management installation scope must not change the MCP runtime's authorization model.
- Loki must never broaden workspace access implicitly.
- Workspace contents may select versions of host-approved toolchain families but cannot grant capabilities, alter command policy, or widen egress policy.
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

After that gate, delete `legacy/python/` and remove its navigation references from this plan and the root README. Shared `bundled_skills/`, `config/loki-gitconfig`, current Go packaging, and Go migration/compatibility fixtures remain outside the retirement unit. Go build and release tests must not depend on the Python archive being present.

## Implementation sequence

The Python retirement boundary above is established before the installation implementation. The remaining implementation should proceed in dependency order:

1. Define and implement the host-management package and `loki host` command boundary.
2. Move or reimplement the existing Compose lifecycle primitives behind that boundary while preserving backup, rollback, credential rotation, and health behavior.
3. Embed or package the versioned Compose/configuration assets so source checkout is not required.
4. Implement workspace selection and safe permission preflight.
5. Implement Docker/Compose detection and the Ubuntu/WSL prerequisite approval flow.
6. Define the signed release manifest and immutable artifact layout.
7. Produce public GitHub Release binaries and GHCR core images with third-party notices.
8. Publish the thin `jinyongp.dev/loki/install.sh` bootstrap.
9. Implement the Loki-owned toolchain index, managed toolchain store, thin runtime shims, project version resolution, trusted download verification, and explicit toolchain update operations.
10. Implement `update status`, `prepare`, and `apply` on top of the release manifest and lifecycle primitives.
11. Add optional browser and signing component management.
12. Replace the current self-hosting first-install documentation with the product installation flow and move candidate/systemd instructions to maintainer-oriented documentation.
13. Add clean-host acceptance that exercises first install, restart, connection reporting, toolchain version selection and installation, update preparation, explicit update application, rollback, uninstall behavior, and optional-component enable/disable without relying on a source checkout.

## Follow-up design work

The following details are intentionally deferred until the base host lifecycle is implemented around the decisions above:

- the exact interactive presentation and flags for browser/signing enablement
- uninstall policy for retaining or deleting state, backups, and workspace contents
- GitHub App onboarding UX
- optional public-tunnel integration
- the exact release-signing technology and key-rotation procedure
- final third-party notice generation and verification format

These follow-up items must preserve the safety invariants and canonical deployment model defined in this document.
