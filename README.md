# Loki

Loki is a local MCP server for AI-assisted development. It gives an MCP client
controlled access to a development workspace for files, Git, isolated jobs,
previews, artifacts, and optional integrations without giving project code the
host's Docker socket or machine credentials.

## Install

### Windows

Run in PowerShell:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

Loki installs as a preconfigured WSL2 appliance. You do not need to install
Ubuntu, Docker, Compose, or systemd manually.

### Ubuntu

On Ubuntu 24.04 amd64:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer verifies the release, prepares the workspace, and handles required
host prerequisites with explicit approval.

## Connect an MCP client

Loki exposes a **local Streamable HTTP MCP origin** protected by a bearer token.

On Windows, the installer writes connection information here:

```text
%LOCALAPPDATA%\Loki\<distribution-name>\
├── connection.json
├── mcp-token
└── ownership.json
```

Inspect the non-secret connection metadata with:

```powershell
Get-Content "$env:LOCALAPPDATA\Loki\loki-mcp\connection.json" -Raw
```

On Linux:

```sh
loki host connection
```

A client that can reach Loki's loopback origin can connect directly if it
supports Streamable HTTP and bearer authentication.

A client running somewhere else cannot use the machine's `127.0.0.1` address
directly. In that case, keep Loki on loopback and choose an ingress appropriate
for the client: a client-specific tunnel or agent, a private VPN, an SSH or
application tunnel, or a reverse proxy. Loki does not choose or provision that
external ingress for you.

See [Connect an MCP client](docs/connect-mcp-client.md) for the general
connection model and provider-specific examples.

## Verify

Windows:

```powershell
wsl -d loki-mcp --user root -- /usr/local/bin/loki host status --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host doctor --system
```

Linux:

```sh
loki host status
loki host doctor
```

## What Loki provides

Once connected, an MCP client can work through Loki to:

- read and edit files inside the selected workspace;
- inspect and modify Git state;
- run isolated development commands and jobs;
- publish previews and collect artifacts;
- use encrypted application secrets without exposing their values through MCP;
- use optional browser, GitHub, and signing integrations when enabled.

Loki keeps host management separate from project execution. Normal project jobs
do not receive the raw Docker socket, host-management state, or platform
credentials.

## Documentation

### User guides

- [First install](docs/first-install.md) — installation, recovery,
  troubleshooting, and updates.
- [Connect an MCP client](docs/connect-mcp-client.md) — direct and remote
  clients, ingress choices, and provider-specific examples.
- [GitHub App integration](docs/github-app.md) — optional GitHub integration.

### Maintainer and developer docs

- [Self-hosting](docs/self-hosting.md) — source-tree and maintainer deployment
  paths.
- Architecture, migration, validation, and release-engineering records are under
  [docs](docs/).

## License

Apache License 2.0. See [LICENSE](LICENSE).
