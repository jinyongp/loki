---
name: review-loop
description: >
  Run a unified scoped code review plus parent-owned fix loop only when the user
  directly requests /review-loop, review until clean, code review with fixes,
  or review-and-fix. Supports web, server, database, node, and planning modes.
---

# Review Loop

Trigger semantics:
- `/review-loop`, `review until clean`, `review-and-fix`, or `code review with fixes` means the user is requesting the review-loop workflow.
- Run this skill only from a direct user review request. Do not infer or inherit
  it from implementation, validation, convergence, closeout, or another parent
  workflow.
- Plain `review` or `code review` means review-only: report findings and do not fix unless separately requested.

Modes:
- `web`: browser/client, SSR/rendering, hydration, client state, assets, UI/UX
- `server`: HTTP/API, middleware, auth/session, webhooks, jobs, queues, integrations
- `database`: schema, migrations, ORM/models, SQL, transactions, indexes, constraints, fixtures/backfills
- `node`: JS/TS runtime, package scripts, CLIs, workers, libs, build tooling, modules/deps/process/fs
- `planning`: plans, specs, ADRs, RFCs, requirements, design docs

Aliases:
- web: `frontend client browser ui ux design ssr rendering hydration assets`
- server: `api backend http middleware auth session webhook jobs queue integration`
- database: `db sql orm migration schema transaction index constraint repository data-access`
- node: `js ts typescript javascript nodejs package npm pnpm yarn cli worker build esm cjs`
- planning: `plan planning docs document design-doc spec proposal adr rfc requirements roadmap task-list implementation-plan`

Normalize aliases. Examples: `/review-loop web server`, `/review-loop frontend api`, `/review-loop backend db`, `/review-loop ts package`, `/review-loop planning docs/checkout-plan.md`. No explicit area/mode/axis: inspect requested scope first, then infer review area, modes, risk axes, and agent plan from changed code plus direct contracts. Ask only when the target or material coverage is genuinely ambiguous.

Infer review plan from paths/content:
- web: `component|page|layout|style|css|client|browser|hydration|asset|screen`
- server: `route|api|middleware|controller|auth|session|webhook|job|queue`
- database: `schema|migration|sql|prisma|drizzle|typeorm|model|repository`
- node: `package.json|tsconfig|script|cli|worker|build|esm|cjs|fs|process`
- planning: `plan|planning|proposal|design-doc|design_doc|adr|rfc|spec|requirements|roadmap|milestone|migration-plan|implementation-plan|docs/.+\\.md|\\.md$`

## Contract

Goal: review the currently implemented and requested behavior against its active
contract, fix only eligible in-scope findings, then repeat scoped
review/validation until none remain or a user decision blocks progress.

The active agent owns scope, coverage, modes, axes, findings, fixes, decisions, validation, and oscillation. The final response is the canonical review record unless the user asks otherwise.

## Review Contract

Freeze a compact Review Contract before choosing axes:

- current goal, requirements, acceptance criteria, and implemented behavior;
- target maturity only when established: prototype, MVP, or production;
- active non-goals, accepted omissions, constraints, and accepted risks;
- governing repository MUST rules;
- validation expectations and available budget;
- the minimum safety floor below.

Treat the currently implemented and requested behavior as the scope ceiling.
Use adjacent code and direct contracts only to judge that behavior. Never turn
them into permission to design or add new capabilities, edge cases, hardening,
fallbacks, abstractions, operational features, or future compatibility.
Maturity changes review depth, not product scope. If maturity is unstated, use
the current request, plan, and implementation as the contract; ask only when an
ambiguity changes finding eligibility.

A finding is actionable only when evidence shows one of:

1. current behavior violates an active requirement, acceptance criterion,
   governing MUST rule, or established public contract;
2. the change regresses behavior already implemented inside the active scope;
3. a reachable current flow violates the minimum safety floor.

Treat undeclared cases, deliberate MVP omissions, speculative scale or abuse
hardening, new failure recovery, new provider fallbacks, future multi-tenancy,
and tests for behavior outside the Review Contract as outside scope. Do not fix,
task, or expand coverage for them. Omit future-hardening suggestions unless the
user explicitly asks for a backlog or production-readiness review.

Minimum safety floor applies only to concrete, reachable behavior:

- bypass of an access or tenant boundary the current implementation claims;
- exposure of secrets or private data handled by the current flow;
- injection or unintended code/command execution;
- irreversible data corruption or destructive action outside explicit intent.

