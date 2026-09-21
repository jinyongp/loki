# Go Loki architecture and readiness review

Review date: 2026-09-17.
Source baseline: `50a0d65d6cacfcae5673889d004a08f8100df392`.
Environment: Go 1.27.1, Linux amd64, unprivileged workspace process, umask 0077; POSIX ACL utility unavailable.

## Assessment

The Go port is not ready to become the canonical operational server. It contains useful security and correctness primitives, but their integration does not yet provide the product-wide execution and authorization guarantees required by the [installation plan](installation-distribution-plan.md). Reproduced inherited defects, newly identified implementation defects, and incompatible assumptions between native and Compose deployment make installer-first development the wrong next step.

This is a repository-wide architecture and implementation assessment, supported by full normal/race test runs, build/vet, source tracing, and isolated diagnostic probes. It is not a claim that every possible defect has been found or that a deployed sandbox has passed penetration testing. The running Python MCP service, real repositories' indexes, host deployment, and live credentials were not used as attack or test targets.

Backward compatibility with the Python API, current Go API, legacy configuration, or internal layouts is not a requirement for the redesign. Python behavior and current snapshot tests are evidence, not correctness oracles. Existing workspace data, secrets, operator changes, and recovery material must nevertheless be preserved. Retain `legacy/python/` until its separate cutover/retirement conditions are satisfied.

The [architecture improvement plan](architecture-improvement-plan.md) is the remediation design and implementation sequence. This review records evidence and limitations; it does not claim the proposed fixes already exist.

## Review coverage

| Area | Sources and paths traced | Assessment scope |
| --- | --- | --- |
| Product and transport | `cmd/loki`, `internal/contract`, `internal/mcpserver`, `internal/service` | MCP-only usability, CLI delegation, request schemas, handler wiring, public errors and capabilities |
| Authority and execution | `internal/rpc`, `internal/execution`, `internal/process`, `internal/devtools`, `internal/daemon`, deployment layouts | UID/peer identity, administration, subprocess context, environment construction, cancellation and resource ownership |
| Workspace and Git | `internal/policy`, `internal/work/workspace`, `internal/work/workspace/git`, `internal/transport/mcp/workspace` | Descriptor-based path safety, nested repositories, deletion/move, filters, CAS scope, checkpoints, confined Git execution and MCP binding ownership |
| Credentials and state | `internal/secret`, `internal/state`, `internal/admin`, `internal/auth`, `internal/audit`, `internal/integrations/github` | Managed versus application secrets, injection, parser semantics, transactions, repository-scoped provider tokens and audit/recovery claims |
| Components and connectivity | `internal/integrations/browser`, `internal/integrations/browser/internal/cdp`, `internal/egress`, `internal/portguard`, `internal/integrations/sharing/previews`, `internal/integrations/sharing/artifacts`, `internal/integrations/signing`, `internal/dockerproxy` | Cross-container endpoints, egress profiles, browser lifecycle, port ownership, sharing and signing broker authority |
| Distribution and verification | `compose.yaml`, `packaging/`, `scripts/`, `internal/work/toolchains`, `internal/host/lifecycle`, `internal/packaging`, `internal/e2e` | Archive installation, ownership, release integrity, lifecycle state, actual acceptance coverage and skipped tests |
| Historical implementation | `legacy/python/` and migration documents | Inherited defects and native-host assumptions; no feature-for-feature compatibility obligation |

All Go package groups were inventoried with their source/test sizes and internal dependencies. Critical paths were followed from entrypoint to enforcement, not judged solely by the existence of a helper, a schema, or a passing mock test. Clean-host and operational integration evidence remains a separate required gate.

## Validation evidence

| Check | Result in this review |
| --- | --- |
| `go build ./...` | Passed |
| `go vet ./...` | Passed |
| `go test -json -count=1 ./...` | Failed: 29 packages passed, 4 failed; 290 passing test/subtest events, 9 skipped integration tests |
| `go test -race -json -count=1 ./...` | Failed in the same 4 packages; not a passing race gate |
| Synthetic core probe | Reproduced Git, configuration, managed-credential, RPC identity and process-lifetime findings |
| Synthetic archive probe | Reproduced chained-symlink extraction escape and permission loss under umask 0077 |
| In-memory MCP / constructor probe | Reproduced silently discarded CAS typo, stale protected-port constants and malformed-browser-proxy panic |
| Real Compose/native deployment and clean-host acceptance | Not run; no operational server was changed |
| Live provider calls or actual credential access | Not run |
| Dependency vulnerability/advisory attestation | Not performed; no claim that dependencies are vulnerability-free |

