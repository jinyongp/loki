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
Then run `loki setup`. Choose individual tool groups; Loki downloads verified
releases, prepares their execution host and starts the selected services.

### Linux and macOS

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer installs only the native Loki CLI. Run `loki setup` afterward to
select and prepare tools. Existing 0.1 installations use
their own lifecycle and must be removed before using the same command directory.

See [installation and tool selection](docs/tools/usage.md) for supported hosts,
manual selection, remote execution and remaining acceptance gates.

Browser-only tools can run on Linux/macOS and native Windows where supported.
Other tool groups use a Linux execution host. Windows setup prepares a dedicated
`loki-tools` WSL distribution automatically; Linux setup prepares its managed
Docker Engine service. An existing WSL Ubuntu distribution can be selected,
and an existing Linux host can also be prepared over SSH.
Add the Codex MCP connection with `loki connections setup codex`; browser-only
connections also take `--workspace /absolute/project/path`.

The restored setup is currently source work. The published 0.2.5 CLI does not
contain this journey yet; see [restoration tracking](docs/tools/usability-restoration.md).
