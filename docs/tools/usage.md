# Installing and selecting tools

Loki 0.2 installs an empty native CLI first. Run `loki setup` afterward to choose
individual tools and prepare them. Setup installs, enables and starts the chosen
full services; `tools install` keeps activation separate for manual workflows.
Guided setup and the restored operator commands require 0.2.6 or newer.
Earlier 0.2 releases use the manual selection path; run `loki upgrade` first.

Management commands print readable text by default. Add `--json` before or
after the command for one structured result, for example `loki status --json`,
`loki --json doctor` or `loki tools plan --json`. This also applies to commands
that change configuration, integration setup and CLI upgrades. Progress and
interactive prompts use stderr in JSON mode. Help stays readable; `tools serve`
uses its MCP protocol stream and rejects `--json`. Internal host relay messages
retain their protocol encoding.

Install the stable [release](https://github.com/jinyongp/loki/releases/latest)
using one command:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer verifies and installs only the native CLI, then completes without
selection prompts. A fresh installation has an empty tool set. Run:

```sh
loki setup
```

The next release replaces interactive setup with a tool management menu.
Each tool shows installation and activation separately. Use ↑/↓ and Enter to
choose a tool, then choose an action. Missing tools offer **Install and enable**
or **Install only**; installed tools offer **Enable** or **Disable**, and
**Uninstall**. Installation alone leaves a new tool disabled. Uninstall asks for
confirmation and retains user data. Full services are reconciled after activation
changes; uninstall can restart full services. The list refreshes after each action.
Esc goes back, or exits from the tool list. Completed actions are retained on exit.
The menu uses the selected execution host, including remembered Windows WSL hosts.
Scripts use explicit commands rather than the interactive menu:

```sh
loki setup workspace git browser
loki tools install github
loki tools enable github
loki tools disable github
loki tools uninstall github
loki connections setup codex
loki status
loki doctor
```

Windows full setup prepares a dedicated, owned `loki-tools` WSL distribution,
installs its Linux manager and enables a hidden keepalive task. Existing
distributions are preserved. Linux full setup asks for administrator access once
to install the required Docker engine and an owned systemd socket service.
Subsequent commands use the remembered host; users do not join the Docker group.
macOS can use its native browser or select a Linux SSH host for full tools:

```sh
loki hosts prepare ssh --address user@host
loki setup workspace git
```

Linux automatic preparation supports Ubuntu/Debian with systemd. A fresh host
uses [Docker's official stable package repository](https://docs.docker.com/engine/install/ubuntu/)
with a pinned signing-key digest. Existing engines are checked for Loki's required
API before use. SSH preparation requires existing SSH access; when needed, setup
asks for the host administrator password with terminal echo disabled and uses
it once over protected SSH stdin.
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

Earlier receipt-bound 0.2 release candidates passed native acceptance before publication.
See [the final acceptance report](final-acceptance.md) for current results and
remaining gates.

For a prepared offline candidate, append `--archives ABSOLUTE_DIRECTORY` to
`tools install` or `tools update`. The directory contains `<sha256>.zip` files
from the trusted catalog. The manager verifies their complete length/digest in
owned staging before extraction; local files do not supply their own authority.
Candidate preparation emits this directory as `release/archives`. Use the global
`--host local` override for offline candidate installation directly on a prepared
Linux host. The administrator socket accepts official release acquisition and
refuses caller-supplied catalogs and archives.

## CLI upgrades

CLI installations from 0.2.2 or earlier need the installer above once to obtain
`loki upgrade`. Subsequent CLI releases can be installed with this command.

```sh
loki upgrade
loki upgrade --check
loki upgrade --version 0.2.3 --force --yes
```

The command shows current and target versions, then asks for confirmation.
Enter or EOF cancels; `--yes` / `-y` skips confirmation. `--check` only reports
versions. `--version MAJOR.MINOR.PATCH` chooses a published stable release;
without it, the latest stable release is selected. Reinstalling or downgrading
requires `--force`. `--timeout 5m` bounds release lookup, downloads and installation.

The official native manager archive is verified against its release SHA-256
checksum before its version is checked and the current manager publishes the
CLI into the current command directory. Existing ownership checks apply.
Installed tools and their configuration remain separate from CLI upgrades.
Use global `--host wsl` or `--host ssh` options before `upgrade` to target that
execution host rather than the local command.

## Project-host browser

Install the native management-only bundle using its included `install.sh` or
`install.ps1`. Then select project-host mode on the execution host:

```sh
loki setup browser --connect codex --workspace /absolute/path/to/project
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
loki setup workspace git browser
loki connections setup codex
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
execution host. Imported file-based configuration and keys are read on the
command frontend, then sent through protected stdin to the selected host.
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
loki tools update
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

## Connections and maintenance

`loki connections setup codex` writes the client configuration on the frontend
and binds it to the selected execution host. Existing unrelated settings remain
intact. In Windows, managed OpenAI tunnels also support setup, start, stop,
status, doctor and removal:

```powershell
loki connections setup openai --tunnel-id YOUR-TUNNEL-ID
loki connections status openai
loki connections stop openai
loki connections start openai
```

The runtime key uses hidden terminal input and Windows Credential Manager.
An owned GUI logon companion restores enabled connections without a terminal
window. Stop disables restoration and stops the owned loopback MCP bridge.

```sh
loki backup
loki backups list
loki restore BACKUP-ID
loki rollback
loki uninstall
loki uninstall --purge-data
loki hosts remove --purge
```

Backup pauses owned services and resumes them after copying selected programs,
configuration, credentials and persistent tool data. Active jobs block maintenance.
Restore first creates a safety backup; an interrupted restore resumes with the
same ID. Backups belong to the same management root and execution host.
Rollback selects the previous complete installation when its generations remain
available; it retains current data. Uninstall removes tool programs and retains
the CLI, credentials, backups and data. It does not remove the Docker engine,
WSL distribution or unrelated resources. `--purge-data` also removes tool-owned
data and credentials while keeping backups. `hosts remove` detaches a selection;
`hosts remove --purge` destroys this installation's owned WSL distribution or
per-user Linux system host, including its data and host-side backups. The shared
Docker/WSL prerequisites and frontend CLI remain installed. External SSH and
user-managed WSL hosts are preserved.

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
