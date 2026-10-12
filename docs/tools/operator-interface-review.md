# Operator interface review — 11 October 2026

Scope: the pending interface changes over `92e01385635f059dbe212188dbc590290e6e0ded`,
including new manager files, system command validation, help, user documentation
and installer/acceptance changes. Contract: [operator interface](operator-interface.md)
and the current user decisions: CLI-only bootstrap, explicit execution hosts,
independent installation and activation, readable default output, explicit JSON,
retained user configuration and explicit deletion scope. Publication is separate.

## Coverage

| Area | Review result and evidence |
| --- | --- |
| Command routing and output | Reviewed and rechecked; JSON/protocol/help tests and remote root-override regression |
| Host selection and setup navigation | Reviewed and rechecked; PTY home/tool/back/cancellation and unavailable-host client-menu regression |
| Remote setup | Reviewed and rechecked; simulated SSH command boundary verifies retained full mode, environment preparation and service start |
| Installation, activation and removal | Reviewed and rechecked; retained-program/data tests, offline full uninstall regression, job-idle guard and underlying deployment ownership/cleanup code inspection |
| Codex configuration and connections | Reviewed and rechecked; retained manual entries, composition replacement, idempotence, private-value omission and frontend routing tests |
| System authority and Windows adapters | Reviewed source and applicable tests; finite command validation and native test cross-compilation |
| Help, documentation and installers | Reviewed and rechecked; all-command help tests, public contract checks and maintainer tests |

## Corrections

- An unreachable execution host prevented access to frontend client inspection
  and disconnection in setup. The client menu now remains accessible.
- Browser-only setup on a remote full host could skip service startup and report
  the wrong mode. The frontend now reads and retains the host's full composition.
- Remembered-host status could probe the saved root instead of the explicit root
  override. The selected metadata now carries the effective override.
- A successful SSH transport with an incomplete JSON response could appear to
  provide usable host state. Missing tool state is reported as unavailable.
- New automatic reconfiguration/removal paths could stop active jobs. They now
  use the existing backend idle check before changing deployed services.
- Removing install-only full tools unnecessarily required Docker and elevated
  execution. With no deployment, explicit program removal uses the owned store
  path and retains data; deployment/generation checks still apply.
- A Windows frontend could reject an absolute Linux workspace during remote
  setup parsing. POSIX absolute paths are accepted; the connection boundary
  verifies the workspace on its actual execution host.
- Connection-list help and manual service-start documentation were aligned with
  passive connection inspection and automatic activation reconciliation.

No further confirmed in-scope source defects remained in the second pass.

## Final checks and limits

Passed: `go test ./...`, manager/management race tests, affected-package `go vet`,
32 Python maintainer tests, real Linux PTY navigation/cancellation tests and
Windows amd64/macOS arm64 manager test cross-compilation. SSH fixtures exercise
argv and response handling; they do not establish actual remote provisioning.

Native Windows WSL provisioning/removal, Windows console execution, live OpenAI
tunnel behavior, actual Codex desktop SSH connections and full Docker jobs/service
changes retain their environment-specific acceptance gates. Cross-compilation
and static inspection do not satisfy those gates. No commit or release was made.

## Host preparation follow-up (2026-10-11)

The user-reported existing-WSL failure exposed gaps between CLI acquisition,
administrator preparation and client connection support. WSL and SSH now share
the verified manager publisher and protected administrator preparation path.
Setup and tool installation match the execution CLI to the frontend release;
missing or older user CLIs receive a private replacement. Failed preparation
retains the previously selected host, and existing distributions remain user-owned.
Full installation prepares its Linux service before issuing tool commands, while
a ready service with the expected CLI version skips repeated administrator work.

The OpenAI tunnel menu and command boundary share the same supported-host rule:
owned WSL or SSH. A user-managed WSL host offers direct Codex connections.

Coverage includes initial preparation, remembered-host reuse, outdated CLI
replacement, interrupted installation, administrator-required and already-ready
states, invalid input before host changes, and client support eligibility.
Tests exercise both transport policies, real SSH process argument construction
with local fixtures, and actual POSIX terminal password input without echo.
Terminal navigation also checks that failed host preparation leaves client
inspection and host recovery accessible. Invalid installation requests are
rejected before contacting an execution host.
These checks do not establish native Windows WSL provisioning or live tunnels.

## Client connection follow-up — 12 October 2026

Codex connection setup now previews installed, enabled groups and writes one
`loki` MCP entry. The execution host chooses its backend when that connection
starts. Owned older browser entries are replaced; unrelated client entries are
preserved. Project-host browser connections use a private temporary empty
directory by default. Optional `--workspace` explicitly grants a project path
on the execution host, with existence checked there.

Regression coverage includes actual POSIX terminal confirmation without a path
prompt, local/SSH client entry generation, idempotent browser-entry replacement,
non-browser full connections, optional path validation and temporary-directory
cleanup after failed browser startup. Source and race preflight checks passed;
Windows amd64/arm64 and macOS arm64 test binaries cross-compiled. Actual Windows
WSL startup and desktop MCP execution remain separate acceptance checks.

Host-switch review also confirmed that older local client entries inherited the
saved default host. Local entries now include `--host local`, matching explicit
WSL/SSH bindings. A regression executes the generated local launch arguments
after changing the saved default to SSH: the local generation check is reached,
the SSH fixture receives no request, and the default selection remains unchanged.
Host selection and connection confirmation explain this distinction. Reconnecting
an existing older local entry publishes the explicit local binding.
