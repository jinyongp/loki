# First install

Loki has separate one-line entry points for Windows and native Linux. Windows
uses a release-built WSL2 appliance; native Ubuntu uses the Linux bootstrap.

## Normal installation in one minute

For a new installation:

1. Run the Windows or Ubuntu one-line installer below.
2. Wait until the installer reports Loki healthy.
3. Connect your MCP client using the generated connection information.
4. Use the configured workspace. You do not need to manage Loki's internal
   containers directly for normal development.

Fresh installs do not require an operational cutover. That term is reserved for
maintainers moving an already-running legacy Python Loki deployment to the Go
runtime: take/verify recovery data, migrate required state, switch the active
service, and verify health/client connectivity. Legacy Python deletion is not
part of that switch and remains a separate retirement operation.

## Windows

Run this in PowerShell:

```powershell
irm https://jinyongp.dev/loki/install.ps1 | iex
```

The public PowerShell file is intentionally a thin release bootstrap. It is
rendered for one immutable Loki release and contains only the accepted
`loki-windows-amd64.exe` length and SHA-256. It downloads that frontend to a
temporary file, verifies the bytes, and invokes the reserved
`loki bootstrap install` handoff.

The verified frontend then owns the Windows lifecycle:

1. It reconciles the canonical frontend at
   `%LOCALAPPDATA%\Programs\Loki\bin\loki.exe` and its ownership record.
2. It adds that bin directory to the current user's persistent `PATH` without
   rewriting unrelated entries. After the trusted handoff succeeds, the
   PowerShell bootstrap also reflects the directory into the current
   PowerShell process so `loki` is immediately available.
3. The canonical frontend runs `loki install`, which classifies existing
   Windows/WSL state, performs approved recovery when required, verifies the
   release-bound WSL appliance, registers/provisions it, writes protected
   Windows connection state, and owns the existing WSL keepalive task.

The PowerShell bootstrap does not classify, register, unregister, or repair WSL
distributions; it does not create Scheduled Tasks or write lifecycle ownership
state. Those operations have one implementation in the Windows Go frontend.

The WSL appliance contains:

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
task manually. Runtime secrets are generated locally and are not embedded in
the appliance or Windows frontend.

### Windows requirements

Windows requires WSL2 with custom `.wsl` distribution support. The frontend
checks for the WSL `--from-file`, `--name`, and `--no-launch` capabilities
before registering a distribution.

Loki does **not** run `wsl --update` automatically. If WSL is too old, update
it explicitly and rerun the installer:

```powershell
wsl --update
irm https://jinyongp.dev/loki/install.ps1 | iex
```

### Installation options

The default distribution is `loki-mcp`. Existing environment-variable
compatibility is preserved and is consumed by `loki install` after the thin
bootstrap handoff.

Choose a different distribution name:

```powershell
$env:LOKI_WSL_NAME = "my-loki"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

Choose another unused loopback MCP port instead of the default `18765`:

```powershell
$env:LOKI_MCP_PORT = "19000"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

Choose an explicit WSL storage location:

```powershell
$env:LOKI_WSL_NAME = "my-loki"
$env:LOKI_WSL_LOCATION = "D:\WSL\my-loki"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

Disable the per-user WSL keepalive task:

```powershell
$env:LOKI_WSL_AUTOSTART = "0"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

The selected port and location are validated before destructive mutation.
Loki never stops an unrelated listener or deletes a path merely because a name
matches.

### Windows ownership and state

The shared Windows program root is:

```text
%LOCALAPPDATA%\Programs\Loki\
├── bin\loki.exe
├── ownership.json
├── helpers\...
└── connections\<distribution>\<provider>\...
```

Per-distribution appliance state remains compatible with existing installations:

```text
%LOCALAPPDATA%\Loki\<distribution-name>\
├── connection.json
├── mcp-token
└── ownership.json
```