At the reviewed baseline, the failing areas included auth/daemon fixture modes, toolchain executable-mode handling, and the old Compose lifecycle ACL integration. Those findings are historical evidence, not the current package layout: toolchains now live under `internal/work/toolchains`, and the retired Compose lifecycle shell no longer owns backup/update/rollback. The current Compose topology acceptance requires `setfacl` explicitly, while host lifecycle transaction acceptance is owned by A11.

The nine skipped tests are Chromium pipe/lifecycle/interactions/debug/download tests, the project-execution contract, real runner-versus-vault permission checks, real devtools secret-process integration, and OCI archive content checks. Unit tests therefore do not establish live browser connectivity, actual secret-process isolation, or a complete MCP-only development workflow.

Diagnostic sources and logs for this workspace session are in ignored `.tmp/architecture-review/`: `review.py`, `inventory.json`, normal/race/build/vet logs and summaries, `probe.go`, `archive_probe.go`, and `api_probe.go`. They are temporary diagnostics, not the permanent test suite. Their scenarios and results below are the durable regression specification. A diagnostic exiting zero means the observation completed, not that the observed product behavior was correct.

## Continuation verification

The interrupted review was resumed against the same source baseline. Comparing the recorded SHA-256 inventory confirmed that no tracked product source changed; only the documented plans were edited. The inventory covers 36 Go package directories, 110 production Go files (15,238 lines), and 106 Go test files (10,987 lines). This is scope accounting, not a claim that every line has equal review depth.

The three synthetic probes were rerun after resumption:

```sh
go run .tmp/architecture-review/probe.go
go run .tmp/architecture-review/archive_probe.go
go run .tmp/architecture-review/api_probe.go
```

They again observed deleted-path and nested-repository failures, project textconv execution during diff, managed-profile injection/mutation, same-UID/cgroup authorization behavior, a live detached descendant after timeout, ZIP extraction outside its staging directory, 0755 becoming 0700 under the review umask, silently discarded misspelled CAS input, stale protected-port constants, and a malformed proxy panic. All credentials and files used for these observations were synthetic. Full test/race/build/vet results above are the preserved results from the unchanged baseline, not a claim that those commands were rerun during continuation. Document links, code fences, finding/work-unit inventory, and unchanged product-source hashes were checked again.

## Finding priorities

P0 means a security or foundational authority defect that blocks cutover. P1 means a correctness, functionality, or recovery issue that blocks the corresponding product milestone. P2 means a structural/operational improvement to complete with the affected subsystem. These are remediation priorities, not CVSS scores or claims of an unauthenticated remote exploit.

Evidence is identified as reproduced, source/topology-confirmed, or an explicitly unverified risk. Prior R1-R3 identifiers are preserved for continuity of the review record, not API compatibility.

## R1 — Git path semantics are wrong for deletion and nested repositories

Priority: P1. Evidence: reproduced against Go controllers.

Containment status: Git path validation now separates filesystem existence from repository identity. Scoped staging and diff accept absent paths only when the nearest repository knows the literal path through its index or `HEAD`, so deletions, renames and deleted directory prefixes work while arbitrary absent paths remain rejected. Repository identity validates the worktree root, git-dir and common-dir against the approved workspace.

At the reviewed baseline, `MutatePaths("stage", ...)` and path-filtered `Diff` required the requested path to exist, so tracked deletions and rename sources failed before Git could interpret them. `RemoveTracked` also ran `git ls-files` from the workspace root rather than the file's nearest owning repository, causing nested tracked files to be reported as untracked. The Python implementation carried the same existence-based staging defect.

Permanent regressions now cover deleted files and parents, renames, staged deletion, unknown absent paths, literal pathspecs, nested repositories, in-workspace linked worktrees, external metadata rejection, nearest-repository destructive removal and recovery. R1 is therefore retired from the observation-only readiness probe. The later S04 package consolidation still owns the temporary `workspace -> gitops` dependency; this fix does not claim that structural cleanup is complete.

