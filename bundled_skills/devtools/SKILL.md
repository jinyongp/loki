---
name: devtools
description: Use the devtools CLI for project environment setup, ports and local reverse proxy routes, managed servers, and task/workstream coordination across agent sessions.
---

# devtools

## Discover only what you need

Start with `devtools <group> --help`. For structured input, request one contract,
such as `devtools schema task claim`. Bare `schema` lists groups; `schema --all`
is for explicit full-catalog work. Reuse discovered contracts during the session.
Command examples here name operations; obtain required flags from their help.

Resolve the profile from tracked `devtools.toml` or explicit `--profile`.
Worktrees with the same profile share values and task history.
Data commands return JSON: check the exit code, then `data` or `error.code`.
Help and `skill` return text; `run` forwards the child's output and exit code.

## Prepare and run

Use `doctor COMMAND` before a configured command when setup is uncertain.
A successful diagnosis can still have `data.ready: false`; inspect checks and remedies.
Variables are readable and may use devtools storage. Loki keeps active secret
values in its AES-GCM vault, so leave the devtools active secret store empty.
Manage those values with Loki secret commands. Keep secret values out of
arguments, conversation, logs, devtools state, and workspace files.

Use `run NAME` for foreground work and `process start NAME` for a persistent server.
Save the returned execution ID. `process status` reports lifetime; a configured
`process wait EXECUTION_ID` establishes readiness before dependent work. `process check`
can exit successfully with `readiness.ready: false`.
Restart applies current config and values.

For a configured process that needs encrypted Loki secrets, replace only its
start or restart command with the brokered form below. Put all options before
the final command name or execution ID. Repeat `--secret` for each selected name.

```sh
loki secret-process start --profile VAULT_PROFILE --secret TOKEN --request-id UUID --dir /workspace/PROJECT COMMAND
loki secret-process restart --profile VAULT_PROFILE --secret TOKEN --request-id UUID EXECUTION_ID
```

The broker resolves values inside the root-owned vault and passes them to the
managed process environment. The agent, argv, devtools state, response, and
audit records receive names only. Continue with ordinary `devtools process
status`, `check`, `wait`, and `stop` commands using the returned execution ID.

Ports belong to execution locations. Inspect `port` and `instance` before changing
assignments. Commands declare `serve` for servers and `bind` for injected values.
Coordinate consumers when a stored port changes.

Use `proxy` when worktrees need stable `.localhost` hostnames instead of direct
port URLs. Routes come from each instance's tracked `[proxies.NAME]` declaration
and current port assignment. When a route uses `${instance.alias}`, give every
routed instance an alias with `instance name`. Allocate the referenced service
port, then inspect `proxy list` before starting the user-global daemon. Do not
start one daemon per project or worktree; route, alias, and assignment changes
are picked up on the next request.

`proxy start` and `proxy stop` require a request UUID. Reuse a UUID with identical
input only after an uncertain response. The listener port remains reserved after
stop; changing it requires starting the stopped daemon with an explicit new port.
Treat non-`ready` list entries as diagnostics to resolve, not fallback targets.
The proxy accepts only loopback HTTP traffic and routes only `.localhost` hosts
to stored local assignments; use the project's own TLS setup when HTTPS is needed.

## Plan, claim, recover

Use an independent task for a small job, or a workstream for a goal needing a
specification and plan. In a workstream, connect requirements, acceptance criteria,
tasks, and validations; set the plan's complete references, then check and activate.
Task dependencies stay within a workstream; workstream dependencies stay within a profile.

Use `task workstream edit` for atomic insertion, definition updates, scope removal,
restoration and ordering in any lifecycle state. Read its schema, preview with
`--dry-run --if-revision`, then submit the same body with a request UUID and the
observed revision. Preview does not consume a request ID. Incomplete coverage can
be saved; resolve returned issues before execution or closure. Removed tasks keep
history and claims; restoring them requires explicit dependency/acceptance links.
Legacy plan set arrays assert the complete included membership.

Before source work, claim the task and check `claimed` and `context_valid`.
Keep the returned context private; pass it explicitly or through
`DEVTOOLS_TASK_CONTEXT`. Public task/run IDs identify work but do not authorize it.
Claims coordinate records; coordinate overlapping files separately.

Checkpoint decisions, remaining work, next action, and evidence before a handoff.
Checkpoint and release target the run ID returned by claim or takeover; done and
sync target the task ID.
In a new session, use `task current --dir PATH` and `task context TASK_ID`,
then inspect the actual working tree. Resume with an existing valid context,
take over the observed active run using `--expected-run`, or claim released work.
Claims end through explicit actions, not elapsed time.

Check `completion_status` and `execution_status` alongside lifecycle `state`.
A done task with stale completion is claimable when ready. A stale current run
must review the changed definition and use `task sync` with context, reason and
observed revision before current validation/completion. Sync preserves the run
and does not create passing evidence. Checkpoint and release remain available.

## Retry and finish

Each task mutation needs a request UUID. Reuse the UUID and identical input after
an uncertain response; changed input gets a new UUID. For revision-guarded edits,
use the latest query revision. On conflict, refresh and reassess before resubmitting.

Create a code basis for the observed definition, perform validation, record its
evidence, then complete the task. Task basis requires the current execution
context; workstream basis requires the observed profile revision. Late records
stay on their run-owned basis and may return `applicable:false`.
The CLI stores evidence; it does not execute or verify the supplied evidence.
After takeover, explicitly accept reusable validation results for the new run.
Close a workstream after its task results and required integration checks satisfy
the acceptance criteria. Use waivers only within the user's agreed scope.
After a closed workstream becomes stale, explicitly close it again once current
evidence is complete. A later pass alone does not close it.

The first real task mutation upgrades a v1 journal to v2 atomically. All writers
sharing the profile must support v2; older binaries reject it. Read, no-op and
preview do not upgrade. Use error details and remedy argv to recover conflicts;
never copy a context credential into shared diagnostics.

Cleanup and restore start with a preview. Apply the selected IDs or digest,
refreshing stale previews. Keep backup identities separate from project files.
Provide a dashboard link when the user needs visual management; its session can
modify all of the user's profiles. Creating a plan does not authorize unrelated
publishing, deletion, or changes to external systems.
