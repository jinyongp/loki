# First install

Loki has separate one-line entry points for Windows and native Linux. Windows
uses a release-built WSL2 appliance; native Ubuntu uses the Linux bootstrap.

## Windows

Run this in PowerShell:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

The installer downloads one immutable `loki-wsl-amd64.wsl` artifact from the
accepted Loki release, verifies its exact length and SHA-256, and registers it
with WSL. The appliance is already configured with:

- Ubuntu 24.04 amd64 userspace;
- systemd;
- Docker Engine, Buildx, and Docker Compose;
- the release-bound Loki host binary and release manifest;
- a precreated `ubuntu` user with UID/GID 1000;
- an initial `/home/ubuntu/workspace`;
- a first-boot service that provisions the normal Loki system-scoped host
  lifecycle.

There is no Ubuntu first-run account prompt. You do not choose a Linux username
or password, install Docker, edit `/etc/wsl.conf`, or create a Windows startup
task manually.

Runtime secrets are not embedded in the appliance. The MCP token and Loki
lifecycle state are generated on the installed machine during first boot.

### Windows requirements

The Windows installer requires WSL2 with custom `.wsl` distribution support.
It checks for the WSL `--from-file`, `--name`, and `--no-launch` install
options before making changes.

The installer does **not** run `wsl --update` automatically. If the installed
WSL is too old, it stops with an error and asks you to update WSL manually:

```powershell
wsl --update
```

Rerun the Loki installer after the WSL update succeeds.

### Choose the WSL distribution name

The default local registration name is:

```text
loki-mcp
```

The name is local Windows configuration; Loki does not depend on it. To use a
different name:

```powershell
$env:LOKI_WSL_NAME = "my-loki"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

The installer fails before mutation if that distribution name already exists.

### Choose the WSL storage location

By default WSL chooses the distribution storage location. To choose an explicit
Windows path:

```powershell
$env:LOKI_WSL_NAME = "my-loki"
$env:LOKI_WSL_LOCATION = "D:\WSL\my-loki"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

The requested location must be an absolute path and must not already exist.

### Windows startup behavior

The installer registers a per-user Scheduled Task by default. At Windows logon
the task starts the installed Loki WSL distribution and keeps it alive with a
benign `sleep infinity` process. The task uses an unlimited execution-time
setting so Windows does not stop the keep-alive after the normal Task Scheduler
limit.

Set this before installation if you do not want the startup task:

```powershell
$env:LOKI_WSL_AUTOSTART = "0"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

The appliance itself enables systemd and Docker. Once WSL is running, systemd
starts Docker and Docker restores Loki's managed containers.

### Windows installation completion

The installer waits for first-boot provisioning and does not report success
until both of these checks pass inside the appliance:

```text
loki host status --system
loki host doctor --system
```

On success it writes Windows-side connection material below:

```text
%LOCALAPPDATA%\Loki\<distribution-name>\
├── connection.json
└── mcp-token
```

The token file ACL is restricted to the current Windows user and SYSTEM. Loki
does not print the token value.

The default appliance workspace is:

```text
/home/ubuntu/workspace
```

To inspect the installation directly from PowerShell, substitute your
distribution name if you changed it:

```powershell
wsl -d loki-mcp --user root -- /usr/local/bin/loki host status --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host doctor --system
```

The appliance does not grant the default `ubuntu` user passwordless sudo.
Administrative maintenance uses the explicit WSL root boundary shown above.

### Windows troubleshooting

If the installer says WSL is too old, run `wsl --update` manually. A WSL
update failure is a Windows/WSL prerequisite problem; the Loki installer does
not attempt to repair Windows Installer or Windows optional features.

If a distribution with the requested name already exists, choose a different
name with `LOKI_WSL_NAME`. The installer never unregisters or overwrites an
existing distribution.

If first-boot Loki provisioning fails, the installer reports the recent
`loki-appliance-provision.service` journal. The service is retryable and the
normal Loki lifecycle journal remains authoritative; a failed attempt is not
reported as a successful installation.

Manual creation of a generic Ubuntu WSL distribution is not part of the normal
Loki Windows installation path.

## Ubuntu Linux

On Ubuntu 24.04 amd64:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The public shell installer is generated for one accepted immutable release. It
downloads the release-bound `loki-bootstrap-linux-amd64`, verifies its exact
SHA-256, and executes it. The bootstrap verifies the matching host binary and
hands lifecycle changes to `loki host install`.

You need `curl` to fetch the public installer. If a minimal Ubuntu image does
not have it:

```sh
sudo apt-get update
sudo apt-get install -y curl ca-certificates
```

You do **not** need a Loki source checkout, Go, Node.js, pnpm, Python, Rust,
Chromium, or project development toolchain before installation.

Docker Engine and Docker Compose are runtime prerequisites, but on supported
Ubuntu hosts the installer can offer the approved installation/update commands
when they are missing or incompatible. Privileged mutations are shown before
approval, and Loki does not silently add the operator to the Docker group.

### Linux interactive installation

The installer asks for a clean absolute workspace path when it is not supplied,
for example:

```text
/home/alice/workspace
```

If the directory does not exist, Loki can create it after approval. If the
container runtime needs a minimal POSIX ACL, Loki shows the exact change before
applying it. Existing workspace contents are preserved.

A successful user-scoped installation puts the host CLI at:

```text
~/.local/bin/loki
```

Add it to the current shell path if necessary:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Then verify:

```sh
loki --version
loki host status
loki host doctor
loki host connection
```

`loki host connection` reports the loopback MCP endpoint, transport,
authentication mode, and token-file path without printing the token value.

### Linux non-interactive installation

Automation must explicitly approve every allowed mutation class:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh -s -- \
  --workspace /srv/workspace \
  --create-workspace \
  --prepare-workspace \
  --install-prerequisites \
  --allow-sudo-workspace \
  --allow-sudo-docker
```

Omit approvals not needed by the target host. Missing required input or approval
fails rather than silently changing the host. Add `--json` for a
machine-readable result.

### Linux system-scoped installation

For machine-wide host-management ownership, download the public installer and
run it with `--system`:

```sh
installer=$(mktemp)
curl -fsSL https://jinyongp.dev/loki/install.sh -o "$installer"
chmod 0755 "$installer"
sudo "$installer" --system
rm -f "$installer"
```

System scope installs the CLI at `/usr/local/bin/loki`. It changes lifecycle
ownership and paths, not MCP/project authority.

## Shared safety boundaries

Neither installation path:

- resolves a mutable `latest` runtime artifact;
- accepts release bytes that fail the release identity checks;
- gives MCP or project jobs the raw Docker socket;
- embeds runtime MCP tokens in a public release artifact;
- silently broadens project filesystem or credential authority.

The Windows appliance is a packaging/bootstrap boundary. After first boot, Loki
host lifecycle behavior remains owned by the same `loki host` implementation
used by native Linux.

## Release publication

The stable public entry points are:

```text
https://jinyongp.dev/loki/install.ps1
https://jinyongp.dev/loki/install.sh
```

Each accepted release binds `install.ps1` to the exact
`loki-wsl-amd64.wsl` length and SHA-256, and binds `install.sh` to the exact
Linux bootstrap SHA-256. Publication occurs only after required release
acceptance succeeds.
