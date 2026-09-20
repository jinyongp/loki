# MCP Tool Contract Review

Date: 2026-09-19
Status: design review with Go implementation convergence through the A06/A07 Job/network slice; no compatibility constraint requires preserving the Python or earlier Go public tool shapes.

## Scope and evidence

This review originated from two 2026-09-19 baselines: the running Python Loki 0.47.1 catalog with 37 MCP tools and the then-checked-in Go 0.49 development contract with 31 tools. Those counts remain historical evidence for why the redesign was needed; they are not the current Go contract state.

As of 2026-09-21, the checked-in Go contract defines 33 tools. The Go direction still intentionally excludes the legacy project/task/Skill/command/process MCP wrappers and exposes narrower coordination/context surfaces plus the public `job` lifecycle. Agent Context, durable semantic checkpoints, fresh-session resume, generated action-specific schemas, Job execution and Job-owned preview endpoint identity are now source-implemented. Remaining work in this document should be read as family-specific semantic/acceptance follow-up rather than evidence that the original flat-schema baseline still exists.

Initial review baseline facts were: 18 of 31 tools multiplexed operations through flat `action` supersets; the catalog had 153 public input properties with only 10 descriptions; 21 output objects were open, four tools lacked output schemas, and two lacked MCP annotations. `preview_publish.environment_routes` was accepted but unused, and several action-irrelevant guards were schema-accepted. Those defects motivated the generated action-contract migration and remain useful historical reproducers, but they no longer describe the current Go catalog.

One baseline observation remains independently relevant: `workspace_edit(action=patch)` was already bounded multi-file behavior in both implementations. The contract work must keep that capability discoverable without weakening its path validation, `git apply --check`, revision capture or mutation-lock semantics. Likewise, operation/correlation identity and model-facing tool-choice evidence remain broader follow-up concerns even after schema closure.

## Verification evidence

The current worktree was rechecked after the A06/A07 implementation batch.

- Current Go contract census: 33 tools total and 20 action unions.
- Contract-audit census: zero flat action unions, 203 public input properties and 203 property descriptions, zero missing output schemas, zero open output schemas and zero missing MCP annotations.
- `job` is now a generated action-specific public surface with start/inspect/output/cancel. Start accepts only request identity, command/lifetime inputs, reviewed logical network selection and bounded logical endpoint declarations; inspect/cancel status returns neutral endpoint leases without Docker/resource authority.
- `preview_publish` now has distinct server, stack and job branches. `environment_routes` has been removed; every publication requires `request_id`; changed inputs conflict; `action=job` resolves Job ID plus logical endpoint name to the exact active endpoint lease and does not accept a caller-chosen host port.
- Job preview proxying revalidates Job ID, lease ID, host port and active lease state for every request. Replacing a lease while reusing the numeric host port invalidates the old share.
- The MCP deployment path is MCP -> executor -> launcher. Native and container packaging give MCP only the executor socket and reserve the launcher/Docker boundary for the dedicated privileged role.
- Multi-file edit evidence remains unchanged: `workspace_edit(action=patch)` parses the complete bounded patch target set, validates paths, runs `git apply --check`, captures revisions and applies under the workspace mutation lock.
- Agent Context implementation evidence remains current: `project_context` and `project_context_write` compose target-owned guidance, selected Skill revisions, Git/worktree evidence, canonical coordination, runtime-owned semantic checkpoints, staleness/completeness and legal claim/takeover guidance.
- Focused `cmd/loki`, launcher/executor, contract, service, preview, deployment and packaging tests pass for this slice, `go run ./tools/archcheck` passes, and the batch-closeout `go test ./...`, `go test -race ./...`, `go vet ./...`, and `go build ./...` gates pass. The real A06/A07 OCI/network fixture is now implemented, including authenticated egress, endpoint publication/recovery and Job preview HTTP/WebSocket, but it remains unexecuted without its explicit supported-host Docker/image/workspace/allowlisted-authority inputs. Release readiness therefore remains open.

## Root cause

The recurring problem is not the number of tools by itself. It is that the contract exposes implementation dispatch while hiding operational semantics.

An agent has to infer facts that should be machine-visible:

- which operation fits the intent;
- which fields are required, forbidden, or ignored for that operation;
- whether one call already supports multiple resources;
- what is atomic, merely serialized, rollback-capable, or crash-recoverable;
- which revision/hash/request ID is the concurrency or replay guard;
- whether a result is complete, truncated, stale, or resumable;
- what to inspect after an uncertain response;
- which alternative tool should be used instead;
- which state-machine transition is currently legal.

A longer global instruction can mitigate this, but it cannot be the primary API contract. Important semantics belong in the tool schema, result, and typed error returned at the point of use.

## Contract design standard

### 1. Model intent, not dispatcher internals

An `action` union is acceptable when all variants belong to one resource domain and have compatible authority/side-effect semantics. Each variant must still be a real discriminated request with action-specific required and forbidden fields.

Split operations into separate tools when a single tool would mix materially different authority, lifecycle, or MCP annotations. Read and destructive/write behavior must not be combined merely to reduce tool count.

