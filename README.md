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

## Self-hosting

The verified self-hosting targets are Linux and WSL2 with Docker Compose v2.
Build or load the core and optional browser images, then follow the
[self-hosting runbook](docs/self-hosting.md) for initialization, recovery,
upgrades, and release acceptance. macOS has a documented host-adapter seam but
is outside the current support and acceptance gate.

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
./scripts/build-loki-toolchain-bundle.sh /tmp/loki-toolchain-bundle
./scripts/build-loki-go-candidate.sh \
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
./scripts/accept-loki-go-candidate.sh /tmp/loki-go-candidate
```

See [the Go candidate runbook](docs/go-candidate-runbook.md) for installation,
health checks, migration, and recovery commands.

Stage the candidate into a new root with the target service identities:

```sh
/tmp/loki-go-candidate/stage.sh \
  /tmp/loki-go-root \
  RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID
```

Staging verifies all checksums, refuses an existing target, renders service
layouts, and generates a fresh MCP token and SSH signing key inside the new
root. It does not install or start services on the host.

The MCP unit requires the runtime, port guard, browser, and signing agent. Its
startup helper waits up to 30 seconds for their Unix sockets, so a socket that
appears after systemd starts the MCP unit is handled without a manual restart.

## Production separation

The currently deployed Python service is separate from this Go candidate.
Building, staging, and testing the candidate do not alter or restart the
deployed service. Activating the Go units is a separate cutover operation.

Machine credentials and state must stay outside the repository. Never commit
MCP or tunnel tokens, signing private keys, vault keys, imported dotenv files,
or files copied from a deployed `/etc` or `/var/lib` tree.

## Repository visibility

No license is granted by this repository. Keep the GitHub repository private
unless a distribution license has been selected and the bundled Agent Skill has
been reviewed for redistribution.
