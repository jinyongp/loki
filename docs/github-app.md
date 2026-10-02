# GitHub App integration

Loki uses GitHub App installation tokens for repository and organization work. Personal Projects use an explicitly authorized user token from the same App. GitHub App permissions and selected repositories form the external authorization boundary. Loki adds its configured target allowlist, project-owner checks, and command constraints.

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
Approve creation of the App. Registration enables **Any account** installation
so the same App can be installed on personal and organization accounts. The
same browser tab continues to its
installation page, where you choose **All repositories** or **Only select
repositories** for each account you want. Return to the terminal and press Enter
when you have finished. Loki discovers the App ID and all approved installation
IDs and accounts, validates the installations, applies the integration,
and checks readiness before reporting success. The default App grants Metadata
read access and Contents, Issues, Pull requests, Actions, Workflows, Checks,
Commit statuses, organization Projects, organization Issue Fields, Issue Types,
and Personal Projects read/write access.
Webhooks and automatic user authorization during installation are disabled;
personal Projects authorization continues through device flow during setup, as
described below. Both repository selections follow
the installation's current access on GitHub, without a local repository snapshot.
Repository commands receive a token scoped to their one requested repository.

Organization Projects permission applies to projects owned by an approved
organization installation. Repository scoping limits repository access; it does
not narrow organization Projects permission to boards linked to that repository.

First setup creates an App owned by the signed-in personal GitHub account.
Choose the installation account in GitHub's **Where do you want to install**
screen. You can install the App on a different personal or organization account
from its owner. Loki reads installation identities from the
authenticated GitHub API. Account names and account types are not CLI options.
Organization approval can leave installation pending.

Loki supports one App with installations on multiple personal or organization
accounts. Once an integration is configured, rerun `loki integration setup github`
to open that same App's installation screen and choose an additional account.
The Linux equivalent is `loki host integration setup github`. Existing installations,
repository restrictions, credentials and limits are preserved. Loki opens the
App's installation page, then verifies all approved installations and applies
the combined configuration through the managed backup and recovery transaction.
An organization approval request can remain pending;
rerun the same command to resume. Configure every account you want in GitHub,
then return to the terminal and press Enter. Setup collects all approved,
active installations of this App, including accounts already installed before
this run. Several accounts are added together in one managed transaction.
When every installation is already connected, setup reports all configured
accounts without rewriting credentials or local repository restrictions.
Suspended installations are not added. File-based `integration import` can
connect a specific set of installations using an explicit configuration.

Browser registration creates a public App (`public = true` in the manifest),
which permits installations on multiple accounts. Each account must approve
installation and select repository access; organization App policies also apply.
The App's installation visibility does not publish repository contents or its
private key. See [GitHub App visibility](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/making-a-github-app-public-or-private).

Apps created by earlier Loki versions may still be private and can be installed
only on their owning account. When adding an account, setup prints the existing
App's Advanced settings link with `loki --verbose integration setup github`
(`loki --verbose host integration setup github` on Linux). Open it and choose **Make public**, then return to
the installation page or rerun the same setup command. Existing installations
and credentials remain available; recreating the App is unnecessary. Membership
in an organization alone does not grant the App access to its repositories.

Choose the repository access you want in GitHub. Browser setup stores
`repositories = ["*"]` for that account with its installation ID. GitHub checks
the requested repository when Loki obtains a repository-scoped token, and
rejects requests outside the installation's current access. Changes to either
**All repositories** or **Only select repositories** take effect without
rerunning Loki setup. Existing tokens are reused until near expiry; GitHub also
enforces access when processing API calls. File-based configurations can retain
an explicit repository allowlist as an additional local restriction.
Setup normally prints the browser action, the Enter instruction, and the result.
It waits while you configure accounts in GitHub. Use global `--verbose` before
the command for migration guidance and internal application progress.

To abandon a pending App registration and start again, run:

```powershell
loki integration remove github
loki integration setup github
```

Removal clears Loki's saved GitHub configuration and pending setup credentials;
if an integration was configured, it also disables that integration. It does not
delete the App or its installation on GitHub. Remove an unused App in GitHub
settings separately. Each setup run handles one account installation; configured
accounts remain available together.

Rerunning setup for an existing integration opens its installation screen after
checking readiness. Pending additions preserve the active integration and resume
without creating another App. Configuration or credential changes during an
addition stop application before any existing settings can be overwritten.
File-based import also supports multiple installations of the same App below.

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

## Upgrade an existing App's permissions

Updating Loki does not change the permissions of an existing GitHub App. Open
the App's **Permissions & events**, set **Actions**, **Workflows**, **Checks**, and
**Commit statuses** to **Read & write**, and set **Organization permissions >
Projects** to **Read & write**. Save the App settings and approve the new
permissions in every installation that needs them. Keep the existing App ID,
private key, and repository selection. GitHub requires installation approval
before the new permissions can be used.

