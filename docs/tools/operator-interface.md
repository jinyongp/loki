# Loki operator interface

The native installer installs an empty CLI. `loki setup` is the guided entry
point: choose an execution host, manage tools, connect a client, inspect status.
CLI installation, execution hosts, tool programs, activation, running services
and client configuration are separate resources with explicit names.

## Command contract

- `loki setup`: guided host selection, tools and client connections.
- `loki status` / `doctor`: inspect configuration / probe the selected host.
  `--all-hosts` requests separate results for every registered host.
- `loki upgrade`: update the frontend CLI and selected host's installed tools.
  `--all-hosts` updates the frontend once and processes each host independently.
- `loki tools manage`: install/uninstall and enable/disable individual tools.
- `loki hosts`: register named hosts, prepare, select, rename or detach them.
- `loki connections`: configure frontend clients and host-specific tunnels.
- `loki integrations`: configure tool-specific authentication.

Native manager publication (`install`) and explicit tool-removal shorthand
(`uninstall`) remain advanced commands with contextual help. The primary help
groups ordinary setup, operation and resource management separately. `tools
connect` delegates to the frontend connection path; `connections setup codex`
is the documented client command.

## Guided flow and state

1. A fresh setup asks for a host before offering tool installation. Cancellation
   before selection writes no state. Existing selections are shown and reusable.
2. The setup home shows the current host and offers tool management, client
   connection, readiness checks and host changes. Unreachable hosts can be changed
   from the home screen without pretending their tools are absent.
3. Tool rows distinguish installation, activation and runtime verification.
   Native Windows/macOS hosts expose their supported browser composition; full
   tools explain their Linux-host requirement. Installing a full tool never
   silently moves an existing installation to another host.
4. Installation and activation are independent. Full activation changes update
   running services. Native browser launch remains on demand through a client.
   Failure messages distinguish saved activation from failed service startup.
5. Connection setup runs on the frontend, names the execution host and explains
   which local client configuration it will edit. Native browser connection asks
   for an existing absolute workspace; full tools do not need a workspace path.
6. Deletion previews name the host, tool scope and retained data. Full tool
   removal reconciles remaining services. CLI and host deletion are not implied
   by removing tools. Owned host destruction remains explicit `hosts remove NAME --purge`.

Status is passive: enabled does not imply running or ready. Unprobed runtime
state is shown as **not checked**, with `loki doctor` as the next action.
Private prerequisites are omitted from the ordinary tool table. Advanced JSON
reports retain their technical fields and add frontend/target metadata.
Prompts/progress/errors use stderr; `--json` keeps one result on stdout.

## Implementation order

1. Shared command contracts and host-aware status output.
2. Host-first setup home, tool management and frontend connection menus.
3. Consistent activation/removal service lifecycle and explicit deletion scope.
4. Task-oriented help, install completion messages and user documentation.
5. Final validation: CLI/JSON/state-preservation regressions, real terminal
   navigation/cancellation, Go race checks, full repository tests and native
   cross-compilation. Windows WSL creation and desktop connection execution
   require a Windows environment and remain separately reported evidence.

Release publication is a separate operation.

## Local validation — 11 October 2026

The repository Go suite, manager/management race checks and 32 maintainer Python
tests passed. Terminal tests exercised the setup home, tool menu, back navigation
and fresh host-selection cancellation without creating state. Regression checks
cover explicit uninstall scope, retained data, passive host-aware JSON status,
unreachable hosts and preserving user-owned Codex configuration. Switching client
composition replaces the previous Loki-owned entry instead of duplicating it.

An isolated Linux bootstrap acceptance passed: empty CLI installation, repeat
installation, retained client configuration, rejected corrupt downloads and concise
three-line completion output. Windows amd64 and macOS arm64 manager test binaries
cross-compiled. These builds do not establish native execution. Fresh Windows WSL
provisioning, live full-service changes, tunnel operation and desktop connections
still require their actual environments. No release was published.

## Host identity and connection scope

Host names are display labels; immutable IDs bind client connections. The CLI
has one default among multiple registrations. Changing that default does not
retarget Codex or OpenAI. Each frontend keeps its own registry; Windows and WSL
registries are not automatically mirrored. Remote state stays on the execution
host and is never copied as part of registration or selection.

Multiple roots or WSL users in the same environment can be registered separately.
Owned infrastructure removal is refused while another registration shares it.
Status does not start tools. Offline hosts produce independent errors in an
all-host report rather than hiding results from reachable hosts.

## Multiple-host validation — 14 October 2026

The full Go suite, manager/config/management race tests, Go vet, architecture
checks, module-tidiness checks and 49 maintainer Python tests passed. New checks
cover read-only legacy import, stable IDs, separate roots/users, ownership record
isolation, concurrent client entries, scoped disconnect, offline partial reports,
shared-infrastructure purge refusal, bridge authorization and grouped upgrades.
Grouped upgrade tests verify one frontend CLI publication and independent host
results after a failure.

Native Windows tests passed for console keyboard flows and relevant connection
regressions. An isolated Windows CLI registered two state roots on the existing
Ubuntu distribution and passed default/explicit/all-host status, connection
listing, rename and detach. It used temporary frontend/Codex configuration and
removed it afterward; existing installations and client configuration were not
changed. Windows amd64 and macOS arm64 test binaries cross-compiled.

Live simultaneous OpenAI tunnels, fresh managed WSL provisioning/purge and
native macOS execution remain separate acceptance checks. No release was
published by this implementation.

## Multiple-host review loop — 14 October 2026

The review covered the host registry/import, CLI and guided setup routing,
Codex/bridge bindings, upgrade planning/application, ownership/removal and
human/JSON output against the multiple-host contract in the usage guide.

Corrected defects:

- Immutable IDs take precedence over names; ID-shaped names and assigning the
  native transport name to a remote host are rejected. Imported SSH tunnel
  namespaces stay fixed when preparation resolves the remote state directory.
- Prepared setup and update commands retain the frontend registry and host ID,
  including ownership. Preparation resolves the state directory even when the
  existing manager already has the requested version.
- Native legacy connections are distinguished from managed system connections
  and other native state directories. Disconnect refuses a configuration that
  changed after selecting the entries to remove.
- Bridge reuse checks the complete bound target. Refresh waits for the old
  listener to stop. Fast reuse of managed WSL verifies the recorded ownership
  token, rather than trusting only the installed CLI version.
- Multiple-host upgrades check candidate protocol support before publishing the
  CLI. Small single-host requests retain the existing request protocol. Human
  output includes per-host completion/failure and correct installed-tool counts.
- Unknown top-level commands fail locally before contacting the default remote
  host. Unsaved preparation metadata is not reported as a saved setup change.
  Infrastructure purge requires an explicit host name.

Five initial failure cases were reproduced before their fixes. Regression checks
were extended for the other affected contracts. The final full Go suite, race
checks, vet, architecture and module-tidiness checks passed; all 49 maintainer
Python tests passed. The full native Windows manager suite and isolated actual
Windows-to-Ubuntu multi-host status/default/override/rename/detach flow passed.
Windows amd64 and macOS arm64 compilation passed. Reinspection found no further
confirmed defects in the reviewed changes.

The earlier external acceptance limits remain: live simultaneous OpenAI tunnels,
fresh managed WSL provisioning/purge and native macOS execution were not exercised.
Existing user installations/configuration were retained. No release was published.
