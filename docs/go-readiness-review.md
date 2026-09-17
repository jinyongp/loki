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
| Workspace and Git | `internal/policy`, `internal/workspace`, `internal/gitops` | Descriptor-based path safety, nested repositories, deletion/move, filters, CAS scope, checkpoints and output work budgets |
| Credentials and state | `internal/secret`, `internal/state`, `internal/admin`, `internal/auth`, `internal/audit`, `internal/githubapp` | Managed versus application secrets, injection, parser semantics, transactions, token scope and audit/recovery claims |
| Components and connectivity | `internal/browser`, `internal/cdp`, `internal/browsernet`, `internal/egress`, `internal/portguard`, `internal/previews`, `internal/artifacts`, `internal/signing`, `internal/dockerproxy` | Cross-container endpoints, egress profiles, browser lifecycle, port ownership, sharing and broker authority |
| Distribution and verification | `compose.yaml`, `packaging/`, `scripts/`, `internal/toolchain`, `internal/packaging`, `internal/e2e` | Archive installation, ownership, release integrity, lifecycle state, actual acceptance coverage and skipped tests |
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

The four failing packages are `internal/auth`, `internal/daemon`, `internal/toolchain`, and `internal/packaging`. Auth/daemon failures expose test assumptions about permissions under umask 0077. Compose lifecycle tests require unavailable `setfacl`. The toolchain executable-mode failure has a reproduced product consequence, described in R9; it must not be dismissed as merely an environment mismatch.

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

`internal/gitops/git.go` requires the requested path to exist for `MutatePaths("stage", ...)` and path-filtered `Diff`. A tracked file renamed away consequently fails with `no such file or directory`. An unfiltered diff and native explicitly scoped Git staging succeed. The same existence requirement is present in `legacy/python/src/loki_mcp/tools.py`; the port inherited the defect.

A second reproduction places a repository under an ordinary workspace directory. `internal/workspace/search_patch.go` runs `RemoveTracked`'s `git ls-files` from the workspace root instead of the owning repository, then reports the nested tracked file as untracked. Its error recommends quarantine even though repository identity, not Git tracking, is the actual failure.

Regression scenarios: deleted files and deleted parents; renames; nested repositories; worktrees with in-bound metadata; unborn indexes; literal pathspecs; unrelated staged hunks; unknown absent paths; in-bound and out-of-bound symlinks; filtered diff of deleted paths. Introduce one repository identity/path model rather than changing a boolean independently in each handler.

## R2 — The Go MCP endpoint cannot carry the intended development workflow

Priority: P1, prerequisite to a usable release. Evidence: catalog and registration confirmed.

`internal/contract/baseline.go` instructs the agent to execute devtools directly. Its 26-tool catalog has no generic execution/start/process-inspection path. `baseline_test.go` explicitly asserts those interfaces are absent. `internal/service/mcp.go` registers no alternative generic execution handler.

A CLI installed inside a container is not an execution channel for a client connected only to the MCP endpoint. This was an intentional earlier delegation design, but it does not meet the adopted product goal. Reintroducing one wrapper per devtools command would reproduce the wrong abstraction.

Acceptance: an MCP-only client must discover, start, inspect, cancel, and collect results for builds, tests, dependency installation, and development servers without a second host shell. Add that interface only on top of the corrected execution boundary. Toolchain provisioning and host administration must remain outside arbitrary job authority.

## R3 — Configuration silently ignores settings and coerces invalid integers

Priority: P1. Evidence: reproduced.

`internal/config/config.go:Parse` consumes selected entries from a generic TOML map without rejecting unused keys. A document containing legacy `max_processes`, `[executables]`, and `[checks]` parses successfully and produces exactly the default Config. The typo `max_output_byte = 8192` is accepted while the effective limit remains 262144. `max_output_bytes = 8192.9` is also accepted and truncated to 8192.

This is not semantic compatibility. With compatibility no longer required, reject obsolete/unknown fields directly and require integer types for integer settings. Effective-policy inspection must report what will actually be enforced, without disclosing credentials. A failed configuration change must leave the active generation unchanged.

## R4 — Managed platform credentials are ordinary mutable/injectable profiles

Priority: P0. Evidence: reproduced with a synthetic vault; live credentials were not read.

`internal/service/github.go` stores the managed GitHub App key under `github-app/PRIVATE_KEY` using `SetManagedSecret`. `internal/secret/vault.go` represents that as the same map structure as ordinary application secrets; the word "reserved" in comments is not enforced by the generic profile/value APIs.

A fake managed entry was successfully selected by `ResolveEnvironment`. The Agent-permitted `public_value_set` handler overwrote it, and the Agent-permitted `profile_remove` handler deleted the profile. The `devtools_call` Agent operation selects secrets by profile/name, and its broker contains no managed-profile exclusion before environment resolution.

