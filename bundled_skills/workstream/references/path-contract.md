# Task Identity and Project-State Contract

Loki owns one central project-state profile for every Git common directory.
All worktrees share its Taskwarrior database and workstream artifacts, while
each worktree keeps an independent active-workstream binding.

## Resolution

1. Settle the exact one-sentence Goal, artifact depth, and intent source.
2. Call `project` with `action: status` and the intended worktree `cwd`.
3. Choose exactly one intent source kind: `plan-local`, or `authoritative` with its exact source.
4. Unless the user supplied an explicit full slug, choose a semantic `slug_base` of three to five lowercase ASCII English tokens that conveys the concrete domain/object and outcome.
5. Call `project action=init` with the Goal, depth, intent source, and either `slug_base` or the user-supplied `slug`.
6. Use the returned slug exactly. Loki appends and validates the Goal hash.
7. Create or update `spec.md`, `plan.md`, and `validation.md` only through `project action=write`. Read an existing artifact first and pass its `sha256` when updating it.

## Canonical state

```text
project-state profile
├── shared Taskwarrior queue
├── workstreams
│   └── <slug>
│       ├── manifest.json     # immutable; owned by Loki
│       ├── spec.md           # Standard/High-risk, plan-local intent only
│       ├── plan.md           # always
│       └── validation.md     # execution validation evidence
└── worktree bindings         # active slug per worktree
```

- Task project: `<slug>` with no workflow prefix.
- Artifact filenames are fixed. Never add dates, titles, sequence numbers, `final`, `v2`, or agent-specific suffixes.
- Authoritative intent omits `spec.md`; record its exact source without rewriting it.
- Plan-only work never creates `validation.md`.
- Create no empty placeholder artifacts.
- Never create a worktree-local Taskwarrior DB, workstream ledger, `.taskrc`, or
  another state root. A repository may reserve `.tasks` for worktree-local build
  and validation output; that data is not part of Loki project state.

## Identity and reuse

- Goal identity is lexical: NFKC-normalized, whitespace-collapsed, case-folded, and SHA-256 hashed by Loki.
- Reuse occurs only when the immutable manifest exactly matches slug, Goal/hash, depth, intent source, and task project.
- A matching slug with different identity is a collision and must fail.
- Resume from a known slug, manifest, plan, or active binding. Do not match workstreams by semantic similarity.
- Use `project action=bind` when switching the current worktree to an existing workstream.
- Project-state writes use optimistic revisions. On a stale SHA-256 error, reread and reconcile; never overwrite another worktree's changes.

## Migration

Legacy `.tasks/` data is migrated only by an administrator with
`sudo loki state migrate <repository>`. The migration preserves Taskwarrior
data and workstream artifacts, verifies the copied task database, publishes the
central state atomically, and removes the legacy directory only after success.
