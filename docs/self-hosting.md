# Self-host Loki with Compose

The current verified host targets are Linux and WSL2. The repository `compose.yaml` defines the portable Linux-container contract for the core `runtime`, `mcp`, and `egress` roles. Browser and signing run as optional profiles. GitHub credentials can be added to the core services through host file paths.

## Requirements

Install current Docker with Compose v2 and Buildx/BuildKit, and choose an absolute workspace path. The repository `compose.yaml` is a generated developer view of the canonical host asset embedded under `internal/host/assets`; edit the canonical asset and regenerate the developer view instead of maintaining a second deployment source.

Lifecycle mutation is owned by the Go host manager. The retired Compose lifecycle shell is not an install/update/rollback implementation. A source checkout may still use Docker Compose directly for topology and isolation smoke tests, but durable install, backup, restore, update, rollback, optional-component mutation and recovery all go through `loki host`.

## Host lifecycle

The host manager requires an authenticated release generation. Until the source-checkout-free bootstrap from A13 is published, this interface is intended for verified candidate/release testing rather than an unauthenticated local image tag.

```sh
# First install from an authenticated release manifest.
loki host install \
  --workspace /absolute/workspace \
  --bootstrap-release-manifest /secure/loki/release-manifest.json

# Inspect and apply a verified update.
loki host update status
loki host update prepare
loki host update apply

# Durable maintenance operations.
loki host backup
loki host restore BACKUP_ID
loki host rollback
loki host enable browser
loki host disable browser
loki host uninstall
```

For unsafe mutations, active or cleanup-pending Jobs block the operation by default. An operator may explicitly pass `--interrupt-active-jobs` only when interruption is intended. Backup, restore, rollback, update and optional-component operations share the same durable journal, recoverable lock, recovery snapshot and truthful `recovery_failed` outcome model.

The selected workspace is preserved by install, update, restore, rollback and uninstall. Deleting workspace data is not part of the lifecycle contract.

## Source-tree Compose smoke

For local development, use Compose directly with a private token file. This path tests the portable container topology; it does not become a second lifecycle engine.

```sh
export LOKI_IMAGE=registry.example/loki@sha256:...
export LOKI_JOB_IMAGE="$LOKI_IMAGE"
export LOKI_WORKSPACE=/absolute/workspace
export LOKI_MCP_TOKEN_FILE=/secure/loki/mcp-token

docker compose config --quiet
docker compose up -d --remove-orphans
docker compose ps
```

Only MCP is published, on `127.0.0.1:18765`. Runtime and runner state stay in their named volumes. Browser and signing remain optional profiles.

## Run Linux or WSL2 topology acceptance

Run the disposable topology/isolation harness with the exact image intended for validation:

```sh
LOKI_IMAGE=registry.example/loki@sha256:... ./scripts/verify/accept-loki-compose.sh
```

The harness creates a unique Compose project under a private temporary directory, prepares the minimal workspace ACL, starts and restarts the core topology, checks role networks and mounts, verifies that credentials are absent from container inspection data, validates a derived project image, exercises optional browser/signing profiles when configured, and removes the disposable stack. Host backup/update/rollback acceptance is separate and belongs to A11/A14 host-manager gates rather than this script.

To prove that an existing deployment remains unchanged, pass newline-separated files or directory roots through `LOKI_ACCEPTANCE_INVARIANT_PATHS`. The harness records file hashes before startup and compares them after the topology smoke without mounting those paths into the test stack.

## Future macOS extension seam

macOS execution is outside the current support and acceptance gate. The reserved future topology entry point is `scripts/verify/accept-loki-compose-macos.sh`; it is intentionally not implemented yet. A future adapter may target Docker Desktop or Colima, but must keep the canonical Compose asset, OCI images, container paths, service identities and network boundaries unchanged.

That adapter must validate bind-mount sharing for the selected absolute workspace, provide the host-specific equivalent of the Linux workspace-permission preparation, select a Docker context, and exercise the same topology/isolation assertions as `scripts/verify/accept-loki-compose.sh`. Host lifecycle semantics remain the Go host manager's responsibility on every supported host.

## Add project runtimes

Loki ships one multi-role image. Add project-specific language runtimes in a derived image instead of changing service roles or adding language-specific runner services. Start from an immutable digest or local image ID:

```sh
BASE_REF=registry.example/loki@sha256:...
docker pull "$BASE_REF"
BASE_ID=$(docker image inspect --format '{{.Id}}' "$BASE_REF")
docker buildx build --load \
  --build-arg LOKI_BASE="$BASE_REF" \
  --build-arg LOKI_BASE_ID="$BASE_ID" \
  --file packaging/images/derived/Dockerfile \
  --tag local/loki-project:current \
  .
./scripts/verify/verify-loki-derived-image.sh "$BASE_REF" local/loki-project:current
```

Copy project runtime binaries and support files only under `/usr/local` or `/opt/project`. Keep the inherited entrypoint, command, user, working directory, labels, Loki binaries, devtools, rg, identity database, workspace metadata, and volume declarations.

The validator compares OCI configuration and provenance labels, hashes Loki-managed files in both images, checks service UID/GID records and workspace mode, and executes `loki version` and `devtools version` without network access. A derived image that changes these invariants is not eligible for the portable topology smoke. Durable host updates require an authenticated release generation and go through `loki host update prepare|apply`; a local image tag is not an update identity.

## Configure the optional GitHub App

GitHub integration uses a repository-scoped GitHub App installation token. Supply the public App and installation configuration plus the private-key host path to the core Compose services:

```sh
export LOKI_GITHUB_CONFIG_FILE=/secure/loki/github.toml
export LOKI_GITHUB_PRIVATE_KEY_FILE=/secure/loki/github-app.pem
docker compose up -d --force-recreate runtime mcp
```

The configuration supports installations on organization and personal accounts. Follow [GitHub App integration](github-app.md) for App registration, permissions, repository selection, configuration format, PEM handling, rotation, and the supported `gh` command contract.


## Enable optional Git signing

The core stack starts without signing. To enable the isolated signing profile, create an unencrypted SSH signing key owned by the host administrator with mode `0600`, then set its absolute path only for the Compose invocation:

```sh
LOKI_SIGNING_KEY_FILE=/secure/loki-signing-key \
  docker compose --profile signing up -d
```

The signing container uses the same Loki image, has no network or workspace mount, and receives the key read-only. Its private agent socket and state remain in the private `signing-state` volume; runtime and MCP can reach only the restricted public socket in the shared socket volume. The proxy permits signing and public-key listing while rejecting agent mutation requests.

Disable the option with `docker compose stop signing`. Core runtime and MCP do not depend on the signing service and continue without it.
