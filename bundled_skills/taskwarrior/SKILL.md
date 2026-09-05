---
name: taskwarrior
description: Operate Loki's shared repository Taskwarrior queue through typed MCP tools.
metadata:
  adapter: loki-mcp
  target-version: "3.5.0"
  required-tools: [project, task_inspect, task_write, task_delete]
---

# Taskwarrior

Loki owns the task configuration and database. All Git worktrees sharing a common
Git directory share one repository queue. Each task belongs to a workstream.
This is Loki's bundled skill, maintained with the Loki server.

## Workflow

1. Call `project action=status` with the intended repository `cwd`.
2. Use its active workstream, or pass an explicit existing `workstream`.
3. If uninitialized, initialize with `project action=init` only when the user
   authorized project work; otherwise ask before initialization.
4. Read with `task_inspect`; mutate individual tasks with `task_write`.
5. Use returned full UUIDs. Check mutation readback and relevant queue state.

## Tools

- `task_inspect`: `status`, `diagnostics`, `list`, `next`, `get`, `count`.
  Required `cwd`; optional `workstream`. `get` requires `uuid`.
  `list`/`count` accept `status`: pending, waiting, completed, deleted, all.
  `limit` is 1–200 (default 50). Check `count` and `has_more`; a truncated
  response is not the complete queue. Continue with `offset + limit`.
  `next` excludes blocked/future tasks.
- `task_write`: `add`, `modify`, `annotate`, `start`, `stop`, `done`.
  `add` takes `fields.description`; other actions require `uuid`.
  `fields`: description, priority (H/M/L/null), due, wait, scheduled, tags
  (string array), depends (full UUID array). Dates use ISO YYYY-MM-DD or
  YYYY-MM-DDTHH:MM:SSZ; null clears a date. `annotate` takes `annotation`.
- `task_delete`: exact `cwd`, `uuid`, optional `workstream`.
  This marks a single task deleted, preserving stored history.

The server fixes project/configuration, serializes mutations, disables hooks,
checks UUID ownership, and reads back changes. Numeric IDs, raw CLI filters,
configuration overrides, imports, purge, and alternate databases are not inputs
to these tools. Never operate another workstream by guessing a UUID.

## Safety and completion

Read before modifying existing tasks. Preserve unrelated tasks and user fields.
Use explicit dependency UUIDs from the same workstream. Complete a task only
after its acceptance checks pass. Delete only a clearly authorized exact task.
For bulk requests, resolve and inspect targets, then make individually auditable
calls. Do not retry an uncertain add blindly; inspect for an existing match first.

## References

Read the applicable reference in full before that operation:
- Read/report: `references/read-and-report.md`
- Mutation: `references/mutate-tasks.md`
- Scope and fields: `references/selectors-and-filters.md`
- Failures: `references/troubleshooting.md`
