# Loki

Loki is a local MCP server for AI-assisted development. It gives an MCP client
controlled access to a development workspace for files, Git, isolated jobs,
previews, artifacts, and optional integrations without giving project code the
host's Docker socket or machine credentials.

## Install

### Windows

Run this in PowerShell:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

The Windows installer downloads and verifies one immutable Loki WSL2 appliance.
The appliance already contains its Ubuntu userspace, systemd configuration,
Docker Engine and Compose, Loki host/runtime components, and the initial
workspace. You do not create an Ubuntu user, install Docker, edit
`/etc/wsl.conf`, or configure Task Scheduler yourself.

The default WSL distribution name is `loki-mcp`. To choose a different local
name:

```powershell
$env:LOKI_WSL_NAME = "my-loki"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

To choose where Windows stores the distribution:

```powershell
$env:LOKI_WSL_NAME = "my-loki"
$env:LOKI_WSL_LOCATION = "D:\WSL\my-loki"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

Windows logon startup is enabled by default. Set
`LOKI_WSL_AUTOSTART=0` before installation to disable it.

The Windows installer requires a current WSL with custom `.wsl` distribution
support. It does not update WSL automatically. If your installed WSL is too old,
the installer stops and tells you to run `wsl --update` yourself.

### Ubuntu Linux

On a supported Ubuntu host, run:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The Linux installer is also bound to one immutable release. It verifies the
release bootstrap and host binary, then handles Docker prerequisites, workspace
onboarding, and Loki runtime installation with explicit approval for privileged
host changes.

The current native Linux target is Ubuntu 24.04 amd64.

## Verify the installation

On Linux:

```sh
loki --version
loki host status
loki host doctor
loki host connection
```

A user-scoped Linux install places the CLI at `~/.local/bin/loki`. Add that
directory to `PATH` if needed.

On Windows, the installer writes MCP connection material below:

```text
%LOCALAPPDATA%\Loki\<distribution-name>\
```

`connection.json` contains the endpoint and the path to the private token
file. The token value is not printed to the terminal. The installer restricts
the Windows token file to the current Windows user and SYSTEM.

The WSL appliance uses `/home/ubuntu/workspace` as its initial Loki workspace.
The internal `ubuntu` user is precreated with UID 1000 and is not given Docker
group membership.

## What Loki can do

Once connected, an assistant can:

- read and edit files inside the selected workspace;
- inspect Git status, diffs, history, and stage precise changes;
- run finite commands and longer-lived development jobs in isolated runtime
  resources;
- inspect job state and output, and explicitly cancel jobs;
- publish local previews and collect artifacts;
- use encrypted application secrets without returning their values through MCP;
- use optional browser automation and isolated Git signing when enabled.

Optional components are not required for the base installation.

## Updates and recovery

Loki does not silently update itself. Host lifecycle changes are explicit:

```sh
loki host update status
loki host update prepare
loki host update apply

loki host backup
loki host rollback
loki host restore BACKUP_ID
```

The Windows appliance uses the same Loki host lifecycle internally, but it is
installed in system scope. From PowerShell, perform appliance maintenance
through the explicit WSL root boundary (substitute the distribution name if
you changed it):

```powershell
wsl -d loki-mcp --user root -- /usr/local/bin/loki host update status --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host update prepare --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host update apply --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host backup --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host rollback --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host restore --system BACKUP_ID
```

WSL is the Windows packaging and boot boundary, not a separate implementation
of Loki updates or rollback.

## Linux unattended installation

For Linux automation, pass every permitted host mutation explicitly:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh -s -- \
  --workspace /srv/workspace \
  --create-workspace \
  --prepare-workspace \
  --install-prerequisites \
  --allow-sudo-workspace \
  --allow-sudo-docker
```

Missing required input or approval fails instead of silently changing the host.

## Safety

Loki separates host management from project execution. Normal MCP/project jobs
do not receive the raw Docker socket, host-management state, or platform
credentials.

The Windows appliance does not put its default human user in the Docker group.
The Linux installer does not silently add the current user to that group either.
Runtime secrets are generated after installation; they are not embedded in the
public WSL appliance.

## Documentation

- [First install](docs/first-install.md) — Windows appliance and native Linux
  installation, verification, overrides, and troubleshooting.
- [Self-hosting](docs/self-hosting.md) — source-tree, Compose, and
  maintainer-oriented deployment paths.
- [GitHub App integration](docs/github-app.md) — optional GitHub integration.

Developer, architecture, validation, and release-engineering records are under
[docs](docs/) and are not required for normal installation.

## License

Loki is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