The legacy `action` MCP tool is the counterexample: registration, execution, process inspection, and stop are different lifecycles and should not return in that form. The job model should expose start/inspect/output/cancel separately from action configuration.

### 2. Make the schema sufficient for correct invocation

Every input property needs a useful description where its semantics are not obvious. For action unions, the schema must express:

- required fields for the selected action;
- fields forbidden for that action;
- defaults that actually apply to that action;
- bounded list/string limits;
- concurrency/revision preconditions;
- whether an identifier is a task, Run, job, share, revision, or request ID.

A field that the selected handler will ignore must be rejected before side effects. “Accepted but ignored” is not permitted for safety guards, destination selectors, scope selectors, or lifecycle controls.

### 3. Publish operation semantics explicitly

Each mutating operation must state and test:

- side-effect class;
- idempotency/replay behavior;
- concurrency guard;
- normal failure atomicity;
- crash/restart recovery level;
- affected-resource limit;
- undo/recovery reference when one exists.

Use precise terms. A multi-file edit is not globally OS-atomic merely because it holds a mutex. If a process can crash between per-file publishes, the public guarantee is journaled/recoverable or rollback-on-detected-failure, not instantaneous multi-path atomicity.

### 4. Make uncertain outcomes recoverable

Replay-sensitive mutations should accept a caller request ID or equivalent idempotency key and retain a bounded terminal result. The result and public error should include a correlation/operation ID.

`system_inspect` should support direct lookup by operation/invocation ID in addition to a recent-activity list. A client that loses a response must be able to inspect the operation before deciding whether to retry.

A typed error should expose a stable category such as `invalid_input`, `conflict`, `denied`, `unavailable`, `quota_exceeded`, `failed`, or `outcome_unknown`, plus retryability and safe next-action data where applicable. Sensitive internal causes remain in protected diagnostics.

### 5. Treat bounded output as part of the contract

List/history/event/search results must say whether they are complete. Where continuation is meaningful, return a cursor or next offset. Where deterministic continuation cannot be provided, return an explicit incomplete/truncated state and a safe narrowing strategy.

Outputs should be closed typed variants by default. Extension maps should be deliberate and named rather than making the whole result an open object. Large structured results should not also be repeated as JSON text.

### 6. Provide machine-readable affordances for state machines

Do not require the agent to memorize lifecycle transitions from prose alone. State-oriented reads should return the current state plus allowed next transitions and the preconditions/identifiers those transitions require.

This applies particularly to project Run coordination, jobs, browser state references, shares, and host lifecycle.

### 7. Add batch only where the semantics are coherent

Batching is useful for bounded independent mutations whose complete preconditions can be checked before any write. It is not a universal optimization.

Do not batch browser interactions merely to save calls: navigation and DOM mutation can invalidate the next target, so observation boundaries remain necessary. Do not hide multiple external side effects behind “atomic” when the upstream system cannot provide that guarantee.

## Tool-family review

