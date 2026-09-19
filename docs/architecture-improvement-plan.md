# Loki architecture improvement plan

Status: proposed implementation architecture based on the 2026-09-17 [repository review](go-readiness-review.md). It is not a description of features already implemented.

## Objective and constraints

Build a client-neutral MCP work environment which is easy to install, useful for real development, and has resource/authority restrictions that apply independently of the chosen tool, interpreter, hook or subprocess. Backward compatibility with Python or the present Go API/configuration is not required. Preserving a flawed behavior or a snapshot test is not a reason to keep it.

Retain the established product choices: Compose for ordinary installation, user-scoped host management by default, equivalent runtime guarantees for system scope, explicit workspace selection, approved minimal Docker prerequisites, optional browser/signing, native Loki toolchain shims, and explicit prepare/apply updates. The number and names of internal service roles are implementation details and may change to enforce those guarantees.

Do not modify the running Python server during this work. Existing workspace contents, credentials, user edits and backups must not be lost as a side effect of redesign. A one-shot, offline importer may preserve operator data without maintaining old APIs or old runtime state formats forever. `legacy/python/` remains a recovery unit until actual cutover and explicit retirement.

Success is demonstrated behavior: an agent with only an MCP endpoint can edit, build, test, install approved dependencies, manage development processes, use enabled integrations and retrieve results while being unable to reach host administration or another component's private authority.

## Design stance

The review found working lower-level primitives surrounded by inconsistent composition. Reuse those primitives after adding their missing adversarial tests. Do not rewrite cryptography, invent a shell language, create an all-purpose plugin engine, add a distributed scheduler, or wrap every development CLI as a separate MCP API.

There are three different concepts which must remain distinct:

1. **Authority:** host-owned grants over resources and operations. It must be enforced regardless of interface.
2. **Execution availability:** approved images/toolchain families and selected versions. This is not proof that an interpreter cannot implement equivalent behavior.
3. **Workflow safeguards:** typed edits, expected hashes, structured results and checkpoints. These help prevent mistakes but are not universal guarantees unless every equivalent mutation is actually mediated.

One installation initially has one operator-controlled trust domain. A selected workspace can contain several repositories; mounting that whole workspace does not provide confidentiality between those repositories. More restrictive workload mounts can be issued by host policy, but multi-tenant isolation must not be claimed without a separately tested design.

## 1. Separate control authority from project execution

The target roles are:

```text
Host operator
  host CLI / protected desired state / release verification
                  |
          authenticated control configuration
                  |
MCP gateway -> operation dispatcher / authorization
                  |
            execution supervisor
                  |
       constrained sandbox launcher
                  |
        workload sandboxes and jobs
                  |
     scoped brokers: egress, toolchains,
     application-secret delivery, signing,
     Git-provider operations, browser/preview
```

This is a map of responsibilities, not a requirement to deploy a network microservice for every box. Roles with equivalent trust can share a process; roles separated to protect credentials must have separate OS identities and appropriate process, mount and network isolation.

### Gateway and application controllers

The gateway authenticates the connection, validates typed requests, attaches a trusted principal, dispatches operations and returns bounded results. It does not execute repository-controlled programs, load workspace plugins or inherit project environment variables. Its authentication material and administrative connections are not mounted into work processes.

Controllers receive a request context containing an authenticated principal, operation ID, registered workspace identity and effective policy generation. These fields originate in trusted control code, not arbitrary caller-provided JSON. Resource IDs are identifiers, not authorization by themselves.

An authenticated broker checks its own permitted operation/resource, even if the request came from another Loki component. Replace two-level UID-only permission checks and cgroup-name-derived administrator identity with explicit role authentication plus resource grants. UID/cgroup checks remain useful defense in depth, not the complete policy model.

### Workload executor and launcher

Use a single job/execution owner for generic commands and every operation capable of executing project code, including Git inspection with helpers and dependency hooks. Initially prefer clearly isolated OCI workloads for this path rather than executing inside the gateway or root vault container. Keep images/toolchain payloads cached so isolation does not imply reinstalling dependencies per command.

The trusted launcher consumes a constrained, validated WorkloadSpec derived from host policy. It accepts registered workspace IDs, approved image digests, read-only toolchain generations and bounded resource/network grants. It never accepts arbitrary Docker API calls, arbitrary host mounts, privileged mode, host PID/network sharing, device grants, added capabilities, or a caller-supplied security profile.

