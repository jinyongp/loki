# First install

Windows PowerShell:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

Linux/macOS:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer selects native archives for your CPU, verifies their release-bound
SHA-256, installs the manager and your chosen tools, checks their health, then
adds its Codex MCP connection while preserving unrelated settings. Reopen your
project after setup. Choose management only to skip tools and MCP configuration.

Windows prompts for WSL or native execution. WSL uses an existing Ubuntu
distribution and an absolute Linux project path. Native Windows browser supports
amd64. Ubuntu 24.04 browser setup installs declared host libraries and an
AppArmor exception for the exact installed Chrome path when required. It does
not disable Chrome sandboxing. Full tools require an accessible Docker Engine
on Linux/WSL.

See [tool installation and connection](tools/usage.md) for manual controls and
current platform support. Actual desktop SSH image display remains a user-side
acceptance check.
