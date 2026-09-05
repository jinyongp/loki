# Go migration plan

## Objective

Complete a Go 1.27.x implementation while preserving Loki's public MCP
contract, persistent-state compatibility, isolation boundaries, and observable
runtime behavior. Deliver a verified candidate and migration instructions;
production migration is a separate, later user-authorized task. The candidate
package contains one `loki` binary invoked by separate least-privilege services.
Browser widgets remain embedded HTML and JavaScript because they execute in
the MCP client.

## Production protection boundary

The user confirmed this boundary on 2026-09-04. It applies throughout the
current Go implementation and validation task, including automated goal
continuations:

- Keep the running Python deployment, its services, configuration, credentials,
  central tasks, and project data unchanged.
- Do not deploy or install candidates in the production `loki` WSL distro,
  restart its services, migrate its state, or remove its Python installation.
- Run validation in the Ubuntu source environment or a separate disposable
  test environment, with dedicated state directories, users, ports, sockets,
  service units, credentials, and test-only endpoints. Alternate ports in the
  production distro are not sufficient isolation for this task.
- Reuse the captured 0.47.1 contract and synthetic test data. Do not mount live
  production state into tests or give candidate services production secrets.
- Prepare migration, rollback, and removal procedures without executing them
  against production. Production execution requires a later explicit request.

This boundary is authoritative if an older goal description still includes
production cutover or Python removal in its completion criteria.

## Invariants

- The Go module path is `loki`.
- The public catalog remains 37 tools until an intentional contract change is
  separately accepted.
- Existing tool names, input schemas, annotations, `_meta`, structured output,
  resource URIs, and error semantics remain compatible.
- Migration compatibility for `/etc/loki` configuration and `/var/lib/loki`
  state is demonstrated using isolated fixtures without revealing secrets.
- The MCP process cannot read signing private keys or raw secret values.
- Action processes cannot mutate central policy, project registration, or
  secret state.
- Project repositories remain independent from Loki configuration.
- The running Python deployment remains operational and unchanged throughout
  this task, even after the Go candidate passes every acceptance gate.
- The Go candidate package runs without Python, dedicated venvs, Python
  packages, or browser sidecar. Existing Python reference sources remain
  available for comparison; production and shared Python installations remain
  untouched. Protected offline rollback archives remain recoverable.

## Target layout

```text
cmd/loki/                 command dispatch and process entrypoint
internal/contract/        MCP DTOs, schemas, annotations, and fixtures
internal/config/          TOML configuration and validation
internal/auth/            bearer and Cloudflare Access verification
internal/rpc/             authenticated Unix socket protocol
internal/policy/          paths, executables, permissions, and peer identity
internal/state/           transactional versioned persistent state
internal/secret/          encryption, import, and private action preparation
internal/redact/          private process-output filtering and boundary context
internal/project/         registration, workflows, workstreams, and tasks
internal/action/          policy validation and execution preparation
internal/process/         bounded process lifecycle and output buffers
internal/git/             status, staging, commit, and signing integration
internal/skill/           Agent Skill discovery and resources
internal/browser/         Chromium CDP session and diagnostics
internal/artifact/        temporary files, images, bundles, and viewers
internal/preview/         ports, callbacks, reverse proxy, and WebSockets
internal/audit/           append-only structured security audit
internal/service/         MCP, runtime, browser, guard, signing, and proxy roles
web/widgets/              embedded MCP App HTML and JavaScript
systemd/                  one unit per privilege boundary
internal/contract/testdata/ frozen Python 0.47.1 compatibility fixtures
```

The installed binary supports role-specific entrypoints:

```text
loki serve mcp
loki serve runtime
loki serve browser
loki serve port-guard
loki serve signing-agent
loki serve egress-proxy
loki project ...
loki secret ...
loki action ...
loki config ...
loki deploy ...
loki doctor ...
```

## Work items and gates

### 1. Freeze the compatibility baseline

Use the captured 0.47.1 tool catalog, resources, server instructions, and widget
metadata. Extend state-envelope, CLI, permission, and successful E2E fixtures
using an isolated Python reference instance. Fixtures must contain no bearer
token, imported secret, private key, or temporary public URL.

Gate: fixtures reproduce all 37 public tools and current resource URIs, and a
secret-pattern scan reports no credential material.

### 2. Establish the Go foundation

Create `go.mod` with Go 1.27, pin the official MCP Go SDK, implement build
metadata and role dispatch, embed static assets, and add deterministic build,
format, vet, unit, race, and contract commands.

