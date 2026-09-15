# GitHub App integration

Loki uses a private GitHub App to run repository-scoped `gh` commands without a user OAuth token. GitHub App installation permissions and the selected repositories form the external authorization boundary. Loki adds its configured target allowlist and command constraints.

## Create and install the App

Create a GitHub App under the account that will own it. Use a unique name and a homepage URL that identifies this Loki deployment or its source repository.

Configure the registration as follows:

- Leave user authorization, OAuth redirect URIs, and Device Flow disabled.
- Disable the webhook unless another service in the deployment consumes GitHub events.
- Select **Only on this account** for a private App.
- Grant **Metadata: Read-only**.
- Grant **Contents**, **Issues**, and **Pull requests: Read and write** for normal repository work.
- Add **Actions: Read**, **Workflows: Read and write**, **Checks: Read and write**, **Commit statuses: Read and write**, **Deployments: Read and write**, **Variables: Read and write**, or **Secrets: Read and write** only for commands the deployment must run.
- Grant the organization Issue Fields permission when the typed `github_issue_fields` tool is required.

After creating the App, record the numeric **App ID** from its settings page and generate a private key. Install the App on each organization or personal account Loki must access. Choose **Only select repositories** and select the repositories in the Loki workspace allowlist. Record each numeric installation ID from the installation settings URL ending in `/settings/installations/<installation-id>`.

One App can have installations on both organizations and personal accounts. Each installation is declared separately.

## Create the public configuration

Copy `config/github.compose.toml` to an operator-owned path and fill in the App and installation values:

```toml
github_app_id = 123456
github_api_version = "2026-03-10"

[[github_installations]]
account = "example-org"
account_type = "organization"
installation_id = 789012
repositories = ["loki", "another-repository"]

[[github_installations]]
account = "personal-owner"
account_type = "user"
installation_id = 345678
repositories = ["private-repository"]
```

Repository names are relative to `account`. Loki accepts 1-16 installations and at most 64 unique `owner/repository` targets in total. The configuration file contains identifiers and allowlists, not the private key.

Store the generated PEM in an operator-owned directory:

```sh
install -d -m 0700 /secure/loki
install -m 0600 /path/from/github.private-key.pem /secure/loki/github-app.pem
```

## Start with Compose

Pass absolute host paths when creating or recreating the core services:

```sh
export LOKI_GITHUB_CONFIG_FILE=/secure/loki/github.toml
export LOKI_GITHUB_PRIVATE_KEY_FILE=/secure/loki/github-app.pem
docker compose up -d --force-recreate runtime mcp
```

Compose mounts the public configuration into both services. It mounts the PEM only into `runtime` at `/run/loki-private/github-app-private-key`; a root-owned `0700` tmpfs protects its parent directory from the MCP and runner identities. The PEM is never copied into the image, named volumes, workspace, or backup state.

The core stack remains usable with the repository's empty GitHub configuration and no PEM. `loki status` reports whether GitHub is configured and whether its credential source is available without reading or returning the key.

To rotate a key, atomically replace the host PEM and recreate `runtime`. Recreate `mcp` as well when the public installation configuration changes:

```sh
install -m 0600 /secure/loki/github-app.pem.new /secure/loki/github-app.pem.next
mv /secure/loki/github-app.pem.next /secure/loki/github-app.pem
docker compose up -d --force-recreate runtime mcp
```

Recreating runtime clears cached installation tokens. GitHub installation tokens are minted for one configured repository and expire automatically.

## Use the MCP tools

The `github` MCP tool accepts a configured `target`, a `gh` argument array, and optional standard input:

```json
{"target":"example-org/loki","args":["issue","list","--limit","20"]}
```

Allowed command groups are `api`, `attestation`, `cache`, `issue`, `label`, `pr`, `release`, `repo`, `ruleset`, `run`, `search`, `secret`, `status`, `variable`, and `workflow`. Loki rejects flags that replace the configured repository or hostname and disables interactive browser or editor flows. Repository search supports `code`, `commits`, `issues`, and `prs`; Loki supplies the repository filter itself.

The `github_issue_fields` tool provides typed organization Issue Fields operations. Personal repositories and ordinary issue or pull request work use the `github` tool. A command still depends on the GitHub App permissions and installation-token support of the corresponding GitHub API. GitHub returns the command error when a permission or API capability is unavailable.

See the [GitHub CLI manual](https://cli.github.com/manual/gh), [GitHub App permission reference](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps), and [GitHub App permission guidance](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app).
