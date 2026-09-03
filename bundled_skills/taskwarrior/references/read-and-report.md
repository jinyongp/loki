# Read And Report

Use for read-only: search/list/count/inspect/summarize/export/discover.

## Start Narrow

```bash
task next
task +PENDING list
task project:Work next
task +OVERDUE list
task 12 information
task +READY count
```

Ambiguous natural language: inspect first; propose command; don't mutate.

## Defaults

- `next`: actionable by urgency
- `ready`: actionable, excludes future scheduled/waiting
- `list`: pending
- `long`: detailed pending
- `information`: full detail
- `count`: numeric
- `summary`, `projects`, `tags`, `timesheet`, `calendar`: summaries
- `reports`, `columns`, `commands`, `show`: discovery/config

## Built-Ins

Tasks: `active all blocked blocking completed list long ls minimal newest next oldest overdue ready recurring unblocked waiting`.

Stats: `count projects stats summary tags timesheet burndown.{daily,weekly,monthly} history.{daily,weekly,monthly,annual} ghistory.{daily,weekly,monthly,annual}`.

Meta: `reports columns commands show "show all" udas ids uuids version news colors diagnostics`.

## Export

`task export` is read-only and returns JSON in the command output. Do not use
shell redirection or persist an export unless the user explicitly requests an
artifact. Loki does not permit Taskwarrior import.

## Custom Reports

1. `task columns`, `task reports`.
2. If exists: `task show report.<name>`.
3. Propose `task config report.<name>.*`; config mutation => safety ref.

Keys: filter, columns, labels, sort, description. Verify names via `columns`/`show`.

## Large Output

First count/narrow:

```bash
task +PENDING count
task project:Work count
task +PENDING limit:20 next
```

Long raw output: summarize by project/tag/due/urgency unless raw requested.
