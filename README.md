# Loki

Loki is a local service that gives AI assistants controlled access to a
development workspace through the Model Context Protocol (MCP).

It can work with files and Git, run isolated development jobs, inspect running
work, publish local previews, collect artifacts, and use optional browser or Git
signing capabilities. Loki keeps host management and privileged operations
outside the workspace so project code does not automatically gain access to
machine credentials or the Docker socket.

> **Release status**
>
> Loki is not publicly released yet. The installer and release workflow are
> implemented, but final clean-host and release acceptance is still in progress.
> The public one-line installer will be documented here after that gate passes.

## Supported systems

The first supported hosts are:

- Ubuntu 24.04 on amd64
- WSL2 with Ubuntu 24.04 on amd64

Loki uses Docker Engine and Docker Compose v2 for its runtime. On supported
Ubuntu systems, the installer can offer to install or update the required Docker
components after showing the exact changes and asking for approval.

You do not need a Loki source checkout or a local Go, Node.js, Python, Rust, or
browser toolchain to install a released build.

## Install a pre-release build

If you have been given a pre-release Loki build, download its
`loki-bootstrap-linux-amd64` artifact and run:

```sh
chmod +x loki-bootstrap-linux-amd64
./loki-bootstrap-linux-amd64
```

The installer will ask which directory Loki may use as its workspace. It checks
the host, explains any Docker or filesystem changes it needs, asks before making
privileged changes, installs the Loki host CLI, and starts the selected release.

For a machine-wide installation:

```sh
sudo ./loki-bootstrap-linux-amd64 --system
```

For unattended installation, see [First install](docs/first-install.md) for the
explicit approval flags and JSON output mode.

## Check the installation

After a normal user-scoped install:

```sh
loki host status
loki host doctor
loki host connection
```

If `loki` is not yet on your shell's `PATH`, the user-scoped executable is
installed at:

```text
~/.local/bin/loki
```

A system-scoped install uses `/usr/local/bin/loki`.

`loki host connection` shows the connection information needed by an AI tool
that supports MCP. It does not print the authentication token itself.

## What Loki can do

Once connected, an assistant can use Loki to work inside the workspace you
selected during installation. Available capabilities include:

- reading and editing workspace files;
- inspecting Git status, diffs, history, and staging precise changes;
- running development commands and isolated jobs;
- inspecting job output and runtime state;
- publishing local previews and collecting artifacts;
- using encrypted application secrets without returning their values through
  the MCP interface;
- enabling optional browser automation and isolated Git signing when needed.

Optional components are not required for the base installation.

## Updates and recovery

Host lifecycle commands are explicit. Loki does not silently update itself in
the background.

Useful commands include:

```sh
loki host update status
loki host update prepare
loki host update apply

loki host backup
loki host rollback
loki host restore BACKUP_ID
```

Update, rollback, restore, and uninstall operations preserve the selected
workspace by default.

## Safety

Loki is designed so that giving an assistant access to a workspace does not
automatically give it full access to the host.

In particular, the normal project execution path does not receive the raw
Docker socket, machine credentials, or Loki's host-management state. The
installer also does not silently add your user to the `docker` group or apply
broad recursive permission changes to the workspace.

When a privileged host change is required, Loki shows the proposed action and
requires explicit approval.

## Documentation

- [First install](docs/first-install.md) — installation options and supported
  host behavior
- [Self-hosting](docs/self-hosting.md) — advanced and maintainer-oriented
  deployment paths

Developer and release-engineering details live under [docs](docs/) and are kept
out of the normal installation path.

## License

Loki is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
