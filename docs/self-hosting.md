# Self-host Loki with Compose

The repository `compose.yaml` is the portable service contract for Linux, WSL2, and macOS with Docker Desktop or Colima. It runs the core `runtime`, `mcp`, and `egress` roles. GitHub, browser, and signing are optional profiles added separately.

## Requirements

Install Docker with Compose v2 and build or load a Loki image whose `org.opencontainers.image.title` label is `Loki`. Choose an absolute workspace path. On Linux and WSL2, a newly created empty workspace receives an ACL for container UID 10000 plus an inheritable default. Existing workspaces are never modified and must already be readable, writable, and searchable by UID 10000. Docker Desktop or Colima must provide equivalent bind-mount access; service health confirms it during install. The lifecycle script keeps operator state under `${XDG_STATE_HOME:-$HOME/.local/state}/loki-compose` by default. Override it with `LOKI_COMPOSE_STATE_DIR` when multiple installations are needed.

The state directory is mode `0700`. Its `mcp-token` is the single Compose client-token file. The file is mode `0444` because the non-root MCP container must read the bind-mounted Compose secret; the private parent directory prevents other host users from opening it. The token must contain at least 43 characters (256 bits of encoded entropy). It is never accepted as an argument or printed.

## Initialize and install

Supply a token without a trailing newline through standard input:

```sh
printf %s "$TOKEN_FROM_YOUR_PASSWORD_MANAGER" |
  ./scripts/loki-compose-lifecycle.sh initialize /absolute/workspace loki:local
./scripts/loki-compose-lifecycle.sh install
./scripts/loki-compose-lifecycle.sh health
```

`initialize` creates the workspace and private lifecycle state, validates the image label, Docker engine, Compose model, and paths, then stores the token atomically. `install` starts the core services and waits for all health checks. Run `preflight` independently before later maintenance.

Only MCP is published, on `127.0.0.1:18765`. Runtime state stays in the `loki_runtime-state` volume. Runner state stays in `loki_runner-state`.

## Restart and rotate the client token

```sh
./scripts/loki-compose-lifecycle.sh restart
printf %s "$NEW_TOKEN" |
  ./scripts/loki-compose-lifecycle.sh rotate-credentials
```

Rotation replaces the token atomically and recreates MCP and its egress relay. If the services do not become healthy, the prior token is restored. Update clients only after the command succeeds.

## Backup and restore

```sh
./scripts/loki-compose-lifecycle.sh backup /secure/backups/loki-2026-09-15
./scripts/loki-compose-lifecycle.sh restore /secure/backups/loki-2026-09-15
```

A backup briefly stops the stack and archives `runtime-state` and `runner-state` with SHA-256 checksums. It excludes the workspace because that bind mount remains under the operator's backup policy. It also excludes caches and sockets because they are recreated.

Restore verifies every checksum before stopping services. It first creates a safety backup of the current state. If extraction or health checks fail, the script attempts to recover that safety backup.

Keep backup directories private: the runtime archive contains the encrypted vault and its master key.

## Upgrade and rollback

Load or pull the new image before upgrading:

```sh
./scripts/loki-compose-lifecycle.sh preflight
./scripts/loki-compose-lifecycle.sh upgrade registry.example/loki:0.2.0
./scripts/loki-compose-lifecycle.sh rollback
```

Upgrade validates the Loki image label, takes a consistent backup, records the current image, recreates services, and waits for health. A failed upgrade restores the prior image and state automatically. `rollback` later switches back to the recorded image and restores the matching pre-upgrade state.

Lifecycle operations use a directory lock and reject concurrent maintenance. Backup destinations must not already exist. Image references, paths, and credentials are stored separately so secrets do not enter Compose arguments, process listings, or lifecycle output.

## Add project runtimes

Loki ships one multi-role image. Add project-specific language runtimes in a derived image instead of changing service roles or adding language-specific runner services. Start from an immutable digest or local image ID:

```sh
BASE_REF=registry.example/loki@sha256:...
docker pull "$BASE_REF"
BASE_ID=$(docker image inspect --format '{{.Id}}' "$BASE_REF")
docker build \
  --build-arg LOKI_BASE="$BASE_REF" \
  --build-arg LOKI_BASE_ID="$BASE_ID" \
  --file packaging/container/derived/Dockerfile \
  --tag local/loki-project:current \
  .
./scripts/verify-loki-derived-image.sh "$BASE_REF" local/loki-project:current
./scripts/loki-compose-lifecycle.sh upgrade local/loki-project:current
```

Copy project runtime binaries and support files only under `/usr/local` or `/opt/project`. Keep the inherited entrypoint, command, user, working directory, labels, Loki binaries, devtools, rg, identity database, workspace metadata, and volume declarations.

The validator compares OCI configuration and provenance labels, hashes Loki-managed files in both images, checks service UID/GID records and workspace mode, and executes `loki version` and `devtools version` without network access. A derived image that changes these invariants is not eligible for `LOKI_IMAGE`.

## Configure the optional GitHub App

Create the GitHub App in GitHub and install it only on repositories Loki may access. Put its public identity and repository allowlist in `config/loki-go.toml`:

```toml
github_app_id = 123456
github_installation_id = 789012
github_targets = ["owner/repository"]
github_api_version = "2026-03-10"
github_max_response_bytes = 1048576
github_max_pages = 20
```

Loki requires positive App and installation IDs, 1-64 unique `owner/repository` targets, the supported API version, a 4 KiB-16 MiB response limit, and a 1-100 page limit. The feature stays disabled when these settings are absent.

Initialize the encrypted vault once, then import or rotate the private key from an interactive terminal:

```sh
loki secret init
loki github app-key set
```

Paste the PEM when prompted and finish with Ctrl-D. The command accepts no key argument, file option, environment variable, or standard input. Loki validates an RSA key of at least 2048 bits before atomically replacing the prior key. It returns only configured/rotated metadata; the key remains encrypted in runtime state.