## R2 — The Go MCP endpoint cannot carry the intended development workflow

Priority: P1, prerequisite to a usable release. Evidence: catalog and registration confirmed.

`internal/contract/baseline.go` instructs the agent to execute devtools directly. Its 26-tool catalog has no generic execution/start/process-inspection path. `baseline_test.go` explicitly asserts those interfaces are absent. `internal/service/mcp.go` registers no alternative generic execution handler.

A CLI installed inside a container is not an execution channel for a client connected only to the MCP endpoint. This was an intentional earlier delegation design, but it does not meet the adopted product goal. Reintroducing one wrapper per devtools command would reproduce the wrong abstraction.

Acceptance: an MCP-only client must discover, start, inspect, cancel, and collect results for builds, tests, dependency installation, and development servers without a second host shell. Add that interface only on top of the corrected execution boundary. Toolchain provisioning and host administration must remain outside arbitrary job authority.

## R3 — Configuration silently ignores settings and coerces invalid integers

Priority: P1. Evidence: reproduced.

Containment status: `internal/config` now owns an explicit top-level setting allowlist and rejects unsupported or obsolete keys before building an effective Config. Integer limits accept only TOML integer scalars within range; numeric strings and floating-point values are not coerced or truncated. The current Go configuration and valid GitHub fragment merge remain positive regressions, while the Python 0.47 fixture is retained as an intentional negative compatibility test.

At the reviewed baseline, `Parse` consumed selected entries from a generic TOML map without rejecting unused keys. Legacy `max_processes`, `[executables]`, and `[checks]` were silently ignored; the misspelled `max_output_byte = 8192` was accepted while the effective limit remained 262144; `max_output_bytes = 8192.9` was truncated to 8192.

Remaining correction: later control-plane work still needs effective-policy inspection and transactional configuration generation/apply semantics so an operator can see exactly what is enforced and a failed change cannot disturb the active generation. The reproduced silent-ignore/coercion path itself is now covered by permanent `internal/config` regressions and is retired from readinessprobe.

## R4 — Managed platform credentials are ordinary mutable/injectable profiles

Priority: P0. Evidence: reproduced with a synthetic vault; live credentials were not read.

Containment status: the application-secret boundary now reserves managed profiles independently of provisioning state, hides them from ordinary discovery, and rejects generic read/write/import/delete/environment selection. Trusted consumers use a closed managed-credential identifier and boolean availability API. This closes the reproduced R4 application-API path; full process/store separation and workload-scoped grants remain A04/S04 work.

At the reviewed baseline, `internal/service/github.go` stored the managed GitHub App key under `github-app/PRIVATE_KEY` using an arbitrary profile/key managed-secret API. The encrypted document used the same map shape as ordinary application secrets, while the generic profile/value APIs did not enforce the intended reservation.

The synthetic baseline probe selected that managed entry through `ResolveEnvironment`, overwrote it through the Agent-permitted public-value path, and deleted its profile. The devtools path also accepted arbitrary profile/name secret selection. These behaviors are now permanent negative regressions rather than readiness observations.

Original impact: the vault-backed platform key lacked a protected application namespace and could be damaged or selected as workload environment data. A deployment using only a separately mounted key file had different exposure; the probe did not assert that a real deployed PEM was extracted or that every deployment used the vault-backed key.

Remaining correction: A04/S04 must move platform credentials, application secrets, and public configuration into distinct ownership/store boundaries and bind application-secret use to host-owned grants plus a concrete workload. Environment merge policy must also reserve execution-critical names such as loader variables, broker addresses, and policy-controlled proxy settings. The current containment intentionally does not claim those process-level guarantees.

## R5 — A read-looking Git request executes project code in the MCP context

Priority: P0. Evidence: local code execution reproduced; deployment consequences traced statically.

At the reviewed baseline, `Diff` disabled external diff but still allowed textconv. Follow-up synthetic controller probes also showed repository `core.fsmonitor` execution from status/diff/stage and executable clean-filter invocation from diff/stage. Those project-selected programs therefore ran from the Git controller's process context rather than a workload sandbox.