Do not treat absent rate limiting, MFA, generalized abuse prevention, exhaustive
input handling, high availability, or speculative deployment hardening as a
safety-floor violation unless the Review Contract explicitly requires it.

Do not describe any in-progress cycle, local-only validation, or candidate clean state with terminal wording that implies the loop is
done. During the loop, use precise non-terminal wording such as "cycle
validation", "local validation", "affected-axis re-review",
"candidate clean pending CI", or "cycle closeout". Reserve
terminal wording such as "final", "done", "complete", or "clean" for states
where all Review Contract axes are closed, no eligible actionable
findings remain, and final validation is complete or explicitly blocked.

## Continuity

Keep a compact cycle ledger in the active plan artifact when one exists, or in the conversation otherwise. At the end of every full cycle, record the cycle number, completed axes, unresolved findings, fixes applied, validation status, and next entry point. A single clean axis or context compaction is not a stop condition.

## Gates

1. Scope explicit wins. No scope = current session changes only: files/behaviors
   created, edited, deleted, or intentionally generated by the current assistant
   session, plus direct contracts needed to judge those changes. Never review
   all Git uncommitted changes by default.
2. Freeze the Review Contract before deriving coverage. Do not widen it after
   discovering optional improvements.
3. Stop+ask if no Git repo, no reliable current-session changed scope,
   unreadable/unclear target, or review plan still unclear after scope
   inference. Do not fall back to all uncommitted changes unless the user
   explicitly asks for that scope.
4. User says plain `review`/`code review` only => review-only. User says
   `/review-loop`, until clean, fix, or review-and-fix => review-and-fix.
5. Derive a coverage plan from the Review Contract. Cover every implemented
   in-scope behavior, but no excluded or undeclared behavior. Select only
   material modes and axes.
6. Run axes serially. Parallelize independent read-only MCP calls when the
   client permits it and they do not compete for mutable state.
7. Run planned axes in waves; do not add axes for excluded
   cases or optional hardening.
8. Refresh the cycle ledger once per cycle before deciding whether to continue, complete, or block.
9. The active agent aggregates, edits, tests, validates, and reports the result.
10. Loop until the Review Contract has no eligible actionable findings or user decision blocks.
11. Oscillation: A->B->A serious. Same issue twice => stop/blocker.
12. Scoped final validation after cleanup. In-contract fail => fix + affected-axis re-review + validation again.
13. DB guard: no prod/staging migrations, destructive resets, seed truncation, backfills, data repair, live provider ops, shared-env writes without explicit approval.

Severity:
- P0: reachable safety-floor breach, data loss, or outage in current scope
- P1: likely in-contract bug, regression, or data-integrity issue
- P2: in-contract edge-case bug or missing validation causing real current risk
- P3: in-contract maintainability issue only when correctness or operation is affected

## Scope Discovery

```text
git rev-parse --show-toplevel
git diff --name-only --cached
git diff --name-only
git ls-files --others --exclude-standard
git diff --cached -- <session-path>
git diff -- <session-path>
git diff --no-index -- /dev/null <untracked-path>
```

Steps: verify root; verify explicit scope readable. For default scope, first
build the current-session changed set from this thread's tool history, active
cycle ledger, available plan/task notes, and files the assistant reports
as edited/created/deleted/generated. Use Git commands to inspect diffs for only
those paths and to detect whether each path is staged, unstaged, untracked, or
deleted. Do not add unrelated uncommitted files merely because Git reports them.
Include generated/lockfile only when generated by this session or required by a
session-touched source change. Exclude vendored/dist unless changed by request.
Empty or unreliable default session scope => ask+stop. Derive review area
(feature/module/file set), modes, axes, coverage map, and review plan from
session-changed paths/content plus direct deps/contracts/tests/config/generated
sources. Read adjacent sources only far enough to judge current in-contract
behavior and confirm eligible findings.

## Axes

Use selected modes only. Reference axis lists are menus filtered by the Review
Contract, not completeness requirements. Cover every implemented in-contract
behavior, but do not select axes for excluded cases or optional hardening.
Select a high-risk axis only when an active requirement, governing MUST rule, or
concrete reachable safety-floor concern requires it. Merely touching auth,
persistence, provider I/O, routing, or user-facing code does not authorize
broader product or hardening work.

