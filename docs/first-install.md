# First install

This is the canonical first-install procedure for the A13/A14 release-validation phase.

The public one-line installer is **not published or advertised yet**. Until A14 release acceptance passes, obtain the exact `loki-bootstrap` artifact from the release-validation fixture. The final public shell frontend and publication workflow are implemented but remain gated: the rendered script is bound to one immutable Git tag and the exact SHA-256 of `loki-bootstrap-linux-amd64`, and it contains no lifecycle logic.

## What you need

On a supported clean host, you need only:

- Ubuntu 24.04 amd64, or WSL2 running Ubuntu 24.04 amd64;
- the authenticated `loki-bootstrap` artifact for that host;
- a directory you want Loki to use as its workspace.

The target host does not need a Loki source checkout. You do **not** need to install Go, Node.js, pnpm, Python, uv, Rust, Chromium, or any other development toolchain.

Docker Engine and Docker Compose v2 are runtime prerequisites, but on supported Ubuntu hosts you do not need to install them manually. If the installed release needs Docker changes, Loki shows the exact prerequisite commands and asks for approval before changing packages or services. Loki never adds the operator to the `docker` group automatically.

## Interactive install

Make the supplied bootstrap executable and run it:

```sh
chmod 0755 ./loki-bootstrap
./loki-bootstrap
```

The installer asks for the workspace when it is not supplied. If the directory does not exist, it shows the exact creation command before asking to create it. If Loki needs a minimal POSIX ACL for the container runtime identity, it shows that exact ACL change before applying it.

If Docker Engine or Compose does not satisfy the authenticated release requirements, supported Ubuntu installations show the official Docker apt-repository/package commands before asking whether to run them. An existing compatible Docker installation is left unchanged.

If the current user cannot access an otherwise compatible Docker daemon, Loki does not silently change group membership. Interactive installation can instead offer an explicit sudo-backed host-lifecycle Docker boundary. MCP, executor, and project jobs still do not receive the raw Docker socket.

A successful user-scoped install persists the verified host CLI at:

```text
~/.local/bin/loki
```

The installed Compose runtime remains bound to the operator-approved workspace and publishes MCP only on loopback.

## System-scoped install

For machine-wide host-management ownership, use the same authenticated flow:

```sh
sudo ./loki-bootstrap --system
```

System scope installs the host CLI at:

```text
/usr/local/bin/loki
```

The scope changes host-management ownership and paths. It does not broaden MCP authorization, project execution authority, filesystem access, network grants, or Docker authority.

## Non-interactive install

Automation must make every privileged mutation class explicit. For example:

```sh
./loki-bootstrap \
  --workspace /srv/workspace \
  --create-workspace \
  --prepare-workspace \
  --install-prerequisites \
  --allow-sudo-workspace \
  --allow-sudo-docker
```

Omit approvals that are not needed by the target host. Missing required input or approval fails instead of prompting when stdin is not interactive.

Use `--json` when the caller needs a machine-readable installation result.

To validate a specific authenticated release instead of the current release selected by metadata:

```sh
./loki-bootstrap --bootstrap-release 1.2.3
```

## After installation

The installer prints the installed release, workspace, MCP endpoint, persistent CLI path, and the commands for connection details and health checks.

For user scope:

```sh
~/.local/bin/loki host status
~/.local/bin/loki host connection
~/.local/bin/loki host doctor
```

For system scope:

```sh
sudo /usr/local/bin/loki host status --system
sudo /usr/local/bin/loki host connection --system
sudo /usr/local/bin/loki host doctor --system
```

`loki host connection` reports the loopback MCP endpoint, transport, authentication mode, and token-file path. It does not print the token value.

## What the installer handles

The authenticated bootstrap and host manager together:

1. detect the supported host;
2. authenticate release metadata using the embedded initial TUF root;
3. select and verify the release manifest and host binary;
4. validate the release-declared Docker Engine and Compose minimum versions;
5. request approval for any supported Ubuntu prerequisite changes;
6. select, create, and minimally prepare the operator-approved workspace;
7. materialize versioned Compose/configuration assets and a private MCP token;
8. apply the immutable release images through the transactional host lifecycle;
9. persist the verified host-management CLI;
10. report status and connection information.

The operator does not need to manage Compose YAML, internal service identities, fixed UID/GID values, token generation, Docker socket mounts, or Loki-managed language toolchains.

## Safety boundaries

The installer does not:

- trust mutable image tags or unchecked release URLs;
- accept a workspace-provided trust root;
- recursively `chmod` the workspace;
- add the operator to the Docker group automatically;
- expose the Docker socket to MCP, executor, or project jobs;
- configure a specific MCP client;
- silently install packages or elevate privileges in non-interactive mode;
- require a source checkout or development toolchain.

Lifecycle mutation remains owned by `loki host`.

## Release engineering

The canonical bootstrap builder lives at `tools/release/bootstrapbuild`. It is release-engineering tooling, not an install-host dependency. It embeds only the authenticated metadata URL and initial trusted TUF root into the standalone bootstrap binary.

Example:

```sh
go run ./tools/release/bootstrapbuild \
  --output /absolute/output/loki-bootstrap \
  --metadata-url https://release-metadata.example.invalid/loki/ \
  --trusted-root /absolute/path/to/root.json
```

The placeholder metadata URL above is intentionally not a public Loki install endpoint.

After A14 has accepted the candidate bundle, `tools/release/publishprep` converts that exact evidence into the public GitHub Release asset set and the release-bound installer. The callable `.github/workflows/release.yml` publishes those assets through `releaseway/actions` and only then deploys the same installer bytes to the Loki project Pages site. The workflow does not create tags, choose versions, build a replacement candidate, or sign TUF metadata.

## Public installer gate

The stable shell frontend reserved by the distribution plan remains unavailable until A14 release acceptance passes with no blocking findings or required skips and the release publication workflow has successfully deployed Pages. Until that gate closes, documentation must not present the public one-line shell command as a live installation path.