Containment status: repository Git execution now lives under `internal/work/workspace/git` and runs through the confined Job/executor path rather than the MCP/control process. The adapter still disables paging, repository hooks and fsmonitor through controller-owned options; diff disables textconv and external diff; executable clean/smudge/process filters fail closed before Git can invoke them. Permanent regressions cover raw-byte transport, exact launcher-owned input mounts, deleted/renamed paths, linked worktrees, repository identity and staged-state CAS.

The workspace facade now lives under `internal/work/workspace`, while the workspace/Git MCP bindings live under `internal/transport/mcp/workspace`. `internal/service` composes those facades but no longer imports the private Git adapter; the corresponding service-to-workspace/Git migration exceptions were removed from archcheck. Integrated A08 acceptance remains open until the final deterministic gate is recorded.

Authority-boundary status: internal RPC no longer infers administrative authority from PID, cgroup paths, or service-unit names. Role composition now resolves trusted Unix peer credentials into typed principals: UID 0 is HostAdministrator, the configured Agent UID is Agent only, and other peers are unknown. Operations declare explicit control-policy grants, with unset/unknown grants failing closed; permanent control/RPC regressions cover the former same-UID elevation path. This establishes the first A02/S02 authority slice but does not yet provide workload/resource-scoped grants.

The fundamental correction remains to execute project-controlled programs only in the work-only Job model. General Git commands, intentional filters/hooks/project scripts and their children belong there. A process must not acquire gateway/host identity merely by inheriting a UID, PID, cgroup, or resource identifier.

## R6 — Declared network profiles are not isolated workload authorities

Priority: P0 for the intended cross-path security guarantee. Evidence: source and Compose topology confirmed; no live egress bypass attempted.

`internal/execution/contract.go:EnvironmentForNetwork` sets proxy environment variables. It does not create an enforced network boundary. Compose gives runtime and MCP access to the same private network as the dependency egress proxy. That proxy listens on all container interfaces and selects `dependency-install` once at startup; `internal/egress/proxy.go` does not authenticate the caller or select a workload-specific grant.

Consequently, omitting proxy variables from `runtime-default` is not evidence that a process cannot explicitly connect to the reachable dependency proxy. The declared `AllowSecrets` distinction is not, by itself, an enforcement mechanism. This finding concerns the already reachable dependency proxy, not the currently loopback-only browser proxy in R10. Merely fixing browser-proxy binding would add another reachability problem unless authority is separated at the same time.

Correction: network access must be tied to a trusted workload identity and host-owned destination grants, with no alternative reachable broad proxy. Workloads cannot reconfigure their network namespace, attach themselves to a broader network, or mint another workload's capability. Application code given a secret can read it; destination restrictions and redaction reduce risk but cannot truthfully promise that arbitrary secret-bearing code can never disclose it. Platform keys require brokered operations instead of raw injection.

## R7 — Cancellation does not own the complete process tree

Priority: P1, foundational for reliable execution. Evidence: reproduced and wiring confirmed.

`internal/process/run.go` creates a process group and signals its negative PID on timeout. The probe forked a child which created a new session and closed inherited output streams. `process.Run` reported timeout, but the detached descendant was still alive. The child self-terminated shortly afterward; no unrelated process was signaled.

`ContainerSupervisor` has graceful-termination unit tests, but source search found its construction only in tests. Production `process.Run` defaults to `SystemdSupervisor`, and GitHub/devtools calls do not consistently wire a different supervisor. Container-level limits are not per-job ownership or a guarantee of immediate per-job cleanup.

Correction: own a complete job sandbox/cgroup and use a persistent job ID, not only a PID/process group. Define launch, running, stopping, exited, timed-out, interrupted, and outcome-unknown states. Cancellation, request disconnect, gateway restart, OOM, detached descendants, inherited pipes and concurrent starts need separate tested semantics. Keep policy generation and immutable executable selection attached to the job.