| Family | Verified issue | Target direction |
| --- | --- | --- |
| System diagnostics | `system_inspect` multiplexes unrelated argument shapes; activity is recent-list only and invocation IDs are not normal result handles | Discriminated schema; operation lookup by ID; return operation IDs from mutating/long-running calls; typed health/activity results |
| Runtime/commands (legacy) | The Python `action`/command/process surfaces still overlap, while the Go replacement now has one generated `job` lifecycle | Keep the landed start/inspect/output/cancel Job model with explicit detached/lifetime semantics and separate action/workflow registration; retire legacy execution paths only after deterministic and real OCI/network acceptance |
| Preview/shares | The original duplicate-share and unused-field defects are corrected for Go preview publication; acceptance still needs the real Job network path | Keep request-ID replay, idempotent revoke and the server/stack/job discriminated contract. Job shares bind exact endpoint leases rather than raw-port lifetime; prove HTTP/WebSocket plus stale/reused-port denial in the supported-host fixture |
| Browser session | Lifecycle/navigation is typed, but ordinary browser-history controls still need complete desktop coverage | Keep discriminated generation-aware lifecycle; add `reload`, `forward`, and `stop_loading`; move history navigation authority here so navigation operations share one generation model |
| Browser observation | Core state/event observation is typed, but user-driven browser workflows still lack public dialog/download state | Keep action-specific typed sequence/completeness semantics; add pending-dialog observation and bounded download status/history so interaction outcomes are observable without filesystem guessing |
| Browser interaction | Current click/fill-like type/key/scroll/tab coverage omits common desktop user gestures such as hover, button/modifier clicks, drag, wheel, form controls, file upload, and dialogs; stale element/index behavior must remain guarded | Complete the desktop interaction surface with high-level discriminated gestures; retain browser/state generation preconditions; model full gestures in one call rather than exposing dangling pointer-down state; keep observation boundaries instead of generic batching |
| Screenshot/image | Separate inspect/save/share intent is useful, but overwrite/CAS semantics are not described at field level and some results are open/untyped | Keep separate tools; describe overwrite preconditions; return typed metadata; request IDs only for share creation |
| Workspace reads | List/file/search/history variants use different pagination and required fields; search can truncate without a continuation cursor | Discriminated variants; consistent `complete/has_more/next_*`; make search truncation impossible to mistake for exhaustive coverage |
| Workspace edit | Multi-file patch exists but is undiscoverable; structured multi-file create/replace/move does not exist; irrelevant guards can be silently ignored | Keep precise single-file replace and unified-diff patch, document patch as bounded multi-file; add structured bounded batch with explicit transactional/recovery semantics; reject irrelevant fields |
| File recovery/delete | Restore and tracked delete are clear single-resource operations but mutations lack a common replay/result envelope; repeated destructive work is call-heavy | Add operation IDs and explicit result/undo metadata; consider guarded bounded multi-delete/restore only if real workflows justify it, without weakening tracked/untracked protections |
| Git inspect | Status/diff/index/commit-context are flat action variants; outputs are mostly open; Git diff overlaps `developer_view` | Discriminated typed results; keep canonical inspection separate from presentation; render the exact captured result rather than rerunning Git when possible |
| Git stage | `paths` already batches paths and `patch` can span files, but descriptions do not advertise either; `reverse` and other irrelevant fields are schema-accepted; index CAS is optional | Describe multi-path/multi-file support; reject irrelevant fields; require or strongly gate on observed index revision for mutation; preserve partial staging semantics |
| Developer view | Presentation reruns Git diff and target Go removes the legacy process-log variant; output schema is absent | Make viewers consume/cite captured result/artifact IDs where possible; keep rendering out of authority/canonical-state decisions |
| Secret inspect | Five read variants are flat and outputs are open | Discriminated typed metadata responses; cursor/completeness for audit/history |
| Secret write | `set` actually writes non-secret public config, while its name can be read as “set secret”; `import_env` consumes a staged opaque import ID rather than raw env input | Rename semantics to `set_public` and `import_staged` or split public config from secret lifecycle; discriminated variants; request IDs for generation/import mutations |
| Secret delete | Secret/profile deletion uses no caller-visible revision guard and has flat action arguments | Discriminated destructive variants, expected profile/state revision where concurrency matters, replay-safe deletion result |
| GitHub generic command | A raw `gh` argument vector hides allowed command groups and cannot truthfully advertise read-only/destructive semantics per invocation; tool annotations are missing | Prefer typed broker operations grouped by domain/authority. If a constrained command escape hatch remains, publish its allowed capability catalog and conservatively classify it; do not make it the only path for privileged provider actions |
| GitHub issue fields | Read and write actions share one tool, so tool-level MCP annotations cannot accurately describe side effects; action fields are flat; annotations/output schema are missing | Split read from mutation or expose separately annotated tools; typed value/result variants and explicit add-vs-set semantics |
| Project coordination read | Ten actions still require different task/Run/workstream IDs and cursors | Canonical devtools reads remain separate; `project_context` now provides the composed new-session entry point, current evidence gaps, latest semantic checkpoint, and legal coordination transition. The action-union schema itself still needs redesign. |
| Project coordination write | Claim/takeover/resume/checkpoint/release/done still have different target identifiers and preconditions; schema requires only action/request ID | Keep session-bound canonical transitions and request-ID replay safety; explanatory handoff state now lives in `project_context_write`, while the remaining action union still needs redesign. |
| Agent guidance | Context and Skill inspection still share one action-union schema, but native target-owned AGENTS.md/Skill resolution, provenance, precedence, completeness, and target-aware Skill inspection are implemented | Keep the native capability; finish discriminated action schemas and closed typed outputs without changing the ownership model. |
| Legacy project/task/Skill MCP | The running server has large multi-action project/Skill surfaces and a separate Taskwarrior API; these are being replaced in the Go direction | Do not recreate these shapes during migration. Preserve only the necessary canonical coordination and Loki-native guidance/context ownership with narrower contracts |

## Confirmed browser interaction completion design

The Browser family should cover normal desktop browser interaction at the same abstraction level as a user, without exposing arbitrary CDP commands or JavaScript execution as public MCP authority. This confirmed scope extends the already-landed generation-aware session/observe/interact contracts.

### Public capability model

`browser_interact` remains the single high-level interaction surface. Its final desktop action set is:

- `click`: element-index or viewport-coordinate target. Add `button` (`left|middle|right`), `click_count` (1..3), and bounded modifier keys (`Alt|Control|Meta|Shift`). This covers ordinary, double, right, middle, and modified clicks without separate overlapping tools.
- `hover`: move the pointer to an element index or viewport coordinate without pressing a button.
- `drag`: one complete left-button drag gesture. Support element -> element, element -> coordinate, and coordinate -> coordinate. Bound interpolation with `steps` and `duration_ms`; dispatch the full move/press/move/release sequence inside one call.
- `fill`: replace the complete value/content of one editable element. This is the current selection-and-replace behavior and is distinct from real typing.
- `type`: focus one editable element and insert text at the current caret/selection using browser input events without selecting the whole value first.
- `key`: dispatch one supported key with optional modifiers.
- `shortcut`: dispatch one bounded keyboard chord such as Control+A, Control+Z, Meta+K, or Shift+Tab. The complete key-down/key-up sequence stays inside one call.
- `wheel`: dispatch a real wheel gesture with bounded `delta_x`/`delta_y`, optionally targeted by element index or coordinate. Replace JS-only page scrolling as the canonical user-wheel interaction so nested scroll containers and wheel listeners behave normally.
- `select_option`: set one or more options on a `select` element using typed option selectors and dispatch the corresponding input/change behavior.
- `set_checked`: set checkbox/radio state to an explicit boolean instead of blind toggling.
- `focus`: focus one observed element.
- `upload`: attach one or more workspace-owned regular files to a file input using the DOM/CDP file-input primitive. Resolve every path through workspace policy before the browser sees it; never read file contents into MCP arguments or logs.
- `dialog`: accept or dismiss the currently observed JavaScript dialog, with optional prompt text only for prompt dialogs.