The MCP token and provider credentials are not printed. The MCP token file is
restricted to the current Windows user and SYSTEM. Provider runtime
credentials, such as an OpenAI tunnel runtime key, are stored in Windows
Credential Manager rather than JSON state.

The existing per-distribution `Loki WSL (<distribution>)` logon task retains
its direct `wsl.exe ... /usr/bin/sleep infinity` ownership signature. When an
owned remote connection is enabled, Loki may additionally create one finite
`Loki Connections (<distribution>)` task that invokes the absolute verified
frontend and restores only enabled managed connection runtimes.

### Verify and operate the installation

Normal Windows operations use the installed frontend:

```powershell
loki status
loki doctor
loki connection
loki connection list --json
loki connection show local
```

`loki status` reports the Windows frontend release and the installed appliance
identity separately. Bare `loki connection` is a passive overview of the local
MCP endpoint and managed provider catalog. `loki connection show local` refreshes
and verifies the protected Windows connection replica from the live appliance.

The installed Windows frontend can update itself in place:

```powershell
loki update
```

This checks the published frontend release pointer, downloads the exact Windows
frontend asset, verifies its SHA-256 and length, validates the candidate's
embedded release binding, and replaces the canonical frontend. It does not
prepare or apply an appliance update. After the frontend check/update, Loki
performs a read-only appliance readiness check and tells the operator whether
the appliance is current, has an update that can be prepared, or already has a
prepared update. If readiness cannot be determined, the successful frontend
update is retained and Loki directs the operator to inspect appliance status
explicitly. Rerunning the public PowerShell installer is not required for normal
frontend upgrades.

To update both the Windows frontend and appliance in one command, run:

```powershell
loki update --all --distribution loki-mcp
```

`--all` updates and verifies the frontend, then uses the installed frontend to
prepare and apply the appliance update without another confirmation prompt.
An appliance already at the published version has its Windows connection state
refreshed, enabled managed connections restored, and health checked without
preparing another update. A prepared
update for the same release is reused; failed steps stop the command and can be
continued by rerunning it. Active jobs remain protected unless you add
`--interrupt-active-jobs`. The Linux host retains responsibility for release
verification, backups, application, health checks, and recovery.

For individual appliance lifecycle steps, use:

```powershell
loki update status
loki update prepare
loki update apply
loki backup
loki rollback
loki restore <backup-id>
```

Optional integrations are managed through the Windows frontend; normal users
do not enter WSL or edit Compose directly:

```powershell
loki integration list

loki integration enable browser
loki integration status browser
loki integration disable browser

loki integration setup signing
loki integration status signing
loki integration rotate signing --key-file C:\secure\signing-key
loki integration disable signing
loki integration remove signing

loki integration setup github
loki integration doctor github
loki integration disable github
loki integration enable github
loki integration remove github
```

Signing setup prompts for the Git identity when it is not supplied and generates
a managed Ed25519 key by default. It prints only the public signing key and
fingerprint; register that public key with the Git provider when verified
signatures are desired. An explicit signing key file is an import source, not
the durable credential location.

GitHub setup opens the browser to create an App with **Any account** installation
enabled for personal and organization accounts, then continues in the
same tab to select all or individual repositories for your accounts. Return to
the terminal and press Enter to connect all approved installations of the App
in one transaction. Rerun the same
command after an installation wait or apply failure to continue with the saved
App. Choose personal or organization accounts in GitHub. Rerunning setup after
configuration opens the existing App's installation screen. Use `--no-browser`
to print the URL, or `loki integration import github --config-file PATH
--private-key-file PATH` to connect an existing App. See [GitHub App setup](github-app.md).

The Linux host owns App creation, private-key storage, and configuration
application. Windows relays the one-time registration code through stdin.
GitHub setup validates the App configuration, exchanges the App credential for
a repository-scoped installation token, and reads an allowlisted repository
before accepting the configuration. Imported GitHub and signing private-key bytes cross
the Windows-to-WSL boundary only through stdin and are stored in lifecycle-owned
private appliance state. They are not placed in command-line arguments,
workspace files, connection JSON, or Loki's application-secret vault.

