# Loki

Loki is a Go MCP server for workspace access, Git, browser control, previews,
artifacts, encrypted secret metadata, and runtime inspection. General developer
workflows run through the `devtools` CLI. The bundled agent skills include
`devtools` guidance and the general planning, Git, review, verification,
documentation, and communication workflows used by agents.

Secrets remain encrypted in Loki's AES-GCM vault. Commands that need secrets run
through `loki secret-process start` or `loki secret-process restart`, which
injects the selected values without returning them through MCP.

## Development

Loki requires Go 1.27.1.

```sh
go test ./...
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -o .tmp/loki ./cmd/loki
```

The repository uses `devtools.toml` as its developer command profile. The
`devtools` executable is an external, versioned input to candidate packaging;
the supplied release must implement protocol version 1 and the required
`process start` and `process restart` command schemas.

## Install

Ordinary installation is source-free. On a supported clean Ubuntu 24.04 or WSL2
host, run the release-bound `loki-bootstrap` artifact and follow its prompts:

```sh
chmod 0755 ./loki-bootstrap
./loki-bootstrap
```

The installer selects the workspace, diagnoses the release-declared Docker and
Compose prerequisites, shows any required host or ACL changes before asking for
approval, starts the immutable Compose release, and persists the verified
host-management CLI. It does not require a Loki checkout or a local development
toolchain. After installation, use `loki host status`, `loki host connection`,
and `loki host doctor`.

See [First install](docs/first-install.md) for interactive, system-scoped, and
non-interactive usage. The public one-line installer remains unadvertised until
A14 release acceptance passes.

## Maintainer self-hosting

Direct source-tree Compose and native/systemd candidate procedures are
maintainer/release-engineering paths, not the ordinary first-install
experience. See the [self-hosting runbook](docs/self-hosting.md) for topology
smoke tests, release acceptance, derived images, and specialized deployments.
macOS has a documented host-adapter seam but is outside the current support and
acceptance gate.

## Go candidate

The Go service candidate uses isolated names and paths:

- systemd units: `loki-go-*.service`
- configuration: `/etc/loki-go`
- sockets: `/run/loki-go`
- state: `/var/lib/loki-go`
- logs: `/var/log/loki-go`
- ports: 18765 (MCP), 18766 (egress), and 18767 (browser proxy)

Build a verified toolchain bundle, then pass it with a new absolute output
directory and a compatible `devtools` executable:

```sh
./scripts/build/build-toolchain-bundle.sh /tmp/loki-toolchain-bundle
./scripts/build/build-candidate.sh \
  /tmp/loki-go-candidate \
  /absolute/path/to/devtools \
  /tmp/loki-toolchain-bundle
```

The result contains static Loki and devtools executables, the verified Chromium
runtime and license, all bundled Agent Skills, configuration templates, systemd
units, checksums, and a staging script.
It contains no Python environment.

Run the isolated install, upgrade, reboot, browser, signing, migration, and
rollback acceptance before considering a cutover:

```sh
./scripts/verify/accept-candidate.sh /tmp/loki-go-candidate
```

See [the Go candidate runbook](docs/go-candidate-runbook.md) for installation,
health checks, migration, and recovery commands.

Stage the candidate into a new root with the target service identities and
the digest-pinned OCI image used for isolated Jobs and their trusted gateway:

```sh
/tmp/loki-go-candidate/stage.sh \
  /tmp/loki-go-root \
  RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID EXECUTOR_UID \
  registry.example/loki@sha256:...
```

Staging verifies all checksums, refuses an existing target, renders runtime,
MCP, executor, and launcher layouts, and generates a fresh MCP token and SSH
signing key inside the new root. It does not install or start services on the
host.

The MCP unit requires the runtime, port guard, browser, signing agent, and
unprivileged executor. The executor requires the privileged launcher, while
MCP has no direct launcher or Docker-socket path. The MCP startup helper waits
up to 30 seconds for its required Unix sockets, including the executor socket,
so a dependency that becomes ready after systemd starts MCP does not require a
manual restart.

## Production separation

The retained Python implementation, browser sidecar, tests, and deployment
assets live together under [legacy/python](legacy/python/README.md). Keep that
retirement unit until the Go deployment and recovery checks have passed; its
README records maintenance entrypoints and removal conditions. The repository
root is the Go project, not an installable Python package.

The currently deployed Python service is separate from this Go candidate.
Building, staging, and testing the candidate do not alter or restart the
deployed service. Activating the Go units is a separate cutover operation.

Machine credentials and state must stay outside the repository. Never commit
MCP or tunnel tokens, signing private keys, vault keys, imported dotenv files,
or files copied from a deployed `/etc` or `/var/lib` tree.

## License

Loki is licensed under the Apache License 2.0. See [LICENSE](LICENSE) for the
full license text.