A03/A05/A06/A07 implementation status: project execution now uses the durable `work/jobs` journal, remote executor and privileged launcher chain, with public MCP start/inspect/output/cancel bound only to the executor. Admission and the original execution deadline are durable before OCI provisioning; exact workload/gateway/network identity is bound before a Job becomes running, and terminal result reads remain non-consuming. Logical network selection is limited to `none` or `dependency-install`, dependency egress is mediated by the per-Job authenticated gateway, and declared development endpoints are published by that gateway to loopback-only ephemeral host ports. Neutral endpoint leases are journaled against the exact Job incarnation and are released on terminal cleanup; recovery rejects replacement resources or changed endpoint bindings. `preview_publish(action=job)` uses the lease rather than a bare port, so host-port reuse alone cannot retain the old share. Native/container packaging keeps MCP off the launcher socket and Docker authority off MCP/executor. Focused Job/preview/contract/packaging tests, archcheck, and the full deterministic closeout gate pass for the source slice. The real OCI suite now includes the A06 authenticated gateway/egress, endpoint publication/recovery, Job preview HTTP/WebSocket and stale/reused-lease scenario. The supported-host acceptance gate remains open only until `scripts/accept-loki-oci-jobs.sh` records a pass on a supported disposable Linux Docker host; the runner now provisions the normal socket-derived peer identity, temporary workspace, digest-addressable fixture image, local registry, and allowlisted authority automatically. R7 also remains a separate legacy `internal/process` readiness observation until project-controlled execution no longer relies on that unsandboxed path.

## R8 — ZIP extraction can write outside its staging directory

Priority: P0 for distribution/toolchain installation. Evidence: reproduced with an offline synthetic archive.

`internal/work/toolchains/install.go:extractZip` checks link targets lexically but creates subsequent entries using path-based filesystem operations that follow symlink parents. A small archive with chained relative symlinks and a regular file was accepted and wrote a marker into a sibling directory outside the intended extraction staging directory. The entire fixture, including that sibling, was inside a disposable test root.

The fixture had the checksum supplied by its test manifest. Checksums identify bytes; they do not make a dangerous archive safe to extract. This is not a demonstration of replacing a trusted release remotely. It is a missing containment property in the privileged installation primitive.

Correction: one traversal-resistant extraction layer for zip/tar with pinned-root operations, bounded entries/expanded bytes/time, explicit mode/type policy, and delayed or tightly checked symlink publication. Reject symlink chains that escape after actual resolution, not just lexical cleaning. Test parent replacement, duplicate paths, hardlinks, absolute paths, devices/FIFOs, archive bombs, truncated streams and interrupted installs. Extraction should run without deployment/credential privileges and promote only a validated tree.

## R9 — Installation permissions depend on the installer's umask

Priority: P1. Evidence: reproduced.

A ZIP executable declared as 0755 is installed as 0700 under umask 0077. `extractZip` supplies mode to OpenFile but does not subsequently enforce the intended final mode. A root-installed executable with that mode will not be executable by the runner. Directory creation also needs explicit cross-UID access validation.

The existing `TestInstallZipArtifactPreservesExecutables` failure is therefore meaningful. Normalizing only the test process umask would hide the product risk. Enforce validated final modes, verify the intended runner can traverse/read/execute, and test both umask 0022 and 0077. Reinstall and doctor must diagnose inaccessible or drifted trees rather than relying only on archive hashes.

## R10 — Compose carries incorrect native loopback and process-namespace assumptions

Priority: P1. Evidence: source/topology confirmed; real Compose behavior remains to be exercised.

`cmd/loki/browser_proxy.go` binds only 127.0.0.1 and exposes no container-listen option. Compose runs browser and browser-proxy as different containers, while Chromium connects to `http://browser-proxy:18767`. A loopback healthcheck inside the proxy can pass while this path is unreachable.

There are additional independent problems: the proxy's runtime inspection request uses UID 10002, while runtime Agent operations allow the configured UID 10000; local port resolution returns the proxy container's own 127.0.0.1. Preview forwarding also targets 127.0.0.1 from MCP, while the current devtools process launches in runtime. `service/previews.go` may inspect a runtime-reported PID under MCP's unrelated `/proc` namespace.

Correction: bind/listen policy and network endpoint identity must be explicit in deployment composition. Identify published services by job-owned endpoint lease and network location rather than a bare port or cross-container PID. Browser local access, preview HTTP/WebSocket, restart, expired ownership, and port reuse must be tested through the real client path. Do not fix this by sharing host networking/PID namespaces or widening broker permissions wholesale.