Do not expose separate public `mouse_down`/`mouse_up` or key-down/key-up tools. A failed/interrupted MCP call must not leave a logical pointer button or modifier held down. Low-level sequences remain internal implementation details.

### Session and observation completion

Navigation/history belongs to `browser_session`, not `browser_interact`. Extend the generation-aware session surface with:

- `reload`
- `forward`
- `stop_loading`
- move the existing `back` behavior from interaction authority into the session/navigation family.

Extend `browser_observe` with:

- `dialog`: return the currently pending JavaScript dialog, including a monotonic `dialog_generation`, type, bounded message, and whether prompt text is accepted.
- `downloads`: return bounded browser-download state/history with stable download IDs, state (`in_progress|completed|canceled`), safe suggested/final filename metadata, completeness/cursor information, and `browser_generation`. Reuse the existing download-event owner rather than guessing from filesystem contents.

### Generation and stale-reference rules

The existing browser generation model is mandatory for every interaction:

1. Every `browser_interact` action requires `expected_browser_generation`.
2. Any action that resolves an observed element index (`click`, `hover`, element-targeted `drag`, `fill`, `type`, element-targeted `wheel`, `select_option`, `set_checked`, `focus`, `upload`) also requires `expected_state_generation`.
3. Dialog handling requires the current `dialog_generation` returned by `browser_observe action=dialog`.
4. A stale generation fails with typed `conflict` before pointer, keyboard, DOM, dialog, or file-input side effects.
5. Every successful interaction advances `browser_generation` exactly once, including interactions that internally cause navigation. The implementation must not double-increment nested navigation/target transitions.
6. A fresh `browser_observe action=state` is required before reusing element indexes after any successful interaction.

### Pointer/drag implementation

Reuse `Input.dispatchMouseEvent` as the primary desktop pointer primitive.

- Resolve element centers through the existing isolated-world element table and frame-coordinate translation.
- Click variants dispatch move -> press -> release with the selected button/modifiers/click count.
- Hover dispatches only pointer movement.
- Drag resolves both endpoints before the first side effect, then dispatches move-to-source -> press -> interpolated mouse moves with the left button held -> release. Bounds on steps/duration prevent unbounded event floods.
- The first implementation must cover sortable UI, sliders, resize handles, canvas/pointer handlers, and ordinary pointer-driven HTML drag behavior.
- OS-level file dragging is not part of this surface; file-input interaction uses `upload`.

### Keyboard/text implementation

Split the current fill-like text behavior instead of overloading `type`.

- `fill` may select existing value/content, replace it, and emit the normal editable-element events.
- `type` preserves the current caret/selection and uses browser input events for incremental text insertion.
- `key` and `shortcut` use bounded key definitions/modifier sets and always release pressed keys before returning.
- Arbitrary raw key codes or arbitrary CDP input payloads are not public.

### Forms, files, and dialogs

- `select_option` accepts a bounded list of typed selectors (value, label, or option index); ambiguous/nonexistent selections fail before mutation.
- `set_checked` is state-setting, making retry intent explicit.
- `upload` accepts workspace-relative paths only. Resolve symlink/magic-link policy through the same workspace authority used by file tools, require regular files, and enforce config-backed count/aggregate-size bounds before calling the browser file-input primitive.
- Dialog events are retained by the browser driver as explicit pending state. Handling a dialog consumes the matching `dialog_generation`; stale or already-closed dialogs return conflict/not-found style public errors rather than guessing.

### Explicitly deferred from this confirmed scope

The following were discussed as possible extensions but are not part of this confirmed implementation plan:

- touch/mobile gestures (tap, swipe, pinch);
- a raw/general `pointer_sequence` escape hatch;
- arbitrary clipboard read/write or permission-management APIs;
- arbitrary JavaScript/CDP command execution;
- OS-level file drag/drop outside standard file inputs.

These can be designed later if a concrete workflow requires them.

### Bounded implementation sequence

Implement this Browser completion in independent commits:

1. **Pointer gestures**
   - extend click with button/count/modifiers;
   - add hover and drag;
   - add real wheel dispatch;
   - add parser/guard helpers shared by pointer actions;
   - preserve one-generation-per-success semantics.

2. **Keyboard and editable/form controls**
   - rename/split the current fill-like text behavior into `fill` and real `type`;
   - add `key` and `shortcut`;
   - add `select_option`, `set_checked`, and `focus`;
   - keep element/state generation guards mandatory.

