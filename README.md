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

Choose browser, full tools, or management only. Browser setup can target an
existing WSL Ubuntu distribution or native Windows. The installer verifies exact
release archives, installs selected tools, checks them and adds the Codex MCP
connection while preserving your settings. Reopen your project after setup.

### Linux and macOS

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The native installer defaults to browser tools and the current workspace.
Ubuntu 24.04 browser prerequisites are prepared with sudo. Full tools require an
accessible Docker Engine on Linux/WSL. Existing 0.1 installations use their own
lifecycle and must be removed before using the same command directory.

See [installation and tool selection](docs/tools/usage.md) for supported hosts,
manual selection, remote execution and remaining acceptance gates.