Gate: clean checkout builds one static `loki` binary and all foundational tests
pass under `go test` and `go test -race`.

### 3. Implement configuration, policy, and internal RPC

Port TOML validation, workspace path confinement, executable policies,
SO_PEERCRED authorization, request limits, audit records, and Unix socket RPC.
Use typed request unions rather than a single unvalidated map.

Gate: valid and invalid Python fixtures produce equivalent results; MCP-service
peers are authorized while general runner and action scopes are rejected.

### 4. Implement transactional state and migration

Introduce a versioned transactional store with global revision and optimistic
updates. Import the existing AES-GCM JSON envelope with its current key and AAD,
write a verified backup, migrate atomically, and retain rollback metadata.
Project onboarding becomes a single validate-and-reconcile transaction.

Gate: synthetic production-shaped state migrates, round-trips, rejects stale
revisions, survives interruption tests, and restores to Python 0.47.1 format.

### 5. Port secrets, actions, projects, tasks, and processes

Port profile lifecycle, opaque imports, generated/public values, project grants,
action policies, materialized env files, redaction, dynamic ports, singleton and
lock probes, process limits, output cursors, workflow execution, central tasks,
and workstream bindings.

Gate: the existing runtime E2E passes against Go, concurrent mutation and
process tests pass under the race detector, and bootstrap reports preflight,
running, succeeded, and failed states without optimistic success wording.

### 6. Port workspace, Git, and Agent Skills

Port safe reads and mutations, revisions, checkpoints, command policy, Git
inspection and partial staging, signed commits, Skill discovery, activation,
resource access, validation, creation, and editing.

Gate: workspace recovery, partial staging, commit-template discovery, signature
verification, worktree sharing, and all bundled Skill tests match 0.47.1.
The typed task tools share a repository queue across logical `/workspace`
and physical workspace paths, including linked worktrees. Worktree-local
active-workstream bindings remain independent.

### 7. Port artifacts, widgets, and previews

Port artifact storage, attachment metadata, image and developer viewers, live
preview reverse proxy, TTL enforcement, local callback binding, HTTP streaming,
and WebSocket upgrade forwarding. Embed widget assets with `go:embed`.

Gate: file, bundle, image, preview, parallel request, iframe CSP, and Vite HMR
tests pass locally and through a separate test-only public endpoint. If a
required test endpoint is unavailable, record that validation gap explicitly;
the production endpoint is not a fallback.

### 8. Replace the browser sidecar with CDP

Connect directly to Chromium's DevTools Protocol. Scope tabs and event buffers
to explicit sessions, and port state, navigation, interaction, screenshots,
console, page errors, network requests, direct requests, diagnostics, and
WebSocket frame capture.

Gate: every existing browser operation passes, simultaneous sessions cannot
steal the active tab, bounded buffers report truncation, and browser-to-local
development-server access remains constrained by the port guard.

### 9. Package and validate in isolation

Build a reproducible binary and install it in a separate disposable test
environment. Run Python reference and Go candidate services there with
independent ports, sockets, credentials, and state. Replay contract tests,
migrate synthetic state, and verify service users, filesystem permissions,
resource limits, and restart behavior. Test provisioning must refuse the
production distro and production endpoints.

Gate: all unit, race, contract, migration, integration, and public E2E suites
pass from the same candidate artifact.

### 10. Deliver the migration-ready candidate

Deliver the candidate artifact, checksums, compatibility report, and reproducible
build, isolated-install, migration, rollback, and removal procedures. Exercise
upgrade and rollback only in the disposable test environment. Keep production
cutover and removal instructions explicitly approval-gated and separate from
build or test commands.

Gate: the isolated Go installation exposes 37 compatible tools, its services
are healthy, state and signatures verify, and the candidate needs no Python
process or package. The repository diff is reviewed and ready for a
user-authorized commit; the candidate contains no credentials or Python runtime
implementation. Production remains unchanged.

## Integrated validation matrix

- `gofmt` produces no diff.
- `go vet ./...` passes.
- `go test ./...` passes.
- `go test -race ./...` passes.
- Contract snapshots match the 0.47.1 baseline (catalog revision 2026-09-04.4).
- State migration and rollback tests pass with corrupted and interrupted cases.
- All current browser, artifact, preview, Skill, Git signing, runtime, and
  progress-guidance E2E scenarios pass.
- systemd sandbox and peer-credential assertions pass in the disposable test
  environment, separate from production `loki` WSL.