Current source status: sandboxed Job previews now use `preview_publish(action=job)` and exact endpoint leases; the proxy revalidates Job ID, lease ID, host port and active state on every request. The legacy `server`/`stack` preview variants remain for runner-owned non-Job development servers and continue to use workspace-port validation. Container/native packaging now has explicit executor/launcher roles and no direct MCP-to-launcher socket path. This resolves the A06 identity model in source, but R10 is not acceptance-closed until a real Compose/OCI fixture proves the cross-container HTTP/WebSocket path and stale/reused-port denial.

## R11 — Most MCP operations silently discard unknown input fields

Priority: P1. Evidence: reproduced over in-memory MCP transports with stub handlers.

Containment status: the generic MCP wrapper now rejects every unknown tool argument before defaults or handler execution; no per-tool exceptions remain. Typed runtime RPC decoding removes only the transport-owned `operation` field, then rejects unknown operation-specific fields, wrong scalar types, and trailing JSON before domain execution. Permanent regressions verify that a misspelled Git index precondition never reaches the handler and that invalid runtime input cannot mutate secret state.

At the reviewed baseline, `internal/mcpserver/server.go:wrap` deleted unknown keys before schema validation except for two GitHub tools. A `git_stage` request with a misspelled expected-index field therefore reached the stub handler with `expected_index_sha256 = null`. The typed runtime decoder likewise ignored unknown operation-specific fields. No real index mutation was performed by the baseline probe.

Current contract status: the generated Go catalog now uses action-specific discriminated schemas for every action union, descriptions for every public input property, closed output schemas and complete MCP annotations. The public `job` surface is typed for start/inspect/output/cancel, and `preview_publish` has distinct server/stack/job branches; the obsolete accepted-but-unused `environment_routes` field is gone. The reproduced silent-discard path remains covered by permanent `internal/mcpserver`, `internal/rpc`, `internal/service`, and contract-audit regressions. Remaining contract work is narrower: common operation/correlation-result identity, end-to-end stable error-envelope coverage where still missing, model-facing tool-choice evidence, and acceptance of the newly exposed Job/network path.

## R12 — Lifecycle failure reporting and state publication are not transactional

Priority: P1. Evidence: source-confirmed; destructive lifecycle commands were not run.

The former `scripts/loki-compose-lifecycle.sh` transaction engine is retired by A11/S06. Compose remains a portable topology/isolation contract, while installation, backup, restore, update, rollback and optional-component mutation run through the Go host manager with one durable journal, recoverable lock, explicit job-drain policy and truthful recovery outcomes.

Only runtime-state and runner-state are archived by the current primitive. External configuration, selected image/Compose assets, token files, signing keys and optional-component state are not a complete versioned recovery set merely because this backup exists. Some may intentionally remain externally managed, but the recovery contract must declare and verify that dependency.

Correction: journal desired/prepared/active generations, asset digests, policy revision and recovery coverage. Distinguish apply-failed/recovered from apply-failed/recovery-failed. Retain both errors and a usable recovery command. Do not implicitly kill active work or claim complete rollback after restoring only part of the required state. Test failures at every publication/stop/start/migrate/recover boundary using isolated fixtures and genuinely different releases.

## R13 — Recovery and concurrency guarantees have a narrower scope than the product needs

Priority: P2, required within state/job redesign. Evidence: source-confirmed limitations, not a reproduced data-loss incident.

The encrypted Store uses good locking and atomic publication, but initialization writes key and store separately; interruption can leave a rejected partially initialized state. Application controllers often call Update with no expected revision. Secret import commits values before deleting the staged input, so cleanup failure can produce an error after a successful mutation. RPC can likewise execute an operation before response encoding/size failure.

File and Git mutexes protect cooperating controller calls, not arbitrary external writers. A successful pre-read hash check is not a universal filesystem CAS. Git Checkpoint records tracked changes and only the names of untracked files, not their contents or a full independent index/worktree restoration image. That is correctly stated in its comment but must constrain user-facing recovery claims.

Correction: transactional initialization/recovery, explicit mutation outcomes and idempotency where possible, operation reconciliation after ambiguous completion, and accurate snapshot coverage. Concurrent work in a shared workspace either accepts documented last-writer risks or uses isolated worktrees/snapshots plus explicit integration. Do not promise full rollback or mandatory CAS that another permitted execution path can omit.

## R14 — Error handling and protected-resource constants retain avoidable defects

Priority: P2. Evidence: reproduced at constructor/validation level.

