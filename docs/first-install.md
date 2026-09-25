# First install

The normal Loki installation path is the public installer:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

Run it inside an Ubuntu shell. On Windows with WSL2, open the Ubuntu
distribution first; do not run the command directly in PowerShell.

The public installer is generated from one accepted immutable release. It
downloads that release's `loki-bootstrap-linux-amd64`, verifies the exact
SHA-256 embedded in the installer, and then executes the verified bootstrap.
The bootstrap is itself bound to one release manifest and verifies the matching
`loki-linux-amd64` host binary before installation.

## Host requirements

Current host targets are:

- Ubuntu 24.04 amd64;
- WSL2 running Ubuntu 24.04 amd64.

The release pipeline performs a source-free installation on Ubuntu 24.04.
Clean-host WSL2 acceptance is still being completed, but the installer includes
WSL2 host detection and specific systemd guidance.

You need `curl` to fetch the public installer. If a minimal Ubuntu image does
not have it:

```sh
sudo apt-get update
sudo apt-get install -y curl ca-certificates
```

You do **not** need a Loki source checkout, Go, Node.js, pnpm, Python, uv, Rust,
Chromium, Docker Compose files, or project development toolchains before
installing Loki.

Docker Engine and Docker Compose are runtime prerequisites, but you normally do
not need to install them yourself. The installer checks the existing runtime and
can offer the supported Ubuntu installation/update commands when required.

## WSL2 prerequisite

If a compatible Docker daemon is already available inside WSL2, Loki can use it.

If Loki needs to install Docker Engine inside WSL2, systemd must be enabled. If
the installer reports that WSL2 systemd is disabled, edit `/etc/wsl.conf`:

```ini
[boot]
systemd=true
```

Then run this from Windows PowerShell or Command Prompt:

```powershell
wsl.exe --shutdown
```

Reopen Ubuntu and run the installer again.

## Interactive install

Start with the one-line installer:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

Although the shell script is piped through stdin, the release bootstrap opens
the terminal directly for interactive questions.

During installation, Loki may ask you to:

1. approve Docker Engine or Compose changes when the existing runtime is
   missing or incompatible;
2. choose the workspace directory;
3. approve creating the workspace if it does not exist;
4. approve the minimal POSIX ACL required by the container runtime identity;
5. approve sudo-backed Docker lifecycle access when the current user cannot
   directly access an otherwise compatible daemon.

Use a clean absolute workspace path, for example:

```text
/home/alice/workspace
```

Do not enter `~/workspace` or a relative path. Loki does not expand shell
syntax in the workspace prompt. Existing workspace contents are preserved.

The installer shows privileged changes before approval. It does not silently add
the operator to the `docker` group.

## Verify the installation

A successful user-scoped install persists the host CLI at:

```text
~/.local/bin/loki
```

If `loki` is not on your current shell `PATH`, run:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

Then check the release and runtime:

```sh
loki --version
loki host status
loki host doctor
```

`loki host status` reports the installed generation and runtime state.
`loki host doctor` checks the host/runtime prerequisites and reports
actionable failures.

## Connect an MCP client

Get the installed connection information with:

```sh
loki host connection
```

The command reports the loopback MCP endpoint, transport, authentication mode,
and token-file path. It deliberately does not print the secret token value.

The default installation publishes MCP only on loopback. Configure the MCP
client from an environment that can reach that endpoint.

## Non-interactive install

Automation must explicitly approve each mutation class it permits. Pass
installation options through the public installer after `sh -s --`:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh -s -- \
  --workspace /srv/workspace \
  --create-workspace \
  --prepare-workspace \
  --install-prerequisites \
  --allow-sudo-workspace \
  --allow-sudo-docker
```

Omit approvals that are not needed on the target host. Missing required input or
approval fails instead of prompting when interactive input is unavailable.

Add `--json` when the caller needs a machine-readable installation result.

## System-scoped install

System scope is intended for machine-wide host-management ownership. Download
the public installer, then execute it as root with `--system`:

```sh
installer=$(mktemp)
curl -fsSL https://jinyongp.dev/loki/install.sh -o "$installer"
chmod 0755 "$installer"
sudo "$installer" --system
rm -f "$installer"
```

System scope installs the CLI at:

```text
/usr/local/bin/loki
```

It changes host-management ownership and paths. It does not broaden MCP
authorization, project filesystem access, network grants, or project Docker
authority.

## What the installer handles

The public installer, release-bound bootstrap, and host manager together:

1. verify the installer-selected immutable bootstrap;
2. detect the host and validate the release-bound host support contract;
3. verify the matching immutable host binary;
4. validate Docker Engine and Compose requirements;
5. request approval for supported host prerequisite changes;
6. select, create, and minimally prepare the workspace;
7. materialize versioned runtime configuration and a private MCP token;
8. apply the immutable release images through the host lifecycle;
9. persist the verified host CLI;
10. report the installed state and MCP connection information.

You do not need to manage Compose YAML, internal service identities, fixed
container UID/GID values, token generation, Docker socket mounts, or
Loki-managed language toolchains.

## Troubleshooting

If the one-line installer fails, keep the complete terminal output. The most
useful follow-up checks are:

```sh
~/.local/bin/loki host status
~/.local/bin/loki host doctor
```

Run those commands only if the CLI was installed. If installation stopped
before the CLI was persisted, rerun the installer after correcting the reported
host prerequisite.

Common first-install cases:

- **`curl: command not found`** — install `curl` and `ca-certificates` with
  apt, then rerun the installer.
- **WSL2 systemd is disabled** — enable `systemd=true` in `/etc/wsl.conf`,
  run `wsl.exe --shutdown` from Windows, reopen Ubuntu, and retry.
- **`loki: command not found` after success** — add
  `$HOME/.local/bin` to the current shell `PATH`.
- **Docker access requires elevation** — use the installer's explicit
  sudo-backed Docker option when prompted; do not manually add broad host
  privileges just to bypass the check.

## Safety boundaries

The installer does not:

- resolve a mutable `latest` bootstrap;
- execute a bootstrap or host binary that fails its release identity checks;
- recursively `chmod` the workspace;
- silently add the operator to the Docker group;
- expose the Docker socket to MCP, executor, or project jobs;
- configure a specific MCP client;
- silently install packages or elevate privileges in non-interactive mode;
- require a source checkout or local development toolchain.

Lifecycle mutation remains owned by `loki host`.

## Release publication

`https://jinyongp.dev/loki/install.sh` is the stable public entry point. An
accepted release updates this URL only after immutable release publication
succeeds. The release workflow then verifies the published installer bytes and
runs a public source-free installation smoke test.
