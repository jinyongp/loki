# Restore the complete Loki operator journey

The 0.2 module architecture retains existing product capabilities and operator
convenience. The native installer installs an empty CLI. Subsequent Loki commands
prepare the execution host, acquire trusted releases, select tools, start services
and connect clients. Users should not download catalogs, compose checksum commands,
install Docker manually or repeat host flags after configuration.

## Scope and source inventory

| Journey | Existing implementation | 0.2 gap to restore |
| --- | --- | --- |
| Native CLI installation/update | `scripts/maintainer/tools/*install*`, `cmd/loki-manager/upgrade.go` | Keep CLI-only installation and readable progress/JSON |
| Guided configuration | `cmd/loki/host_onboarding.go`, `internal/host/windows/install_controller.go` | Add guided post-install setup with individual tool selection |
| Trusted acquisition | `internal/host/windows/appliance.go`, manager upgrade verification | Resolve release catalogs and verify release checksums automatically |
| Linux/WSL preparation | `packaging/wsl`, `internal/host/appliance`, `internal/host/windows` | Prepare declared prerequisites and managed Windows execution host |
| Host selection | `internal/management/host.go` | Remember the selected host; preserve explicit overrides and frontend wizard behavior |
| Tool lifecycle | `internal/management`, `cmd/loki-manager` | Expose ordinary install/update/start/stop/restart and actionable recovery |
| Git/GitHub setup | `cmd/loki-manager/integrations*`, protected provider stores | Keep signing in Git and optional Personal Projects; guided setup uses existing Apps |
| Client and remote connection | `connect_codex.go`, `internal/host/windows/connection*` | Bind the installed frontend/execution host correctly; restore managed connection operations |
| Backup/restore/rollback/removal | `cmd/loki/host_lifecycle.go`, Windows ownership/lifecycle controllers | Adapt operator lifecycle to selected modules and owned data |

Existing 0.1 runtime and appliance schemas are implementation references. 0.2
keeps its fresh state and selected modules. It does not require preserving obsolete
command spellings or importing previous configurations automatically.

## Work order

1. Restore trusted automatic catalog acquisition and a usable post-install setup.
   Retain explicit catalogs/offline archives as advanced options. Reject unsupported
   tools and conflicting mode transitions before downloads or host mutation.
2. Restore owned host preparation and persisted routing. Reuse platform ownership,
   hidden keepalive and prerequisite code where its contracts fit. Recover interrupted
   preparation without adopting or deleting another user's environment.
3. Complete start/stop/restart, client connection, provider configuration and the
   operator lifecycle inventory above for the selected composition.
4. Align help, public examples and diagnostics with the working journey. Normal
   commands use readable output; explicit `--json` keeps one structured stdout result.
5. Final validation: isolated setup/install/update/recovery journeys; checksummed
   acquisition failures; host overrides/forwarding; native Windows/Linux/macOS
   checks; selected discovery and disablement; public consumer contracts. Actual
   desktop SSH screenshot rendering needs evidence from that desktop.

## Completion

### Current source state

Guided selection, verified automatic catalogs, remembered WSL/SSH hosts,
protected frontend integration imports, managed Windows OpenAI connection
restoration, Linux socket management, transaction promotion, backup/restore and
rollback are implemented in the working tree. This is not a publication or an
acceptance result. Local checks have passed; native host and desktop evidence remain pending.

Windows needs a modern WSL installation before the dedicated distribution can
be prepared. Full local macOS execution remains a Linux-host requirement. Ordinary
uninstall retains data and the host. Explicit `uninstall --purge-data` and
`hosts remove --purge` provide owned data/host removal; their native acceptance
still needs evidence. Shared Docker/WSL prerequisites and the frontend CLI are
retained. External SSH hosts are detached without deleting their machine.

Source delivery, native acceptance and public release are separate results.
Publication is a separately authorized step. A feature is restored only when the
new CLI exposes its usable success and recovery path. Code remaining in the tree
or lower-level unit tests alone do not establish restoration.

### Sequential review and local evidence

The final review covered acquisition, host routing/administrator authority,
frontend credential transfer, managed Windows connection state/tasks, transaction
promotion, backup recovery and owned removal. Corrections include the adapter's
credential-target contract, SSH connection aliases, private nested Windows
connection directories, refusing host changes while a managed connection is
configured, fresh host-state publication, removal of inactive program generations
and keeping restore recovery active until previous-data cleanup succeeds.

Local Go tests, affected-package race checks, `go vet`, 31 Python maintainer tests,
four architecture variants and workflow lint passed. Windows amd64/arm64 and
macOS arm64 cross-builds passed. A checksummed native Linux manager archive was
installed in temporary directories and exercised setup cancellation, backup,
restore and data purge. Cross-builds do not establish native Windows/macOS behavior.

The candidate workflow adds actual Linux administrator socket preparation,
remembered routing, finite-authority rejection and owned service/data cleanup on
disposable Linux runners. Windows native checks include protected connection
storage and credential-file import. The workflow is run with publication disabled;
actual WSL creation, live OpenAI tunnel credentials and desktop SSH browser
rendering still require evidence from the intended Windows machine.
