# Troubleshooting

Start with `task_inspect action=diagnostics` for the intended cwd. Check version,
initialized, active_workstream, health.metadata_readable and health.runner_access.
TASK_NOT_INITIALIZED: resolve project state and initialize only when authorized.
TASK_WORKSTREAM_REQUIRED/UNKNOWN: choose an existing workstream or bind it through
`project`. TASK_NOT_FOUND: inspect the intended workstream; do not widen scope.
TASK_PERMISSION_DENIED: central metadata needs administrator repair. Loki's
installer repairs metadata ownership while leaving task data owned by runner.
Do not chmod the database or create an alternate taskrc/database as a workaround.
If the server catalog contains task_inspect/task_write/task_delete but the client
cannot call them, refresh the connection's tool catalog and begin a new session.
If the server lacks them, update the server first. A frozen client snapshot is
not evidence of server support. Do not fall back to arbitrary shell commands.
