# MCP Tool Contract Review

Date: 2026-09-19
Status: design review for the Go migration; no compatibility constraint requires preserving the current Python or Go public tool shapes.

## Scope and evidence

This review examines two surfaces:

- the running Python Loki 0.47.1 catalog, which currently exposes 37 MCP tools;
- the checked-in Go 0.49 development contract, which currently defines 31 MCP tools.

The Go contract intentionally removes the legacy project/task/Skill/command/process MCP wrappers and now exposes `project_coordination`, `project_coordination_write`, `agent_guidance`, `project_context`, and `project_context_write`. The Agent Context slice has landed, including durable semantic checkpoints and fresh-session resume, while the remaining file, browser, sharing, Git, secret, and system tools still retain much of the current public shape.

Verified contract facts for the Go candidate:

- 18 of 31 tools multiplex operations through an `action` field.
- All 18 action-multiplexed schemas expose a flat superset of fields rather than action-specific required/forbidden shapes.
- 153 input properties are present in the captured catalog and only 10 currently have a JSON Schema `description`.
- 21 tools use an open `additionalProperties: true` output object, four have no output schema, and two have no MCP annotations.
- `internal/mcpserver` validates the captured JSON Schema before calling a handler, but action-specific requirements are commonly deferred to handler-time `Require` checks. The public schema therefore cannot tell an agent which arguments are required for a selected action.
- `workspace_edit(action=patch)` is already multi-file in both implementations. Python defaults to `max_patch_files = 50`; Python and Go both parse the entire diff, validate every target path, run `git apply --check`, capture pre-mutation revisions, and then issue one `git apply` under the workspace mutation lock. The public description only says “patch” and the Python server guidance only says “multi-hunk”, so this capability is not discoverable from the contract.
- The Go `preview_publish` schema exposes `environment_routes`, but the current Go handler stores that field in the request struct and never consumes it. There is no corresponding test. This is concrete schema/handler drift.
- `workspace_edit` accepts fields such as `expected_sha256` for every action at schema level, although the guard is consumed only by `replace`. Supplying that guard with `patch`, `create`, or `move` does not protect those operations. This is exactly the class of silently ineffective precondition that the architecture plan says to reject.
- `mcpserver.Object` currently serializes the same result into both structured content and JSON text content. This conflicts with the architecture goal of avoiding duplicate large output.
- `system_inspect(action=activity)` now records server-side start/terminal evidence with invocation IDs and pseudonymous session references, but the running 0.47.1 server predates that action and a normal tool result does not yet return the audit invocation ID to the caller.

The baseline contract tests protect catalog presence, unknown top-level fields, credentials, and selected legacy removals. They do not currently assert action-specific field legality, semantic use of accepted concurrency guards, closed output variants, or model-facing tool-choice behavior.

## Verification evidence

The review was rechecked against the repository after the inventory was written.

- Go contract census: 31 tools total; 18 expose an `action` property; all 18 still use a flat superset schema rather than top-level `oneOf`/`anyOf` action variants.
- Input-schema census: 153 public properties; 10 property-level JSON Schema descriptions.
- Output/annotation census: four tools have no output schema; 21 use an open `additionalProperties: true` output object; two have no MCP annotations.
- Per-tool accounting: all 31 Go tools have an explicit disposition in this document.
- Running-server accounting: all 13 tools present in Python 0.47.1 but absent from the Go 0.49 contract have an explicit migration disposition.
- Running Python server evidence: version 0.47.1, catalog revision 2026-09-04.4, 37 exposed tools.
- Multi-file edit evidence: both `legacy/python/src/loki_mcp/tools.py` and `internal/workspace/search_patch.go` parse the complete patch file list, validate bounded paths, run `git apply --check`, capture existing-file revisions and apply the diff under the workspace mutation lock. Python configuration defaults `max_patch_files` to 50.
- Schema/handler drift evidence: `environment_routes` appears in the Go `preview_publish` contract/request struct but has no consumer elsewhere in `internal/`.
- Ownership-drift check after this review: the stale phrases assigning Skill inventory, AGENTS.md resolution, or semantic handoff state to the devtools integration no longer remain in the active architecture/project-structure plans.
- Agent Context implementation evidence: `project_context` and `project_context_write` now compose target-owned guidance, selected Skill revisions, Git/worktree evidence, canonical devtools coordination, runtime-owned semantic checkpoints, staleness/completeness, and legal claim/takeover guidance. Fresh-session restart and uncertain semantic-checkpoint response replay are covered by Go integration tests.
- Devtools candidate convergence evidence: the active source-reviewed fixture no longer contains Skill/guidance/compaction extensions, and runtime startup fingerprints the executable approved contract and rejects drift before readiness.

These counts are baseline evidence, not permanent target numbers. Contract generation work is expected to change them.

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
| Runtime/commands (legacy) | `action`, `command_run`, `command_start`, `process_inspect`, and `runtime_stop` overlap and require the agent to choose among execution lifecycles | Do not port the monolith. Finish the single Job model and expose start/inspect/output/cancel with explicit detached/lifetime semantics; keep action/workflow registration separate |
| Preview/shares | Publish/create-share operations are non-idempotent; retry after a lost response can create duplicates; `environment_routes` is currently accepted but unused in Go | Request IDs for creation; idempotent revoke; endpoint/job identity rather than raw-port lifetime; remove or implement every exposed field; action-specific schema |
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