If the implementation uses Docker daemon access, only this minimal host-managed launcher receives it. Its compromise has serious host implications, so keep it separate from the gateway, project execution, secret decryption and generic CLI handling, and audit its finite interface. A transparent Docker socket proxy does not reduce daemon authority. The concrete backend may be rootless or daemon-mediated only when it passes the same supported-host isolation contract; backend choice does not relax the remaining sandbox controls.

User-scope installation refers to host management and ownership, not permission to bypass kernel/daemon isolation. Installation must diagnose the exact runtime prerequisites and must fail rather than silently falling back to an unsandboxed worker. A prototype of this enforcement boundary is the first implementation milestone, before the public installer grows around it.

Long-running project services are jobs with explicit lifetime, not hidden children of an MCP request. A reusable worker pool is an optimization only after tests establish complete cleanup, no credential carryover and policy-generation matching; it must not become a shortcut around the initial isolation model.

## 2. Make jobs the shared execution primitive

Use one job model for finite commands and development servers. A minimal MCP surface can expose start, inspect, output and cancel. Exact spelling is secondary; there must be no separate implementation for CLI, finite command, background process and secret-bearing process lifecycles.

A job record includes its stable ID, trusted owner, workspace, policy generation, resolved toolchain generation, sanitized command metadata, resource limits, creation/start/end times, exit/cancellation reason, output cursor and cleanup status. Never persist raw secret environment values or bearer capabilities in the public job record.

State progression distinguishes `queued`, `starting`, `running`, `stopping`, `exited`, `failed`, `timed_out`, `canceled` and `interrupted`. External side effects with uncertain completion require an `outcome_unknown` result rather than a false failure/success and an automatic retry.

Each job has a cgroup/process-container boundary and bounded CPU, memory, process count, output, wall time and temporary storage. Cancellation requests graceful shutdown first, then terminates the entire owned subtree. Completion includes cleanup verification; it is not merely reaping the first PID. Workload processes cannot migrate outside their assigned cgroup or create an authority-equivalent control process.

The MCP request's connection lifetime and job lifetime are distinct. Disconnecting an observer does not silently destroy a development server or orphan an untracked process. Explicit job deadlines and cancel operations decide termination. Gateway restart reconnects to supervisor-owned records; supervisor restart reconciles real sandbox state with its journal.

Output is bounded and cursor-based. Report truncated/expired ranges explicitly. Do not duplicate large output in JSON strings and structured objects by default. Redaction is best-effort output hygiene, never proof that arbitrary code receiving a secret cannot disclose it.

Devtools is the required, sole authority for Go Loki's canonical project-work coordination: workstreams, specifications/plans, tasks, dependencies, runs/claims, validation basis and task history. Its own lifecycle checkpoint/history records remain canonical for that coordination domain. The Go product has no Taskwarrior dependency, compatibility backend, dual-write path or fallback task store. The currently operated server's tools, installed versions and task store do not constrain this design; migrating that server's task data is outside this implementation scope.

Loki supplies authenticated MCP access, trusted project/profile mapping, safe session attribution and links to observed jobs, code revisions and artifacts. Task state and completion rules remain owned by devtools. Loki separately owns agent-session recovery context, current AGENTS.md/Skill resolution, and runtime/Git/evidence linkage because those depend on Loki's authenticated session and confined filesystem/runtime view. Loki context records may reference devtools IDs and revisions but never manufacture or replace canonical task state. Any cross-repository implementation is scoped separately.

Runtime Environment/Job lifecycle, confinement, resource budgets, cleanup and host authority remain Loki responsibilities. A devtools Task/Run and a Loki runtime Job have distinct identities and are linked explicitly; neither a claim nor an agent-context checkpoint grants runtime authority. The coordination store must not become the runtime supervisor journal, and the context journal must not become a parallel task database.

An MCP-only client reaches this functionality through a constrained integration within Loki. It need not understand a second transport or rely on an out-of-band shell, and the integration does not require one wrapper per devtools CLI command. Review the selected devtools command contracts and version its CLI protocol independently from the JSON envelope; packaging and acceptance validate the Go candidate's pinned dependency.

Shared context uses deterministic current-state assembly plus a Loki-owned durable journal of scoped, agent-authored explanatory checkpoints. Canonical devtools coordination, current repository/runtime evidence and current guidance remain authoritative; summaries carry their input revisions and cannot change those records. The implemented Go runtime persists this journal below its private StateDirectory (`/var/lib/loki-go/runtime/context` in the native layout), and MCP reaches it only through narrow runtime operations. Resume combines current facts, the relevant explanation, changes after its basis and source references. It remains usable when no checkpoint exists or when the client disappears before writing a final handoff. Conversation termination is not a reliable lifecycle callback, so work after the latest checkpoint is reconciled from canonical state and actual Git/workspace evidence; live Job evidence joins that composition when the unified Job service surface lands. A separate model service and automatic full-transcript collection are outside the initial contract.

