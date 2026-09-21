# GitHub live validation on WSL2

Validated on 2026-09-16 (Asia/Seoul) using an isolated Compose project named
`loki-validation`. Existing Python deployment services and state were not changed.

## Deployment

- Endpoint: `http://127.0.0.1:18765/mcp` (Bearer authentication required).
- Image: `loki:release-e71e706`, built from commit `e71e706` using the core OCI
  build script for amd64 and arm64. Browser image: `loki-browser:release-e71e706`.
- Workspace: `/home/jinyongp/loki-validation-workspace`.
- Lifecycle state: `/home/jinyongp/.local/state/loki-validation`.
- GitHub configuration: `/home/jinyongp/.config/loki/github-validation.toml`.
- PEM source: `/home/jinyongp/.config/loki/secrets/github-app.pem` (mode 0600).
- MCP client token: `mcp-token` in the private lifecycle state directory.
- Configured targets: `jinyongp/loki`, `sectile/sectile`,
  `connextable/homebrew-tap`, `connextable/stamp.is-api`,
  `connextable/stamp.is-web`, `connextable/stamp.is-fab`.

The host configuration maps personal installation `162035578`, sectile
installation `162041287`, and connextable installation `162041495` to these
repositories. GitHub App installation access to other repositories does not
automatically add them to Loki's target map.

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
- Fresh amd64/arm64 core and browser builds passed. Downloaded tool archives were
  checked against their official release checksums; actual binary hashes are in
  image provenance.
- The complete `scripts/verify-loki-release.sh` gate passed on `e71e706`, including
  full Go tests, full race tests, vet, two systemd candidate runs, populated-state
  Compose restore, credential rotation, upgrade/rollback, derived image and browser.
- GitHub writes through MCP passed: temporary issue #1 and PR #2 were created,
  edited, read back and closed. The temporary branch was deleted. Nothing was merged
  and the default branch was unchanged. Closed issue/PR records remain on GitHub.
- Existing Python MCP separately responded with version `0.47.1` during validation.
  Its deployment filesystem hash was not collected from this Ubuntu session.

Compressed release logs and the write-test result are stored under
`/home/jinyongp/.local/state/loki-validation/evidence/e71e706` (private host state).

## Defects found

The gh configuration directory was created below runtime's root-only `/tmp`.
Commit `55bd835` selects the existing runner temporary root instead.

The restore container lacked CHOWN capability, so extracted runner files became
root-owned and prevented MCP startup. The lifecycle restore now preserves archive
ownership. Compose acceptance includes a mode-0600 runner-owned file to detect
this regression with populated state.

## Remaining validation

Both organization installations were verified against the App identity and their
repository lists. After recreating runtime and MCP with the updated host config,
repository, issue and PR reads passed through MCP for all six targets (18 checks).
Organization writes were not exercised.

Both installations now grant organization-level Issue Fields and Projects write
permissions. Runtime and MCP were recreated to discard installation tokens issued
before the permission update. Issue Fields and organization Projects listing then
passed through MCP for both organizations: sectile returned five issue fields and
one project; connextable returned four issue fields and no projects. No project or
organization Issue Fields mutations were made during this read-only validation.

The personal repository and release gates passed. This is a running validation
deployment; production client cutover and
removal of the preserved Python implementation are separate steps. The older
workstream's deployment-hash invariant remains unverified; Python MCP liveness
alone is not evidence of filesystem equality.

## Lifecycle note

This document records an older validation deployment. The former Compose lifecycle shell used by that deployment has been retired by the transactional host-manager work. Do not use this historical deployment as evidence for current backup, restore, update or rollback semantics.

For a retained validation stack, direct `docker compose ps`/health inspection may still be used as a topology diagnostic with its existing non-secret configuration paths. Current lifecycle validation must use `loki host` and the A11/A14 acceptance gates, which record a durable operation journal and explicit recovery outcome.