- A clean installation and an in-place upgrade both pass in that test environment.
- Secret scanning covers the repository, generated artifacts, logs, fixtures,
  and final Git diff.

## Completion condition

This implementation task is complete when the Go candidate passes the required
isolated compatibility, state, privilege, browser, preview, and restart tests;
reproducible artifacts and migration/rollback procedures are delivered; and the
source repository is reviewed and ready for a user-authorized signed commit.
Production deployment, production state migration, and removal of its Python
installation are outside this task. Any unavailable required acceptance test
remains an explicit completion gap.

## Execution state

The captured Python baseline exposes 37 tools; a client-side frozen catalog is
not a reliable source for this migration. Reuse the captured authenticated
0.47.1 baseline and extend it using isolated reference instances.

Go 1.27.1 is available in the Ubuntu source distro through
`/home/linuxbrew/.linuxbrew/bin/go`. The deployed service runs in the separate
`loki` distro, which is outside the deployment and validation targets for this
task. Build and run unit tests in Ubuntu; run service-level acceptance tests in
a separate disposable environment with its own state and credentials.

Required acceptance gates remain open until their recorded commands pass.
An advertised tool catalog alone does not establish implementation parity.

### Verified implementation checkpoint: 2026-09-05

Implemented foundations include the captured catalog/resources and invalid-call
fixtures, configuration parsing, authentication and host policy, bounded Unix
RPC, confined workspace I/O and revisions, transactional encrypted state and
synthetic legacy-envelope import, and bounded subprocess execution.

Central project state now derives repository identity from the normalized Git
common directory. Main and linked worktrees share one Taskwarrior queue while
keeping independent active-workstream bindings. Workstream manifests and
artifacts retain the Python shape, normalized goal hash, and optimistic revision
checks. Cross-instance file locks serialize metadata and task mutations.

The three typed task tools and six project workstream actions are connected
through the runtime socket and exercised using temporary Git repositories and
Taskwarrior data. Tests cover add/modify/annotate/start/stop/done/logical-delete,
dependency cycles, next-task eligibility, pagination, shared worktree queues,
and concurrent additions.

The private secret controller now validates the full version-1 domain document
and preserves extra top-level/profile/action fields and integer precision during
updates. Profile/secret/action CRUD, public values, non-overwriting random
generation, private staged dotenv import, project registration, and workflow
configuration are implemented. Transactions reject broken action/workflow
references without changing the ciphertext; workflow actions must resolve to
the same repository. Staged import verifies ownership, permissions, regular-file
type, encoding, and limits, and consumes its source only after a successful
state commit. Public metadata exposes names/readiness rather than secret values.

MCP secret/action configuration and project registration/workflow handlers are
connected to typed runtime operations. Unix socket tests exercise these paths
and administrator/delegated classifications. The cgroup reader is injected in
these tests; actual service-user and systemd confinement tests remain open.
Command-policy and shell-free hyperfine parsing follow the Python baseline.
These scoped integration tests do not establish parity for remaining advanced
action execution, status/audit, materialization cleanup, or other tool families.

Workflow bootstrap preflight now reports sorted missing secret names without
exposing values. The internal Go bootstrap CLI executes registered actions in
sequence over UID-verified runtime RPC, drains paged output, preserves failing
exit codes, and requests action termination on cancellation or read/write failure.
Regression tests cover these cases and a temporary Unix-socket CLI round trip.
The public bootstrap tool now admits the helper through the bounded process
manager with the configured workflow timeout and an 8 MiB output history.
Mandatory unprivileged sandbox tests exercise MCP/RPC/helper/action execution,
missing-secret rejection, worktree binding, redaction, successful and failing
steps, and cancellation without a surviving action. Root-run systemd scope
confinement and complete service assembly remain separate acceptance gates.

The ordinary Go process manager now implements atomic global/profile admission,
singleton reuse, bounded tail output with byte cursors, UTF-8 replacement,
timeout escalation, retained histories, and concurrent shutdown. Configured
process/check starts use workspace-confined cwd and an explicit environment;
both finite and managed commands resolve executables using the child's PATH.
The `process_inspect` MCP handler is connected and tested through the Go SDK.
Other command-start/run and runtime-stop routes are still being assembled.