3. **File upload and dialog lifecycle**
   - add policy-checked `upload`;
   - retain pending dialog state from CDP events;
   - add `browser_observe action=dialog` and guarded `browser_interact action=dialog`.

4. **Navigation/download completion**
   - move `back` to `browser_session`;
   - add `forward`, `reload`, and `stop_loading`;
   - expose bounded `browser_observe action=downloads` over the existing download owner.

5. **Contract/scenario convergence**
   - close every new input/output branch;
   - describe every field and bound;
   - add operation metadata and truthful replay/concurrency semantics;
   - update catalog audit expectations only from measured results;
   - run the desktop interaction scenario corpus before declaring Browser migration complete.

### Browser completion acceptance scenarios

At minimum, validation covers:

- left/double/right/middle/modifier click;
- hover-triggered UI;
- drag for sortable item, slider/resize handle, and a pointer-driven drop target;
- nested scroll container through wheel input;
- fill versus caret-preserving type;
- common key/shortcut chords with guaranteed key release;
- select, checkbox/radio state setting, and focus;
- policy-checked file input upload;
- alert/confirm/prompt observation and accept/dismiss;
- back/forward/reload/stop-loading navigation;
- download initiation followed by observable completion/cancellation metadata;
- stale browser/state/dialog generations rejected before side effects;
- one generation increment per successful interaction even when a gesture causes navigation;
- action-irrelevant fields rejected by MCP schema before handlers.

Chromium-backed tests exercise real browser behavior when the repository browser fixture is enabled; deterministic schema/guard/parser tests remain runnable without Chromium.


## Per-tool disposition

The following matrix accounts for the current checked-in Go contract, including the newly exposed `job` tool. It originated as the 2026-09-19 disposition matrix, so rows not explicitly status-updated may still preserve the original issue wording. The current audit census above supersedes generic claims about flat action unions, missing descriptions, missing annotations or open/missing outputs; use each row for its remaining domain semantics and acceptance direction rather than as a fresh schema census.

| Tool | Disposition | Contract issue / required change |
| --- | --- | --- |
| `system_inspect` | refine | Keep diagnostics and the landed discriminated action contract; add direct operation/invocation lookup rather than recent-list-only recovery. |
| `job` | implemented; acceptance pending | Generated start/inspect/output/cancel branches are replay-safe and expose only logical network/endpoint input plus neutral lease metadata. Backend policy/image/mount/network IDs, proxy credentials, host-port selection and instance refs stay internal. Remaining blocker is real supported-host OCI/network/endpoint acceptance. |
| `preview_publish` | implemented/refine | Server/stack/job are action-specific, creation is request-ID replay-safe, `environment_routes` is removed, and Job publication binds exact endpoint leases without caller-chosen host ports. Keep runner-owned server/stack validation for non-Job servers; prove Job HTTP/WebSocket and stale/reused-port denial in the real fixture. |
| `shared_resources` | refine | Keep bounded listing; type preview/artifact variants and completeness instead of an open nested object. |
| `revoke_share` | fix semantics | Make repeated revoke replay-safe/idempotent and return a stable terminal result so a lost response does not require guessing. |
| `browser_session` | redesign | Keep the landed generation-aware start/navigate/stop contract and complete desktop navigation with back/forward/reload/stop_loading under the same session authority. History/navigation transitions return the resulting browser generation. |
| `browser_observe` | redesign | Keep the landed action-specific state/event contracts and add typed pending-dialog plus bounded download status/history views with completeness/cursor semantics. |
| `browser_interact` | redesign | Complete normal desktop user interaction: guarded click variants, hover, drag, fill, real typing, key/shortcut input, wheel, select/check/focus, file upload, and dialog handling. Element references bind to browser/state generations and stale references fail as conflicts. Keep each pointer gesture self-contained so failed calls cannot leave a button logically held down. |
| `browser_screenshot` | keep/refine | Single-purpose surface is good; document bounded image result and return consistent typed metadata where useful. |
| `browser_save_screenshot` | refine | Preserve explicit save intent; describe overwrite/CAS requirements and reject irrelevant overwrite guards. |
| `browser_share_screenshot` | refine | Preserve explicit share intent; add replay-safe share creation identity. |
| `workspace_read` | redesign | List/file/search/revision variants have different requirements and continuation models; use discriminated requests and explicit completeness. |
| `read_image` | keep | Already single-purpose and typed; retain bounded path/image semantics. |
| `share_image` | refine | Keep explicit share intent; add request-ID/replay semantics for share creation. |
| `artifact_publish` | redesign | File/bundle requirements differ and output schema is absent; use discriminated typed variants and replay-safe publication. |
| `write_image` | refine | Keep single-purpose write; describe create/overwrite CAS behavior at field level and return a closed result. |
| `workspace_edit` | redesign | Advertise existing bounded multi-file patching, reject irrelevant fields, retain guarded single-file operations, and add structured multi-file batch with tested recovery semantics. |
| `restore_workspace_file` | refine | Existing current-digest guard is useful; add common operation/result identity and typed undo metadata. |
| `remove_tracked_file` | refine | Preserve tracked-only safety and revision capture; add an observed-content/version precondition so an external writer cannot be silently removed. |
| `git_inspect` | redesign | Status/diff/index/commit-context variants need typed results; keep canonical inspection separate from presentation. |
| `git_stage` | redesign | Advertise existing multi-path and multi-file patch support, reject irrelevant fields, and make observed-index CAS the normal mutation contract. |
| `developer_view` | narrow | Keep presentation-only behavior, but render a captured result/artifact when possible instead of rerunning stateful inspection; define an output schema. |
| `secret_inspect` | redesign | Five metadata views need action-specific inputs and typed/paginated results. |
| `secret_write` | redesign | Rename/split public configuration from secret lifecycle; `set` must not look like a safe raw-secret path, and staged import semantics must be explicit. |
| `secret_delete` | redesign | Split/profile-vs-secret destructive variants or discriminate them fully; add revision/replay protection where concurrent state matters. |
| `github` | replace as primary API | Raw constrained `gh` arguments hide per-operation authority and side effects. Prefer typed broker operations; retain an escape hatch only if its capability catalog and conservative annotations are explicit. |
| `github_issue_fields` | split | Read and mutation actions cannot share truthful tool-level side-effect annotations; expose typed read/write operations separately. |
| `project_coordination` | redesign | Ten canonical reads have different identifiers; use discriminated requests and provide state/allowed-transition data needed by resume. |
| `project_coordination_write` | redesign | Claim/takeover/resume/checkpoint/release/done require different target/precondition shapes; keep request-ID replay safety but express each transition in schema. |
| `project_context` | keep/refine, implemented | Loki recomputes target-owned guidance, selected Skill revisions, repository/worktree/code evidence, canonical coordination, checkpoint staleness/completeness, and legal claim/takeover guidance. Keep this ownership model; the unified Job surface has landed, while composed live-Job evidence remains a separate follow-up if the context workflow needs it. |
| `project_context_write` | keep/refine, implemented | The public caller supplies request ID, expected basis, expected previous checkpoint, and bounded explanatory fields only. Loki recomputes authoritative basis, requires active Run ownership, and writes through the runtime-owned journal. Keep this CAS/replay model; move to generated typed schemas with the wider contract redesign. |
| `agent_guidance` | keep/refine, implemented | Native source/revision/completeness, target-aware Skill loading and the generated action-specific contract are implemented. Keep the capability and refine only product-driven nested/completeness semantics rather than reopening the ownership model. |