An existing installation token retains its issued permissions until it expires.
Loki caches tokens until near expiry, so permission changes are reflected when a
new token is issued. A local CLI readiness check does not validate every GitHub
permission or prove that a private project is accessible.

## Repository operations and Projects

Use the `github` tool's `api` command for GitHub REST or GraphQL operations that
do not have a dedicated Loki tool. **Issues: Read & write** covers milestones,
labels, assignees, parent/sub-issue relationships, and blocking dependencies.
**Pull requests: Read & write** covers PR metadata, review requests, code review
comments, replies, and review thread resolution. Merging also needs **Contents:
Read & write** and must satisfy the repository's branch protection and rulesets.
GitHub does not allow the PR author to approve or request changes on their own PR.

Organization Projects use **Organization permissions > Projects**, independently
of repository issue permissions. This permission does not grant authority over
user-owned Projects. Personal Projects require the App user authorization below.
Loki does not use an ambient personal `gh auth` session.
An empty project list alone does not verify project access.

## Personal Projects authorization

Set **Account permissions > Personal Projects** to **Read & write** in the
existing App's **Permissions & events**, and enable **Device flow** in its
general settings. The personal account must already be connected as an App
installation in Loki.

On Windows:

```powershell
loki integration setup github
loki integration user-status github
loki integration logout github
```

On a system-scoped managed Linux host:

```sh
sudo loki host integration setup --system --no-browser github
sudo loki host integration user-status --system github
sudo loki host integration logout --system github
```

Setup creates or reuses the App, connects its installations, then continues with
personal Projects authorization in the same command. It prints a short code and
opens `https://github.com/login/device`. Enter the code there and authorize the
App with a connected personal account. Loki identifies the signed-in account
from GitHub and checks its installation before saving any token. Setup skips
accounts with usable authorization and skips this step for organization-only
installations. With multiple personal accounts, it lists those still needing
approval and repeats this step for each. `--no-browser` prints the URL without
launching a browser. Each device authorization expires after 15 minutes; rerun
setup if it is denied, expires, or runtime restarts while approval is pending.

Access and refresh tokens stay in Loki's encrypted managed vault, outside
application secret profiles. Expiring device-flow tokens are refreshed before
use, with the replacement access and refresh tokens saved together. An expired
refresh token requires another setup. `user-status` shows local authorization
state without token values or network requests; it does not prove access to a
private project. `logout` removes all locally saved user tokens and pending
device authorizations for this integration.
To revoke the App's authorization at GitHub as well, use GitHub's **Settings >
Applications > Authorized GitHub Apps**. Backups contain the encrypted vault;
restoring an older backup can restore local authorization state.

The `github` tool's `project` command uses the configured target's owner:

```json
{"target":"example-user/repo","command":"project","args":["list","--format","json"]}
{"target":"example-user/repo","command":"project","args":["create","--title","Roadmap","--format","json"]}
{"target":"example-user/repo","command":"project","args":["view","1","--format","json"]}
{"target":"example-user/repo","command":"project","args":["item-create","1","--title","Investigate a bug","--format","json"]}
```

The target must match a configured repository access rule. Projects belong to
the target's owner; repository scoping does not limit Projects to boards linked
to that repository. User-owned Projects use the saved App user token.
Organization-owned Projects use the existing installation token. Repository
commands, including `api graphql`, continue to use installation tokens and never
fall back to the user token.

Supported Project subcommands are `list`, `view`, `create`, `edit`, `close`,
`delete`, `mark-template`, `field-list`, `field-create`, `field-delete`,
`item-list`, `item-create`, `item-add`, `item-edit`, `item-delete`, and
`item-archive`. Provide a project number for commands that accept one; Loki
injects `--owner`. For ID-based field and item edits, Loki checks the node's
project owner and requires all supplied item, field, and project IDs to refer to
the same project. Draft edits use the draft's content ID and require the draft
to belong to exactly one project owned by the selected account. Item URLs must
refer to an issue or pull request in the selected repository.

Scope overrides, file input, arbitrary stdin, jq/template output filters,
linking, and cross-owner copying
are unavailable through this command. List limits are bounded to 100. Projects
mutations are not replay-safe: inspect the project after an uncertain result
before retrying. After updating Loki, refresh the app's actions and start a new
chat to load the `project` command's updated public schema.

See GitHub's [App user token and device flow documentation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
and [token refresh documentation](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens).

Creating a repository is a separate operation that needs **Administration**
permission for an organization. The default App does not request it, and Loki's
repository-token command path requires an existing configured repository target.

## Create and install an existing App manually

Create a GitHub App under the account that will own it. Use a unique name and a homepage URL that identifies this Loki deployment or its source repository.

