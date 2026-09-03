# Troubleshooting

Use for install, environment, filter, quoting, data warnings, and bug reports.

## Preflight

```bash
command -v task
task --version
task diagnostics
```

Missing `task`: don't guess install command unless OS known.

## Missing Tasks

Check context/status/wait/scheduled/report filters:

```bash
task context show
task all
task +WAITING waiting
task +PENDING count
task show default.command
```

Common: active context, waiting hidden, completed/deleted, future scheduled, custom report filter.

## ID Mismatch

IDs are display-scoped. Wrong task/disappeared ID => refresh + use UUID:

```bash
task <filter> uuids
task <uuid> information
```

## Quoting

Quote `or`, regex, parens, spaces, symbols:

```bash
task '( project:Work or +ops )' list
task '/supplier invoice/' list
```

## Data Warning

No repair-like command unless documented and explicit. Capture `task export`
and `task diagnostics` through `command_run`; keep them in tool output unless
the user explicitly requests a redacted workspace artifact.

Then inspect diagnostics + representative `information`.

## Bug Report

Collect: version, OS/shell, exact command, redacted diagnostics, minimal
repro/export with sensitive text removed. Create a workspace artifact only when requested.