The following matrix accounts for every tool in the checked-in Go 0.49 contract.

| Tool | Disposition | Contract issue / required change |
| --- | --- | --- |
| `system_inspect` | refine | Keep diagnostics, but make action variants explicit and add direct operation/invocation lookup rather than recent-list-only recovery. |
| `preview_publish` | redesign | Action-specific server/stack inputs, request-ID replay safety, job/endpoint identity, and removal or implementation of the currently unused `environment_routes` field. |
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
| `project_context` | keep/refine, implemented | Loki recomputes target-owned guidance, selected Skill revisions, repository/worktree/code evidence, canonical coordination, checkpoint staleness/completeness, and legal claim/takeover guidance. Keep this ownership model; tighten nested output schemas and integrate live Job evidence when the unified Job surface lands. |
| `project_context_write` | keep/refine, implemented | The public caller supplies request ID, expected basis, expected previous checkpoint, and bounded explanatory fields only. Loki recomputes authoritative basis, requires active Run ownership, and writes through the runtime-owned journal. Keep this CAS/replay model; move to generated typed schemas with the wider contract redesign. |
| `agent_guidance` | redesign contract, keep capability | Native source/revision/completeness and target-aware Skill loading are implemented; context/Skill request variants still need discriminated schemas and tighter outputs. |

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
| `bootstrap_project` | Express setup as a registered workflow/job entry point after the unified Job model is exposed; do not keep a special execution lifecycle. |
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
  -> inspect current Git/workspace evidence and relevant live jobs when the unified Job API is available
  -> reconcile checkpoint + current canonical/actual state
  -> choose one legal claim/resume/takeover transition
  -> continue from the recorded/reconciled next action
```

The user should be able to say only “continue the interrupted work”. They should not need to provide a task ID, Run ID, last filename, or prior chat summary when Loki has enough durable state to resolve it.

### Abrupt-loss acceptance scenarios

The recovery harness covers the Loki-native cases below; the live-Job case remains blocked on the separate unified Job service/API:

- clean checkpoint, then immediate new MCP session;
- file mutation after checkpoint, response received, then conversation ends before a new checkpoint;
- mutation request reaches the server but the client loses the terminal response;
- validation evidence is rehydrated after restart; live Job state remains an external Job-workstream gap;
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
   - Fix confirmed zero-risk drift such as `preview_publish.environment_routes` by either implementing it with tests or removing it from the public contract.

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
   - Complete durable Job start/inspect/output/cancel before removing the legacy execution/process tools.
   - Ensure finite commands and long-running services differ by requested lifetime, not by unrelated MCP APIs.
   - Make disconnect/restart semantics observable and independent from an MCP turn.

5. **Stateful UI and sharing families**
   - Migrate browser session/observe/interact with page-generation or equivalent stale-reference protection.
   - Make preview/image/artifact share creation replay-safe and revoke idempotent.
   - Bind previews to owned endpoint/job identity rather than treating a numeric port as durable service identity.

6. **Secrets and provider integrations**
   - Separate public configuration from secret lifecycle at the contract level.
   - Split or discriminate provider reads and mutations so annotations are truthful.
   - Replace generic provider command strings with typed operations for common privileged workflows; retain a constrained escape hatch only when its authority is explicit.

7. **Coordination, guidance, and resume**
   - Loki-native `agent_guidance`, runtime-owned semantic checkpoints, `project_context`, expected-basis checkpoint writes, fresh-session restart recovery, and devtools compaction decoupling are implemented.
   - The reduced devtools candidate contract is fingerprint-gated before runtime readiness and has passed runtime+MCP integration acceptance.
   - Remaining work is the broader action-specific schema cleanup and unified Job evidence integration; do not reopen context ownership or restore devtools compaction extensions.

8. **Client-neutral scenario gate**
   - Run the scenario corpus against each declared client integration and release artifact.
   - Reject releases where a valid user intent still predictably leads to an unsupported call shape, ignored safety guard, unsafe retry, false completeness, or stale-resource action.
   - Retire old tools only after their named replacement passes the same user capability scenario.

Each migrated family gets contract snapshots, semantic unit tests, fault/retry tests where applicable, and at least one agent-use scenario. Tool-count reduction is not itself a success metric; predictable safe selection is.

## Implementation placement

The overarching architecture plan should own these cross-cutting rules.

- A02 owns typed request/result/error/idempotency contract primitives.
- A05 owns durable operation/job status needed for uncertain-result recovery.
- A07 owns MCP generation, discriminated schemas, operation metadata, and removal of legacy execution-tool ambiguity.
- A08 owns workspace/Git batch, CAS, recovery, and precise edit/stage semantics.
- A10 owns browser/share/Git-provider domain-specific contract cleanup.
- A14 must include the agent-use and abrupt-loss recovery scenarios, not only server unit/integration tests.

The active Loki-native agent-context workstream should carry only the handoff/resume requirements that belong to context ownership. It should not absorb the entire tool-catalog redesign.