Process-group cleanup retains the unreaped leader PID until descendants are
signalled, and completed sessions cannot signal recycled PIDs. A child left in
the same group is terminated when its leader exits. This tightens the Python
orphan lifecycle; tests cover both normal leader exit and explicit stop. A
deliberate setsid/cgroup escape still requires the service sandbox, which has
not yet been exercised in the separate service-level test environment.
The process manager now supports a private output filter that examines context
outside a requested page. It preserves raw byte offsets, masks matches crossing
read/write/truncation boundaries, masks unfinished secret prefixes, and combines
overlapping or adjacent matches into a single redacted run. The last two rules
tighten Python's per-page string replacement; they may mask more text while
avoiding partial-value exposure. Private managers can require a filter before
admitting any process. Tests include a real split-output process, 10,000 prefix
oracle cases, 3,000 independent randomized window comparisons, ordinary process
concurrency, and private process snapshots. Secret actions use a distinct runtime
manager with mandatory filtering rather than the ordinary output handler.

Private action preparation resolves selected/all secrets and required values
from one validated state snapshot, confines cwd overrides to the same Git
common directory, and derives public preview backend/origin mapping needs.
Private plans and filters reject JSON serialization and redact diagnostic
formatting. The configured test preview domain is independent of production;
required loopback dependency mappings are checked before accepting prepared
public values. This does not yet establish proxy routing or browser reachability.

Basic registered actions now execute through MCP, an authenticated Unix socket,
the encrypted-state controller, and a real bubblewrap namespace running the Go
candidate. A sealed memory-file stdin transports credentials; controller,
runuser, bubblewrap, and Go helper argv/environment contain no credential values.
The helper verifies non-root identity, new mount/PID namespaces, the pinned
workspace identity, and dropped capabilities. It locks its OS thread, sets
no-new-privileges, replaces stdin with `/dev/null`, marks remaining descriptors
close-on-exec, and only then execs the command with its approved environment.

Workspace and helper mounts use pinned descriptors. Actual namespace tests
replace their source paths after preparation and verify the original workspace
and executable remain in use. Tests also verify host file/environment exclusion,
workspace writes, masked secret output, and rejection of direct helper execution
outside the sandbox. Launch descriptors are single-use and synchronized with
cleanup. Singleton/group limits and running/succeeded/failed/stopped outcomes
are connected through the action runtime; scoped MCP tests exercise reuse,
worktree cwd selection, success, failure, and explicit stop.

This proof uses the existing unprivileged Ubuntu test user, temporary state,
and a CGO-disabled candidate (verified to have no ELF interpreter). Root service
scope creation, runuser descriptor handoff, production-shaped service users,
and full cgroup/egress confinement remain unverified in the separate test
environment. Callback binding and Docker-backed launches remain explicitly
rejected until their execution paths
are connected; they are not parity-complete.

Registered Node-family actions now resolve `.node-version`, `.nvmrc`, or the
highest numeric installed workspace FNM version, then execute through FNM.
`pnpm` resolves through Corepack; Node/npm/just/actions-up retain the reference
argv mapping. Version metadata is read beneath a pinned workspace, with regular
file, UTF-8, and 4 KiB limits. Symlink version hints are ignored; symlinked
installation directories are excluded. These bounded reads are a hardening
boundary beyond Python's unbounded metadata reads. Actual Node execution is
verified with the source Ubuntu user's existing public FNM installation mounted
read-only; the Go candidate, Node script, encrypted state, and outputs are
disposable. Corepack/pnpm package acquisition and other Node-family commands
have argv-comparison coverage but not live package-manager execution coverage.

Dynamic action preparation/run is connected through the private Unix RPC.
Preparation returns a 32-hex launch token, port/local URL, 30-second expiration,
and preview backend/environment mapping requirements. Tokens are consumed once
and bound to profile/action/physical worktree cwd. Runtime-level serialization
keeps singleton reuse ahead of token consumption and allocation. Rejected
mapping or failed launches release their reservation; runtime shutdown releases
unused prepared tokens. Expired tokens are pruned and the pending token count
is bounded at 1,024. A stale lease cannot release a newer allocation of the same
port. Allocations are registry exclusions, not held sockets: another host
process can still win a bind race, and process status establishes launch outcome.
Prepared launches retain their allocation exclusion through the original
30-second expiry, tightening the Python consume-time release window.

The allocated port replaces only an exact `{LOKI_PORT}` argv item. Generated
port/origin values and validated public preview mappings override selected
private values inside the sealed input, not in the control process environment.
The configured preview domain is independent of production. A required mapping
missing from a token-based launch fails with `PREVIEW_MAPPING_REQUIRED` before
any child is created. Actual Go sandbox -> FNM -> Node -> loopback HTTP tests
verify these environment/argv values, secret-output filtering, singleton reuse,
explicit stop, and refusal of token replay after completion. They do not verify
public URL routing, API proxy/WebSocket forwarding, or browser reachability.
Scoped MCP/Unix tests additionally cover prepare/run into a secondary worktree.
Prepare/run preserves the reference agent-UID delegation; administrative
configuration additionally checks the MCP service cgroup, and the action
namespace excludes the runtime control socket.