Impact: the vault-backed platform key has neither a protected namespace nor a distinct operation authority. General secret manipulation can damage it, and the selection path permits treating it as an application environment variable. A deployment using only a separately mounted key file has different exposure; this probe does not assert that a real deployed PEM was extracted or that every deployment uses the vault-backed key.

Correction: distinct typed credential classes/stores and controllers for platform credentials, application secrets, and public configuration. Platform credentials are never listable, writable, removable, importable, or selectable through application-secret operations. Application-secret usage is bound to host-owned grants and a concrete workload, not an arbitrary profile name alone. Reserve critical execution environment names; merging secrets must not override PATH, loader variables, broker addresses, or policy-controlled proxy settings.

## R5 — A read-looking Git request executes project code in the MCP context

Priority: P0. Evidence: local code execution reproduced; deployment consequences traced statically.

The Git controller's `Diff` uses `--no-ext-diff` but not `--no-textconv`. A disposable repository's textconv command wrote a synthetic marker when the Go controller performed a diff. Git documents these as separate switches.

`internal/service/mcp.go` constructs this controller inside the MCP service; `internal/gitops/git.go` calls `process.Run` without a separate Identity or workload sandbox. The Compose MCP process has its transport token and broker sockets mounted. Code run by a repository-controlled Git extension is therefore not necessarily executing with a work-only view of the world.

`internal/rpc/rpc.go:Authorized` also treats every matching AgentUID as the same Agent principal and treats a matching MCP cgroup suffix as administrative authority. A synthetic same-UID peer with that suffix passed Administrative authorization. Native/Compose cgroups differ, so this result is not a claim that the current Compose deployment permits every administrative request.

The fundamental correction is to stop executing project-controlled programs in the gateway/controller security context. Harden inspection commands, but do not rely on a growing list of Git flags as the only sandbox. Git hooks, filters, fsmonitor, local configuration, project scripts and their children must run in the same work-only execution model. A process must not acquire gateway/host identity merely by inheriting its UID or cgroup.

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

## R8 — ZIP extraction can write outside its staging directory

Priority: P0 for distribution/toolchain installation. Evidence: reproduced with an offline synthetic archive.

`internal/toolchain/install.go:extractZip` checks link targets lexically but creates subsequent entries using path-based filesystem operations that follow symlink parents. A small archive with chained relative symlinks and a regular file was accepted and wrote a marker into a sibling directory outside the intended extraction staging directory. The entire fixture, including that sibling, was inside a disposable test root.

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

## R11 — Most MCP operations silently discard unknown input fields

Priority: P1. Evidence: reproduced over in-memory MCP transports with stub handlers.

`internal/mcpserver/server.go:wrap` deletes unknown keys before schema validation except for two GitHub tools. A `git_stage` request with a misspelled expected-index field succeeded against the stub handler; the actual `expected_index_sha256` default became null. No real index mutation was performed by this probe.

This turns a caller's intended concurrency precondition into no precondition without an error. Captured catalog JSON, Go input structs, handler maps and compatibility wrappers are separate sources of behavior. Generic dictionary output schemas provide little assurance about real results.

Correction: reject unknown input/config fields, validate discriminated action-specific requests, define typed result/error contracts, and generate catalog/schema/docs from reviewed definitions. Snapshots are regression artifacts, not the canonical reason an old behavior must remain. Mandatory preconditions are explicit; public errors carry stable codes, operation IDs and retryability instead of generic advice to retry every failure.

## R12 — Lifecycle failure reporting and state publication are not transactional

Priority: P1. Evidence: source-confirmed; destructive lifecycle commands were not run.

`scripts/loki-compose-lifecycle.sh` attempts rollback with `restore_payload ... || true` and then reports that the previous state/release was restored. Recovery can therefore fail while the message asserts success. Image, previous-image and rollback-backup are separate state publications. Backup stops services; update/restore have no durable job-drain or operation journal. A directory lock can be stranded after an untrappable interruption.

Only runtime-state and runner-state are archived by the current primitive. External configuration, selected image/Compose assets, token files, signing keys and optional-component state are not a complete versioned recovery set merely because this backup exists. Some may intentionally remain externally managed, but the recovery contract must declare and verify that dependency.

Correction: journal desired/prepared/active generations, asset digests, policy revision and recovery coverage. Distinguish apply-failed/recovered from apply-failed/recovery-failed. Retain both errors and a usable recovery command. Do not implicitly kill active work or claim complete rollback after restoring only part of the required state. Test failures at every publication/stop/start/migrate/recover boundary using isolated fixtures and genuinely different releases.

## R13 — Recovery and concurrency guarantees have a narrower scope than the product needs

