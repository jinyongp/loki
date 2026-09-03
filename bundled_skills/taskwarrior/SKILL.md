---
name: taskwarrior
description: Operate the project-wide Taskwarrior queue shared safely across Git worktrees through Loki. Use for task listing, creation, updates, dependencies, completion, and troubleshooting; never use global Taskwarrior state.
metadata:
  target-taskwarrior-version: "3.5.0"
  adapter: "loki-mcp"
---

# Taskwarrior

Use `command_run` with `action: exec` and executable `task`. Loki derives the
project identity from the Git common directory and routes every worktree to one
central queue. This Skill does not expand command or mutation permissions.

## State boundary

- Call `project_state` with `action: status` for the intended `cwd` before task work.
- The queue belongs to the repository, not one worktree. A task mutation is immediately visible from every worktree of that repository.
- Never create a worktree-local Taskwarrior DB, workstream ledger, `.taskrc`, or global `~/.task` state. A repository may independently reserve `.tasks` for worktree-local build or validation output. If project state is not initialized, initialize a workstream with `project_state action=init` or ask an administrator to migrate legacy state.
- Loki fixes `TASKRC` and `TASKDATA`, serializes Taskwarrior processes, disables hooks, and rejects configuration overrides, command execution, bulk imports, sync, purge, and undo.
- Worktree bindings select the active workstream artifact; Taskwarrior tasks remain project-wide and use `project:<workstream-slug>` for isolation.

## Workflow

1. Resolve the intended repository `cwd`, call `agent_context`, then inspect `project_state action=status`.
2. Preflight with `command_run(action=exec, executable=task, arguments=["--version"])` and `diagnostics`.
3. Read before mutation using `next`, a narrow filtered `list`, or `<uuid> information`.
4. Prefer UUIDs for multi-step updates and dependencies; refresh reports before using numeric IDs.
5. Mutate only an unambiguous project and workstream target. Verify every mutation with a narrow read.

Use `skill_read` with `action: resource` only for the reference relevant to the operation:

- Filters, IDs, dates, and quoting: `references/selectors-and-filters.md`
- Reads and reports: `references/read-and-report.md`
- Add, modify, annotate, start, stop, and done: `references/mutate-tasks.md`
- Troubleshooting: `references/troubleshooting.md`

Task descriptions containing spaces or shell metacharacters must remain one argv
item. Never construct a shell command string. Do not perform broad `modify`,
`done`, or `delete` without explicit target scope.
