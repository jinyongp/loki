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

Fresh setup starts with **Choose execution host**. After choosing a target, the
setup home shows the host and offers **Manage tools**, **Client connections**,
**Check readiness**, and **Change execution host**. Existing host selections are
reused. An unreachable host can be changed from the home screen.

The host chooser lists saved hosts, including the native local host, and marks
the **Loki default**. **Set up another execution host** opens a separate menu:
on Windows, choose an existing WSL distribution, a dedicated `loki-tools` WSL
distribution, or an SSH Linux host. On Linux, managed local services are also
available. Existing WSL prepares Loki in a distribution you already have;
dedicated WSL creates or reuses Loki's separate distribution. Saved hosts can be
selected directly without repeating preparation. Back returns to the saved-host
chooser. Host selection sets Loki's default; existing Codex connections keep
their original hosts.

Manage tools shows installation, activation and runtime verification separately.
Use ↑/↓ and Enter to choose a tool and an action. Names and states are aligned;
the focused tool's description and runtime details appear above the list. Space
marks missing tools for installation directly in this list; Enter opens the
installation choices for the marked tools. Missing tools offer **Install and enable** or
**Install only**; installed tools offer **Enable** or **Disable**, and **Uninstall**.
Installation alone leaves new tools disabled. Unsupported tools explain their
Linux-host requirement. Full services are reconciled after activation changes;
uninstall can restart remaining enabled services. Native browsers start on demand.
Uninstall confirmation names the tool and target, and retains user data.
Esc goes back or exits. Completed actions are retained on exit. `loki tools manage`
opens the tool menu directly. The target is shown in every management screen.

Enabled full-mode Git offers **Configure commit signing**, which collects the
name and email, reuses the saved identity, and confirms the service restart.
GitHub offers **Configure GitHub access**. Enable these tools before configuring
their integrations. Disabled tools do not offer an action that requires enabled
services.

Opening setup for a saved remote host does not start or prepare that host.
Manage tools prepares its CLI when needed. Check readiness prepares the host,
repairs missing browser dependencies when applicable, and then checks tool runtime.
Client configuration can still be inspected or removed while the host is unavailable.

Interactive setup uses a dedicated terminal screen for each choice. **CHOOSE**
means input is required; **WORKING** shows temporary progress output; **RESULT**
remains visible until Continue or Esc. Enter chooses an action or confirms the current
selection. Connection setup confirms saved settings separately from tool runtime
readiness. Readiness results show concise causes and keep full diagnostics under
**Details**, with next/previous pages for long logs. Narrow screens shorten the
summary while keeping the complete output in Details. Leaving a result screen
restores the original terminal. Setup prints the collected result summaries once
on exit, including on Esc, and omits repeated identical results. Direct tool menus
collect their results until that menu exits as well. Progress logs disappear when the action
finishes. Failed actions keep the latest detailed output under Details; long output
states when earlier progress was omitted. Setup exits with a short closing message. Readiness checks
tools on the selected host; it does not test the client app connection.
`loki status` shows configuration without starting tools or
testing connections; `loki doctor` runs a fresh tool readiness check.

Client-connection actions and cancellation return to **Client connections**;
Back leaves that menu. Repeating an existing Codex connection or disconnecting
an absent connection reports **unchanged**. Reload the Loki MCP connection in
Codex after saved connection changes; saving settings does not itself establish
a live client connection. GitHub's installation confirmation reads only its
answer and leaves subsequent menu input untouched.

On Windows, an older Loki MCP connection can keep its renamed CLI executable
locked after an upgrade. Locked, verified backups retain their ownership records
separately from the current install journal and do not block another upgrade or
recovery. After those connections exit, the next manager installation or
`loki tools recover` removes the retained files. Changed or unowned backup bytes
are rejected rather than deleted.

For browser features, choose **Manage tools > browser > Optional features** in
`loki setup`. Navigation and screenshots are included. The multiselect shows
existing selections and describes each feature: **Video recording and diagnostics
[devtools]** provides WebM recording, and **PDF export [pdf]** saves PDFs. Space
toggles, Enter saves, and Esc cancels. Saving preserves browser activation, so
configuring a disabled browser does not enable it. `loki status` shows selected
features; reconnect the client's Loki MCP server after changing the selection.
The upstream name `devtools` here is unrelated to the separate devtools CLI.
Advanced scripts can use `loki tools enable browser --capabilities devtools,pdf`.
The option replaces the whole selection; an empty value clears it, and omitting
the option preserves it. See `loki tools enable --help` for all names and meanings.