Action lock probes and materialized dotenv files now have Go execution paths.
A finite, pinned Go file helper receives no credentials and performs filesystem
operations as the configured runner. The root launch path selects its UID/GID
and supplementary groups explicitly; production-shaped credential/capability
tests are still pending. Lock probes open an existing regular file beneath the
workspace without following symlinks or creating a missing lock path, and
perform a nonblocking advisory lock test. This is a startup probe, not ownership
of the application's eventual lock.

Materialized dotenv bodies travel in the sealed private launch input. The Go
helper creates a namespace-local tmpfs source and attaches a `0600` read-only,
nosuid/nodev/noexec clone to a validated `O_PATH` placeholder descriptor using
`open_tree`, `mount_setattr`, and `move_mount`. It never resolves a mutable
destination name during attachment. The regular host-workspace file remains
empty. The
target path and snapshot TMPDIR retain the Python layout inside the namespace;
physical snapshot and private journal directories are administrator configuration.
Selected values are sorted and escaped against the actual Python formatter,
and output filtering includes escaped credential forms. A private per-target
flock serializes fixed-path use. Temporary paths use up to 32 reusable journal
slots per workspace temporary directory instead of an ever-growing journal per
launch. An unnamed placeholder's inode is journaled and synced before it is
linked into the workspace, so interruption before linking does not create an
unrecorded named placeholder.

Only the trusted helper receives `CAP_SYS_ADMIN` inside the new user namespace.
It establishes a mount namespace owned by that user namespace before mounting;
this also supports bubblewrap's nested user namespace for devpts. It removes
the temporary writable source and mount, clears effective/permitted/inheritable/
ambient capabilities, and enforces no-new-privileges before the registered
command executes. Other `/tmp` behavior remains unchanged. The actual Ubuntu
bubblewrap 0.9.0-1ubuntu0.1 fixture verifies that the command cannot write or
remount the materialized file, has zero active/inheritable/ambient capabilities,
can still execute an ordinary `/tmp` file, and sees no writable source alias.
The private helper's failure diagnostics expose fixed stage names and numeric
errno only, never raw paths, environment values, or payloads.

The process manager owns successful launches' cleanup callbacks, preserves
admission/singleton ownership until cleanup ends, and waits for cleanup during
Stop/Close. Cleanup records its intent in a checksummed append-only journal,
then atomically captures the workspace entry in a separate runner-owned `0700`
recovery directory on the same filesystem. That directory is outside action
mounts. Automatic cleanup checks the captured device/inode, owner, mode, link
count, and empty size, and acquires a read lease to reject existing writers.
Unexpected files are restored without replacement. If the original name is
occupied, the captured file and recovery intent remain available for a later
retry. Public errors contain no private paths or values. Explicit
`action clear_materialization` and `secret_delete` materialization routing
retain the Python policy for a private single-linked regular file, even when
credentials are unset. Explicit clearing preserves its own recorded intent.

Required namespace tests cover fixed/temporary mounts, permissions, host-empty
files, writable snapshots, competing claims, abandoned/start-failed launches,
fixed/temporary recovery, natural success/failure, timeout, explicit stop,
changed-file preservation, and MCP explicit cleanup. Replacement, edited,
symlink, hardlink, and symlink-parent destinations inserted after preparation
are rejected before command execution while preserving the observed user files;
a helper missing the required mount capability is also rejected. These tests
use controlled temporary directories. Concurrent adversarial namespace tests
remain required in addition to these deterministic late-change cases.
Cleanup regression tests cover replacement between intent and capture, occupied
restore targets, an existing writable descriptor, 100 concurrent filename-swap
attempts, and recovery after intent, rename, capture, a torn journal frame, and
unlink. These checks passed with the race detector. Full root/systemd service
acceptance remains required before this candidate is considered complete.

The current Python worktree also contains scope-result/OOM diagnostics in
`src/loki_mcp/processes.py` and `tests/test_process_oom.py`, added independently
of the Go sandbox work. Those changes remain intact. Their optional
`systemd_result`, `oom_killed`, and `termination_reason` behavior must be included
in root-service acceptance. The current Python action launcher also records
the scope unit and a 4 GiB memory limit, retaining failed scopes for diagnostics;
the Go scope launcher still needs that production-shaped integration. The Go
implementation and comparisons above do not yet establish parity for those
scope diagnostics or limits.

