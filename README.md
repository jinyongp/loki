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

A client running on the same machine can use the local origin directly if it
supports Streamable HTTP and bearer authentication.

**ChatGPT cannot connect directly to a localhost MCP server.** The simplest
private path is OpenAI Secure MCP Tunnel, which keeps Loki on loopback and uses
an outbound tunnel instead of exposing Loki publicly.

See [Connect an MCP client](docs/connect-mcp-client.md) for ChatGPT, local
clients, and other remote-access options.

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

- [Connect an MCP client](docs/connect-mcp-client.md) — local clients, ChatGPT,
  Secure MCP Tunnel, and remote access.
- [First install](docs/first-install.md) — installer options, recovery,
  troubleshooting, and updates.
- [Self-hosting](docs/self-hosting.md) — source-tree and maintainer deployment
  paths.
- [GitHub App integration](docs/github-app.md) — optional GitHub integration.

Architecture, migration, validation, and release-engineering records are under
[docs](docs/) and are not required for normal installation.

## License

Apache License 2.0. See [LICENSE](LICENSE).
