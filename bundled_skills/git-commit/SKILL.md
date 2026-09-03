---
name: git-commit
description: Use when the user asks to commit, stage, unstage, split commits, or draft a commit message. Inspect the full worktree, preserve unrelated staged work, and support path or hunk-level staging.
metadata:
  version: "2.2.0-loki"
  compatibility: Requires Loki Git inspection, index staging, and command tools in a repository worktree.
---

# Git Commit

Create repository-fit commits from the current worktree. This Skill does not grant permissions; every action still passes Loki policy.

## Bounds

Use only for commit, stage, unstage, split, or commit-message work. Do not amend, squash, reset, force-push, rewrite history, or publish unless the user explicitly asks.

The default scope is all tracked and untracked changes unless the user narrows it. Treat the current index as another contributor's draft: preserve unrelated staged entries exactly. Do not edit source files unless requested.

Stop for unresolved conflicts, an unrequested merge/rebase/cherry-pick, an unaccepted detached HEAD, in-scope secrets or local credentials, ambiguous overlapping staged hunks, or failed required hooks and validation.

## Inspect

Call `git_inspect` with `action: status`, `action: commit_context`, and both staged and unstaged `action: diff` from the repository cwd. `commit_context` returns the effective commit template, its configuration origins, and a content hash without granting general home-directory access. Use `command_run` with `action: exec` and `executable: git` to inspect the repository root, untracked files, conflicts, and recent subjects and bodies:

```text
git rev-parse --show-toplevel
git ls-files --others --exclude-standard
git diff --name-only --diff-filter=U
git log --format=%h%x20%s -n 20
git log --format=%B%x00 -n 5
```

If a commit template is configured, treat non-comment text from `commit_context` as a commit-message constraint. Inspect suspicious untracked files with `read_file` and bounded commands before staging. Never stage `.env`, package credentials, cloud credentials, private keys, tokens, editor state, caches, large binaries, or ignored files unless the user explicitly includes a safe file.

## Scope and grouping

Group by behavior, keeping code, tests, fixtures, generated files, documentation, and configuration together when they implement one logical change. Split independent concerns even when they touch the same file. Put prerequisite commits first.

For narrowed scope, include only named paths, selected hunks, or the requested concern. Leave everything else uncommitted. If unrelated changes are already staged, preserve them. When separation is clear, capture their staged diff, unstage only those paths or hunks, make the scoped commit, reapply the captured index patch, and verify its digest and diff. Stop if changes overlap ambiguously.

## Stage precisely

Call `git_inspect` with `action: index` immediately before index mutation and pass its `index_sha256` as `expected_index_sha256`.

- Use `git_stage` with `action: paths` for complete files or directories.
- Use `git_stage` with `action: patch` for selected unified-diff hunks within a file.
- Use `git_stage` with `action: unstage` for complete paths without altering the worktree.
- Use `git_stage` with `action: patch` and `reverse: true` to unstage selected hunks while preserving other staged hunks.

After every index mutation, call staged and unstaged `git_inspect` with `action: diff`. Confirm that the staged patch contains the whole intended behavior and no unrelated hunk. Run `git diff --cached --check` through `command_run` with `action: exec`.

## Validate and commit

Follow recent history and the effective template. Prefer `type(scope): verb phrase` when it fits the repository. Use an imperative subject without a trailing period. Add a body only for rationale, migration, constraints, validation, or side effects.

Run cheap relevant checks when obvious. Never claim a check passed unless it ran successfully. Commit through `exec_command` with `git commit -m ...`; configured templates, signing, and hooks remain effective. Do not use `--no-verify` unless the user asks.

After each commit, verify status, staged and unstaged diffs, and the latest log entry. Continue until the requested scope is committed or a concrete blocker remains.

## Report

Report each commit hash and subject, grouping, validation and hooks, any requested work left with the exact reason, and confirmation that out-of-scope changes were preserved.