## Running Python-only tool disposition

The currently connected Python 0.47.1 server exposes 13 additional tools that are absent from the Go 0.49 contract. Their absence is intentional only where the replacement path below lands; they should not be copied into Go merely for compatibility.

| Running Python tool | Target disposition |
| --- | --- |
| `runtime_stop` | Fold ordinary process cancellation into the unified Job cancel lifecycle; do not keep a separate port/process stop dispatcher. |
| `project` | Do not recreate the large multi-action MCP surface. Canonical project/workstream state stays behind the narrower devtools coordination integration. |
| `task_inspect` | Replace by canonical devtools coordination reads needed by the active workstream, not a parallel Taskwarrior MCP API. |
| `task_write` | Replace by canonical devtools task/Run transitions with typed transition contracts. |
| `task_delete` | Keep deletion semantics in the canonical coordination owner; expose only when required by the target product workflow. |
| `agent_context` | Replaced by Loki-native `agent_guidance`/composed project context, with target-scoped AGENTS.md and Skill provenance. |
| `skill_read` | Replaced by Loki-native Agent Skill discovery/inspection under the standard portable Skill format. |
| `skill_write` | Replace with a guarded Loki-native Skill lifecycle API only after Repository Scope and user-scope storage authority are complete. |
| `bootstrap_project` | The unified Job model is now exposed; express setup as a registered workflow/Job entry point when this capability is reintroduced, and do not keep a special execution lifecycle. |
| `action` | Split configuration from execution and process state; do not port the configure/run/process/stop monolith. |
| `command_run` | Replace with unified finite Job start/wait/result behavior. |
| `command_start` | Replace with the same Job start API plus explicit lifetime/deadline semantics. |
| `process_inspect` | Replace with Job inspect/output APIs backed by durable supervisor-owned state. |

This inventory is the migration checklist for public tool coverage. A removed tool is acceptable only when its required user capability has a named replacement and acceptance coverage.

## Workspace batch design

A structured batch is justified because the existing `patch` is efficient only when a unified diff is already natural. The common case “apply guarded replacements/creates/moves across several files” should not require the model to manufacture a diff merely to obtain one call.

A suitable `workspace_edit(action=batch)` request is a bounded list of discriminated operations. Initial variants should stay narrow: `create`, `replace`, and `move`. Destructive tracked deletion remains a separate capability until its recovery and authorization semantics are explicitly designed.

Every existing source file has an expected digest. Every expected-absent destination states that precondition explicitly. The planner validates path confinement, UTF-8/write limits, replacement counts, duplicate/conflicting paths, destination conflicts, and the complete virtual operation sequence before mutation.

Execution semantics:

1. validate the whole request and build a deterministic plan;
2. acquire the workspace mutation lock;
3. re-check every expected state under the lock;
4. capture all required preimages/revision records before the first write;
5. write an in-progress transaction journal containing only bounded non-secret recovery metadata;
6. apply the planned mutations;
7. on a synchronous failure, restore the captured preimages and report whether rollback completed;
8. publish a terminal transaction record and return affected paths, resulting digests, previous revision IDs, and operation ID;
9. on process crash/restart, reconcile any in-progress journal before allowing an overlapping mutation and report recovered or `outcome_unknown` truthfully.

Until crash reconciliation exists, the API must not describe the batch as fully atomic. A mutex plus rollback only provides serialization and best-effort failure recovery.

The existing unified-diff action remains because it is the right primitive for already-diff-shaped, multi-hunk, multi-file text changes. Its public description should explicitly state the configured file bound, supported create/modify behavior, preflight, revision capture, and prohibited delete/rename/copy/symlink/binary operations.

## Session interruption and handoff contract

Conversation termination cannot be treated as a reliable lifecycle callback. ChatGPT can hit a conversation-length limit after a tool result and before the agent can write a final handoff. Recovery therefore has to be constructed from durable state that is updated during normal work.

Canonical recovery inputs are:

1. current AGENTS.md chain and selected Skill revisions, re-resolved for the actual target;
2. canonical task/workstream/Run state from devtools;
3. Loki-owned semantic checkpoints written at meaningful state transitions;
4. durable tool/job/validation/Git evidence recorded after that checkpoint;
5. the actual current Git/workspace state;
6. explicit staleness, truncation, and unknown-outcome diagnostics.

Semantic checkpoints are refreshed after material decisions, work-item state changes, validation outcomes, blocker changes, and changes to the exact next action. They do not need to follow every read call. The implemented `project_context` path re-resolves current guidance, Skill revisions, canonical coordination, Git/worktree/code evidence, and the latest runtime-owned checkpoint; stale or incomplete evidence is reported instead of replaying checkpoint prose as current truth.

A new-session bootstrap should be deterministic:

```text
resolve agent guidance for target
  -> resolve current project/workstream/task/run
  -> call project_context for composed resume state
  -> inspect current Git/workspace evidence and relevant live jobs when live execution state matters
  -> reconcile checkpoint + current canonical/actual state
  -> choose one legal claim/resume/takeover transition
  -> continue from the recorded/reconciled next action
```

The user should be able to say only “continue the interrupted work”. They should not need to provide a task ID, Run ID, last filename, or prior chat summary when Loki has enough durable state to resolve it.

### Abrupt-loss acceptance scenarios

The public unified Job service/API has landed. The recovery harness still needs a composed live-Job scenario before Job state can be treated as normal resume evidence, and the real OCI/network fixture remains the acceptance blocker for the underlying execution path:

- clean checkpoint, then immediate new MCP session;
- file mutation after checkpoint, response received, then conversation ends before a new checkpoint;
- mutation request reaches the server but the client loses the terminal response;
- validation evidence is rehydrated after restart; a composed live-Job resume scenario is still required even though the public Job API now exists;
- checkpoint becomes stale because task definition, Git basis, AGENTS.md, or selected Skill revision changed;
- no semantic checkpoint exists at all;
- prior session disappears while a devtools Run is still active;
- multiple repositories/worktrees exist and only the correct repository scope is resumed.

Success means the next session reports what is authoritative, what is inferred, what is stale/unknown, and the exact safe next transition without replaying hidden conversation state.

## Validation plan

Static contract validation:

- every action variant has schema-level required and forbidden fields;
- unknown and irrelevant fields fail before handlers;
- no concurrency/idempotency/scope guard is accepted and ignored;
- read/write annotations match the whole tool; mixed tools are split;
- output variants are closed unless an extension map is explicitly designed;
- every bounded collection exposes completeness and continuation semantics;
- tool descriptions and field descriptions include capability limits and selection guidance needed for normal use;
- handler/catalog drift tests prove every public property is consumed or explicitly rejected.

Behavioral safety validation:

- batch edit preflight leaves all target files untouched on any invalid operation;
- synchronous mid-batch failpoints restore all preimages or return an explicit recovery failure;
- crash/restart failpoints reconcile an in-progress batch journal;
- duplicate request IDs return the same terminal mutation result without replay;
- Git index CAS prevents staging over an independently changed index;
- lost-response tests inspect operation status before retry;
- browser stale references fail as conflicts rather than acting on a different element;
- secret/public-config tests make the unsafe “set secret through visible value” path structurally unavailable.

Agent-use validation:

Maintain a small client-neutral scenario corpus and run it against each declared client integration. Assert safe capability selection rather than exact hidden reasoning. Representative scenarios:

- replace the same construct across several guarded files -> structured batch or one multi-file patch, not a fabricated unsupported array of `replace` calls;
- apply an existing unified diff across several files -> one patch call;
- stage three whole files -> one `git_stage paths` call;
- partially stage hunks across two files -> one staging patch;
- publish several files for download -> one bundle;
- store an API credential -> staged import/generation path, never visible public-value `set`;
- continue interrupted repository work -> guidance + composed resume context + actual-state reconciliation;
- response to a mutation is lost -> operation lookup before retry;
- click after navigation changed the page -> re-observe or receive a stale-reference conflict, not blind replay.

