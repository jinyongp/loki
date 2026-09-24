# First install

The normal Loki installation entry point is:

```sh
curl -fsSL https://jinyongp.dev/loki/install.sh | sh
```

The installer served at that URL is generated for one immutable release. It
contains that release's exact Git tag and the SHA-256 of
`loki-bootstrap-linux-amd64`, verifies the downloaded bootstrap, and only then
executes it.

The first public release has not been published yet. Until it is available,
pre-release builds can be installed by running the supplied
`loki-bootstrap-linux-amd64` artifact directly.

## What you need

On a supported clean host, you need only:

- Ubuntu 24.04 amd64, or WSL2 running Ubuntu 24.04 amd64;
- the `loki-bootstrap-linux-amd64` artifact when using a pre-release build;
- a directory you want Loki to use as its workspace.

The target host does not need a Loki source checkout or a local Go, Node.js,
pnpm, Python, uv, Rust, Chromium, or other development toolchain.

Docker Engine and Docker Compose are runtime prerequisites, but on supported
Ubuntu hosts you do not need to install them manually. If the release needs
Docker changes, Loki shows the exact prerequisite commands and asks for approval
before changing packages or services. Loki never adds the operator to the
`docker` group automatically.

## Interactive install

Make the supplied bootstrap executable and run it:

```sh
chmod 0755 ./loki-bootstrap-linux-amd64
./loki-bootstrap-linux-amd64
```

The bootstrap is built for one exact release. It contains that release's
manifest, downloads `loki-linux-amd64` from the same immutable GitHub Release,
verifies its manifest-bound length and SHA-256, stages it privately, and invokes
`loki host install`.

The installer asks for the workspace when it is not supplied. If the directory
does not exist, it shows the exact creation command before asking to create it.
If Loki needs a minimal POSIX ACL for the container runtime identity, it shows
that exact ACL change before applying it.

If Docker Engine or Compose does not satisfy the release requirements,
supported Ubuntu installations show the official Docker
apt-repository/package commands before asking whether to run them. An existing
compatible Docker installation is left unchanged.

If the current user cannot access an otherwise compatible Docker daemon, Loki
does not silently change group membership. Interactive installation can instead
offer an explicit sudo-backed host-lifecycle Docker boundary. MCP, executor, and
project jobs still do not receive the raw Docker socket.

A successful user-scoped install persists the verified host CLI at:

```text
~/.local/bin/loki
```

The installed Compose runtime remains bound to the operator-approved workspace
and publishes MCP only on loopback.

## System-scoped install

For machine-wide host-management ownership:

```sh
sudo ./loki-bootstrap-linux-amd64 --system
```

System scope installs the host CLI at:

```text
/usr/local/bin/loki
```

The scope changes host-management ownership and paths. It does not broaden MCP
authorization, project execution authority, filesystem access, network grants,
or Docker authority.

## Non-interactive install

Automation must make every privileged mutation class explicit:

```sh
./loki-bootstrap-linux-amd64 \
  --workspace /srv/workspace \
  --create-workspace \
  --prepare-workspace \
  --install-prerequisites \
  --allow-sudo-workspace \
  --allow-sudo-docker
```

Omit approvals that are not needed by the target host. Missing required input or
approval fails instead of prompting when stdin is not interactive.

Use `--json` when the caller needs a machine-readable installation result.

A bootstrap cannot be redirected to another release with a command-line flag.
Release identity is fixed when the bootstrap is built.

## After installation

For user scope:

```sh
~/.local/bin/loki host status
~/.local/bin/loki host connection
~/.local/bin/loki host doctor
```

For system scope:

```sh
sudo /usr/local/bin/loki host status --system
sudo /usr/local/bin/loki host connection --system
sudo /usr/local/bin/loki host doctor --system
```

`loki host connection` reports the loopback MCP endpoint, transport,
authentication mode, and token-file path. It does not print the token value.

## What the installer handles

The release-bound bootstrap and host manager together:

1. detect the supported host;
2. validate the embedded release manifest and exact release tag;
3. download the matching immutable GitHub Release host binary and verify its
   length/SHA-256;
4. validate the release-declared Docker Engine and Compose minimum versions;
5. request approval for supported Ubuntu prerequisite changes;
6. select, create, and minimally prepare the operator-approved workspace;
7. materialize versioned Compose/configuration assets and a private MCP token;
8. apply immutable release images through the transactional host lifecycle;
9. persist the verified host-management CLI;
10. report status and connection information.

The operator does not need to manage Compose YAML, internal service identities,
fixed UID/GID values, token generation, Docker socket mounts, or Loki-managed
language toolchains.

## Safety boundaries

The installer does not:

- resolve a mutable `latest` release;
- execute a bootstrap or host binary whose bytes differ from the accepted
  release identities;
- recursively `chmod` the workspace;
- add the operator to the Docker group automatically;
- expose the Docker socket to MCP, executor, or project jobs;
- configure a specific MCP client;
- silently install packages or elevate privileges in non-interactive mode;
- require a source checkout or development toolchain.

Lifecycle mutation remains owned by `loki host`.

## Public installer publication

The stable frontend is `https://jinyongp.dev/loki/install.sh`. Each accepted
release publication replaces it with a release-bound installer only after the
immutable GitHub Release succeeds. Until the first release is published, use a
supplied pre-release bootstrap for validation.
