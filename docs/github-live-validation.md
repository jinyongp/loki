# GitHub live validation on WSL2

Validated on 2026-09-16 (Asia/Seoul) using an isolated Compose project named
`loki-validation`. Existing Python deployment services and state were not changed.

## Deployment

- Endpoint: `http://127.0.0.1:18765/mcp` (Bearer authentication required).
- Image: `loki:validation-55bd835`, an amd64 validation image built from the
  previously verified release candidate with the Go binary from commit `55bd835`.
- Workspace: `/home/jinyongp/loki-validation-workspace`.
- Lifecycle state: `/home/jinyongp/.local/state/loki-validation`.
- GitHub configuration: `/home/jinyongp/.config/loki/github-validation.toml`.
- PEM source: `/home/jinyongp/.config/loki/secrets/github-app.pem` (mode 0600).
- MCP client token: `mcp-token` in the private lifecycle state directory.
- Configured target: `jinyongp/loki`.

The validation configuration deliberately includes one repository. GitHub App
installation access to other repositories does not add them to Loki's target map.

## Results

- GitHub App signature, installation identity, token issuance and repository listing passed.
- MCP initialize and the `github` tool passed through runtime and the bundled gh CLI.
- `repo view jinyongp/loki --json nameWithOwner,isPrivate`, `issue list` and
  `pr list` returned exit code 0. Issue and PR lists were empty.
- Requests for an unconfigured target were rejected.
- Runner UID 10000 could not read the PEM; the MCP container had no PEM mount.
- PEM and MCP token contents were absent from inspected container metadata and logs.
- Restart and backup/restore completed, followed by successful repeated GitHub calls.
- Restored MCP audit data retained UID/GID 10000:10000 and mode 0600.
- `go test ./...`, `go vet ./...`, and race tests for `internal/githubapp` and
  `internal/service` passed. Packaging tests passed after the restore correction.

## Defects found

The gh configuration directory was created below runtime's root-only `/tmp`.
Commit `55bd835` selects the existing runner temporary root instead.

The restore container lacked CHOWN capability, so extracted runner files became
root-owned and prevented MCP startup. The lifecycle restore now preserves archive
ownership. Compose acceptance includes a mode-0600 runner-owned file to detect
this regression with populated state.

## Remaining validation

No GitHub issues, pull requests or branches were created or changed. Organization
installation and Issue Fields checks remain untested. The modified binary has
not undergone a fresh multi-architecture release build or the full systemd release
suite. This is a running validation deployment, not a completed production cutover
or authorization to delete the preserved Python implementation.

## Lifecycle commands

Run from the Loki repository with these non-secret path settings:

```sh
export LOKI_COMPOSE_PROJECT=loki-validation
export LOKI_COMPOSE_STATE_DIR=/home/jinyongp/.local/state/loki-validation
export LOKI_GITHUB_CONFIG_FILE=/home/jinyongp/.config/loki/github-validation.toml
export LOKI_GITHUB_PRIVATE_KEY_FILE=/home/jinyongp/.config/loki/secrets/github-app.pem
./scripts/loki-compose-lifecycle.sh health
```

Use the same environment for restart, backup and restore. The successful live
backup is `backups/live-github-55bd835` below the lifecycle state directory.
Keep backups private: they contain runtime vault state and its master key.