Constructor containment status: `browser.NewDriver` checks URL parsing errors before reading the parsed proxy and returns a static validation error without exposing the input. Managed native/Compose HTTP endpoints remain supported; credentials, paths, queries (including an empty `?`), fragments (including an empty `#`), unsupported hosts/protocols and invalid ports are rejected. Deterministic and fuzz regressions in `internal/integrations/browser/proxy_validation_test.go` replace the constructor observation and verify that construction does not create browser process/filesystem state.

Protected-port containment status: port protection no longer depends on Python-era 8765/8766/8767 constants. Loki builds an explicit policy from the active MCP `config.Port` and the unique proxy ports in the validated execution contract. Native port-guard, runtime Docker inspection, MCP preview/system fallback, and browser-local authorization all use that policy or the runtime callback backed by it, and protected requests fail before native/Docker ownership fallback. Go defaults are aligned to 18765/18766/18767, but changing a valid administrator-owned MCP/proxy port changes the protected set without editing source constants. Permanent regressions cover direct guard/Docker rejection, fallback non-invocation, runtime RPC rejection, browser callback authorization, and native/container layout inputs; the synthetic protected-port readiness observation is retired.

At the reviewed baseline, `browser.NewDriver` dereferenced a failed URL parse and the global port validator protected only Python-era service ports while accepting Go's MCP port. Those reproduced defects are now contained. Browser startup still disables Chromium's sandbox, and service/cgroup identity remains distributed across the pre-redesign architecture; those concerns belong to the broader browser/execution and control-plane ownership work rather than the retired constructor/port observations.

Remaining correction: keep constructor/parser validation fail-closed, move protected endpoint ownership into the planned job/endpoint authority model, and use workload identity rather than legacy service-name suffixes. Keep browser sandboxing enabled where the supported host can enforce it; document and independently assess any exception.

## R15 — Release acceptance can pass without proving the advertised workflow

Priority: P1. Evidence: acceptance source and current test results confirmed.

`scripts/accept-loki-compose.sh` checks health, networks, mounts, a directly executed devtools version, and selected lifecycle operations. It does not issue a real MCP development request, navigate through the browser path, or verify an actual signature. Its ordinary upgrade case retags the same image, so it does not prove switching code or migrating between different state schemas. Optional tests are skipped when inputs are absent.

Nine integration tests were skipped in both full test runs. Several catalog tests intentionally preserve R2/R11-era behavior. A count of passing tests or a healthy Unix socket is not evidence of a complete user workflow.

Correction: separate cheap unit, real integration, adversarial cross-path, and release gates. Missing required fixtures in the release gate must fail, not silently skip. Exercise fresh hosts, actual MCP-only workflows, optional feature use, two distinct digests, interrupted jobs, denied routes and recovery failure. Record exact source/image/policy identities for each result.

## R16 — Boundary ownership, retained code and resource budgets need simplification

Priority: P2. Evidence: source review; individual risks below are not all reproduced failures.

The service/runtime layer aggregates process launch, secrets, GitHub commands, ports, audit and deployment layout. Many domain methods exchange `map[string]any`, marshal/unmarshal again at the transport, and use generic public strings. The configuration, execution contract, deployment contract, static catalog, Compose file and shell scripts duplicate invariants. This makes it possible for independently passing parts to disagree operationally.

The [package-structure follow-up](project-structure-plan.md) recorded the reviewed baseline graph with `go list -mod=readonly -json ./...`: 36 package directories, 85 direct in-module import edges and 33 immediate internal directories. At that baseline, `service` contained 23 production files and imported 24 internal packages; `cmd/loki` imported 21, and the graph included `auth -> browsernet -> portguard`, `devtools -> secret -> state`, and ordinary file/Git revision code importing `state.AtomicWrite`. S02 has since removed both misplaced shared-mechanism paths: destination validation/dial/proxy mechanics now live in `internal/platform/netguard`, and trusted-parent private atomic publication now lives in `internal/platform/safeio`, so Git checkpoints and workspace revisions no longer import the encrypted state package. A06 has also landed the reviewed workload network/endpoint grant model in `work/jobs` plus the sandbox gateway/resource domain. Broader safeio extraction and the remaining `devtools -> secret -> state` ownership problem are still future work. The baseline examples identify misplaced ownership rather than proving every high-fan-out package is wrong. The follow-up specifies responsibility groups, private feature internals, package target owners, role dependency checks and S01-S06 structural slices that accompany the functional A work units. Package enforcement starts early; it is not deferred until final cleanup.