The harness should tolerate multiple valid plans where the contract permits them, but reject unsupported batching, ignored safety guards, wrong authority paths, false completeness, and unsafe retries.

## Recommended execution sequence

Do not rewrite every tool in one catalog-sized patch. Apply one common contract model, then migrate families with regression coverage.

1. **Contract source of truth**
   - Move public request/result definitions out of the frozen JSON fixture as the authoring source.
   - Generate or construct MCP schemas, field descriptions, annotations, examples, and snapshot artifacts from reviewed typed operation definitions.
   - Add action-discriminated required/forbidden-field tests and a handler/catalog drift check.
   - The confirmed `preview_publish.environment_routes` drift is resolved: the field is removed, contract/handler drift tests cover it, and Job publication uses explicit `job_id` plus logical `endpoint` instead.

2. **Operation and error envelope**
   - Define stable operation/correlation IDs, request-ID replay rules, typed public error categories, retryability, and `outcome_unknown`.
   - Add status lookup for operations whose response can be lost.
   - Keep sensitive causes only in protected diagnostics.
   - This precedes replay-sensitive share creation, multi-resource mutation, provider mutation, and session-recovery guarantees.

3. **Workspace/Git reference migration**
   - Make `workspace_edit` and `git_stage` the first full examples because their present ambiguity is reproduced and their concurrency primitives already exist.
   - Publish multi-file patch/stage semantics directly in descriptions and branch schemas.
   - Add guarded structured workspace batch with failpoint/rollback/restart tests.
   - Add missing observed-state guards to destructive file mutation and make Git index CAS the normal staging path.
   - Use this family to prove the generated contract, typed output, operation-ID, and recovery model before copying it elsewhere.

4. **Unified Job surface**
   - Durable Job start/inspect/output/cancel is source-implemented with request-ID replay, disconnect-independent lifetime, bounded output, restart reconciliation, logical network profiles and Job-owned endpoint leases.
   - MCP reaches the unprivileged executor only; the executor reaches the privileged launcher; launcher/backend authority is absent from the public request/result contract.
   - Remaining work for this step is acceptance: run the accumulated deterministic gate and the real supported-host OCI/network fixture before retiring legacy execution paths or treating the surface as release-enabled.

5. **Stateful UI and sharing families**
   - Browser session/observe/interact keeps its generation/stale-reference model and continues its separate desktop-interaction completion work.
   - Preview creation is request-ID replay-safe and Job publication now binds exact endpoint leases; image/artifact sharing retains its own family-specific follow-up.
   - Real preview HTTP/WebSocket, restart, stale lease and numeric host-port reuse must still pass through the supported-host Job fixture before the endpoint-identity correction is acceptance-closed.

6. **Secrets and provider integrations**
   - Separate public configuration from secret lifecycle at the contract level.
   - Split or discriminate provider reads and mutations so annotations are truthful.
   - Replace generic provider command strings with typed operations for common privileged workflows; retain a constrained escape hatch only when its authority is explicit.

7. **Coordination, guidance, and resume**
   - Loki-native `agent_guidance`, runtime-owned semantic checkpoints, `project_context`, expected-basis checkpoint writes, fresh-session restart recovery, and devtools compaction decoupling are implemented.
   - The reduced devtools candidate contract is fingerprint-gated before runtime readiness and has passed runtime+MCP integration acceptance.
   - Action-specific schema generation and the public Job surface have landed. Remaining context work is limited to product-driven composed evidence needs; do not reopen context ownership or restore devtools compaction extensions.

8. **Client-neutral scenario gate**
   - Run the scenario corpus against each declared client integration and release artifact.
   - Reject releases where a valid user intent still predictably leads to an unsupported call shape, ignored safety guard, unsafe retry, false completeness, or stale-resource action.
   - Retire old tools only after their named replacement passes the same user capability scenario.

Each migrated family gets contract snapshots, semantic unit tests, fault/retry tests where applicable, and at least one agent-use scenario. These checks are authored with the family but follow the repository validation cadence: narrow execution only when later implementation depends on the result, with broad contract and repository gates deferred until the coherent implementation batch is complete. Tool-count reduction is not itself a success metric; predictable safe selection is.

## Implementation placement

The overarching architecture plan should own these cross-cutting rules.

- A02 owns typed request/result/error/idempotency contract primitives.
- A05 owns durable operation/job status needed for uncertain-result recovery.
- A07 owns MCP generation, discriminated schemas, operation metadata, and removal of legacy execution-tool ambiguity.
- A08 owns workspace/Git batch, CAS, recovery, and precise edit/stage semantics.
- A10 owns browser/share/Git-provider domain-specific contract cleanup.
- A14 must include the agent-use and abrupt-loss recovery scenarios, not only server unit/integration tests.

The active Loki-native agent-context workstream should carry only the handoff/resume requirements that belong to context ownership. It should not absorb the entire tool-catalog redesign.
