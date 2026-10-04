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

Successful installation shows a short progress line, completion and the next
command:

```text
Installing Loki...
Loki 0.2.1 installed.
Next: loki --help
```

If your shell does not yet include the command directory in PATH, `Next` uses
the installed command's full path. Installation failures show their cause.

After installation, use `loki --help` and `loki tools list` to inspect the CLI.
Configure the execution host, install and enable individual tool groups, then
connect your MCP client using the commands in the tool installation guide.
For WSL or SSH projects, install the native CLI on that execution host as well.

See [tool installation and connection](tools/usage.md) for manual controls and
current platform support. Actual desktop SSH image display remains a user-side
acceptance check.