The CLI binary builds, but complete service-role entrypoints have not yet been
assembled. This checkpoint is not a deployable Go MCP release.

Commands passed in the Ubuntu source environment:

- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `gofmt -l cmd internal` (no output)
- `git diff --check`
- `go test -v ./internal/project -run TestPython0471Differential` (41 matching
  successful results and policy errors against actual Python 0.47.1 functions)
- `go test -v ./internal/secret -run TestPython0471SecretDifferential` (156
  matching secret/action/workflow/command-policy, dotenv, action cwd, and
  preview environment cases)
- `go test -v ./internal/process -run TestPython0471ProcessDifferential` (324
  matching output/status snapshots, including truncation, cursor boundaries,
  invalid UTF-8, and running/exited/timeout fields)
- `go test -v ./internal/action -run TestPython0471ActionDifferential` (90 matching
  Node-version precedence/fallback/argv, one-shot launch-token, and dotenv cases)
- `go test -race ./internal/process ./internal/service ./internal/workspace ./internal/project -count=1 -timeout=90s`
  (managed process lifecycle, actual subprocesses, scoped MCP integration, and
  shared subprocess consumers)
- `GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 .tmp/python-baseline/bin/python -m pytest -q tests/test_project_state.py tests/test_task_mcp.py tests/test_process_limits.py`
  (28 passed; temporary fixtures are isolated from user-global Git hooks)
- `go test -race ./internal/redact ./internal/process ./internal/secret ./internal/service -count=1 -timeout=90s`
  (private output filtering, action preparation, and scoped MCP regressions)
- `make check reference sandbox` (format, vet, all Go unit/race tests, static binary build,
  private output filtering, four differential suites with 611 matching cases,
  28 Python tests, required actual namespace/MCP action acceptance, and an actual
  FNM/Node HTTP action with dynamic ports and prepared preview values, plus
  materialized-file lifecycle/recovery, descriptor-based mount guards and
  capability/readonly checks, and advisory-lock probes)

The differential test uses the disposable `.tmp/python-baseline` environment
and temporary synthetic state only. Reproduce its setup in Ubuntu with:

```sh
python3 -m venv .tmp/python-baseline
.tmp/python-baseline/bin/pip --disable-pip-version-check install -e '.[test]'
make reference
```

The `make reference` target requires the reference interpreter and fails its preflight
if it is absent. Ordinary Go unit tests report the differential test as skipped
when that optional environment is absent; a skipped comparison does not satisfy
the migration acceptance gate. The Go candidate does not depend on this test
interpreter. Build and validation commands use GNU Make; the installed `task`
executable is Taskwarrior, not the unrelated Go Task runner. The reference
interpreter in this checkpoint uses Python 3.12.3,
MCP 2.1.1, and Pydantic 2.13.5; Taskwarrior is 3.5.0.

`make sandbox` requires a non-root test user with working bubblewrap user/PID/
mount namespaces, `/home/linuxbrew/.linuxbrew/bin/fnm`, and an existing public
Node installation under that test user's `~/.local/share/fnm/node-versions`.
The workspace filesystem must support `O_TMPFILE`, file locking, and durable
directory synchronization for materialization acceptance. The kernel must
support `open_tree`, `mount_setattr`, and `move_mount` inside a user-owned mount
namespace. Materialization fails closed if that boundary is unavailable. The toolchains are
mounted read-only and this target performs no Node/package
installation. It fails if required confinement or the Node fixture is unavailable; optional skips in
ordinary Go test runs do not satisfy that acceptance gate. It compiles the actual
candidate offline from the existing Go module cache and uses temporary fixture
directories only. No production service, endpoint, state, or credentials are used.

Remaining gates include complete role assembly and all 37 successful-tool
fixtures; root-service action execution and scope/OOM handling, full Node-family
package-manager execution, public preview routing/callbacks, materialization
concurrent destination-guard testing and root-runner
acceptance, Docker execution,
audit/status, workflow execution;
service-owned process shutdown;
Git/signing; Loki's own bundled Skill operations; artifacts/previews/browser;
CLI and packaging; interrupted migration and rollback drills; and isolated
service-level privilege, real bind-mount worktree, public preview, restart, and
end-to-end acceptance tests. The current alias test verifies normalization of
Git output but does not replace a real separate-namespace bind-mount test.

Production deployment and production migration remain outside this task.