With `devtools` selected, `browser_annotate` captures the current page and returns
a one-time annotation URL on the browser's execution host. Open it, draw labelled
boxes or enter feedback, then submit. Call `browser_annotate` again to retrieve
the annotated PNG and page snapshot; `loki_browser_files` can read the PNG inline.
The screen also supports cancellation and expires after five minutes. It uses
the existing browser session, so a system Chrome installation or graphical WSL
display is unnecessary. The URL binds only to runtime loopback: remote SSH or
isolated container deployments need forwarding to that runtime's displayed port.
The screen serves the captured image, not a live interactive view of the page.

DevTools screenshots activate the requested tab before capture, including after
a Lighthouse audit. `resize_page` sets the page viewport; docked DevTools does
not consume the requested width. That size persists when changing other
emulation features and when Lighthouse restores the page after an audit.
Protocol errors return before the outer MCP call deadline. If an MCP request is
canceled or reaches that deadline, Loki stops the affected owned engine and
subsequent calls explain that the client must reconnect. Other engines remain
available. Interrupted actions are never replayed automatically.

Playwright recordings retain actual MCP upload paths and handled dialog outcomes
in JavaScript/TypeScript replay code. A fake upload path is replaced with
`setInputFiles`; ordinary text containing a fake path remains ordinary text.
If a manual upload's real path or a dialog outcome cannot be recovered, the
affected action is omitted with an explicit warning. Check these warnings before
replaying code, and retain its referenced files on the execution host.
`browser_resume` without stepping returns when execution resumes; it does not
wait for another pause. Step and location requests retain their next-pause behavior.

Extension installation/reload waits for the new service worker to start before
returning. An extension action activates the selected MCP page first, ensuring
the callback receives the intended page even when DevTools has focus.

Readable `loki doctor` output summarizes multiline failures and avoids repeating
the same tool failure under Issues. `loki doctor --json` retains original reasons
and issues for diagnosis. A missing shared library is an execution-host runtime
dependency failure; a saved Codex connection does not establish browser readiness.

Choose **Change execution host** to select native execution, a managed Linux/WSL
host, an existing WSL distribution from its actual list, or an SSH destination.
For an existing WSL distribution or SSH host, setup checks access and reuses a
CLI matching the frontend release, or downloads and verifies the Linux manager
and installs it into the selected user's `~/.local/lib/loki/bin`. Setup then continues in the same session;
no separate installation command or PATH change is needed. The distribution
remains user-managed. An inaccessible distribution is reported separately from
a missing CLI. Scripts can use `loki hosts prepare wsl --distribution Ubuntu`.
Remembered hosts are checked again when entering setup or installing tools, so
upgrading the frontend does not leave setup using an older execution-host CLI.

Project-host browser installation and updates automatically prepare missing
Chromium system packages on Ubuntu 22.04+ and Debian 12+, including WSL and SSH
execution hosts. Packages come from the host's configured distribution
repositories; prepared hosts require no additional administrator authentication.
When required, setup reads the administrator password locally with echo disabled
and sends it only through protected stdin. Browser-only setup prepares these
libraries without provisioning Docker or a privileged Loki service. Installation
and activation stay separate.

Opening `loki setup` also repairs missing packages for an already-installed
project-host browser. An interrupted package operation reports its failure and can
be resumed by rerunning setup. Other Linux distributions retain their existing
system libraries and require manual prerequisite preparation. Full-mode browser
dependencies belong to its container image. Project-host installation and updates
verify the installed Chrome with its sandbox enabled, render a local screenshot
and record a short video before reporting success. A failed check retains files
and data for repair and does not enable a fresh installation. **Check readiness**
can repeat the runtime check later.

Before installing full tools, both WSL and SSH prepare the protected Linux
service in the same session. Administrator passwords are read locally with
echo disabled and sent through the selected transport's stdin. A ready service
is reused; its CLI version is checked before skipping administrator preparation.
OpenAI tunnel management is offered for the dedicated `loki-tools` WSL host and
SSH hosts. Existing user-managed WSL distributions use a direct Codex connection.
Each host has its own tool state; switching hosts does not move programs or data.
Installing a tool that needs Linux services asks for a host before preparing it.
Creating the dedicated `loki-tools` WSL host requires confirmation. Scripts must
select or prepare a suitable execution host explicitly before installing full tools.
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
selection using `loki tools connect codex`; use
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

## Upgrades

Run one command to update this computer's CLI, the selected execution-host CLI
and installed tools. Tool selection, activation, capabilities and user data are
retained. Other execution hosts are outside the update unless you choose `--all-hosts`.

```sh
loki upgrade
loki upgrade --check
loki upgrade --yes
loki upgrade --cli-only
loki upgrade --tools-only --yes
```

