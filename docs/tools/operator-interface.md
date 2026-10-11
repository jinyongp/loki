# Loki operator interface

The native installer installs an empty CLI. `loki setup` is the guided entry
point: choose an execution host, manage tools, connect a client, inspect status.
CLI installation, execution hosts, tool programs, activation, running services
and client configuration are separate resources with explicit names.

## Command contract

| Command | Responsibility |
| --- | --- |
| `loki setup` | Host-first guided configuration and management |
| `loki status` | CLI version, execution target, tool state and next actions |
| `loki doctor` | Probe resources on the selected host and report remedies |
| `loki upgrade` | Update the native CLI; retain tools and settings |
| `loki tools manage` | Manage individual tools on the displayed host |
| `loki tools install TOOL...` | Install programs, leaving activation unchanged |
| `loki tools enable/disable TOOL` | Change desired activation; reconcile full services |
| `loki tools uninstall TOOL` | Remove that tool's programs; retain data and credentials |
| `loki tools uninstall --all [--purge-data]` | Explicitly remove all tool programs, optionally their data |
| `loki hosts` | Inspect, prepare, select or detach an execution host |
| `loki connections` | Configure frontend clients and managed tunnels |
| `loki integrations` | Configure tool-specific authentication |

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
   by removing tools. Owned host destruction remains explicit `hosts remove --purge`.

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
