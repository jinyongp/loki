# Loki

Loki is a local MCP server for AI-assisted development. It gives an MCP client
controlled access to a workspace for file editing, Git, isolated development
jobs, previews, artifacts, and optional browser or Git-signing workflows without
giving project code the host's Docker socket or machine credentials.

## Install on Ubuntu or WSL2

The public installer is live. Run this command **inside the Ubuntu shell**:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

That is the normal installation path. You do not need a Loki source checkout,
Go, Node.js, pnpm, Python, Rust, Chromium, Docker Compose files, or other
development tooling beforehand.

The current host targets are:

- Ubuntu 24.04 on amd64. The release pipeline verifies a source-free install on
  this host.
- WSL2 with Ubuntu 24.04 on amd64. The installer has WSL-specific detection and
  prerequisite guidance; clean-host WSL acceptance is still being completed.

The installer downloads a bootstrap bound to one immutable release, verifies its
SHA-256, verifies the matching host binary, then starts the host installation.
It asks for the workspace directory and shows any privileged Docker or
filesystem changes before asking for approval.

Use an absolute workspace path when prompted, for example:

```text
/home/alice/workspace
```

If the directory does not exist, Loki can create it after showing the command it
will run.

### WSL2 and Docker

You do not need to install Docker manually before trying Loki. If a compatible
Docker daemon is already available, Loki uses it. If Loki needs to install
Docker Engine inside WSL2, WSL systemd must be enabled.

If the installer reports that WSL2 systemd is disabled, add this to
`/etc/wsl.conf`:

```ini
[boot]
systemd=true
```

Then run the following from Windows PowerShell or Command Prompt and reopen the
Ubuntu shell:

```powershell
wsl.exe --shutdown
```

Run the installer again after WSL restarts.

## Verify the installation

A normal user-scoped install places the CLI at `~/.local/bin/loki`. If your
shell does not find `loki` immediately, run:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Then verify the installed release and runtime:

```sh
loki --version
loki host status
loki host doctor
```

`host status` reports the installed generation and service state.
`host doctor` checks the host/runtime prerequisites and reports actionable
failures.

## Connect an MCP client

Once the runtime is healthy, run:

```sh
loki host connection
```

This prints the MCP endpoint, transport, authentication mode, and token-file
path needed to configure an MCP client. The secret token value is not printed to
the terminal.

The installed MCP endpoint is loopback-only by default. Keep it local unless you
deliberately add a trusted transport in front of it.

## What Loki can do

Once connected, an assistant can:

- read and edit files inside the selected workspace;
- inspect Git status, diffs, history, and stage precise changes;
- run finite commands and longer-lived development jobs in isolated runtime
  resources;
- inspect job state and output, and explicitly cancel jobs;
- publish local previews and collect artifacts;
- use encrypted application secrets without returning their values through MCP;
- use optional browser automation and isolated Git signing when enabled.

Optional components are not required for the base installation.

## Updates and recovery

Loki does not silently update itself. Host lifecycle changes are explicit:

```sh
loki host update status
loki host update prepare
loki host update apply

loki host backup
loki host rollback
loki host restore BACKUP_ID
```

Install, update, rollback, restore, and uninstall preserve the selected workspace
by default.

## Unattended installation

For automation, pass the approvals explicitly through the public installer:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh -s -- \
  --workspace /srv/workspace \
  --create-workspace \
  --prepare-workspace \
  --install-prerequisites \
  --allow-sudo-workspace \
  --allow-sudo-docker
```

Missing required input or approval fails instead of silently changing the host.
See [First install](docs/first-install.md) for the full installation behavior,
system-scoped installation, and troubleshooting.

## Safety

Loki separates host management from project execution. Normal MCP/project jobs
do not receive the raw Docker socket, host-management state, or platform
credentials. The installer does not silently add the current user to the
`docker` group and does not recursively change workspace permissions.

When a host mutation needs elevated privileges, Loki shows the action and
requires approval.

## Documentation

- [First install](docs/first-install.md) — Ubuntu/WSL installation, verification,
  non-interactive options, and troubleshooting.
- [Self-hosting](docs/self-hosting.md) — source-tree, Compose, and
  maintainer-oriented deployment paths.
- [GitHub App integration](docs/github-app.md) — optional GitHub integration.

Developer, architecture, validation, and release-engineering records are under
[docs](docs/) and are not required for normal installation.

## License

Loki is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
