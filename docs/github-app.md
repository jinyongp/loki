# GitHub App integration

Loki uses a private GitHub App to run repository-scoped `gh` commands without a user OAuth token. GitHub App installation permissions and the selected repositories form the external authorization boundary. Loki adds its configured target allowlist and command constraints.

## Browser setup on a managed host

On Windows, run:

```powershell
loki integration setup github
```

On native Linux, run:

```sh
loki host integration setup github
```

For a system-scoped Linux installation, use
`sudo loki host integration setup --system --no-browser github` and open the
printed loopback URL in a browser on that machine. `--no-browser` also works on
Windows and user-scoped Linux. It prints a URL; registration still requires a
browser on the same machine.

The browser page submits a [GitHub App manifest](https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest).
Approve creation of the private App. The same browser tab continues to its
installation page, where you choose **All repositories** or **Only select
repositories**. Loki discovers the App ID, installation ID, account,
and repository access, validates the installation, applies the integration,
and checks readiness before reporting success. The default App grants Metadata
read access and Contents, Issues, and Pull requests read/write access, with
webhooks and user OAuth authorization disabled. Both repository selections follow
the installation's current access on GitHub, without a local repository snapshot.
Each command still receives a token scoped to its one requested repository.

With no account argument, setup creates a personal-account App for the signed-in
GitHub user. When `--account` is supplied, Loki detects whether it identifies a
user or organization through GitHub's public account API:

```powershell
loki integration setup github --account example-org
```

The Linux equivalent is
`loki host integration setup --account example-org github`.
Use `--account-type user|organization` to specify the type explicitly, such as
when public account lookup is unavailable. A resumed setup retains its known
account type and does not repeat the lookup.
You need permission to create and install Apps for the chosen owner. Organization
approval can leave installation pending.

Browser setup creates a private App, which GitHub allows to be installed only on
the account that owns it. An organization repository therefore requires an
organization-owned App in this flow. A personal-account App does not grant access
to organization repositories just because its owner belongs to the organization.

Choose the repository access you want in GitHub. Browser setup stores
`repositories = ["*"]` for that account with its installation ID. GitHub checks
the requested repository when Loki obtains a repository-scoped token, and
rejects requests outside the installation's current access. Changes to either
**All repositories** or **Only select repositories** take effect without
rerunning Loki setup. Existing tokens are reused until near expiry; GitHub also
enforces access when processing API calls. File-based configurations can retain
an explicit repository allowlist as an additional local restriction.
Installation status polls run quietly while the terminal waits; setup and
application progress remain visible.

To abandon a pending personal-account setup and start an organization setup, run:

```powershell
loki integration remove github
loki integration setup github --account example-org
```

Removal clears Loki's saved GitHub configuration and pending setup credentials;
if an integration was configured, it also disables that integration. It does not
delete the App or its installation on GitHub. Remove an unused App in GitHub
settings separately. Browser setup configures one account at a time.

The Linux host owns GitHub API calls, the App private key, private resumable
setup state, and the managed integration transaction. Windows opens the browser
and relays the callback code through stdin to that same host implementation.
Neither platform reads `gh auth` credentials for this flow.

If installation is pending or configuration application fails, rerun the same
setup command to continue with the saved App. Pending private state is stored in
a host-owned `0700` directory with a `0600` session file and is removed after
successful setup. Registration and installation waits stop after ten minutes;
an installation wait does not discard the App key.

The manifest conversion code is single-use. If a connection failure makes its
conversion result uncertain, Loki preserves that condition and reports recovery
instructions. Open the existing App's GitHub settings, generate a new private
key, and import that App using the file-based setup below.

## Create and install an existing App manually

Create a GitHub App under the account that will own it. Use a unique name and a homepage URL that identifies this Loki deployment or its source repository.

Configure the registration as follows:

- Leave user authorization, OAuth redirect URIs, and Device Flow disabled.
- Disable the webhook unless another service in the deployment consumes GitHub events.
- Select **Only on this account** for a private App.
- Grant **Metadata: Read-only**.
- Grant **Contents**, **Issues**, and **Pull requests: Read and write** for normal repository work.
- Add **Actions: Read**, **Workflows: Read and write**, **Checks: Read and write**, **Commit statuses: Read and write**, **Deployments: Read and write**, **Variables: Read and write**, or **Secrets: Read and write** only for commands the deployment must run.
- Grant the organization Issue Fields permission when the typed `github_issue_fields` tool is required.

After creating the App, record the numeric **App ID** from its settings page and generate a private key. Install the App on each organization or personal account Loki must access. Choose **All repositories** or select individual repositories. Record each numeric installation ID from the installation settings URL ending in `/settings/installations/<installation-id>`.

A public App can have installations on both organizations and personal accounts;
a private App can be installed only on its owning account. To use one App across
accounts, enable public installation in its GitHub settings and declare each
installation separately in the configuration below.

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

Repository names are relative to `account`. Use `repositories = ["*"]` alone to
follow the GitHub installation's current repository access. Loki accepts 1-16
installations and at most 64 configured target entries; an installation-wide
entry delegates repository selection to GitHub. Calls use a concrete
`owner/repository`, never `owner/*`. The configuration file contains identifiers
and access selections, not the private key.

Store the generated PEM in an operator-owned directory:

```sh
install -d -m 0700 /secure/loki
install -m 0600 /path/from/github.private-key.pem /secure/loki/github-app.pem
```

## Managed installation

To import an existing App on a managed Windows installation, configure it through the frontend:

```powershell
loki integration setup github `
  --config-file C:\secure\loki\github.toml `
  --private-key-file C:\secure\loki\github-app.pem
loki integration doctor github
```

For prompted entry of an existing App's identifiers on Windows, use
`loki integration setup github --manual`. The file-based path also supports
multiple installations and additional App permissions.

On a lifecycle-managed Linux/WSL host, use the equivalent `loki host integration setup --config-file /secure/loki/github.toml --private-key-file /secure/loki/github-app.pem github` command. The input PEM is imported into lifecycle-owned private credential state. For explicit repository lists, Loki validates it by minting a repository-scoped installation token and reading an allowlisted repository. For `repositories = ["*"]`, Loki checks the installation's account and authentication, and reads one accessible repository when available; an installation with no repositories can still be configured. Rotation and removal use `integration rotate github` and `integration remove github`; the runtime never falls back to ambient `gh auth` credentials.

## Start with Compose

Direct self-hosted Compose continues to support external configuration and PEM files. Pass absolute host paths when creating or recreating the core services:

```sh
export LOKI_GITHUB_CONFIG_FILE=/secure/loki/github.toml
export LOKI_GITHUB_PRIVATE_KEY_FILE=/secure/loki/github-app.pem
docker compose up -d --force-recreate launcher executor runtime mcp
```

Compose mounts the public configuration into `launcher`, `executor`, `runtime`, and `mcp` so their effective policy stays consistent. It mounts the PEM only into `runtime` at `/run/loki-private/github-app-private-key`; a root-owned `0700` tmpfs protects its parent directory from the MCP and runner identities. The PEM is never copied into the image, named volumes, workspace, or backup state.

The core stack remains usable with the repository's empty GitHub configuration and no PEM. `loki status` reports whether GitHub is configured and whether its credential source is available without reading or returning the key.

To rotate a key, atomically replace the host PEM and recreate `runtime`. Recreate `mcp` as well when the public installation configuration changes:

```sh
install -m 0600 /secure/loki/github-app.pem.new /secure/loki/github-app.pem.next
mv /secure/loki/github-app.pem.next /secure/loki/github-app.pem
docker compose up -d --force-recreate launcher executor runtime mcp
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