The unused raw Docker forwarding session has been removed. The retained `internal/dockerproxy` code is inspection-only and is no longer classified as launcher authority. A03-A07 now have a separated `internal/platform/sandbox` foundation, privileged `app/launcher` + `cmd/loki-launcher` boundary, backend-neutral `work/jobs` core and remote adapters, unprivileged `app/executor` + `cmd/loki-executor` role, and a public MCP `job` binding that reaches only the executor. Public Job requests can add the reviewed logical network profile and bounded endpoint declarations, but still cannot supply job IDs, policy digests, OCI image/mount/security authority, Docker network identity, gateway credentials, host ports or launcher/backend refs. The sandbox keeps a fixed non-root/read-only OCI envelope and conditionally creates the trusted gateway/network domain required for authenticated egress or endpoint publication. Native/container packaging reserves Docker and the launcher socket for the launcher/executor chain rather than exposing them to MCP. Architecture checks pass for these role roots. Real supported-host OCI/network acceptance remains required before treating the boundary as release-complete. The signing proxy still exposes generic SSH signing requests, not a universal guarantee that every signature represents an approved Git commit.

List pagination first reads/stats an entire directory; small text reads load and split an entire bounded file. Audit append, checkpoint retention and histories need installation-level disk budgets. Output is frequently duplicated as both text JSON and structured JSON. These are maintainability, latency, disk and token-efficiency issues; they should be measured and simplified without adding speculative distributed infrastructure.

Correction: assign one owner to each domain and each privilege, use narrow typed interfaces at real boundaries, add per-job and installation-wide resource budgets, and retain only components with a current use and a verified enforcement contract. No global event bus, general plugin VM, or distributed control plane is needed to address these findings.

## Improvements from the Go port worth preserving

The port is not uniformly a line-for-line translation. Preserve and strengthen the following implementations:

- `policy.Workspace.Open` and pinned-parent writes use openat2/renameat2 to avoid common traversal and link-replacement errors within their scope.
- Encrypted state uses authenticated encryption, private files, advisory file locking, fsync and atomic replacement. Improve transaction/credential ownership around it rather than inventing cryptography.
- Port termination uses pidfd and start-time revalidation instead of blindly signaling a recycled numeric PID.
- Browser traffic validates resolved public addresses and dials those addresses; the CDP pipe has bounded messages and pending work.
- GitHub token issuance restricts repository targets, limits responses and rejects redirects. These controls should remain inside a dedicated credential boundary.
- Artifact links are high-entropy, bounded, temporary and revocable, with defensive response headers. Clarify their public capability-link policy and restart lifetime rather than mixing them with transport authentication.

These are positive implementation observations, not proof that the surrounding integration is secure.

## Handoff and release gate

Begin the remediation sequence in the [improvement plan](architecture-improvement-plan.md), not by expanding the public installer. P0 authority/extraction issues and the generic-execution boundary must be addressed before enabling broader workloads. P1 issues must be resolved before their milestone is released. Each change needs a negative test reproducing the defect and a positive test showing ordinary development still works.

The review/plan deliverable is complete at this baseline, with explicit limits. Remediation, clean-host acceptance, operational cutover and legacy retirement are not complete. Recheck source observations after implementation changes; do not use this report as evidence about a later commit or the running Python deployment.

## Primary technical references

- [Go traversal-resistant file APIs](https://go.dev/blog/osroot): descriptor/root-based access and TOCTOU limits.
- [Git diff documentation](https://git-scm.com/docs/git-diff): external diff drivers and text conversion are separate mechanisms.
- [Linux cgroup v2 documentation](https://docs.kernel.org/admin-guide/cgroup-v2.html): process hierarchy, resource ownership and cgroup.kill.
- [Docker security](https://docs.docker.com/engine/security/): daemon authority and container isolation responsibilities.
- [Docker networking](https://docs.docker.com/engine/network/): container network isolation and explicit network connectivity.
- [The Update Framework specification](https://theupdateframework.github.io/specification/latest/): authenticated metadata, rollback/freeze protection and key-role separation for the proposed distribution design.