Priority: P2, required within state/job redesign. Evidence: source-confirmed limitations, not a reproduced data-loss incident.

The encrypted Store uses good locking and atomic publication, but initialization writes key and store separately; interruption can leave a rejected partially initialized state. Application controllers often call Update with no expected revision. Secret import commits values before deleting the staged input, so cleanup failure can produce an error after a successful mutation. RPC can likewise execute an operation before response encoding/size failure.

File and Git mutexes protect cooperating controller calls, not arbitrary external writers. A successful pre-read hash check is not a universal filesystem CAS. Git Checkpoint records tracked changes and only the names of untracked files, not their contents or a full independent index/worktree restoration image. That is correctly stated in its comment but must constrain user-facing recovery claims.

Correction: transactional initialization/recovery, explicit mutation outcomes and idempotency where possible, operation reconciliation after ambiguous completion, and accurate snapshot coverage. Concurrent work in a shared workspace either accepts documented last-writer risks or uses isolated worktrees/snapshots plus explicit integration. Do not promise full rollback or mandatory CAS that another permitted execution path can omit.

## R14 — Error handling and protected-resource constants retain avoidable defects

Priority: P2. Evidence: reproduced at constructor/validation level.

`browser.NewDriver` calls Hostname on the parsed proxy before checking the URL parse error; a malformed configured proxy such as an invalid percent escape panics instead of returning a validation error. Browser startup also disables Chromium's sandbox, making the outer browser worker boundary especially important; changing that flag requires a real clean-environment test rather than assumption.

`portguard.Validate` rejects Python-era 8765/8766/8767 but accepts Go's 18765. The probe confirms the validator inconsistency, not that a real core process was terminated: ownership checks are additional restrictions. Service/cgroup names are similarly spread across policy, manifests and native/Compose layouts.

Correction: validate constructors before dereferencing, fuzz untrusted parsers and typed requests, derive protected endpoints from active control-plane ownership, and use workload identity rather than legacy service-name suffixes. Keep browser sandboxing enabled where the supported host can enforce it; document and independently assess any exception.

## R15 — Release acceptance can pass without proving the advertised workflow

Priority: P1. Evidence: acceptance source and current test results confirmed.

`scripts/accept-loki-compose.sh` checks health, networks, mounts, a directly executed devtools version, and selected lifecycle operations. It does not issue a real MCP development request, navigate through the browser path, or verify an actual signature. Its ordinary upgrade case retags the same image, so it does not prove switching code or migrating between different state schemas. Optional tests are skipped when inputs are absent.

Nine integration tests were skipped in both full test runs. Several catalog tests intentionally preserve R2/R11-era behavior. A count of passing tests or a healthy Unix socket is not evidence of a complete user workflow.

Correction: separate cheap unit, real integration, adversarial cross-path, and release gates. Missing required fixtures in the release gate must fail, not silently skip. Exercise fresh hosts, actual MCP-only workflows, optional feature use, two distinct digests, interrupted jobs, denied routes and recovery failure. Record exact source/image/policy identities for each result.

## R16 — Boundary ownership, retained code and resource budgets need simplification

Priority: P2. Evidence: source review; individual risks below are not all reproduced failures.

The service/runtime layer aggregates process launch, secrets, GitHub commands, ports, audit and deployment layout. Many domain methods exchange `map[string]any`, marshal/unmarshal again at the transport, and use generic public strings. The configuration, execution contract, deployment contract, static catalog, Compose file and shell scripts duplicate invariants. This makes it possible for independently passing parts to disagree operationally.

The [package-structure follow-up](project-structure-plan.md) verified the current build graph with `go list -mod=readonly -json ./...`: 36 package directories, 85 direct in-module import edges and 33 immediate internal directories. `service` contains 23 production files and imports 24 internal packages; `cmd/loki` imports 21. The graph includes `auth -> browsernet -> portguard`, `devtools -> secret -> state`, and ordinary file/Git revision code importing `state.AtomicWrite`. These identify misplaced shared mechanisms and mixed composition, not proof that every high-fan-out package is wrong. The follow-up specifies responsibility groups, private feature internals, every current package's target owner, role dependency checks and S01-S06 structural slices that accompany the functional A work units. Package enforcement starts early; it is not deferred until final cleanup.

`dockerproxy/session.go` transparently forwards Docker API bytes; its own comment explicitly says it does not filter methods. No production caller of `dockerproxy.Start` was found in the current source search. Treat it as unused authority-expanding infrastructure to remove, not a currently exposed restricted container broker. Keep a narrow Docker inspector only if needed; never reconnect raw forwarding as the new sandbox launch API. The signing proxy similarly exposes generic SSH signing requests, not a universal guarantee that every signature represents an approved Git commit.

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
