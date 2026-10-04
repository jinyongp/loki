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

The installer verifies the native release archive and installs the Loki CLI.
Configure the execution host and add the tool groups you need afterward using
`loki tools`.

### Linux and macOS

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer installs the native Loki CLI. Tool installation, activation and
MCP client configuration are separate steps. Existing 0.1 installations use
their own lifecycle and must be removed before using the same command directory.

See [installation and tool selection](docs/tools/usage.md) for supported hosts,
manual selection, remote execution and remaining acceptance gates.
