# Read and report

Call `task_inspect action=diagnostics` for version and central-state health.
Use `list` with cwd and the active or explicit workstream. Use status=all when
checking duplicates or history. Respect count/has_more and the bounded limit.
Use `get` with the returned full UUID for exact inspection. Use `count` for
queue totals and `next` for pending, unblocked, currently eligible work.
The default list is pending tasks; completed and deleted tasks require their
explicit status or all. Preserve returned UUIDs for subsequent operations.