Use `skill_read` with `action: resource` to load selected mode references:
- web: `references/web.md`
- server: `references/server.md`
- database: `references/database.md`
- node: `references/node.md`
- planning: `references/planning.md`

Use `skill_read` with `action: resource` for `references/code-quality.md` only when the Review Contract or a governing
rule requires a quality axis, or current helper/API structure creates a concrete
in-contract correctness risk. Do not load it merely because a helper changed.
Use `skill_read` with `action: resource` for `references/ui-ux.md` only when the Review Contract includes presentation,
interaction, or usability behavior, or evidence shows a current user-facing
regression. Do not load it merely because the scope contains UI.
For an explicitly selected Planning mode, activate `planning` before reading
`references/planning.md` through `skill_read` with `action: resource`.

Custom axes: preserve. Too many axes become serial waves. Parallelize only independent read-only inspections when the MCP client permits it.

## Finding format

```text
path:line | P0-P3 | mode/axis | status | issue | fix | validation
```

Status: `actionable|suspected|out-of-scope|user-decision`. Clean: `no actionable findings`. Include evidence, not prose. Keep out-of-scope items out of the final record unless the user explicitly requested backlog or hardening suggestions.

## Fix Loop

1. Start each cycle from the latest coverage map plus cycle ledger when available.
2. Dedup by root cause/files/fix; keep mode+axis tags.
3. Recheck every candidate against the frozen Review Contract before editing.
   Drop candidates that require new behavior or optional hardening.
4. Queue only eligible user decisions that block the current contract.
5. If not review-only, fix only eligible local in-scope issues. Keep the
   smallest behavior-preserving patch that restores the current contract.
6. Prove suspected security/race/runtime/cache/webhook/data/transaction/migration/query/integration bugs before edit with smallest reliable repro/test/dry-run/schema/query/transaction/replay/mock/import check.
7. Do not add new flows, fallbacks, configuration, retries, abstractions,
   permissions, validation branches, operational controls, compatibility layers,
   or tests for excluded behavior. Safety-floor fixes remain allowed only for a
   concrete reachable violation.
8. No broad redesign, public contract reshape, package/routing/auth/session/deploy/DB engine/ORM migration/destructive repair/product change without approval.
9. Do not “fix” by weakening tests, changing expected behavior without approval, adding speculative refactor, dependency upgrade, legacy path, or compatibility shim that violates stated direction.
10. Test cleanup is allowed when a test asserts volatile implementation details that are not behavior contracts. Remove or rewrite assertions against Tailwind/static class strings, incidental DOM structure, generated ids, ordering without semantic meaning, exact copy not owned by the feature, timestamps, random values, or other frequently changing values unless the test is explicitly about that style/content/serialization contract.
11. After fixes: re-review touched files/contracts; run targeted validation.
12. Affected-axis re-review after fixes. If fixes alter reviewed behavior, refresh the coverage map and repeat selected axes until the Review Contract is clean.
13. End each cycle by recording completed axes, remaining findings, validation results, and next entry point in the cycle ledger.
14. Check oscillation before touching same issue again. Same root cause after 2 fix attempts => stop+ask. Product decision required => defer, don't guess. Validation unavailable after workaround => stop, report risk.

## Validation

Discover repo commands. Prefer targeted tests, touched-package typecheck/lint, route/API/middleware/webhook/integration mocks, browser/runtime tests, schema/generated-client checks, migration dry-run, SQL lint/parse, local/test DB, query/transaction tests, CLI/import checks.

Final: run the smallest reliable validation that proves the Review Contract.
Run project-wide CI only when the user, repository rules, or the active contract
requires it. If format/lint-fix exists and code changed, run only its scoped
form when available, re-review changed files/contracts, and rerun affected
validation. Final status cannot be clean unless in-contract coverage, fixes,
re-review, and scoped validation are complete or explicitly blocked.

Never prod-affecting/destructive/deploy/dependency update/secret rotation/live provider/destructive migration/seed reset/backfill/shared-env DB write without approval.

## Final

Include the Review Contract summary, scope, coverage summary, modes, review-only
vs fix, axis execution, final status, fixed issues, deferred decisions,
validation, and remaining in-contract risks. Do not include excluded cases or
future-hardening ideas unless requested. No code changes? say so. No decisions?
“No remaining user decisions.” Validation blocked? exact missing
service/credential/dependency/env.