Configure the registration as follows:

- Leave automatic user authorization during installation and OAuth redirect URIs disabled. Enable Device flow for personal Projects authorization during setup.
- Disable the webhook unless another service in the deployment consumes GitHub events.
- Select **Any account** to use the same App on multiple personal or organization accounts.
- Grant **Metadata: Read-only**.
- Grant **Contents**, **Issues**, **Pull requests**, **Actions**, **Workflows**, **Checks**, and **Commit statuses: Read and write** for repository and CI work.
- Grant **Organization permissions > Projects: Read and write** to manage organization Projects.
- Grant **Organization permissions > Issue Fields** and **Issue Types: Read and write** to manage organization issue fields and types.
- Grant **Account permissions > Personal Projects: Read and write** to manage personal Projects with App user authorization during setup.
- Add **Deployments**, **Variables**, or **Secrets: Read and write** only for commands the deployment must run.

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
loki integration import github `
  --config-file C:\secure\loki\github.toml `
  --private-key-file C:\secure\loki\github-app.pem
loki integration doctor github
```

`import` requires both files and validates their contents before applying them.
It supports multiple installations and additional App permissions. Use `rotate`
with both files to replace an already configured App or credentials. Browser
setup takes no configuration, key, App ID, installation ID, or repository flags.

On a lifecycle-managed Linux/WSL host, use the equivalent `loki host integration import --config-file /secure/loki/github.toml --private-key-file /secure/loki/github-app.pem github` command. The input PEM is imported into lifecycle-owned private credential state. For explicit repository lists, Loki validates it by minting a repository-scoped installation token and reading an allowlisted repository. For `repositories = ["*"]`, Loki checks the installation's account and authentication, and reads one accessible repository when available; an installation with no repositories can still be configured. Rotation and removal use `integration rotate github` and `integration remove github`; the runtime never falls back to ambient `gh auth` credentials.

## Start with Compose

Direct self-hosted Compose continues to support external configuration and PEM files. Pass absolute host paths when creating or recreating the core services:

```sh
export LOKI_GITHUB_CONFIG_FILE=/secure/loki/github.toml
export LOKI_GITHUB_PRIVATE_KEY_FILE=/secure/loki/github-app.pem
docker compose up -d --force-recreate launcher executor runtime mcp
```

Compose mounts the public configuration into `launcher`, `executor`, `runtime`, and `mcp` so their effective policy stays consistent. It mounts the PEM only into `runtime` at `/run/loki-private/github-app-private-key`; a root-owned `0700` tmpfs protects its parent directory from the MCP and runner identities. The PEM is never copied into the image, named volumes, workspace, or backup state.

The runtime creates an isolated configuration directory for each `gh` invocation. Its temporary root uses mode `2710` and the workspace group so the delegated process can read its own configuration. Compose and native Linux packaging use the same group inheritance rule. `system_inspect` reports GitHub as ready only when the delegated CLI can read this configuration; this local check does not verify every repository permission or contact GitHub.

The core stack remains usable with the repository's empty GitHub configuration and no PEM. `loki status` reports whether GitHub is configured and whether its credential source is available without reading or returning the key.

To rotate a key, atomically replace the host PEM and recreate `runtime`. Recreate `mcp` as well when the public installation configuration changes:

```sh
install -m 0600 /secure/loki/github-app.pem.new /secure/loki/github-app.pem.next
mv /secure/loki/github-app.pem.next /secure/loki/github-app.pem
docker compose up -d --force-recreate launcher executor runtime mcp
```

Recreating runtime clears cached installation tokens. GitHub installation tokens are minted for one configured repository and expire automatically.

## Use the MCP tools

The `github` MCP tool accepts a configured `target`, a top-level `command`, an
argument array after that command, and optional standard input:

```json
{"target":"example-org/loki","command":"issue","args":["list","--limit","20"]}
```

Allowed command groups are `api`, `attestation`, `cache`, `issue`, `label`, `pr`, `project`, `release`, `repo`, `ruleset`, `run`, `search`, `secret`, `status`, `variable`, and `workflow`. Loki rejects flags that replace the configured repository or hostname and disables interactive browser or editor flows. Repository search supports `code`, `commits`, `issues`, and `prs`; Loki supplies the repository filter itself. `project` uses the separate authority rules described above.

The `github_issue_fields_read` and `github_issue_fields_write` tools provide typed
organization Issue Fields operations. Typed repository metadata and comments use
`github_read` and `github_write`; other issue or pull request operations use
`github`. A command still depends on the App permissions and the corresponding
API's token support. GitHub returns the command error when a permission or API
capability is unavailable.

See the [GitHub CLI manual](https://cli.github.com/manual/gh), [GitHub App permission reference](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps), and [GitHub App permission guidance](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app).
