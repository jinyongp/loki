# Scope and fields

The cwd selects a repository by its common Git directory. Linked worktrees share
that repository's central queue. The workstream parameter selects an existing
workstream; omission uses the active binding. Use `project action=status` to
resolve these values. Project/configuration cannot be supplied as task fields.
Use canonical full UUIDs, not numeric IDs, UUID prefixes, or CLI expressions.
Allowed fields: description; priority H/M/L/null; due, wait, scheduled as ISO
YYYY-MM-DD or YYYY-MM-DDTHH:MM:SSZ/null; tags as a bounded array of simple names;
depends as full UUID arrays. Empty arrays clear tags/dependencies. Recurrence,
raw filters, hooks, configuration, and imports are outside this tool contract.
