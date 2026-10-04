# First install

Windows PowerShell:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

Linux/macOS:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer selects the native CLI archive for your CPU, verifies its
release-bound SHA-256 and installs the management command. Installation completes
without selection prompts. A fresh installation starts with an empty tool set.

After installation, use `loki --help` and `loki tools list` to inspect the CLI.
Configure the execution host, install and enable individual tool groups, then
connect your MCP client using the commands in the tool installation guide.
For WSL or SSH projects, install the native CLI on that execution host as well.

See [tool installation and connection](tools/usage.md) for manual controls and
current platform support. Actual desktop SSH image display remains a user-side
acceptance check.
