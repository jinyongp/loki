from __future__ import annotations

from typing import Any, Literal

from .policy import PolicyError
from .tools import WorkspaceTools


class CatalogTools:
    """Compact MCP-facing facade over the finer-grained workspace implementation."""

    def __init__(self, tools: WorkspaceTools) -> None:
        self.tools = tools

    @staticmethod
    def _required(value: Any, name: str) -> Any:
        if value is None:
            raise PolicyError(f"{name} is required for this action")
        return value

    def task_inspect(
        self, cwd: str, action: Literal["status", "diagnostics", "list", "next", "get", "count"] = "list",
        workstream: str | None = None, uuid: str | None = None,
        status: Literal["pending", "waiting", "completed", "deleted", "all"] = "pending", limit: int = 50, offset: int = 0,
    ) -> dict[str, Any]:
        """Read the central repository Taskwarrior queue. Defaults to the active workstream; use full UUIDs."""
        if action not in {"status", "diagnostics", "list", "next", "get", "count"}:
            raise PolicyError("invalid task inspection action")
        return self.tools.task_request(cwd, action=action, workstream=workstream, uuid=uuid, status=status, limit=limit, offset=offset)

    def task_write(
        self, cwd: str, action: Literal["add", "modify", "annotate", "start", "stop", "done"],
        workstream: str | None = None, uuid: str | None = None,
        fields: dict[str, Any] | None = None, annotation: str | None = None,
    ) -> dict[str, Any]:
        """Change one central task. fields: description, priority H/M/L, ISO due/wait/scheduled, tags, depends UUIDs."""
        if action not in {"add", "modify", "annotate", "start", "stop", "done"}:
            raise PolicyError("invalid task write action")
        return self.tools.task_request(cwd, action=action, workstream=workstream, uuid=uuid, fields=fields, annotation=annotation)

    def task_delete(self, cwd: str, uuid: str, workstream: str | None = None) -> dict[str, Any]:
        """Logically delete exactly one task UUID in the selected workstream; never purge data."""
        return self.tools.task_request(cwd, action="delete", workstream=workstream, uuid=uuid)

    def system_inspect(
        self, action: Literal["server", "diagnostics", "workspace", "port"] = "server",
        port: int | None = None,
    ) -> dict[str, Any]:
        """Inspect Loki itself: action server, diagnostics, workspace, or port."""
        if action == "server":
            return self.tools.server_info()
        if action == "diagnostics":
            return self.tools.diagnostics()
        if action == "workspace":
            return self.tools.workspace_info()
        if action == "port":
            return self.tools.port_info(self._required(port, "port"))
        raise PolicyError("system_inspect action must be server, diagnostics, workspace, or port")

    def preview_publish(
        self,
        action: Literal["server", "stack", "action"],
        port: int | None = None,
        routes: dict[str, int] | None = None,
        environment_routes: dict[str, str] | None = None,
        ttl_seconds: int = 900,
        profile: str | None = None,
        action_name: str | None = None,
        cwd: str | None = None,
    ) -> dict[str, Any]:
        """Publish a live preview: server, arbitrary route stack, or atomically started registered action."""
        if action == "server":
            return self.tools.share_dev_server(self._required(port, "port"), ttl_seconds)
        if action == "stack":
            return self.tools.share_dev_stack(self._required(routes, "routes"), ttl_seconds)
        if action == "action":
            return self.tools.run_preview_action(
                self._required(profile, "profile"),
                self._required(action_name, "action_name"), routes or {},
                environment_routes or {}, ttl_seconds, cwd,
            )
        raise PolicyError("preview_publish action must be server, stack, or action")

    def shared_resources(self, kind: Literal["all", "previews", "artifacts"] = "all") -> dict[str, Any]:
        """List active temporary resources: kind all, previews, or artifacts."""
        if kind == "previews":
            return self.tools.list_shared_servers()
        if kind == "artifacts":
            return self.tools.list_shared_files()
        if kind == "all":
            return {
                "previews": self.tools.list_shared_servers(),
                "artifacts": self.tools.list_shared_files(),
            }
        raise PolicyError("shared_resources kind must be all, previews, or artifacts")

    def revoke_share(self, kind: Literal["preview", "artifact"], share_id: str) -> dict[str, Any]:
        """Revoke a temporary share: kind preview or artifact."""
        if kind == "preview":
            return self.tools.stop_shared_server(share_id)
        if kind == "artifact":
            return self.tools.revoke_shared_file(share_id)
        raise PolicyError("revoke_share kind must be preview or artifact")

    def browser_session(
        self, action: Literal["start", "navigate", "stop"],
        url: str | None = None, new_tab: bool = False,
    ) -> dict[str, Any]:
        """Control browser lifecycle and navigation: action start, navigate, or stop."""
        if action == "start":
            return self.tools.browser_start()
        if action == "navigate":
            return self.tools.browser_navigate(self._required(url, "url"), new_tab)
        if action == "stop":
            return self.tools.browser_stop()
        raise PolicyError("browser_session action must be start, navigate, or stop")

    def browser_observe(
        self,
        action: Literal[
            "state", "tabs", "console", "network", "request", "websockets", "errors", "diagnostics"
        ] = "state",
        level: str | None = None,
        since_sequence: int = 0,
        limit: int = 100,
        status_min: int | None = None,
        failed_only: bool = False,
        resource_type: str | None = None,
        request_id: str | None = None,
        include_body: bool = False,
        max_body_chars: int = 65_536,
    ) -> dict[str, Any]:
        """Observe the browser: action state, tabs, console, network, request, websockets, errors, or diagnostics."""
        if action == "state":
            return self.tools.browser_state()
        if action == "tabs":
            return self.tools.browser_list_tabs()
        if action == "console":
            return self.tools.browser_console(level, since_sequence, limit)
        if action == "network":
            return self.tools.browser_network(
                status_min, failed_only, resource_type, since_sequence, limit,
            )
        if action == "request":
            return self.tools.browser_request(
                self._required(request_id, "request_id"), include_body, max_body_chars,
            )
        if action == "websockets":
            return self.tools.browser_websockets(since_sequence, limit)
        if action == "errors":
            return self.tools.browser_page_errors(since_sequence, limit)
        if action == "diagnostics":
            return self.tools.browser_diagnostics(since_sequence, limit)
        raise PolicyError(
            "browser_observe action must be state, tabs, console, network, request, websockets, errors, or diagnostics"
        )

    def browser_interact(
        self,
        action: Literal["click", "type", "press", "scroll", "back", "switch_tab", "close_tab"],
        index: int | None = None,
        x: int | None = None,
        y: int | None = None,
        new_tab: bool = False,
        text: str | None = None,
        key: str | None = None,
        direction: str = "down",
        amount: int = 500,
        tab_id: str | None = None,
    ) -> dict[str, Any]:
        """Interact with the browser: action click, type, press, scroll, back, switch_tab, or close_tab."""
        if action == "click":
            return self.tools.browser_click(index, x, y, new_tab)
        if action == "type":
            return self.tools.browser_type(
                self._required(index, "index"), self._required(text, "text"),
            )
        if action == "press":
            return self.tools.browser_press(self._required(key, "key"))
        if action == "scroll":
            return self.tools.browser_scroll(direction, amount)
        if action == "back":
            return self.tools.browser_back()
        if action == "switch_tab":
            return self.tools.browser_switch_tab(self._required(tab_id, "tab_id"))
        if action == "close_tab":
            return self.tools.browser_close_tab(self._required(tab_id, "tab_id"))
        raise PolicyError(
            "browser_interact action must be click, type, press, scroll, back, switch_tab, or close_tab"
        )

    def workspace_read(
        self,
        action: Literal["list", "file", "search", "revisions", "revision_diff"],
        path: str = ".",
        query: str | None = None,
        max_depth: int = 3,
        offset: int = 0,
        limit: int = 200,
        max_results: int = 100,
        regex: bool = False,
        revision: str | None = None,
    ) -> dict[str, Any]:
        """Read workspace text and history: action list, file, search, revisions, or revision_diff."""
        if action == "list":
            return self.tools.list_files(path, max_depth, offset, limit)
        if action == "file":
            return self.tools.read_file(path, offset, limit)
        if action == "search":
            return self.tools.search_text(self._required(query, "query"), path, max_results, regex)
        if action == "revisions":
            return self.tools.list_file_revisions(path, limit)
        if action == "revision_diff":
            return self.tools.show_file_revision_diff(path, self._required(revision, "revision"))
        raise PolicyError("workspace_read action must be list, file, search, revisions, or revision_diff")

    def workspace_edit(
        self,
        action: Literal["create", "replace", "patch", "move"],
        path: str | None = None,
        content: str | None = None,
        old: str | None = None,
        new: str | None = None,
        expected_sha256: str | None = None,
        expected_replacements: int = 1,
        patch: str | None = None,
        source: str | None = None,
        destination: str | None = None,
    ) -> dict[str, Any]:
        """Edit workspace text safely: action create, replace, patch, or move."""
        if action == "create":
            return self.tools.write_file(
                self._required(path, "path"), self._required(content, "content"),
            )
        if action == "replace":
            return self.tools.replace_text(
                self._required(path, "path"), self._required(old, "old"),
                self._required(new, "new"), self._required(expected_sha256, "expected_sha256"),
                expected_replacements,
            )
        if action == "patch":
            return self.tools.apply_patch(self._required(patch, "patch"))
        if action == "move":
            return self.tools.move_path(
                self._required(source, "source"), self._required(destination, "destination"),
            )
        raise PolicyError("workspace_edit action must be create, replace, patch, or move")

    def project(
        self,
        action: Literal[
            "status", "list", "read", "init", "bind", "write",
            "register", "unregister", "registration", "workflow",
            "set_workflow", "remove_workflow",
        ],
        cwd: str = ".",
        workstream: str | None = None,
        filename: str | None = None,
        content: str | None = None,
        expected_sha256: str | None = None,
        goal: str | None = None,
        slug_base: str | None = None,
        slug: str | None = None,
        depth: Literal["light", "standard", "high-risk"] = "standard",
        intent_source_kind: Literal["plan-local", "authoritative"] = "plan-local",
        intent_source: str | None = None,
        name: str | None = None,
        workflow: str | None = None,
        steps: list[str] | None = None,
        required_secrets: list[str] | None = None,
        timeout_seconds: int = 3_600,
    ) -> dict[str, Any]:
        """Manage central project registration, workflows, and workstream state shared across Git worktrees."""
        if action in {
            "register", "unregister", "registration", "workflow",
            "set_workflow", "remove_workflow",
        }:
            return self.tools.project_configuration(
                action, cwd, name, workflow, steps or [], required_secrets or [],
                timeout_seconds,
            )
        return self.tools.project_state(
            action, cwd, workstream, filename, content, expected_sha256,
            goal, slug_base, slug, depth, intent_source_kind, intent_source,
        )

    def artifact_publish(
        self,
        action: Literal["file", "bundle"],
        path: str | None = None,
        paths: list[str] | None = None,
        filename: str = "loki-workspace.zip",
        ttl_seconds: int = 900,
    ) -> Any:
        """Publish downloadable workspace content: action file or bundle."""
        if action == "file":
            return self.tools.share_file(self._required(path, "path"), ttl_seconds)
        if action == "bundle":
            return self.tools.workspace_bundle(self._required(paths, "paths"), filename, ttl_seconds)
        raise PolicyError("artifact_publish action must be file or bundle")

    def restore_workspace_file(self, path: str, revision: str, expected_sha256: str) -> dict[str, Any]:
        """Restore one saved pre-mutation file revision after an optimistic hash check."""
        return self.tools.restore_file_revision(path, revision, expected_sha256)

    def skill_read(
        self,
        action: Literal["list", "activate", "resource", "validate"],
        name: str | None = None,
        path: str | None = None,
        cwd: str = ".",
    ) -> dict[str, Any]:
        """Read Agent Skills: action list, activate, resource, or validate."""
        if action == "list":
            return self.tools.list_skills(cwd)
        if action == "activate":
            return self.tools.activate_skill(self._required(name, "name"), cwd)
        if action == "resource":
            return self.tools.read_skill_resource(
                self._required(name, "name"), self._required(path, "path"), cwd,
            )
        if action == "validate":
            return self.tools.validate_skill(self._required(name, "name"), cwd)
        raise PolicyError("skill_read action must be list, activate, resource, or validate")

    def skill_write(
        self,
        action: Literal["create", "edit", "resource"],
        name: str,
        cwd: str = ".",
        scope: str | None = None,
        description: str | None = None,
        instructions: str | None = None,
        metadata: dict[str, Any] | None = None,
        patch: str | None = None,
        expected_sha256: str | None = None,
        path: str | None = None,
        content: str | None = None,
        overwrite: bool = False,
        encoding: str = "utf-8",
    ) -> dict[str, Any]:
        """Create or edit Agent Skills: action create, edit, or resource."""
        if action == "create":
            return self.tools.create_skill(
                self._required(scope, "scope"), name,
                self._required(description, "description"),
                self._required(instructions, "instructions"), cwd, metadata,
            )
        if action == "edit":
            return self.tools.edit_skill(
                name, self._required(patch, "patch"),
                self._required(expected_sha256, "expected_sha256"), cwd,
            )
        if action == "resource":
            return self.tools.write_skill_resource(
                name, self._required(path, "path"), self._required(content, "content"),
                cwd, overwrite, expected_sha256, encoding,
            )
        raise PolicyError("skill_write action must be create, edit, or resource")

    def git_inspect(
        self,
        action: Literal["status", "diff", "index", "commit_context"] = "status",
        cwd: str = ".",
        staged: bool = False,
        path: str | None = None,
    ) -> dict[str, Any]:
        """Inspect Git: action status, diff, index, or commit_context."""
        if action == "status":
            return self.tools.git_status(cwd)
        if action == "diff":
            return self.tools.git_diff(staged, path, cwd)
        if action == "index":
            return self.tools.git_index_state(cwd)
        if action == "commit_context":
            return self.tools.git_commit_context(cwd)
        raise PolicyError("git_inspect action must be status, diff, index, or commit_context")

    def git_stage(
        self,
        action: Literal["paths", "unstage", "patch"],
        cwd: str = ".",
        paths: list[str] | None = None,
        patch: str | None = None,
        reverse: bool = False,
        expected_index_sha256: str | None = None,
    ) -> dict[str, Any]:
        """Modify only the Git index: action paths, unstage, or patch."""
        if action == "paths":
            return self.tools.git_stage_paths(
                self._required(paths, "paths"), cwd, expected_index_sha256,
            )
        if action == "unstage":
            return self.tools.git_unstage_paths(
                self._required(paths, "paths"), cwd, expected_index_sha256,
            )
        if action == "patch":
            return self.tools.git_stage_patch(
                self._required(patch, "patch"), cwd, reverse, expected_index_sha256,
            )
        raise PolicyError("git_stage action must be paths, unstage, or patch")

    def developer_view(
        self,
        action: Literal["git_diff", "test_report", "process_log"],
        cwd: str = ".",
        staged: bool = False,
        path: str | None = None,
        session_id: str | None = None,
        offset: int | None = None,
        limit: int = 65_536,
    ) -> Any:
        """Render developer output: action git_diff, test_report, or process_log."""
        if action == "git_diff":
            return self.tools.view_git_diff(staged, path, cwd)
        if action == "test_report":
            return self.tools.view_test_report(self._required(path, "path"))
        if action == "process_log":
            return self.tools.view_process_log(
                self._required(session_id, "session_id"), offset, limit,
            )
        raise PolicyError("developer_view action must be git_diff, test_report, or process_log")

    def secret_inspect(
        self,
        action: Literal["profiles", "imports", "profile", "status", "audit"],
        profile: str | None = None,
        limit: int = 50,
    ) -> dict[str, Any]:
        """Inspect encrypted secret metadata: action profiles, imports, profile, status, or audit."""
        if action == "profiles":
            return self.tools.list_secret_profiles()
        if action == "imports":
            return self.tools.list_secret_imports()
        if action == "profile":
            return self.tools.get_secret_profile(self._required(profile, "profile"))
        if action == "status":
            return self.tools.secret_status()
        if action == "audit":
            return self.tools.secret_audit_log(limit)
        raise PolicyError(
            "secret_inspect action must be profiles, imports, profile, status, or audit"
        )

    def secret_write(
        self,
        action: Literal["create_profile", "import_env", "set", "generate"],
        profile: str,
        import_id: str | None = None,
        secret: str | None = None,
        value: str | None = None,
        bytes: int = 32,
    ) -> dict[str, Any]:
        """Write profile state. Use set only for non-sensitive public config; its value is visible in the MCP request. Use import_env or generate for credentials and other secrets."""
        if action == "create_profile":
            return self.tools.create_secret_profile(profile)
        if action == "import_env":
            return self.tools.import_secret_env(profile, self._required(import_id, "import_id"))
        if action == "set":
            return self.tools.set_public_secret_value(
                profile,
                self._required(secret, "secret"),
                self._required(value, "value"),
            )
        if action == "generate":
            return self.tools.generate_secret(profile, self._required(secret, "secret"), bytes)
        raise PolicyError("secret_write action must be create_profile, import_env, set, or generate")

    def secret_delete(
        self,
        action: Literal["secret", "profile", "materialization"],
        profile: str | None = None,
        secret: str | None = None,
        action_name: str | None = None,
    ) -> dict[str, Any]:
        """Delete secret state: action secret, profile, or materialization."""
        if action == "secret":
            return self.tools.remove_secret(
                self._required(profile, "profile"), self._required(secret, "secret"),
            )
        if action == "profile":
            return self.tools.remove_secret_profile(self._required(profile, "profile"))
        if action == "materialization":
            return self.tools.clear_action_materialization(
                self._required(profile, "profile"), self._required(action_name, "action_name"),
            )
        raise PolicyError("secret_delete action must be secret, profile, or materialization")

    def action(
        self,
        operation: Literal[
            "list", "set", "remove", "clear_materialization",
            "run", "processes", "process", "stop",
        ],
        profile: str | None = None,
        action_name: str | None = None,
        cwd: str | None = None,
        command: list[str] | None = None,
        secrets: list[str] | None = None,
        all_secrets: bool = False,
        required_secrets: list[str] | None = None,
        timeout_seconds: int = 3_600,
        max_output_bytes: int = 1_048_576,
        materialize_env_file: str | None = None,
        materialize_env_path: str | None = None,
        docker_access: bool = False,
        preferred_port: int | None = None,
        port_environment: str | None = None,
        origin_environment: str | None = None,
        singleton: bool = False,
        lock_probe: str | None = None,
        local_callback: bool = False,
        public_environment: list[str] | None = None,
        bind_local_callback: bool = False,
        session_id: str | None = None,
        offset: int | None = None,
        limit: int = 65_536,
        preview_environment: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        """Configure, run, or inspect central actions without project-local Loki files."""
        if operation == "list":
            return self.tools.get_secret_profile(self._required(profile, "profile"))
        if operation == "set":
            return self.tools.set_action_policy(
                self._required(profile, "profile"),
                self._required(action_name, "action_name"),
                self._required(cwd, "cwd"),
                self._required(command, "command"),
                secrets, all_secrets, required_secrets,
                timeout_seconds, max_output_bytes,
                materialize_env_file, materialize_env_path, docker_access,
                preferred_port, port_environment, origin_environment,
                singleton, lock_probe, local_callback, public_environment, preview_environment,
            )
        if operation == "remove":
            return self.tools.remove_action_policy(
                self._required(profile, "profile"),
                self._required(action_name, "action_name"),
            )
        if operation == "clear_materialization":
            return self.tools.clear_action_materialization(
                self._required(profile, "profile"),
                self._required(action_name, "action_name"),
            )
        if operation == "run":
            return self.tools.run_action(
                self._required(profile, "profile"),
                self._required(action_name, "action_name"),
                cwd,
                bind_local_callback,
            )
        if operation == "processes":
            return self.tools.list_action_processes()
        if operation == "process":
            return self.tools.read_action_process(
                self._required(session_id, "session_id"), offset, max(limit, 1),
            )
        if operation == "stop":
            return self.tools.stop_action_process(self._required(session_id, "session_id"))
        raise PolicyError(
            "action operation must be list, set, remove, clear_materialization, "
            "run, processes, process, or stop"
        )

    def command_run(
        self,
        action: Literal["check", "npm", "fnm", "just", "pnpm", "exec"],
        arguments: list[str] | None = None,
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 900,
        name: str | None = None,
        executable: str | None = None,
    ) -> dict[str, Any]:
        """Run a finite command: action check, npm, fnm, just, pnpm, or exec."""
        clean_arguments = arguments or []
        if action == "check":
            return self.tools.run_check(self._required(name, "name"))
        if action == "npm":
            return self.tools.npm(clean_arguments, cwd, node_version, timeout_seconds)
        if action == "fnm":
            return self.tools.fnm(clean_arguments, timeout_seconds)
        if action == "just":
            return self.tools.just(clean_arguments, cwd, node_version, timeout_seconds)
        if action == "pnpm":
            return self.tools.run_pnpm(clean_arguments, cwd, node_version, timeout_seconds)
        if action == "exec":
            return self.tools.exec_command(
                self._required(executable, "executable"), clean_arguments,
                cwd, node_version, timeout_seconds,
            )
        raise PolicyError("command_run action must be check, npm, fnm, just, pnpm, or exec")

    def command_start(
        self,
        action: Literal["check", "pnpm", "exec", "configured"],
        arguments: list[str] | None = None,
        cwd: str = ".",
        node_version: str | None = None,
        timeout_seconds: int = 14_400,
        name: str | None = None,
        executable: str | None = None,
    ) -> dict[str, Any]:
        """Start a managed command: action check, pnpm, exec, or configured."""
        clean_arguments = arguments or []
        if action == "check":
            return self.tools.start_check(self._required(name, "name"))
        if action == "pnpm":
            return self.tools.start_pnpm(
                clean_arguments, cwd, node_version, timeout_seconds,
            )
        if action == "exec":
            return self.tools.start_command(
                self._required(executable, "executable"), clean_arguments,
                cwd, node_version, timeout_seconds,
            )
        if action == "configured":
            return self.tools.start_process(self._required(name, "name"))
        raise PolicyError("command_start action must be check, pnpm, exec, or configured")

    def process_inspect(
        self,
        action: Literal["list", "read"] = "list",
        session_id: str | None = None,
        offset: int | None = None,
        limit: int = 65_536,
    ) -> dict[str, Any]:
        """Inspect ordinary managed processes: action list or read."""
        if action == "list":
            return self.tools.list_processes()
        if action == "read":
            return self.tools.read_process(
                self._required(session_id, "session_id"), offset, limit,
            )
        raise PolicyError("process_inspect action must be list or read")

    def runtime_stop(
        self,
        action: Literal["port", "process"],
        port: int | None = None,
        session_id: str | None = None,
    ) -> dict[str, Any]:
        """Stop an ordinary runtime: action port or process."""
        if action == "port":
            return self.tools.stop_port(self._required(port, "port"))
        if action == "process":
            return self.tools.stop_process(self._required(session_id, "session_id"))
        raise PolicyError("runtime_stop action must be port or process")