Mutating operations that can interrupt work or destroy state require their
documented approval flags or an interactive confirmation. After appliance
update, rollback, or restore, Loki refreshes the Windows connection/token
replica and reconciles enabled managed connections.

### Progress output

Long-running install, update, backup, restore, rollback, uninstall, and managed
connection mutations emit stable line-oriented progress on **stderr**. Final
human output and machine-readable `--json` results remain on **stdout**, so
redirecting or parsing stdout does not mix progress events into the result.

Default output shows the operation summary, required input, final result, and
actual warnings or errors. Internal inspections, per-volume backups, checksums,
and service transitions are detailed progress. A long operation emits at most
one waiting notice from each command's reporter.

Put the global `--verbose` option before the command for detailed progress:

```powershell
loki --verbose integration setup github
loki --verbose update --all --distribution loki-mcp
```

```sh
loki --verbose host integration setup github
loki --verbose host update prepare
```

Verbose output includes observable phases, measured download byte counts, and
repeated waiting notices. The output preference follows Windows-to-WSL and
frontend update child processes. Loki preserves unrelated environment settings
and does not invent percentages or ETAs. Both modes keep ordinary child warnings
and errors separate from progress and keep `--json` results on stdout.

Progress lines use the stable prefix:

```text
[loki] Preparing the verified WSL appliance image...
[loki] WSL appliance: 64.0 MiB / 160.9 MiB
[loki] Provisioning service state: activating/start.
[loki] Still waiting for provisioning (30s elapsed)...
```

### Recovery behavior

Rerunning the one-line installer is safe and idempotent. The temporary frontend
first reconciles the canonical frontend, then the canonical `loki install`
classifies the requested distribution and Windows state.

- A verified healthy installation is adopted without reinstalling it.
- A provisioning installation is left untouched until provisioning finishes.
- Foreign or unverifiable WSL/Windows resources fail closed and are never
  deleted merely because their names match Loki defaults.
- Verified Windows-only orphans can be removed and recreated safely.
- A verified stale appliance requires destructive approval before unregistering
  its WSL distribution.

For non-interactive stale recovery, opt in explicitly:

```powershell
$env:LOKI_WSL_REINSTALL = "1"
irm https://jinyongp.dev/loki/install.ps1 | iex
```

This is not a generic force switch. The frontend first proves Loki ownership
again immediately before destructive WSL recovery.

Existing v0.1.19-style ownership is migration input. Loki recognizes the
manifest-owned state, the earlier schema-v1 connection shape, and the original
strict flat connection shape. Unverified state is preserved for manual
inspection rather than guessed or deleted.

For low-level appliance diagnostics, the Windows frontend normally provides the
safer operator surface. If direct inspection is necessary:

```powershell
wsl -d loki-mcp --user root -- /usr/local/bin/loki host status --system
wsl -d loki-mcp --user root -- /usr/local/bin/loki host doctor --system
```

The appliance does not grant the default `ubuntu` user passwordless sudo.
Administrative maintenance crosses the explicit WSL root boundary.

System-scoped `doctor` also checks the managed WSL OS: required packages and
files, the PAM/systemd version pairing, boot services, the default user's
systemd session and persistent linger setting, and all failed systemd units.
Healthy Loki containers alone do not establish a healthy appliance boot.
Inspection does not change packages or service state.

Approved installation and appliance update apply reconcile these prerequisites.
The verified candidate Linux binary owns the requirements for the next release;
the Windows frontend relays the approved repair after an update from older
managers. Repair preserves installed package versions, pairs PAM with the
installed systemd version, and restores missing prerequisites without replacing
the distribution or Loki data. It enables linger for the managed `ubuntu` user
so its systemd manager starts at boot and stays active without a login session.
Fresh appliance images include the same persistent setting. These OS changes
persist across Loki rollback.
A failed repair returns an error; unrelated failed services remain visible.

