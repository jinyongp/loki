# Installing and selecting tools

Loki 0.2 installs a native management command first. Product tools are selected
afterward and remain disabled until explicitly enabled. The execution host
determines tool support and workspace paths. A Windows desktop can connect to
an existing Linux execution host through WSL or SSH.

Install the stable [0.2.2 release](https://github.com/jinyongp/loki/releases/tag/v0.2.2)
using one command:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer verifies and installs only the native CLI, then completes without
selection prompts. A fresh installation has an empty tool set. Configure the
execution host and add tool groups afterward with `loki tools` as shown below.
Use the same CLI installer inside WSL or on an SSH execution host. Windows
supports explicit `-BinDirectory` and `-ManagementRoot` paths; Linux/macOS
support `--bin-dir` and `--root`. `-SourceDirectory` / `--source-dir` supply an
offline manager archive. Tool catalogs and MCP client configuration belong to
the later configuration steps.

For manual/offline installation, download the native management ZIP and matching
mode catalog. `SHA256SUMS` covers the published files. Reconnect an installed
selection using `loki tools connect --workspace ABSOLUTE-PROJECT codex`; use
`--remote` on a Codex SSH execution host. The default config honors `CODEX_HOME`
and otherwise uses `~/.codex/config.toml`. User-owned conflicting server entries
are left intact and produce an actionable error.

Receipt-bound 0.2 release candidates passed native acceptance before publication.
See [the final acceptance report](final-acceptance.md) for current results and
remaining gates.

For a prepared offline candidate, append `--archives ABSOLUTE_DIRECTORY` to
`tools install` or `tools update`. The directory contains `<sha256>.zip` files
from the trusted catalog. The manager verifies their complete length/digest in
owned staging before extraction; local files do not supply their own authority.
Candidate preparation emits this directory as `release/archives`.

## Project-host browser

Install the native management-only bundle using its included `install.sh` or
`install.ps1`. Then select project-host mode on the execution host:

```sh
loki tools configure --mode project-host
loki tools install --catalog /absolute/path/to/trusted-catalog.json browser
loki tools enable browser
loki doctor
loki tools serve browser --workspace /absolute/path/to/project --engine both
```

Use `playwright`, `devtools`, or `both` for the engine. The two engines have
separate profiles and tabs. `tools serve` reserves stdout for MCP protocol;
startup progress and public errors go to stderr. The catalog's archive URLs,
sizes and checksums are independently trusted release inputs.

On a Windows management host, target an existing WSL distribution or SSH host:

```powershell
loki --host wsl --distribution Ubuntu tools serve browser --workspace /home/user/project --engine both
loki --host ssh --address user@host tools serve browser --workspace /home/user/project --engine both
```

The remote host must already have its native manager and selected browser
bundle installed and enabled. The portable relay invokes that manager; it
preserves stderr and exit status. A desktop application's SSH project does not
by itself certify where that application's MCP processes execute. Actual
Windows Codex desktop-to-SSH-to-WSL connection remains a final acceptance gate.

## Selected full tools

Full mode runs on a Linux execution host with its declared Docker prerequisites.
Choose only the public groups needed for the project:

```sh
loki tools configure --mode full
loki tools install --catalog /absolute/path/to/trusted-full-catalog.json workspace git browser
loki tools enable workspace
loki tools enable git
loki tools enable browser
loki tools start
loki doctor
loki tools serve
```

Installation resolves private prerequisites from the exact release catalog.
For example, Git's private executor does not expose the execution job tools.
Add and enable `execution`, `github`, `secrets`, `sharing` or `coordination` as
needed. Installed disabled groups start no selected services. Browser-only full
mode uses a private browser workspace; project-host mode uses the explicit local
project path.

Changes to the enabled full selection require `loki tools start` to reconcile
service layouts. Reconnect MCP clients after changing installation or selection.
`loki tools stop` stops the owned deployment and retains its tool data.

## Git and GitHub setup

Git signing belongs to the Git integration:

```sh
loki integrations setup git --identity-name 'Your Name' --identity-email you@example.org
loki integrations status git
loki integrations doctor git
```

Setup creates or reuses the protected signing key. Follow its returned public
key guidance to register that signing key in the provider account. Import options
take private key files or stdin; private keys stay out of command arguments.

GitHub App setup uses its own provider store:

```sh
loki integrations setup github
loki integrations status github
loki integrations doctor github
loki integrations refresh github
```

Default setup registers or reuses an App, opens its installation settings and
discovers approved installations. With WSL/SSH host selection, default setup opens the browser and
serves the loopback callback on the command's frontend host. The one-time code
is relayed through protected stdin; API calls and durable keys remain on the
execution host. Imported file-based configuration is read on the execution host.
Repository-linked Projects use installation
authorization, including repositories under personal accounts. Personal Projects
is an optional capability and requires explicit `--personal-projects` setup and
the App's Device Flow setting. Ordinary repository access does not request this
user authorization. `refresh` invalidates installation permission caches through
the managed runtime.

If organization Projects is disabled, the returned guidance links its organization
settings. Enabling that feature requires organization administration authorization;
the normal tool flow reports the prerequisite instead of silently changing it.

## Updates and retained resources

```sh
loki tools update --catalog /absolute/path/to/trusted-new-release-catalog.json
loki tools disable browser
loki tools prune browser --keep 1
loki tools remove browser
loki tools recover
```

Update acquires one complete release for all installed tools before publishing
active pointers. Enablement and data persist. Disconnect sessions before removing
their programs. Pruning retains current and leased generations, plus the requested
number of inactive generations. Tool removal retains data owned by that tool.
Recovery repairs owned interrupted operations and reports unresolved ownership.

## Local plugin packages

`plugins/loki-browser` contains the browser-only instructions and presentation;
`plugins/loki-tools` contains instructions for the selected full tool groups.
Materialize an explicit connection without embedding credentials:

```sh
python3 scripts/maintainer/tools/prepare_plugin.py --mode project-host \
  --workspace /home/user/project --output /tmp/loki-browser
python3 scripts/maintainer/tools/prepare_plugin.py --mode full \
  --output /tmp/loki-tools
```

For a plugin process running on Windows, provide `--host wsl --distribution Ubuntu`
or `--host ssh --address user@host`. `--command` chooses the management executable
on the process host; `--management-root` selects its execution-host state namespace.
The generated package includes portable `plugin.json`/`mcp.json` and a synchronized
Codex local-loader manifest. Install through the target host's supported local
plugin flow after candidate and schema acceptance. Preparation does not install
the plugin into the user's app or modify its marketplace.

The browser plugin identifies a **Loki Project Browser** with separate profiles
and login state. It is used when selected by the user. Its instructions preserve
the identity of requests for the desktop application's in-app browser. Native
startup, harmless calls, file/image return and the actual desktop SSH connection
are final acceptance checks.