The command checks the latest stable release and prints one plan: current and
target CLI versions, selected host, changed package versions, runtime replacement
and MCP reconnection needs. Package SHA-256 identities determine changes; a tool
package may be reused across releases. Older host managers without digest metadata
are shown as requiring verification. Their packages are compared after preparing
the host manager, before acquiring replacements.

Enter or EOF cancels; `--yes` / `-y` accepts the displayed plan. `--check` reads
installation metadata and trusted catalogs without installing a CLI, preparing a
host, starting tools or changing configuration. `--cli-only` updates only the
frontend command. `--tools-only` retains that CLI while updating the selected host
and tools, using a verified target-release manager temporarily when needed.
The two scope options are mutually exclusive. A CLI already at the target release
does not prevent updating an older host, tool packages or a pending runtime switch.

Global `--host` options before `upgrade` override the tool target; the frontend CLI
still updates on this computer. Each native manager and release catalog is checked
against the official release SHA-256 manifest. `--version MAJOR.MINOR.PATCH` selects
a stable release, `--force` permits CLI reinstall or downgrade, and `--timeout`
bounds the whole operation (default: 30 minutes). Host preparation can still require
administrator authentication through the existing setup path.

Stopped full services remain stopped. Updating a disabled tool does not restart
unaffected services. Changed packages used by running full services are applied
after active-job checks. An active job blocks replacement; if one starts after
files are staged, the command reports a partial update and retains the running
deployment. The next upgrade compares its package identities against the desired
packages and resumes replacement. Existing client-owned MCP sessions may need
reconnection to use a new CLI or browser engine; the Codex app can remain open.
The command reports this separately from service replacement.

If the frontend succeeds but host/tool work fails, output identifies the partial
result and gives a resume command:

```sh
loki upgrade --tools-only --version VERSION --yes
```

`--json` emits one result, including the plan, completed changes, runtime and
reconnection state. Progress and prompts stay on stderr. `loki tools update`
remains a targeted management command; ordinary updates need only `loki upgrade`.
Older releases that predate integrated upgrades need the public installer once
to obtain this command flow.

## Project-host browser

Install the native management-only bundle using its included `install.sh` or
`install.ps1`. Then select project-host mode on the execution host:

```sh
loki setup browser --connect codex
loki doctor
loki tools serve --engine both
```

Use `playwright`, `devtools`, or `both` for the engine. The two engines have
separate profiles and tabs. `tools serve` reserves stdout for MCP protocol;
startup progress and public errors go to stderr. The catalog's archive URLs,
sizes and checksums are independently trusted release inputs.

Client setup registers a host-bound MCP entry for the execution host's installed,
enabled tools. The confirmation lists those groups; it does not request a project
directory. Project-host currently supports browser; full mode supports all groups.
The launcher selects the backend from the host's current configuration.

The saved execution host is the default for Loki management commands. Change it
through setup's **Change execution host**, or override one command with `--host`.
Every Codex connection pins its execution host, including local connections.
Changing the default keeps existing client connections on their original host;
choose **Connect Codex** to add a connection for the newly selected host.
Tools and user data stay on each host. Existing local entries created by an older
CLI acquire explicit host pinning when **Connect Codex** is run again.

Without `--workspace`, project-host browser files use a private empty temporary
directory, removed when the connection ends. Screenshots and downloads use the
browser's owned results directories. To grant access to a project's files, use
`loki connections setup codex --workspace /absolute/path/to/project`. The path
must exist on the execution host. Reconnect the client after changing it.

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
mode uses a private browser workspace; project-host mode uses a temporary empty
directory or an explicitly granted local project path.

Public sharing still requires an artifact or preview origin. The current native
full setup does not configure that public origin; enabling `sharing` alone does
not make publishing available. Doctor reports this configuration gap instead of
reporting the group ready merely because its private services are running.