Run the same repeatable repair explicitly when diagnostics identify missing
prerequisites:

```powershell
wsl -d loki-mcp --user root -- /usr/local/bin/loki host appliance check
wsl -d loki-mcp --user root -- /usr/local/bin/loki host appliance repair --approve
```

An update applied directly through a manager older than this repair feature
needs the explicit repair command once the new Linux binary is active. Normal
Windows frontend updates perform this compatibility step automatically.

### Uninstall

Uninstall one verified local appliance with:

```powershell
loki uninstall
```

In non-interactive automation, pass `--approve` explicitly. The command removes
only verified local connection runtimes/state, the owned connection-startup and
WSL keepalive tasks, the verified WSL distribution, and its owned
per-distribution Windows state. The shared Windows frontend and shared helper
cache remain installed.

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

To connect GitHub after installation:

```sh
loki host integration setup github
loki host integration doctor github
```

`loki host connection` reports the loopback-only MCP local origin, transport,
authentication mode, and token-file path without printing the token value. It
also makes clear that remote exposure is operator-managed and outside the Loki
installation boundary.

The local-origin port defaults to `18765` but is operator configuration, not a
public protocol constant. Choose another port during installation when needed:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh -s -- \
  --workspace /srv/workspace \
  --mcp-port 19000
```

The selected port is stored in Loki host lifecycle state, survives update and
backup/restore operations, and is reported by `loki host connection`.

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

## MCP connectivity boundary

Loki installation owns the local MCP origin and its local bearer-authentication
material. The operator owns any external exposure. Supported operator choices
may include a reverse proxy, Cloudflare Tunnel, OpenAI Secure MCP Tunnel,
Tailscale or another VPN, SSH forwarding, or another gateway, but none of those
providers are required by Loki core and the installer does not provision them.

A user-managed ingress may preserve the Loki bearer token end to end or
terminate a separate external authentication scheme and forward to the local
origin using credentials controlled by the operator. Loki must still validate
the inbound Host and its own local-origin authentication policy. Provider-
specific integrations are optional adapters, not installation prerequisites.

Cloudflare Access remains supported for operators who explicitly configure its
existing team-domain/audience settings. Those two settings are compatibility-only
config-schema-v1 inputs: they remain in core configuration and the effective-policy
digest so current installations and rollback paths keep the same configuration
identity. New access providers must not add provider-specific fields to those core
contracts. Removing the Cloudflare compatibility fields requires an explicit
config-schema migration that drops v1 read/rollback compatibility.

The assertion header, JWT validation, JWKS loading, and JWKS refresh
implementation live in the optional Cloudflare integration boundary; core
bearer/request authentication does not depend on Cloudflare. Loki does not create
or manage a Cloudflare Tunnel or Access application.

When an ingress preserves a public Host header, explicitly allow that hostname
in Loki. For a native Linux installation:

```sh
loki host ingress allow mcp.example.com
loki host ingress list
```

For the system-scoped Windows WSL appliance, run the same operator command
inside the appliance:

```powershell
wsl -d loki-mcp --user root -- /usr/local/bin/loki host ingress allow --system mcp.example.com
wsl -d loki-mcp --user root -- /usr/local/bin/loki host ingress list --system
```

Remove a hostname with `host ingress remove`. These commands only update the
bounded Host-header allowlist and reconcile the local MCP runtime; they do not
create DNS records, certificates, tunnels, proxies, firewall rules, OAuth
clients, or accounts with an external provider. The allowlist is stored in host
lifecycle state and participates in backup/restore. Repeating an allow/remove
operation also re-reconciles the runtime, which makes an interrupted fail-closed
configuration change recoverable.

`jinyongp.dev` is only the distribution location for immutable Loki installers
and release artifacts. It is not an MCP hosting service or control plane for
installed Loki instances.

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