Users can register and manage portable [Agent Skills](https://agentskills.io/specification) using standard `SKILL.md` directories and optional resources. The implemented native read path keeps project `.agents/skills`, user `~/.agents/skills`, and immutable packaged `/opt/loki/share/skills` as distinct source classes with precedence `project > user > packaged`. Native packaging backs the logical user path from durable `/var/lib/loki-go/runner/agents` and exposes it read-only to MCP; project/user mutation remains deferred until its separate mutation authorities exist. Loki provides bounded metadata discovery at project entry/resume, a reachable instruction to consult relevant skills, and on-demand full content/resource reads. Reuse interoperable distribution conventions, but do not require devtools-specific Skill inventory or runtime contracts. No proprietary Skill manifest, client-specific format or privileged runtime plugin loader is introduced.

Context restoration retains qualified Skill references and served revisions separately from lossy narrative compression, then reloads relevant allowed instructions for the new context. Previously delivering a Skill does not establish that it remains in an external client's context or that the agent followed it. Validate no-name activation and instruction adherence separately across declared native-file and MCP-only clients, including negative cases, updates and post-checkpoint resume. Skill content and optional tool hints do not expand runtime authority; registration and reading execute no scripts.

Project guidance also uses plain [AGENTS.md](https://agents.md/) files as canonical, path-scoped instructions. Loki resolves the approved ancestor chain through its confined filesystem authority for each actual work target, including new-file and rename destinations, rather than using cwd alone. Retain applicable parent guidance and let a closer file specialize conflicting project guidance within its subtree; sibling instructions remain separate. Return ordered original content, source paths, scope and revision evidence instead of compiling arbitrary prose into an invented rules schema. The agent reconciles semantic conflicts with the current user request and host instruction hierarchy; repository text cannot change enforced runtime permissions. Discovery stops at approved repository/workspace boundaries, distinguishes absence from unreadable or incomplete results, and executes no code.

Project entry, changed target scope, new/delegated sessions and post-checkpoint resume re-resolve current AGENTS.md guidance and Skill inventory alongside canonical devtools coordination and Loki recovery context. Preserve source/chain revisions separately from lossy explanations, including additions/removals and worktree differences; reread changed sources rather than replaying old summary text as current rules. Provide bounded complete content or explicit continuation/incomplete diagnostics, never silent instruction truncation. Canonical files remain user-editable Markdown under ordinary revision-guarded workspace editing, with no required frontmatter, proprietary registration file, forced scaffold or automatic history append. Validate path scope and rehydration deterministically and instruction adherence separately in declared client harnesses.

## 3. Split credential classes and bind their use

Use separate controllers and protected namespaces, with storage separation where it simplifies auditing:

- **Platform credentials:** MCP authentication, GitHub App private keys, signing keys and control-plane credentials. Agent application-secret APIs cannot enumerate, mutate, delete or select them for injection.
- **Application secrets:** opaque values the operator explicitly permits selected workloads to consume. Grants identify the workload/resource and permitted names, not just the fact that a caller can guess a profile name.
- **Public configuration:** ordinary non-secret values, with an API and validation separate from secret storage.

For platform integrations, expose the intended operation through a narrow broker. For application secrets, acknowledge that the receiving process can read the delivered value and may disclose it through its permitted outputs. Where that is unacceptable, use a brokered API operation rather than delivering the value to editable project code.

Validate the approved action/workload and its effective grants before resolving values. A workspace config cannot redirect a privileged process to a different executable, add a secret name, change a destination, or turn a normal job into a privileged one. Reserved execution variables such as PATH, loader controls, proxy routing and broker endpoints cannot be replaced by secret names or public config.

Any temporary secret delivery stays outside ordinary workspace files and public process records. Confirm isolation from sibling jobs and from gateway observation using fake canaries in a real disposable deployment. Global substring replacement and disabling a log flag are not acceptable substitutes.

For signing, distinguish protected key access from a policy that all published commits must be signed. A raw SSH sign capability is broader than a semantic Git operation; either expose the intended narrow operation or accurately document the broader grant. Mandatory publication checks must live at an enforceable publishing boundary, not only in mutable repository Git configuration.

## 4. Enforce network grants and address services by ownership

Retain closed-by-default execution. Dependency downloads and explicit application integrations receive their declared destinations through an enforcement path that authenticates the workload. Browser public-web access is a separate opt-in authority and must not become a general-purpose proxy accessible to unrelated jobs.

Proxy environment variables are convenience settings only. A job must remain unable to obtain more network access by removing them, addressing another proxy directly, following redirects, choosing an IP literal, using a child interpreter, or switching protocol. Validate DNS answers and connect to the validated destination; protect local/private/control endpoints explicitly.

Do not use a bare port or a numeric PID from another namespace as service identity. A managed endpoint lease binds workspace, job, sandbox/network location, port, protocol, policy and expiry. Browser-local navigation and previews resolve that lease through a trusted gateway. Reusing the numeric port for a new process cannot keep an old share authorized.

No component reads another namespace's `/proc/<pid>/environ` to infer configuration. The job owner supplies selected public metadata without returning the complete environment. Preview HTTP/WebSocket behavior, revocation, redirects and browser downloads are validated across the actual Compose topology.

## 5. Replace permissive compatibility contracts with typed definitions

Define reviewed request, result, error and configuration types. Use action-specific discriminated requests where actions have different mandatory fields. Reject unknown fields, misspellings, wrong scalar types and unsupported versions before any operation side effect. Do not silently drop a requested concurrency guard.

Generate MCP schemas, catalog metadata, examples and validation from one definition source, with explicit runtime semantic validation for paths, grants and state. Snapshot tests detect drift; they do not prescribe obsolete public behavior. Client-specific presentation metadata belongs in optional adapters, not the authority model or installation flow.

The public contract must be sufficient for an agent to choose and invoke the correct operation without relying on hidden implementation knowledge. The [MCP tool contract review](mcp-tool-contract-review.md) is the current catalog-level evidence and design record. Its current Go baseline has 31 tools: 18 action-multiplexed tools still use flat superset input shapes, only 10 of 153 captured input properties have field descriptions, 21 outputs remain open objects, four tools have no output schema, and two lack MCP annotations. `project_context`/`project_context_write` are implemented examples of explicit read/write separation and basis/CAS semantics, but the broader catalog redesign remains unfinished. Treat the remaining gaps as contract defects, not documentation polish.

Use action variants only when they share one resource domain and compatible authority/side-effect annotations. Represent each variant as a discriminated request with action-specific required and forbidden fields. Split a tool when one name would mix read/write authority, unrelated lifecycle state machines or materially different annotations. Every accepted concurrency, scope, destination or replay guard must be consumed by the selected operation; an irrelevant guard is rejected before side effects rather than silently ignored.

Mutation contracts state idempotency, concurrency preconditions, normal failure atomicity, crash/restart recovery level, affected-resource bounds and recovery references. Replay-sensitive mutations use request IDs or an equivalent idempotency mechanism, and public results/errors carry a correlation or operation ID so an uncertain client response can be inspected before retry. State-machine reads expose legal next transitions and their required identifiers instead of requiring the agent to memorize them from prose.

Batch operations are added only where complete preflight is meaningful. Workspace structured multi-file edit is such a case: keep precise single-file replacement and bounded unified-diff patching, and add a guarded structured batch for create/replace/move with whole-request preflight, one mutation lock, preimage capture, synchronous rollback and crash reconciliation. Do not call the operation fully atomic until restart recovery proves that guarantee. Stateful browser interactions are the opposite case: navigation and DOM changes require observation boundaries, so a generic interaction batch would create stale-target hazards rather than a useful transaction.

Use error categories such as invalid input, denied, conflict, unavailable, quota exceeded, failed, and outcome unknown. Include a correlation/operation ID, bounded public details and explicit retryability. Keep sensitive causes in protected diagnostics. A policy denial never suggests retrying through a broader interface.

The host policy/configuration loader produces an immutable effective-policy generation with a digest and human-readable diff. Unknown old Python settings fail with a clear migration message. No automatic legacy interpretation is needed. Existing native assets may use an explicit maintainer adapter, but normal runtime code does not branch repeatedly on Python/Go, native/Compose and old/new schema variants.

## 6. Unify workspace and repository operations

Reuse traversal-resistant filesystem primitives. Introduce registered Workspace and Repository identities with pinned roots and explicit metadata ownership. Git commands, path-filtered reads and file operations share this identity rather than assuming the entire workspace is a single Git repository.

Support normal create/edit/move/delete behavior under the same authority. Deleted tracked paths and directory moves are normal states, not permission violations. Use literal pathspecs and validate both worktree and Git metadata location. Run Git inside the work execution context, with deterministic inspection settings, not inside the gateway.

A blanket ban on every symlink or filename resembling a secret is not the product-wide authority model. Define legitimate in-bound links and protected resources explicitly, and test the chosen behavior across tools and project code. Host credentials and policies must be outside the workload mount view; naming a file `.env` cannot be relied upon to hide it from an interpreter with filesystem access.

Keep precise edit and partial-stage APIs where useful. Document their cooperating-writer scope; use isolated worktrees or revision-based apply when a stronger integration guarantee is required. Never overwrite an unrelated staged draft or silently restore the user's workspace during host update/rollback.

Checkpoint/revision APIs state what they preserve: tracked file content, index state, untracked content, or just an inventory. Add missing data capture only when the promised recovery mode requires it, with quotas and explicit expiry. A checkpoint must not be presented as a complete backup when it is not one.

## 7. Build safe native toolchain provisioning, not another broad package manager

Implement the already agreed shim semantics: exact selectors select exactly; partial selectors choose the highest installed permitted matching version; only a missing match triggers trusted acquisition. Explicit update acquires a newer match. Another project's installation can affect a floating selector in a shared store; exact pins are the reproducible choice.

Separate provider responsibilities: parse ecosystem declarations, resolve available versions, supply authenticated artifact identity, describe required components, verify/extract, and produce an executable plan. uv's version requirement and Python selection, Rust components/targets, Go toolchain behavior and Node package-manager requirements must not be flattened into one invented SemVer grammar.

Store installed content outside the workspace, read-only to jobs, using content-addressed or immutable generation directories. Per-artifact locks deduplicate concurrent installs. Publish a validated complete generation atomically; retain previous generations while jobs reference them. Garbage collection uses references, quotas and explicit policy, never deletes an executing job's toolchain.

Extraction has a separate confinement boundary, type/mode rules and size/time/entry limits. Enforce permissions after creation regardless of umask. No untrusted install hook runs with host/controller privileges. A failed or interrupted install cannot leave an apparently valid partial tree or clobber another installer's fixed `.new` path.

Start with Node/pnpm as the end-to-end implementation exercise, then Python/uv, Rust components and Go. This is incremental validation of the common architecture, not permission to hardcode Node assumptions into it. Derived OCI images remain the separate mechanism for relatively static native/OS dependencies.

Authenticate independently updateable toolchain metadata as well as Loki releases. The release mechanism must provide authenticated metadata, freshness/expiry handling, rollback protection, scoped trust roles where needed, and explicit key rotation/recovery. Select the concrete signing/update technology during the release work after these properties are tested against the bootstrap and recovery model; do not invent an ad hoc protocol merely to satisfy the initial implementation. OCI digests and checksums identify content but do not replace source authenticity, license notices or provenance validation.

## 8. Turn host lifecycle into a recoverable transaction

Keep the small bootstrap and the user-facing status/prepare/apply split. Only the host management boundary changes policy, deployment, mounts and active release generations; a workload cannot call it with host authority.

An immutable release generation describes CLI/runtime/component digests, configuration schema, policy compatibility, toolchain compatibility, state transitions and rollback coverage. Prepare acquires/verifies assets and computes the plan without switching running services. Apply checks that the inspected plan is still current, records its operation, and then performs bounded state transitions.

Default apply behavior must not kill active jobs. Report the jobs which prevent safe application; allow the operator to drain or explicitly approve interruption. The same principle applies to backup, restore, component disable and uninstall.

Use a durable operation journal and recoverable lock. At every transition, a crash must leave enough information to identify the active and candidate generation and finish or undo the transition safely. Recovery failure is a distinct state with both the original and recovery errors; it never prints a successful rollback message merely because rollback was attempted.

Define backup coverage explicitly, including external references to keys/configuration and the policy for optional-component state. An old image with new incompatible state is not a rollback. Use two genuinely different releases in acceptance, not two tags for the same image. Preserve workspace contents by default during install/update/uninstall; deleting user data is a separate explicitly approved operation.

The release installer is publicly advertised only after a source-checkout-free clean-host acceptance passes. Native/systemd candidate tooling remains maintainer-only and must not set the default architecture. Remove unnecessary legacy dependencies from the final runtime rather than keeping hidden compatibility branches.

## 9. Package by responsibility and dependency, not by language history

The [package structure and dependency plan](project-structure-plan.md) defines the detailed target layout, allowed import direction, all 36 current package destinations, private-module boundaries, asset ownership and S01-S06 implementation slices. It is the authoritative refinement of this section; its structure is proposed, not already implemented.

```text
cmd/<role>/             thin operator/server entrypoints
internal/app/           explicit composition per deployed role
internal/control/       identity, policy, outcomes, platform credentials, audit
internal/work/          jobs, workspace/repositories, toolchains, app secrets, networking/endpoints
internal/integrations/  provider, browser, signing, sharing and devtools capabilities
internal/host/          configuration, releases, lifecycle, diagnostics/assets
internal/transport/     CLI, MCP, internal RPC and HTTP bindings
internal/platform/      reusable safe I/O, process, cgroup, sandbox-launch, network and encrypted-store mechanics
```

The grouping directories are not umbrella Go packages or separately versioned modules. Keep one Go module; nest cohesive feature packages inside the groups. Separate a feature's typed use cases from its local/remote adapters, hide implementation details with nested internal packages where useful, and define small interfaces at actual consumption points. Composition wires implementations without making the feature import its own adapter. Platform primitives carry no feature policy, global Config or transport DTOs.

Split the current service package by function into role wiring, protocol bindings and owning use cases; do not merely rename it to app. Extract truly neutral file/network primitives so authentication does not import browser policy and ordinary file revisions do not import vault storage. Platform credentials and application-secret use cases have different owners and actual isolation boundaries. Role-specific entrypoint dependency checks must keep local execution and key-storage implementations out of the gateway and keep general project/provider behavior out of the privileged launcher.

Dependency and exported-type checks begin with A01-A02 and accompany every capability change, rather than postponing package architecture until A12. Test imports, generators, supported build variants, canonical embedded assets and actual runtime wiring are included. Import rules are not sandbox enforcement and cannot replace the cross-path runtime acceptance matrix.

Delete unused raw Docker forwarding, obsolete schema aliases and assertions that delegated execution must be absent. Move one-shot legacy import/restore support to maintainer tooling when needed for data transfer. Python source remains in its existing retained directory until retirement; it is not a runtime dependency of the new architecture. Update shape-based tests when introducing the new Go integration-test layout instead of preserving a blanket ban on the root tests directory.

## Implementation work units

Work is scoped by behavior with permanent regression tests. Do not combine the complete redesign into one patch, and do not enable a capability until its enforcing dependencies are ready.

A01 establishes the baseline reproducers, test prerequisites and architecture checks; it is not a request to commit an intentionally failing default suite. Demonstrate each failing regression on this baseline, then commit its permanent regression assertion together with the corresponding fix. Do not turn known failures into permanent skips or relaxed expectations to obtain a green release gate.

Before the structural executor rollout, land independently safe containment fixes for reproduced defects whose correction does not depend on the new authority model: safe archive extraction and final modes (R8/R9), strict unknown-field and scalar decoding (R3/R11), constructor validation that currently panics (R14), and normal deleted-path Git semantics (R1). Add an immediate defense that prevents managed platform credentials from flowing through ordinary application-secret mutation/injection APIs (R4), while keeping the full credential split in A04. Disable project-controlled Git text conversion in control-context inspection as an immediate R5 defense, while keeping the full project-execution separation in A03/A08. Each early fix carries its own regression and must not be used as evidence that the broader boundary is complete.

| ID | Unit and concrete output | Dependencies | Completion evidence |
| --- | --- | --- | --- |
| A01 | Preserve deterministic baseline reproducers for R1/R3/R4/R5/R7/R8/R9/R11/R14, document supported test prerequisites, and establish architecture/import checks | Review | Every listed defect has a reproducible baseline and a named future regression location; default suite is not left intentionally failing; new dependency violations fail immediately |
| A02 | Define typed principals, resource grants, immutable effective policy and strict error/config contracts | A01 | Unknown/obsolete/incorrect fields rejected; workspace changes cannot alter effective authority |
| A03 | Implement minimal constrained OCI launcher and supervisor isolation prototype; separate gateway from project execution | A02 | Synthetic Git filter/project child cannot read control tokens/vault or invoke administration; no host namespace/socket leakage |
| A04 | Split platform credentials, app secrets and public config; constrain environment delivery and broker operations | A02, A03 | R4 regressions denied; legitimate approved application receives selected canary values only; sibling jobs cannot inherit them |
| A05 | Complete job lifecycle, bounded output, cancellation, cgroup cleanup and restart reconciliation | A03 | Detached descendants/OOM/timeouts/pipe inheritance cleaned; concurrent jobs isolated; gateway reconnection recovers job state |
| A06 | Add authenticated workload egress and job-owned endpoint leases | A03, A05 | Direct/indirect prohibited egress fails; approved package/API access works; stale/reused ports cannot retain shares |
| A07 | Expose typed MCP execution operations, derive the public catalog from action-specific contracts, and remove dependence on an out-of-band devtools shell | A02, A04, A05, A06 | An MCP-only client runs builds/tests/dependency installation and manages a dev server; action-specific required/forbidden fields, typed outputs, operation IDs, idempotent retry handling and representative tool-choice scenarios pass |
| A08 | Repair file/Git path and concurrency semantics through shared repository identities and isolated execution; add guarded structured multi-file editing without weakening precise patch/stage APIs | A03, A05, A07 | Delete/rename/nested/worktree/partial-stage regressions pass; multi-file patch/stage capability is contract-visible; batch preflight/rollback/crash-recovery and index/file CAS regressions pass; inspection helpers never run in control context |
| A09 | Build immutable toolchain generations on the already-contained extraction primitive; add shims and the initial Node/pnpm provider | A02, A03, A06 | Archive containment/modes remain green, concurrent install is atomic, generation publication is immutable, and two projects choose different permitted versions |
| A10 | Connect browser/signing/Git-provider/preview/artifact features through role-specific grants, endpoint leases and domain-specific typed contracts | A04, A06, A07 | Real browser navigation/download and preview WebSocket paths work; stale browser references fail safely; share creation/revocation is replay-safe; Git-provider read/write annotations are truthful; signatures verified; disabled components disclose no extra authority |
| A11 | Implement transactional core host lifecycle with job drain, versioned state, generic optional-component hooks and honest recovery outcomes | A02, A05, A09 | Core install/update/rollback works independently of optional integrations; failpoint/crash tests cover every transition; different-image upgrade and failed-recovery status are correct; workspace preserved |
| A12 | Complete remaining toolchain providers, quotas/retention, diagnostics and package cleanup | A08, A09, A10, A11 | Provider-specific version/component fixtures, load/backpressure tests, bounded storage, no current dependency on retired modules |
| A13 | Produce authenticated releases/index, notices/provenance and thin bootstrap; replace first-install documentation | A09, A11, A12 | Fresh Ubuntu/WSL install from artifacts, no source checkout or development compiler required on host |
| A14 | Run independent integration/adversarial/recovery review against release artifacts, including agent tool-selection and abrupt-session-loss scenarios; then separately approve operational cutover | A01-A13 | No blocking findings, no required skipped acceptance tests, source/image/policy identities recorded, verified recovery fallback, and a fresh client can resume interrupted work without prior transcript state |

A03 implementation status: the finite no-network sandbox backend, privileged launcher role, remote jobs adapter, and unprivileged executor role are now implemented with strict Unix-RPC authority boundaries. Agent-facing executor requests contain only CWD/argv; job IDs and policy generations are supplied by trusted code. The executor remains a one-shot internal API, while the launcher now owns a bounded asynchronous `start`/`wait`/`cancel` registry with trusted run timeout, bounded result retention/capacity, shutdown cleanup, and independent cleanup cancellation after uncertain remote calls. The cross-role fixture verifies that forged authority fields are rejected, launcher failures remain errors, and executor-client cancellation reaches the launcher-owned runner before the trusted launcher timeout. A03 is not considered fully closed until a real supported-host OCI integration proves project-controlled children cannot access control credentials, host namespaces/sockets or broader mounts. A05 still owns bounded output, durable job state and restart reconciliation, cgroup descendant cleanup/OOM handling, and real OCI lifecycle acceptance; A07 remains the first MCP exposure.

A10 and A12 are milestone umbrellas, not single patches. Browser/sharing, signing, Git-provider integration, each remaining toolchain provider, resource-retention work, diagnostics, and package cleanup land as independently reviewable sub-units once their listed dependencies are ready. A13 likewise separates release metadata/authentication, artifact/notices production, bootstrap publication, and documentation so a failure in one concern does not force unrelated changes into the same commit. The milestone closes only when all of its sub-units meet the shared acceptance row.

The first structural slice is A01-A06. It must prove the isolated executor/job boundary through internal integration fixtures, including project-controlled child processes, credentials and network grants, before that capability is exposed through MCP. A07 is the first public MCP-only execution milestone and must use the already-proven boundary rather than acting as the prototype for it. The early containment fixes above may land sooner, but none of them justifies enabling arbitrary execution inside the current gateway.

Milestones: M1 = trusted boundary and credential separation; M2 = useful MCP-only development; M3 = components/toolchains/operational lifecycle; M4 = public source-free install and verified cutover candidate. A release is evaluated against the completed milestone, not a percentage of work items or a passing unit-test count.

## Acceptance matrix

For each protected resource, exercise every applicable route: dedicated tool, generic executable, script/interpreter, dependency hook, detached child and direct broker request. Confirm denied access and positive normal work in the same test environment.

| Boundary | Required negative checks | Required positive checks |
| --- | --- | --- |
| Host policy/control | Workspace aliases, path traversal, forged IDs, inherited cgroup/UID, direct launcher/daemon access | Operator-approved plan applies exactly the inspected generation |
| Credentials | Reserved profile APIs, unauthorized injection, environment override, sibling-process access, output/state persistence | Scoped provider operation and approved app-secret job work without raw platform-key exposure |
| Workload lifecycle | setsid/double fork, process limits, OOM, timeout, disconnect, SIGKILL during supervision | Deterministic result, complete owned-subtree cleanup, recoverable status and bounded output |
| Network/endpoints | Other proxy, raw IP, private address, redirects, DNS rebinding, reused port, namespace confusion | Approved installs/APIs, local preview HTTP/WebSocket, browser development target |
| Files/Git | Missing parents, symlink replacement, foreign Git metadata, stale expected revision, overlapping writers | Normal directory moves, nested repositories, exact scoped staging and explicit recoverability |
| Toolchain/release | Archive traversal/chains/bombs, wrong digest/signature, expired/older metadata, partial install, mode mismatch | Atomic reuse/install/update, correct runner access, rollback with active generation references |
| Host recovery | Crash before/after publication, disk full, failed restoration, stale lock, active jobs, disabled component | Truthful status, deterministic repair/rollback, no unexpected workspace or credential deletion |
| Protocol/config contracts | Unknown or misspelled fields, wrong scalar types, omitted mandatory preconditions, irrelevant action fields, silently ignored concurrency/replay guards, stale revisions, unsupported schema versions, unsafe retry after an uncertain response | Canonical generated action-specific schemas/config examples round-trip into the exact effective policy; typed results expose completeness and operation identity; stable typed errors identify invalid/conflicting requests and safe retry/inspection behavior |

Validation follows the concrete prerequisite contract in [Validation tiers and prerequisites](validation-strategy.md), so the default developer suite is trustworthy rather than environment-dependent:

- **Unit/default:** `go test ./...` covers pure package behavior and local deterministic fixtures without requiring Docker, Chromium, systemd or `setfacl`. A host-specific assumption belongs in an integration fixture, not an unconditional unit assertion.
- **Race/static:** `go test -race` for packages whose tests are local/deterministic, plus `go vet` and architecture/import checks. Race success is not treated as process/network isolation evidence.
- **Integration:** disposable local/container fixtures exercise real UID/GID ownership, ACLs, Chromium, job sandboxes, devtools/toolchain flows and Compose role connectivity. Missing prerequisites may be reported as an explicit skip only outside a release gate.
- **Release acceptance:** provisions every required prerequisite on the supported clean host/WSL fixture and treats a missing required integration as a failure, not a skip. It records source revision, image/toolchain digests and effective-policy generation.

Use property/fuzz tests for parsers and paths, deterministic concurrency tests for state transitions, and real disposable OCI/WSL tests for namespace/ownership guarantees. A race detector cannot prove process isolation, cross-process CAS or network policy; all are separately evidenced.

Benchmark before optimizing: idle/core memory, cold and warm job start latency, high-fanout directory traversal, repeated diff/read costs, output transport bytes, concurrent toolchain installs, vault mutation latency and recovery duration. Fix measured bottlenecks without collapsing trust boundaries for convenience.

## Delivery and completion

Implementation changes should be reviewed and committed in the work units above, with commands, results and exact remaining gaps recorded. No implementation commit should claim readiness while its required integration gate is skipped. Update active plans when the new architecture replaces an old assumption; do not retain contradictory compatibility instructions as live guidance.

The current deliverable is the assessment and plan. Runtime fixes, API replacement, public publishing, production deployment and deletion of the retained Python server are separate subsequent actions.