Enable/disable applies the selected full service layouts automatically. Use
`--no-start` to save activation separately, then `loki tools start` to apply it.
Active jobs block service reconfiguration and tool removal; let them finish and
retry. If activation was saved but services could not apply it, the error names
the recovery command. Reconnect MCP clients after changing installation or selection.
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
loki tools uninstall browser
loki tools uninstall --all
loki tools uninstall --all --purge-data
loki hosts remove NAME --purge
```

Backup pauses owned services and resumes them after copying selected programs,
configuration, credentials and persistent tool data. Active jobs block maintenance.
Restore first creates a safety backup; an interrupted restore resumes with the
same ID. Backups belong to the same management root and execution host.
Rollback selects the previous complete installation when its generations remain
available; it retains current data. `tools uninstall TOOL` removes one tool, while
`tools uninstall --all` removes all tool programs on the selected execution host.
Bare `tools uninstall` refuses to make changes and asks for a target. These commands retain
the CLI, credentials, backups and data. It does not remove the Docker engine,
WSL distribution or unrelated resources. `--purge-data` also removes tool-owned
data and credentials while keeping backups. `hosts remove NAME` detaches a registration;
`hosts remove NAME --purge` destroys this installation's owned WSL distribution or
per-user Linux system host, including its data and host-side backups. The shared
Docker/WSL prerequisites and frontend CLI remain installed. External SSH and
user-managed WSL hosts are preserved.

`loki status` reports the native CLI version and selected execution host, public
tools and frontend client configuration. It is passive: **enabled** is desired
activation, while **not checked** means runtime readiness has not been probed.
`loki doctor` performs those checks. Both commands include useful next actions;
`--json` keeps one structured stdout result with execution-host metadata.

Client configuration is managed on the computer running the CLI. Use
`loki connections list` to inspect Codex configuration and local tunnel metadata,
or `loki connections status openai` to check a tunnel's runtime health. Setup's
client menu guides Codex connection and disconnection, and Windows managed
tunnels. `loki connections remove codex` removes only Loki-marked blocks;
installed tools, data and other client entries remain. Changing execution hosts
keeps existing client targets until you reconnect them.

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

### Minimal Linux browser acceptance

Maintainers can run the complete browser lifecycle on disposable minimal hosts:

```sh
python3 scripts/maintainer/tools/accept_browser_environment.py \
  --candidate /absolute/path/to/project-host-candidate \
  --image ubuntu:22.04 --image ubuntu:24.04 \
  --image debian:12-slim --image debian:13-slim \
  --output /absolute/path/to/new-evidence-directory
```

This requires a Docker daemon and a native Linux candidate. It installs only
sudo as a host fixture, then exercises the public CLI with real administrator
authentication and package installation. Checks cover runtime startup, damaged
executables, both engines through the generated Codex connection, screenshots,
file transfer, disable/re-enable, process cleanup, update, disconnect, removal
and reinstallation with user data retained. Logs and exact image and binary
digests remain in the evidence directory. The release workflow runs these checks
on Linux amd64. Containers permit user namespaces without disabling Chrome’s
sandbox. Windows-to-WSL transport and desktop rendering remain separate checks.

## Multiple execution hosts

Each frontend has a registry of named hosts. Each host has its own tools, data
and credentials; the CLI default only chooses where ordinary commands execute.

```sh
loki hosts add ubuntu --type wsl --distribution Ubuntu
loki hosts add build --type ssh --address user@server
loki hosts prepare ubuntu
loki hosts use ubuntu
loki hosts list
loki --host build status
loki status --all-hosts
loki doctor --all-hosts
```

Registration alone makes no host changes. `hosts prepare NAME` prepares that
host without changing the default. `hosts use NAME` selects a registered host
without requiring network access. The guided host picker lists registered hosts
alongside options for adding other execution environments.

Connect each host independently:

```sh
loki --host ubuntu connections setup codex
loki --host build connections setup codex
loki connections list
loki --host build connections remove codex
```

Codex connections bind to immutable host IDs. Renaming a host or switching the
CLI default preserves existing targets. Reconnecting a host reuses its managed
entry. `--name NAME` creates a separately named Codex connection. Removal selects
this host's entries by default; `connections remove codex --all` explicitly removes
all Loki-managed entries. User-managed MCP entries are retained.

Existing single-host selections are imported without moving tool state. Existing
Codex entries retain their transport arguments until explicitly reconnected.
Windows OpenAI tunnels have separate host bindings, bridges, credentials and
restoration tasks. Their restoration does not depend on the CLI default.

```sh
loki --host build upgrade --check
loki upgrade --all-hosts --check
loki upgrade --all-hosts --yes
loki hosts rename build builder
loki hosts remove builder
```

An all-host upgrade updates this frontend CLI once, then handles registered hosts
individually. An unreachable or failed host is reported alongside completed hosts;
retry the all-host operation or select the failed host explicitly. Status also
returns partial results and a failure exit code when a host cannot be inspected.

A host can be detached after its connections have been removed. Detachment keeps
the host and data. `hosts remove NAME --purge` deletes only verified Loki-owned
infrastructure; external SSH/WSL hosts and shared infrastructure are retained.
The native frontend host cannot be removed.

When using a custom management root during installation, the installer prints the
next setup command with that same root. Keep its `--root` option for subsequent
commands. `doctor` with no enabled tools reports that no tools are enabled for
client use; a healthy installation alone does not grant tool access.
`status --all-hosts` and `doctor --all-hosts` show each host's installed/enabled tools
and runtime state. A reachable host with degraded tools is reported as needing
attention, separately from an unavailable host. Repeat a diagnostic command with
`--json` and the same host/root options to read its full report.
