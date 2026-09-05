# Mutate tasks

Read the target first with `task_inspect action=get`. Use `task_write` for one
task at a time, with cwd and optional workstream. Add uses fields.description;
modify uses uuid and fields. Annotate uses uuid and annotation. Start, stop,
and done use uuid. Every successful mutation returns the task readback.
Tags and depends are replacement arrays, so preserve values not being changed.
Create dependency targets first, then wire full UUIDs with fields.depends.
Dependencies must belong to this workstream. Avoid cycles.
For deletion, confirm the exact authorized task and use `task_delete` with its
full UUID. Deletion is logical, not purge. Never mark work done without evidence.
