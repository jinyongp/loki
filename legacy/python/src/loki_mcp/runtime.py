from __future__ import annotations

from pathlib import Path, PurePosixPath
from types import SimpleNamespace
from typing import Any
from urllib.parse import urlsplit
import base64
import copy
import fcntl
import hashlib
import json
import os
import re
import secrets
import socket
import socketserver
import stat
import struct
import subprocess
import threading
import time

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from .local_callback_proxy import LocalCallbackProxy
from .policy import PolicyError, WorkspacePolicy, atomic_write_bytes
from .processes import ProcessManager
from .project_state import ProjectStateStore, STATE_ROOT
from .runtime_settings import ActionProcessLimits, load_action_process_limits
from .tools import EXECUTABLES, WorkspaceTools
from .tasks import operate as operate_task


SOCKET_PATH = Path(os.environ.get("LOKI_RUNTIME_SOCKET", "/run/loki/runtime/control.sock"))
STATE_DIRECTORY = Path(os.environ.get("LOKI_RUNTIME_STATE", "/var/lib/loki/runtime"))
AUDIT_PATH = Path(os.environ.get("LOKI_RUNTIME_AUDIT", "/var/log/loki/runtime/audit.jsonl"))
WORKSPACE_ROOT = Path(os.environ.get("LOKI_RUNTIME_WORKSPACE", "/srv/workspace/loki"))
INBOX_DIRECTORY = Path(
    os.environ.get("LOKI_RUNTIME_INBOX", str(STATE_DIRECTORY / "inbox"))
)
KEY_PATH = STATE_DIRECTORY / "master.key"
STORE_PATH = STATE_DIRECTORY / "store.json"
AAD = b"loki-secret-store-v1"
NAME_PATTERN = re.compile(r"^[A-Za-z_][A-Za-z0-9_.-]{0,127}$")
PROFILE_PATTERN = re.compile(r"^[a-z][a-z0-9-]{0,62}$")
IMPORT_ID_PATTERN = re.compile(r"^[a-f0-9]{32}$")
MAX_REQUEST_BYTES = 2_097_152
MAX_INBOX_BYTES = 2_000_000
MAX_PROFILES = 128
MAX_SECRETS = 512
MAX_ACTIONS = 64
MAX_PROJECTS = 256
MAX_WORKFLOWS = 32
AGENT_UID = int(os.environ.get("LOKI_RUNTIME_AGENT_UID", "1000"))
DOCKER_SOCKET = "unix:///run/docker.sock"
LOCAL_DEPLOY_SNAPSHOT_ROOT = Path("/var/lib/loki/local-deploy/snapshots")
PORT_ALLOCATION_HOLD_SECONDS = 30.0
_PORT_ALLOCATION_LOCK = threading.Lock()
_PENDING_PORTS: dict[int, float] = {}
ACTION_LAUNCH_TOKEN_PATTERN = re.compile(r"^[a-f0-9]{32}$")


class LokiRuntimeError(ValueError):
    pass


def parse_dotenv(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        if "=" not in line:
            raise LokiRuntimeError(f"invalid dotenv assignment at line {number}")
        name, value = line.split("=", 1)
        name = name.strip()
        value = value.strip()
        _validate_secret_name(name)
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            if value[0] == '"':
                try:
                    value = json.loads(value)
                except json.JSONDecodeError as error:
                    raise LokiRuntimeError(
                        f"invalid quoted dotenv value at line {number}"
                    ) from error
            else:
                value = value[1:-1]
        values[name] = value
    if not values:
        raise LokiRuntimeError("dotenv import must contain secrets")
    return values


class EncryptedStateStore:
    def __init__(self, key_path: Path = KEY_PATH, store_path: Path = STORE_PATH) -> None:
        self.key_path = key_path
        self.store_path = store_path
        self._lock = threading.RLock()

    @property
    def initialized(self) -> bool:
        return self.key_path.is_file() and self.store_path.is_file()

    def initialize(self) -> bool:
        with self._lock:
            if self.initialized:
                return False
            if self.key_path.exists() or self.store_path.exists():
                raise LokiRuntimeError("secret store is only partially initialized")
            self.key_path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            key = AESGCM.generate_key(bit_length=256)
            atomic_write_bytes(self.key_path, key)
            os.chmod(self.key_path, 0o600)
            self._save_unlocked({"version": 1, "profiles": {}, "projects": {}})
            return True

    def load(self) -> dict[str, Any]:
        with self._lock:
            return self._load_unlocked()

    def update(self, operation: Any) -> Any:
        with self._lock:
            document = self._load_unlocked()
            result = operation(document)
            self._validate(document)
            self._save_unlocked(document)
            return result

    def _load_unlocked(self) -> dict[str, Any]:
        if not self.initialized:
            raise LokiRuntimeError("secret store is not initialized; run 'sudo loki secret init'")
        try:
            key = self.key_path.read_bytes()
            envelope = json.loads(self.store_path.read_text(encoding="utf-8"))
            nonce = base64.b64decode(envelope["nonce"], validate=True)
            ciphertext = base64.b64decode(envelope["ciphertext"], validate=True)
            plaintext = AESGCM(key).decrypt(nonce, ciphertext, AAD)
            document = json.loads(plaintext)
        except Exception as error:
            raise LokiRuntimeError("secret store cannot be decrypted") from error
        self._validate(document)
        return document

    def _save_unlocked(self, document: dict[str, Any]) -> None:
        key = self.key_path.read_bytes()
        nonce = os.urandom(12)
        plaintext = json.dumps(document, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
        ciphertext = AESGCM(key).encrypt(nonce, plaintext, AAD)
        envelope = json.dumps({
            "version": 1,
            "nonce": base64.b64encode(nonce).decode("ascii"),
            "ciphertext": base64.b64encode(ciphertext).decode("ascii"),
        }, separators=(",", ":")).encode() + b"\n"
        atomic_write_bytes(self.store_path, envelope)
        os.chmod(self.store_path, 0o600)

    @staticmethod
    def _validate(document: object) -> None:
        if not isinstance(document, dict) or document.get("version") != 1:
            raise LokiRuntimeError("secret store schema is invalid")
        profiles = document.get("profiles")
        if not isinstance(profiles, dict) or len(profiles) > MAX_PROFILES:
            raise LokiRuntimeError("secret profile collection is invalid")
        for profile_name, profile in profiles.items():
            _validate_profile_name(profile_name)
            if not isinstance(profile, dict):
                raise LokiRuntimeError("secret profile is invalid")
            values = profile.get("secrets")
            actions = profile.get("actions")
            if not isinstance(values, dict) or len(values) > MAX_SECRETS:
                raise LokiRuntimeError("secret collection is invalid")
            if not isinstance(actions, dict) or len(actions) > MAX_ACTIONS:
                raise LokiRuntimeError("action collection is invalid")
            for name, value in values.items():
                _validate_secret_name(name)
                if not isinstance(value, str) or len(value.encode()) > 1_048_576:
                    raise LokiRuntimeError("secret value is invalid")
            for name, action in actions.items():
                _validate_action_name(name)
                _validate_action(action, set(values))
        projects = document.setdefault("projects", {})
        if not isinstance(projects, dict) or len(projects) > MAX_PROJECTS:
            raise LokiRuntimeError("project collection is invalid")
        for project_id, project in projects.items():
            _validate_project(project_id, project, profiles)


class RuntimeController:
    def __init__(
        self,
        store: EncryptedStateStore | None = None,
        process_limits: ActionProcessLimits | None = None,
    ) -> None:
        self.store = store or EncryptedStateStore()
        limits = process_limits or load_action_process_limits()
        config = SimpleNamespace(
            max_processes=limits.total,
            process_retention_seconds=3600,
            max_output_bytes=1_048_576,
        )
        self.processes = ProcessManager(config, WorkspacePolicy(WORKSPACE_ROOT))  # type: ignore[arg-type]
        self.project_state = ProjectStateStore(WORKSPACE_ROOT, STATE_ROOT)
        self.max_processes_per_profile = limits.per_profile
        self._scrubbers: dict[str, tuple[str, ...]] = {}
        self._action_launches: dict[str, dict[str, Any]] = {}
        self.local_callback = LocalCallbackProxy(self._resolve_local_callback_target)

    def close(self) -> None:
        self.local_callback.close()
        self.processes.close()

    def dispatch(self, request: dict[str, Any], uid: int, pid: int | None = None) -> dict[str, Any]:
        operation = request.get("operation")
        if not isinstance(operation, str):
            raise LokiRuntimeError("operation is required")
        try:
            root_only = {
                "init", "import_env", "secret_set", "action_set", "action_remove",
                "project_register", "project_unregister", "project_set_workflow",
                "project_remove_workflow",
            }
            delegated = {
                "profile_create", "profile_remove", "import_staged_env", "secret_generate",
                "secret_remove", "public_value_set", "clear_action_materialization", "bootstrap_project",
                "project_state", "project_task", "task", "project_status", "project_workflow",
                "local_callback_bind",
            }
            if operation in root_only and uid != 0 and not _peer_is_mcp_service(uid, pid):
                raise LokiRuntimeError("administrative operations require root or the Loki MCP service")
            if operation in delegated and uid not in {0, AGENT_UID}:
                raise LokiRuntimeError("delegated secret operation requires the Loki agent user")
            handler = getattr(self, f"op_{operation}", None)
            if handler is None:
                raise LokiRuntimeError("unknown operation")
            result = handler(request)
        except Exception:
            self._audit(operation, uid, False, request)
            raise
        self._audit(operation, uid, True, request)
        return result

    def op_init(self, _: dict[str, Any]) -> dict[str, Any]:
        return {"initialized": True, "created": self.store.initialize()}

    def op_status(self, _: dict[str, Any]) -> dict[str, Any]:
        usage = self.processes.usage()
        return {
            "initialized": self.store.initialized,
            "running_processes": usage["total"],
            "running_processes_by_profile": usage["groups"],
            "process_limits": {
                "total": self.processes.config.max_processes,
                "per_profile": self.max_processes_per_profile,
            },
            "local_callback": self.local_callback.status(),
        }

    def op_project_state(self, request: dict[str, Any]) -> dict[str, Any]:
        action = request.get("action")
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        if action == "status":
            return self.project_state.status(cwd)
        if action == "list":
            return self.project_state.list_workstreams(cwd)
        if action == "read":
            return self.project_state.read_artifact(
                cwd, request.get("workstream"), _required_string(request, "filename"),
            )
        if action == "init":
            return self.project_state.initialize_workstream(
                cwd,
                goal=_required_string(request, "goal"),
                slug_base=_optional_string(request.get("slug_base")),
                slug=_optional_string(request.get("slug")),
                depth=_required_string(request, "depth"),
                intent_source_kind=_required_string(request, "intent_source_kind"),
                intent_source=_optional_string(request.get("intent_source")),
            )
        if action == "bind":
            return self.project_state.bind_workstream(
                cwd, _required_string(request, "workstream"),
            )
        if action == "write":
            return self.project_state.write_artifact(
                cwd,
                _optional_string(request.get("workstream")),
                _required_string(request, "filename"),
                _required_string(request, "content"),
                _optional_string(request.get("expected_sha256")),
            )
        raise LokiRuntimeError("project state action must be status, list, read, init, bind, or write")

    def op_project_task(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        arguments = request.get("arguments")
        if (
            not isinstance(arguments, list)
            or len(arguments) > 128
            or not all(isinstance(item, str) and len(item.encode()) <= 8_192 for item in arguments)
        ):
            raise LokiRuntimeError("Taskwarrior arguments are invalid")
        lowered = [item.lower() for item in arguments]
        forbidden = {"config", "execute", "import", "purge", "sync", "synchronize", "undo"}
        if any(item in forbidden or item.startswith("rc.") for item in lowered):
            raise LokiRuntimeError("Taskwarrior configuration, import, sync, purge, undo, and command execution are not allowed")
        identity = self.project_state.resolve(cwd)
        taskrc = identity.state_directory / "taskwarrior" / "taskrc"
        taskdata = identity.state_directory / "taskwarrior" / "data"
        if not taskrc.is_file() or not taskdata.is_dir():
            raise LokiRuntimeError("central project state is not initialized")
        timeout = request.get("timeout_seconds", 900)
        if isinstance(timeout, bool) or not isinstance(timeout, int) or not 1 <= timeout <= 1_800:
            raise LokiRuntimeError("Taskwarrior timeout is invalid")
        identity.state_directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        lock = identity.state_directory / "task.lock"
        with lock.open("a+b") as handle:
            fcntl.flock(handle, fcntl.LOCK_EX)
            completed = subprocess.run(
                [
                    "/usr/sbin/runuser", "-u", "runner", "--", "/usr/bin/env", "-i",
                    "HOME=/home/runner", "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
                    "PATH=/home/linuxbrew/.linuxbrew/bin:/usr/bin:/bin",
                    f"TASKRC={taskrc}", f"TASKDATA={taskdata}",
                    "/home/linuxbrew/.linuxbrew/bin/task", *arguments,
                ],
                cwd=WORKSPACE_ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                timeout=timeout, check=False,
            )
        output = completed.stdout[-1_048_576:].decode("utf-8", "replace")
        return {"exit_code": completed.returncode, "output": output}

    def op_task(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        state = self.project_state.status(cwd)
        identity = self.project_state.resolve(cwd)
        action = request.get("action")
        if action in {"status", "diagnostics"}:
            version = subprocess.run(
                ["/home/linuxbrew/.linuxbrew/bin/task", "--version"],
                env={"PATH": "/usr/bin:/bin", "HOME": "/tmp", "TASKRC": "/dev/null"},
                capture_output=True, text=True, timeout=10, check=False,
            )
            health = {"initialized": state["initialized"], "metadata_readable": False, "runner_access": False}
            if state["initialized"]:
                taskroot = identity.state_directory / "taskwarrior"
                try:
                    health["metadata_readable"] = (taskroot / "taskrc").is_file()
                    probe = subprocess.run(
                        ["/usr/sbin/runuser", "-u", "runner", "--", "/usr/bin/test", "-r", str(taskroot / "taskrc")],
                        capture_output=True, timeout=10, check=False,
                    )
                    data_probe = subprocess.run(
                        ["/usr/sbin/runuser", "-u", "runner", "--", "/usr/bin/test", "-w", str(taskroot / "data")],
                        capture_output=True, timeout=10, check=False,
                    )
                    health["runner_access"] = probe.returncode == 0 and data_probe.returncode == 0
                except PermissionError:
                    pass
            return {**state, "version": version.stdout.strip(), "available": version.returncode == 0,
                    "health": health}
        if not state["initialized"]:
            raise LokiRuntimeError("TASK_NOT_INITIALIZED: call project action=init for this repository")
        slug = request.get("workstream") or state["active_workstream"]
        if not isinstance(slug, str):
            raise LokiRuntimeError("TASK_WORKSTREAM_REQUIRED: select or bind a workstream")
        directory = self.project_state._workstream_directory(identity, slug)
        if not (directory / "manifest.json").is_file():
            raise LokiRuntimeError("TASK_WORKSTREAM_UNKNOWN")
        taskrc = identity.state_directory / "taskwarrior" / "taskrc"
        taskdata = identity.state_directory / "taskwarrior" / "data"
        try:
            if not taskrc.is_file() or not taskdata.is_dir():
                raise LokiRuntimeError("TASK_NOT_INITIALIZED")
        except PermissionError as error:
            raise LokiRuntimeError("TASK_PERMISSION_DENIED: central metadata requires administrator repair") from error

        def run(arguments: list[str]) -> dict[str, Any]:
            completed = subprocess.run(
                ["/usr/sbin/runuser", "-u", "runner", "--", "/usr/bin/env", "-i",
                 "HOME=/home/runner", "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
                 "PATH=/home/linuxbrew/.linuxbrew/bin:/usr/bin:/bin",
                 f"TASKRC={taskrc}", f"TASKDATA={taskdata}",
                 "/home/linuxbrew/.linuxbrew/bin/task", *arguments],
                # TASKRC/TASKDATA are absolute. Do not chdir into a runner-only
                # worktree before runuser has dropped the runtime's root UID.
                cwd=WORKSPACE_ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                timeout=30, check=False,
            )
            if len(completed.stdout) > 1_048_576:
                raise LokiRuntimeError("TASK_OUTPUT_LIMIT: narrow the queue before retrying")
            return {"exit_code": completed.returncode, "output": completed.stdout.decode("utf-8", "replace")}

        with (identity.state_directory / "task.lock").open("a+b") as handle:
            fcntl.flock(handle, fcntl.LOCK_EX)
            result = operate_task(run, request, slug)
        return {**state, "workstream": slug, **result}

    def op_inspect_docker_port(self, request: dict[str, Any]) -> dict[str, Any]:
        """Return bounded metadata only for trusted loopback Compose ports."""
        return inspect_docker_port(request.get("port"))

    def op_list_processes(self, _: dict[str, Any]) -> dict[str, Any]:
        return {"processes": [self._scrub_snapshot(item) for item in self.processes.list()["processes"]]}

    def op_list_profiles(self, _: dict[str, Any]) -> dict[str, Any]:
        document = self.store.load()
        return {"profiles": [self._profile_metadata(name, value) for name, value in sorted(document["profiles"].items())]}

    def op_list_imports(self, _: dict[str, Any]) -> dict[str, Any]:
        imports = []
        try:
            candidates = INBOX_DIRECTORY.iterdir()
        except FileNotFoundError:
            candidates = []
        for candidate in candidates:
            match = re.fullmatch(r"([a-f0-9]{32})\.env", candidate.name)
            if match is None or candidate.is_symlink():
                continue
            try:
                metadata = candidate.stat()
            except OSError:
                continue
            if not stat.S_ISREG(metadata.st_mode):
                continue
            imports.append({
                "import_id": match.group(1),
                "bytes": metadata.st_size,
                "created_at": metadata.st_mtime,
            })
        return {"imports": sorted(imports, key=lambda item: item["created_at"])}

    def op_get_profile(self, request: dict[str, Any]) -> dict[str, Any]:
        name, profile = self._get_profile(request)
        return self._profile_metadata(name, profile, include_actions=True)

    def op_profile_create(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        def update(document: dict[str, Any]) -> None:
            if name in document["profiles"]:
                raise LokiRuntimeError("profile already exists")
            document["profiles"][name] = {"secrets": {}, "actions": {}}
        self.store.update(update)
        return {"profile": name, "created": True}

    def op_profile_remove(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        def update(document: dict[str, Any]) -> None:
            if document["profiles"].pop(name, None) is None:
                raise LokiRuntimeError("unknown profile")
        self.store.update(update)
        return {"profile": name, "removed": True}

    def op_import_env(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        values = request.get("values")
        if not isinstance(values, dict) or not values:
            raise LokiRuntimeError("dotenv import must contain secrets")
        clean: dict[str, str] = {}
        for key, value in values.items():
            _validate_secret_name(key)
            if not isinstance(value, str):
                raise LokiRuntimeError("dotenv value must be a string")
            clean[key] = value
        self._import_values(name, clean)
        return {"profile": name, "imported": sorted(clean), "count": len(clean)}

    def op_import_staged_env(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        import_id = _required_import_id(request)
        source, values = _read_staged_import(import_id)
        self._import_values(name, values)
        source.unlink()
        return {
            "profile": name,
            "import_id": import_id,
            "imported": sorted(values),
            "count": len(values),
            "source_deleted": True,
        }

    def op_secret_set(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        secret_name = _required_secret(request)
        value = request.get("value")
        if not isinstance(value, str):
            raise LokiRuntimeError("secret value is required")
        def update(document: dict[str, Any]) -> None:
            profile = document["profiles"].get(name)
            if profile is None:
                raise LokiRuntimeError("unknown profile")
            profile["secrets"][secret_name] = value
        self.store.update(update)
        return {"profile": name, "secret": secret_name, "stored": True}

    def op_public_value_set(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        secret_name = _required_secret(request)
        value = request.get("value")
        if not isinstance(value, str):
            raise LokiRuntimeError("public configuration value is required")
        if "\x00" in value:
            raise LokiRuntimeError("public configuration value contains NUL")
        if len(value.encode("utf-8")) > 65_536:
            raise LokiRuntimeError("public configuration value exceeds 65536 bytes")

        def update(document: dict[str, Any]) -> None:
            profile = document["profiles"].get(name)
            if profile is None:
                raise LokiRuntimeError("unknown profile")
            profile["secrets"][secret_name] = value

        self.store.update(update)
        return {"profile": name, "secret": secret_name, "stored": True}

    def op_secret_generate(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        secret_name = _required_secret(request)
        byte_count = request.get("bytes", 32)
        if not isinstance(byte_count, int) or not 16 <= byte_count <= 128:
            raise LokiRuntimeError("generated secret size must be between 16 and 128 bytes")
        value = secrets.token_urlsafe(byte_count)

        def update(document: dict[str, Any]) -> None:
            profile = document["profiles"].get(name)
            if profile is None:
                raise LokiRuntimeError("unknown profile")
            if profile["secrets"].get(secret_name):
                raise LokiRuntimeError("secret is already configured")
            profile["secrets"][secret_name] = value

        self.store.update(update)
        return {"profile": name, "secret": secret_name, "generated": True, "bytes": byte_count}

    def op_secret_remove(self, request: dict[str, Any]) -> dict[str, Any]:
        name = _required_profile(request)
        secret_name = _required_secret(request)
        def update(document: dict[str, Any]) -> None:
            profile = document["profiles"].get(name)
            if profile is None or profile["secrets"].pop(secret_name, None) is None:
                raise LokiRuntimeError("unknown secret")
            for action in profile["actions"].values():
                if secret_name in action["secrets"] or secret_name in action.get("required_secrets", []):
                    raise LokiRuntimeError("secret is still referenced by an action")
        self.store.update(update)
        return {"profile": name, "secret": secret_name, "removed": True}

    def op_action_set(self, request: dict[str, Any]) -> dict[str, Any]:
        profile_name = _required_profile(request)
        action_name = _required_action(request)
        raw_action = request.get("action")
        def update(document: dict[str, Any]) -> None:
            profile = document["profiles"].get(profile_name)
            if profile is None:
                raise LokiRuntimeError("unknown profile")
            _validate_action(raw_action, set(profile["secrets"]))
            profile["actions"][action_name] = raw_action
        self.store.update(update)
        return {"profile": profile_name, "action": action_name, "stored": True}

    def op_action_remove(self, request: dict[str, Any]) -> dict[str, Any]:
        profile_name = _required_profile(request)
        action_name = _required_action(request)
        def update(document: dict[str, Any]) -> None:
            profile = document["profiles"].get(profile_name)
            if profile is None or profile["actions"].pop(action_name, None) is None:
                raise LokiRuntimeError("unknown action")
        self.store.update(update)
        return {"profile": profile_name, "action": action_name, "removed": True}

    def op_project_register(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        identity = self.project_state.resolve(cwd)
        name = request.get("name")
        if name is None:
            name = identity.repository_root.name
        if not isinstance(name, str) or not PROFILE_PATTERN.fullmatch(name):
            raise LokiRuntimeError("project name is invalid")
        repository = identity.repository_root.relative_to(WORKSPACE_ROOT).as_posix()

        def update(document: dict[str, Any]) -> bool:
            projects = document.setdefault("projects", {})
            existing = projects.get(identity.project_id)
            if existing is not None:
                existing["name"] = name
                existing["repository"] = repository
                return False
            projects[identity.project_id] = {
                "name": name,
                "repository": repository,
                "workflows": {},
            }
            return True

        created = self.store.update(update)
        return {
            "project_id": identity.project_id,
            "name": name,
            "repository": repository,
            "created": created,
        }

    def op_project_unregister(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        identity = self.project_state.resolve(cwd)

        def update(document: dict[str, Any]) -> None:
            if document.setdefault("projects", {}).pop(identity.project_id, None) is None:
                raise LokiRuntimeError("project is not registered")

        self.store.update(update)
        return {"project_id": identity.project_id, "unregistered": True}

    def op_project_set_workflow(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        identity = self.project_state.resolve(cwd)
        workflow_name = _required_workflow(request)
        workflow = {
            "steps": request.get("steps"),
            "required_secrets": request.get("required_secrets", {}),
            "timeout_seconds": request.get("timeout_seconds", 3_600),
        }

        def update(document: dict[str, Any]) -> None:
            project = document.setdefault("projects", {}).get(identity.project_id)
            if project is None:
                raise LokiRuntimeError("project is not registered")
            _validate_workflow(workflow, document["profiles"])
            self._validate_workflow_repository(workflow, document["profiles"], identity.project_id)
            project["workflows"][workflow_name] = workflow

        self.store.update(update)
        return {
            "project_id": identity.project_id,
            "workflow": workflow_name,
            "stored": True,
            "steps": copy.deepcopy(workflow["steps"]),
        }

    def op_project_remove_workflow(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        identity = self.project_state.resolve(cwd)
        workflow_name = _required_workflow(request)

        def update(document: dict[str, Any]) -> None:
            project = document.setdefault("projects", {}).get(identity.project_id)
            if project is None or project["workflows"].pop(workflow_name, None) is None:
                raise LokiRuntimeError("project workflow is not registered")

        self.store.update(update)
        return {
            "project_id": identity.project_id,
            "workflow": workflow_name,
            "removed": True,
        }

    def op_project_status(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        identity = self.project_state.resolve(cwd)
        project = self.store.load().setdefault("projects", {}).get(identity.project_id)
        base = {
            "project_id": identity.project_id,
            "worktree_id": identity.worktree_id,
            "cwd": cwd.relative_to(WORKSPACE_ROOT).as_posix(),
        }
        if project is None:
            return {**base, "registered": False, "workflows": []}
        return {
            **base,
            "registered": True,
            "name": project["name"],
            "repository": project["repository"],
            "workflows": sorted(project["workflows"]),
        }

    def op_project_workflow(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        identity = self.project_state.resolve(cwd)
        workflow_name = _required_workflow(request)
        document = self.store.load()
        project = document.setdefault("projects", {}).get(identity.project_id)
        if project is None:
            raise LokiRuntimeError("project is not registered")
        workflow = project["workflows"].get(workflow_name)
        if workflow is None:
            raise LokiRuntimeError("project workflow is not registered")
        _validate_workflow(workflow, document["profiles"])
        self._validate_workflow_repository(workflow, document["profiles"], identity.project_id)
        return {
            "project_id": identity.project_id,
            "workflow": workflow_name,
            "cwd": cwd.relative_to(WORKSPACE_ROOT).as_posix(),
            **copy.deepcopy(workflow),
        }

    def _validate_workflow_repository(
        self, workflow: dict[str, Any], profiles: dict[str, Any], project_id: str,
    ) -> None:
        for profile_name, action_name in workflow["steps"]:
            action = profiles[profile_name]["actions"][action_name]
            action_root = _workspace_cwd(str(action["cwd"]))
            if self.project_state.resolve(action_root).project_id != project_id:
                raise LokiRuntimeError("workflow action belongs to another Git repository")

    def op_bootstrap_project(self, request: dict[str, Any]) -> dict[str, Any]:
        cwd = _workspace_cwd(_required_relative_cwd(request.get("cwd", ".")))
        workflow_name = _required_workflow(request)
        workflow = self.op_project_workflow({"cwd": cwd.relative_to(WORKSPACE_ROOT).as_posix(), "workflow": workflow_name})
        document = self.store.load()
        missing: dict[str, list[str]] = {}
        for profile_name, names in workflow["required_secrets"].items():
            profile = document["profiles"][profile_name]
            absent = [name for name in names if not profile["secrets"].get(name)]
            if absent:
                missing[profile_name] = sorted(absent)
        base = {
            "project_id": workflow["project_id"],
            "cwd": workflow["cwd"],
            "workflow": workflow_name,
            "missing_required_secrets": missing,
            "configuration_ready": not missing,
        }
        if missing:
            return {**base, "accepted": False, "status": "blocked"}

        command = [
            "/usr/bin/systemd-run", "--scope", "--quiet", "--collect",
            f"--unit=loki-project-bootstrap-{secrets.token_hex(8)}",
            "--property=MemoryMax=2G", "--",
            "/usr/sbin/runuser", "-u", "runner", "-m", "--",
            "/usr/local/libexec/loki-project-bootstrap", workflow["cwd"], workflow_name,
        ]
        result = self.processes.start_command(
            name=f"bootstrap/{workflow['project_id']}/{workflow_name}", command=command, cwd=WORKSPACE_ROOT,
            environment={
                "HOME": "/home/runner", "TMPDIR": "/tmp", "LANG": "C.UTF-8",
                "LC_ALL": "C.UTF-8", "PATH": "/opt/loki-mcp/venv/bin:/usr/bin:/bin",
            },
            timeout_seconds=int(workflow["timeout_seconds"]), max_output_bytes=8_388_608,
        )
        return {**base, "accepted": True, **self._scrub_snapshot(result)}

    def op_prepare_action(self, request: dict[str, Any]) -> dict[str, Any]:
        profile_name, profile = self._get_profile(request)
        action_name = _required_action(request)
        action = profile["actions"].get(action_name)
        if action is None:
            raise LokiRuntimeError("unknown action")
        missing = [
            name for name in action.get("required_secrets", [])
            if not profile["secrets"].get(name)
        ]
        if missing:
            raise LokiRuntimeError(
                "required secrets are not configured: " + ", ".join(sorted(missing))
            )
        dynamic_port = action.get("dynamic_port")
        if dynamic_port is None:
            raise LokiRuntimeError("action does not use a dynamic port")
        cwd = _action_cwd(action, request.get("cwd"))
        port = _allocate_loopback_port(int(dynamic_port["preferred"]))
        token = secrets.token_hex(16)
        self._action_launches[token] = {
            "profile": profile_name,
            "action": action_name,
            "cwd": str(cwd),
            "port": port,
            "expires_at": time.monotonic() + PORT_ALLOCATION_HOLD_SECONDS,
        }
        return {
            "launch_token": token,
            "port": port,
            "local_url": f"http://127.0.0.1:{port}",
            "expires_in_seconds": int(PORT_ALLOCATION_HOLD_SECONDS),
            **_preview_bindings(action, profile["secrets"]),
        }

    def op_run_action(self, request: dict[str, Any]) -> dict[str, Any]:
        profile_name, profile = self._get_profile(request)
        action_name = _required_action(request)
        action = profile["actions"].get(action_name)
        if action is None:
            raise LokiRuntimeError("unknown action")
        bind_local_callback = request.get("bind_local_callback", False)
        if not isinstance(bind_local_callback, bool):
            raise LokiRuntimeError("bind_local_callback must be a boolean")
        if bind_local_callback and not action.get("local_callback", False):
            raise LokiRuntimeError("action is not approved as a local callback target")
        missing = [
            name for name in action.get("required_secrets", [])
            if not profile["secrets"].get(name)
        ]
        if missing:
            raise LokiRuntimeError(
                "required secrets are not configured: " + ", ".join(sorted(missing))
            )
        cwd = _action_cwd(action, request.get("cwd"))
        instance_key = None
        if action.get("singleton", False):
            instance_key = hashlib.sha256(
                f"{profile_name}\0{action_name}\0{cwd}".encode("utf-8")
            ).hexdigest()
            existing = self.processes.find_running(instance_key)
            if existing is not None:
                response = self._scrub_snapshot(existing)
                return self._attach_local_callback(response, bind_local_callback)
        lock_probe = action.get("lock_probe")
        if lock_probe is not None:
            target = cwd / str(lock_probe)
            if _file_lock_is_held(target):
                visible_target = PurePosixPath(
                    "/workspace", *target.relative_to(WORKSPACE_ROOT).parts,
                )
                raise LokiRuntimeError(
                    "action runtime is already owned outside the current Loki session: "
                    f"{visible_target}; stop the owner or run this action from another worktree"
                )
        dynamic_port = action.get("dynamic_port")
        allocated_port: int | None = None
        generated_environment: dict[str, str] = {}
        raw_command = list(action["command"])
        if dynamic_port is not None:
            launch_token = request.get("launch_token")
            if launch_token is None:
                allocated_port = _allocate_loopback_port(int(dynamic_port["preferred"]))
            else:
                allocated_port = self._consume_action_launch(
                    launch_token, profile_name, action_name, cwd,
                )
            raw_command = [
                str(allocated_port) if item == "{LOKI_PORT}" else item
                for item in raw_command
            ]
            generated_environment[str(dynamic_port["environment"])] = str(allocated_port)
            origin_environment = dynamic_port.get("origin_environment")
            if origin_environment is not None:
                generated_environment[str(origin_environment)] = (
                    f"http://127.0.0.1:{allocated_port}"
                )
        command = _resolved_command(raw_command, cwd)
        selected = _selected_secret_names(action, profile["secrets"])
        values = {name: profile["secrets"][name] for name in selected}
        allowed_public = set(action.get("public_environment", []))
        if dynamic_port and dynamic_port.get("origin_environment"):
            allowed_public.add(dynamic_port["origin_environment"])
        public_environment = _validated_public_environment(request.get("public_environment", {}), allowed_public)
        if request.get("launch_token") is not None:
            required_public = _preview_bindings(action, profile["secrets"])["required_environment"]
            if set(required_public) - set(public_environment):
                raise LokiRuntimeError("PREVIEW_MAPPING_REQUIRED: missing public dependency or origin mapping")
        sandbox_cwd = PurePosixPath("/workspace", *cwd.relative_to(WORKSPACE_ROOT).parts)
        runner = ["/usr/sbin/runuser", "-u", "runner", "-m", "--", "/usr/local/libexec/loki-action-runner", "--cwd", str(sandbox_cwd)]
        for name in selected:
            runner.extend(["--env", name])
        materialize_env_file = action.get("materialize_env_file")
        if materialize_env_file:
            runner.extend(["--materialize-env-file", materialize_env_file])
        materialize_env_path = action.get("materialize_env_path")
        if materialize_env_path:
            runner.extend(["--materialize-env-path", materialize_env_path])
        if action.get("docker_access", False):
            runner.extend(["--docker-socket", "{LOKI_DOCKER_PROXY_SOCKET}"])
        runner.extend(["--", *command])
        if action.get("docker_access", False):
            runner = ["/usr/local/libexec/loki-docker-action-runner", "--", *runner]
        action_unit = f"loki-action-{secrets.token_hex(8)}.scope"
        runner = [
            "/usr/bin/systemd-run", "--scope", "--quiet",
            f"--unit={action_unit}",
            "--property=MemoryMax=4G", "--", *runner,
        ]
        environment = {
            **os.environ, **values, **generated_environment, **public_environment,
        }
        try:
            result = self.processes.start_command(
                name=f"{profile_name}/{action_name}", command=runner, cwd=WORKSPACE_ROOT,
                environment=environment, timeout_seconds=action["timeout_seconds"],
                max_output_bytes=action["max_output_bytes"], group=profile_name,
                max_group_processes=getattr(
                    self, "max_processes_per_profile", 6,
                ),
                instance_key=instance_key,
                metadata={
                    "profile": profile_name,
                    "action": action_name,
                    "systemd_unit": action_unit,
                    "memory_limit_bytes": 4 * 1024 ** 3,
                    "cwd": str(sandbox_cwd),
                    **(
                        {
                            "port": allocated_port,
                            "local_url": f"http://127.0.0.1:{allocated_port}",
                        }
                        if allocated_port is not None else {}
                    ),
                },
            )
        except PolicyError as error:
            raise LokiRuntimeError(
                f"{error}; stop an existing action process before retrying"
            ) from error
        self._scrubbers[result["session_id"]] = tuple(value for value in values.values() if value)
        response = self._scrub_snapshot(result)
        if allocated_port is not None and not result.get("reused"):
            response.update({
                "port": allocated_port,
                "local_url": f"http://127.0.0.1:{allocated_port}",
                "cwd": str(PurePosixPath("/workspace", *cwd.relative_to(WORKSPACE_ROOT).parts)),
            })
        elif allocated_port is not None:
            _release_pending_port(allocated_port)
        return self._attach_local_callback(response, bind_local_callback)

    def _attach_local_callback(
        self, response: dict[str, Any], requested: bool,
    ) -> dict[str, Any]:
        if not requested:
            return response
        try:
            binding = self.local_callback.bind(str(response["session_id"]))
        except (OSError, RuntimeError) as error:
            binding = {
                "bound": False,
                "origin": self.local_callback.status()["origin"],
                "error": str(error),
            }
        return {**response, "local_callback": binding}

    def _resolve_local_callback_target(self, session_id: str) -> int | None:
        try:
            snapshot = self.processes.read(session_id, offset=0, limit=1)
        except (KeyError, PolicyError):
            return None
        port = snapshot.get("port")
        if (
            snapshot.get("status") != "running"
            or snapshot.get("action") != "api"
            or isinstance(port, bool)
            or not isinstance(port, int)
            or not 1 <= port <= 65_535
        ):
            return None
        return port

    def op_local_callback_bind(self, request: dict[str, Any]) -> dict[str, Any]:
        return self.local_callback.bind(_required_session(request))

    def _consume_action_launch(
        self, token: object, profile: str, action: str, cwd: Path,
    ) -> int:
        if not isinstance(token, str) or ACTION_LAUNCH_TOKEN_PATTERN.fullmatch(token) is None:
            raise LokiRuntimeError("invalid action launch token")
        launch = self._action_launches.pop(token, None)
        if launch is None or float(launch["expires_at"]) <= time.monotonic():
            raise LokiRuntimeError("action launch token is unknown or expired")
        if (
            launch["profile"] != profile
            or launch["action"] != action
            or launch["cwd"] != str(cwd)
        ):
            raise LokiRuntimeError("action launch token does not match the action")
        port = int(launch["port"])
        with _PORT_ALLOCATION_LOCK:
            _PENDING_PORTS.pop(port, None)
        return port

    def op_clear_action_materialization(self, request: dict[str, Any]) -> dict[str, Any]:
        profile_name, profile = self._get_profile(request)
        action_name = _required_action(request)
        action = profile["actions"].get(action_name)
        if action is None:
            raise LokiRuntimeError("unknown action")
        relative_path = action.get("materialize_env_path")
        if not action.get("materialize_env_file") or not relative_path:
            raise LokiRuntimeError("action has no fixed secret materialization target")
        running = self.processes.list()["processes"]
        if any(
            item.get("name") == f"{profile_name}/{action_name}"
            and item.get("status") == "running"
            for item in running
        ):
            raise LokiRuntimeError("action is still running")
        cwd = _workspace_cwd(str(action["cwd"]))
        target = cwd / str(relative_path)
        parent = target.parent.resolve()
        if parent != cwd and cwd not in parent.parents:
            raise LokiRuntimeError("materialized secret target escapes action cwd")
        if target.is_symlink():
            raise LokiRuntimeError("materialized secret target is unsafe to clear")
        if not target.exists():
            return {"profile": profile_name, "action": action_name, "cleared": False}
        metadata = target.lstat()
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid not in {0, AGENT_UID}
            or stat.S_IMODE(metadata.st_mode) & 0o077
            or metadata.st_nlink != 1
        ):
            raise LokiRuntimeError("materialized secret target is unsafe to clear")
        _unlink_materialization_as_agent(target)
        return {"profile": profile_name, "action": action_name, "cleared": True}

    def op_read_process(self, request: dict[str, Any]) -> dict[str, Any]:
        session_id = _required_session(request)
        result = self.processes.read(session_id, request.get("offset"), int(request.get("limit", 65_536)))
        return self._scrub_snapshot(result)

    def op_stop_process(self, request: dict[str, Any]) -> dict[str, Any]:
        session_id = _required_session(request)
        result = self._scrub_snapshot(self.processes.stop(session_id))
        local_callback = getattr(self, "local_callback", None)
        if local_callback is not None:
            local_callback.clear(session_id)
        if result.get("status") == "exited" and result.get("timed_out") is not True:
            result["outcome"] = "stopped"
        return result

    def op_audit(self, request: dict[str, Any]) -> dict[str, Any]:
        limit = max(1, min(int(request.get("limit", 50)), 200))
        try:
            lines = AUDIT_PATH.read_text(encoding="utf-8").splitlines()
        except FileNotFoundError:
            lines = []
        records = []
        for line in reversed(lines):
            try:
                records.append(json.loads(line))
            except json.JSONDecodeError:
                continue
            if len(records) >= limit:
                break
        return {"records": records}

    @staticmethod
    def _profile_metadata(name: str, profile: dict[str, Any], include_actions: bool = False) -> dict[str, Any]:
        result: dict[str, Any] = {
            "name": name,
            "secret_names": sorted(profile["secrets"]),
            "secret_count": len(profile["secrets"]),
            "configured_secret_count": sum(
                bool(value) for value in profile["secrets"].values()
            ),
            "empty_secret_names": sorted(
                name for name, value in profile["secrets"].items() if not value
            ),
            "actions": sorted(profile["actions"]),
        }
        if include_actions:
            result["action_policies"] = {
                action_name: {
                    "command": action["command"], "cwd": action["cwd"],
                    "secrets": action["secrets"],
                    "required_secrets": action.get("required_secrets", []),
                    "missing_required_secrets": sorted(
                        name for name in action.get("required_secrets", [])
                        if not profile["secrets"].get(name)
                    ),
                    "ready": not any(
                        not profile["secrets"].get(name)
                        for name in action.get("required_secrets", [])
                    ),
                    "all_secrets": action.get("all_secrets", not action["secrets"]),
                    "materialize_env_file": action.get("materialize_env_file"),
                    "materialize_env_path": action.get("materialize_env_path"),
                    "dynamic_port": action.get("dynamic_port"),
                    "singleton": action.get("singleton", False),
                    "lock_probe": action.get("lock_probe"),
                    "local_callback": action.get("local_callback", False),
                    "public_environment": action.get("public_environment", []),
                    "preview_environment": action.get("preview_environment", {}),
                    "docker_access": action.get("docker_access", False),
                    "timeout_seconds": action["timeout_seconds"],
                    "max_output_bytes": action["max_output_bytes"],
                }
                for action_name, action in sorted(profile["actions"].items())
            }
        return result

    def _get_profile(self, request: dict[str, Any]) -> tuple[str, dict[str, Any]]:
        name = _required_profile(request)
        profile = self.store.load()["profiles"].get(name)
        if profile is None:
            raise LokiRuntimeError("unknown profile")
        return name, profile

    def _import_values(self, name: str, values: dict[str, str]) -> None:
        def update(document: dict[str, Any]) -> None:
            profile = document["profiles"].get(name)
            if profile is None:
                raise LokiRuntimeError("unknown profile")
            profile["secrets"].update(values)
        self.store.update(update)

    def _scrub_snapshot(self, result: dict[str, Any]) -> dict[str, Any]:
        scrubbed = dict(result)
        output = str(scrubbed.get("output", ""))
        for value in self._scrubbers.get(str(result.get("session_id")), ()):
            output = output.replace(value, "[REDACTED]")
        scrubbed["output"] = output
        status = scrubbed.get("status")
        exit_code = scrubbed.get("exit_code")
        if status == "running":
            scrubbed["outcome"] = "running"
        elif status == "exited":
            scrubbed["outcome"] = "succeeded" if exit_code == 0 else "failed"
        return scrubbed

    @staticmethod
    def _audit(operation: str, uid: int, success: bool, request: dict[str, Any]) -> None:
        AUDIT_PATH.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        record = {
            "timestamp": time.time(), "operation": operation, "uid": uid, "success": success,
            "profile": request.get("profile"), "action": request.get("action_name"),
            "project_action": request.get("action") if operation == "project_state" else None,
            "cwd": request.get("cwd") if operation in {"project_state", "project_task"} else None,
        }
        descriptor = os.open(AUDIT_PATH, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
        try:
            os.write(descriptor, (json.dumps(record, separators=(",", ":")) + "\n").encode())
        finally:
            os.close(descriptor)


def _peer_is_mcp_service(uid: int, pid: int | None) -> bool:
    """Trust administrative requests only from the managed MCP service cgroup."""
    if uid != AGENT_UID or pid is None or pid <= 0:
        return False
    try:
        lines = Path(f"/proc/{pid}/cgroup").read_text(encoding="utf-8").splitlines()
    except (OSError, ValueError):
        return False
    return any(
        line.partition("::")[2].rstrip("/").endswith("/loki-mcp.service")
        for line in lines
        if "::" in line
    )


class Handler(socketserver.StreamRequestHandler):
    def handle(self) -> None:
        pid, uid, _ = struct.unpack("3i", self.request.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        raw = self.rfile.readline(MAX_REQUEST_BYTES + 1)
        try:
            if len(raw) > MAX_REQUEST_BYTES:
                raise LokiRuntimeError("request is too large")
            request = json.loads(raw)
            if not isinstance(request, dict):
                raise LokiRuntimeError("request must be an object")
            result = self.server.runtime.dispatch(request, uid, pid)  # type: ignore[attr-defined]
            response = {"ok": True, "result": result}
        except Exception as error:
            response = {"ok": False, "error": str(error)}
        self.wfile.write(json.dumps(response, ensure_ascii=False, separators=(",", ":")).encode() + b"\n")


class UnixServer(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, path: str, runtime: RuntimeController) -> None:
        self.runtime = runtime
        super().__init__(path, Handler)


def request_runtime(request: dict[str, Any], socket_path: Path = SOCKET_PATH) -> dict[str, Any]:
    encoded = json.dumps(request, ensure_ascii=False, separators=(",", ":")).encode() + b"\n"
    if len(encoded) > MAX_REQUEST_BYTES:
        raise LokiRuntimeError("request is too large")
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(30)
        client.connect(str(socket_path))
        client.sendall(encoded)
        chunks = bytearray()
        while not chunks.endswith(b"\n"):
            chunk = client.recv(65_536)
            if not chunk:
                break
            chunks.extend(chunk)
            if len(chunks) > MAX_REQUEST_BYTES:
                raise LokiRuntimeError("response is too large")
    if not chunks:
        raise LokiRuntimeError("Loki runtime returned no response")
    response = json.loads(chunks)
    if not response.get("ok"):
        raise LokiRuntimeError(str(response.get("error", "Loki runtime request failed")))
    return response["result"]


def serve() -> None:
    SOCKET_PATH.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    try:
        SOCKET_PATH.unlink()
    except FileNotFoundError:
        pass
    runtime = RuntimeController()
    server = UnixServer(str(SOCKET_PATH), runtime)
    os.chmod(SOCKET_PATH, 0o660)
    try:
        server.serve_forever()
    finally:
        server.server_close()
        runtime.close()


def _validate_profile_name(name: object) -> None:
    if not isinstance(name, str) or PROFILE_PATTERN.fullmatch(name) is None:
        raise LokiRuntimeError("invalid profile name")


def _validate_action_name(name: object) -> None:
    if not isinstance(name, str) or PROFILE_PATTERN.fullmatch(name) is None:
        raise LokiRuntimeError("invalid action name")


def _validate_secret_name(name: object) -> None:
    if not isinstance(name, str) or NAME_PATTERN.fullmatch(name) is None:
        raise LokiRuntimeError("invalid secret name")


def inspect_docker_port(value: object) -> dict[str, Any]:
    if isinstance(value, bool) or not isinstance(value, int) or not 1024 <= value <= 65_535:
        raise LokiRuntimeError("port must be an integer between 1024 and 65535")
    if value in {8765, 8766, 8767}:
        raise LokiRuntimeError("protected Loki service port")

    listed = _docker(
        "container", "ls", "--filter", f"publish={value}", "--format", "{{.ID}}",
    )
    container_ids = [item.strip() for item in listed.splitlines() if item.strip()]
    if len(container_ids) > 32 or any(re.fullmatch(r"[0-9a-f]{12,64}", item) is None for item in container_ids):
        raise LokiRuntimeError("Docker returned invalid container metadata")

    listeners: list[dict[str, Any]] = []
    for container_id in container_ids:
        raw = _docker(
            "inspect", "--format",
            "{{.State.Running}}\t{{json .NetworkSettings.Ports}}\t{{json .Config.Labels}}",
            container_id,
        ).strip()
        fields = raw.split("\t", 2)
        if len(fields) != 3 or fields[0] != "true":
            continue
        try:
            ports = json.loads(fields[1])
            labels = json.loads(fields[2])
        except json.JSONDecodeError as error:
            raise LokiRuntimeError("Docker returned invalid container metadata") from error
        if not isinstance(ports, dict) or not isinstance(labels, dict):
            raise LokiRuntimeError("Docker returned invalid container metadata")
        if not _trusted_compose_labels(labels):
            continue
        bindings = [
            binding
            for mappings in ports.values()
            if isinstance(mappings, list)
            for binding in mappings
            if isinstance(binding, dict) and binding.get("HostPort") == str(value)
        ]
        if not bindings or any(binding.get("HostIp") not in {"127.0.0.1", "::1"} for binding in bindings):
            continue
        listeners.append({
            "container_id": container_id,
            "cwd": _compose_display_cwd(str(labels["com.docker.compose.project.working_dir"])),
            "command": (
                f"docker-compose:{labels['com.docker.compose.project']}"
                f"/{labels['com.docker.compose.service']}"
            ),
            "local_address": f"127.0.0.1:{value}",
        })
    if len(listeners) > 1:
        raise LokiRuntimeError("multiple trusted Docker containers publish this port")
    return {"port": value, "in_use": bool(listeners), "listeners": listeners}


def _docker(*arguments: str) -> str:
    try:
        completed = subprocess.run(
            ["/usr/bin/docker", "--host", DOCKER_SOCKET, *arguments],
            check=False, capture_output=True, text=True, timeout=15,
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise LokiRuntimeError("Docker inspection is unavailable") from error
    if completed.returncode != 0:
        raise LokiRuntimeError("Docker inspection failed")
    if len(completed.stdout.encode("utf-8")) > 1_048_576:
        raise LokiRuntimeError("Docker inspection response is too large")
    return completed.stdout


def _trusted_compose_labels(labels: dict[str, object]) -> bool:
    required = (
        "com.docker.compose.project",
        "com.docker.compose.service",
        "com.docker.compose.project.working_dir",
        "com.docker.compose.project.config_files",
    )
    if any(not isinstance(labels.get(key), str) or not labels[key] for key in required):
        return False
    working = Path(str(labels["com.docker.compose.project.working_dir"]))
    if not working.is_absolute():
        return False
    trusted_roots = (WORKSPACE_ROOT, Path("/workspace"), LOCAL_DEPLOY_SNAPSHOT_ROOT)
    if not any(_path_within(working, root) for root in trusted_roots):
        return False
    config_files = str(labels["com.docker.compose.project.config_files"]).split(",")
    return bool(config_files) and all(
        Path(item).suffix in {".yml", ".yaml"} and _path_within(Path(item), working)
        for item in config_files
    )


def _path_within(path: Path, root: Path) -> bool:
    if ".." in path.parts:
        return False
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def _compose_display_cwd(value: str) -> str:
    path = Path(value)
    for root in (WORKSPACE_ROOT, Path("/workspace")):
        try:
            return str(Path("/workspace") / path.relative_to(root))
        except ValueError:
            pass
    return "/workspace"


def _unlink_materialization_as_agent(target: Path) -> None:
    if os.geteuid() == AGENT_UID:
        target.unlink()
        return
    completed = subprocess.run(
        ["/usr/sbin/runuser", "-u", "runner", "--", "/usr/bin/unlink", "--", str(target)],
        check=False, capture_output=True, timeout=10,
    )
    if completed.returncode != 0:
        raise LokiRuntimeError("materialized secret target could not be cleared")


def _required_profile(request: dict[str, Any]) -> str:
    name = request.get("profile")
    _validate_profile_name(name)
    return name


def _required_string(request: dict[str, Any], name: str) -> str:
    value = request.get(name)
    if not isinstance(value, str) or not value or len(value.encode("utf-8")) > 2_097_152:
        raise LokiRuntimeError(f"{name} is required or too large")
    return value


def _optional_string(value: object) -> str | None:
    if value is None:
        return None
    if not isinstance(value, str) or not value or len(value.encode("utf-8")) > 2_097_152:
        raise LokiRuntimeError("optional project state value is invalid")
    return value


def _required_relative_cwd(value: object) -> str:
    if (
        not isinstance(value, str)
        or not value
        or value.startswith("/")
        or "\\" in value
        or "\x00" in value
        or ".." in PurePosixPath(value).parts
    ):
        raise LokiRuntimeError("project state cwd must be workspace-relative")
    return value


def _required_action(request: dict[str, Any]) -> str:
    name = request.get("action_name")
    _validate_action_name(name)
    return name


def _required_secret(request: dict[str, Any]) -> str:
    name = request.get("secret")
    _validate_secret_name(name)
    return name


def _required_import_id(request: dict[str, Any]) -> str:
    value = request.get("import_id")
    if not isinstance(value, str) or IMPORT_ID_PATTERN.fullmatch(value) is None:
        raise LokiRuntimeError("invalid secret import ID")
    return value


def _read_staged_import(import_id: str) -> tuple[Path, dict[str, str]]:
    source = INBOX_DIRECTORY / f"{import_id}.env"
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(source, flags)
    except FileNotFoundError as error:
        raise LokiRuntimeError("unknown secret import") from error
    try:
        metadata = os.fstat(descriptor)
        if (
            not stat.S_ISREG(metadata.st_mode)
            or metadata.st_uid != os.geteuid()
            or metadata.st_mode & 0o077
            or not 0 < metadata.st_size <= MAX_INBOX_BYTES
        ):
            raise LokiRuntimeError("staged dotenv file has unsafe ownership or permissions")
        raw = bytearray()
        while len(raw) <= MAX_INBOX_BYTES:
            chunk = os.read(descriptor, min(65_536, MAX_INBOX_BYTES + 1 - len(raw)))
            if not chunk:
                break
            raw.extend(chunk)
        if len(raw) > MAX_INBOX_BYTES:
            raise LokiRuntimeError("staged dotenv file is too large")
    finally:
        os.close(descriptor)
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError as error:
        raise LokiRuntimeError("staged dotenv file must be UTF-8") from error
    return source, parse_dotenv(text)


def _required_session(request: dict[str, Any]) -> str:
    value = request.get("session_id")
    if not isinstance(value, str) or not 8 <= len(value) <= 128:
        raise LokiRuntimeError("invalid process session")
    return value


def _required_workflow(request: dict[str, Any]) -> str:
    value = request.get("workflow", "development")
    if not isinstance(value, str) or not PROFILE_PATTERN.fullmatch(value):
        raise LokiRuntimeError("workflow name is invalid")
    return value


def _validate_project(
    project_id: object, project: object, profiles: dict[str, Any],
) -> None:
    if not isinstance(project_id, str) or re.fullmatch(r"[a-f0-9]{32}", project_id) is None:
        raise LokiRuntimeError("project id is invalid")
    if not isinstance(project, dict) or set(project) != {"name", "repository", "workflows"}:
        raise LokiRuntimeError("project policy is invalid")
    name = project.get("name")
    repository = project.get("repository")
    workflows = project.get("workflows")
    if not isinstance(name, str) or not PROFILE_PATTERN.fullmatch(name):
        raise LokiRuntimeError("project name is invalid")
    if (
        not isinstance(repository, str)
        or not repository
        or repository.startswith("/")
        or "\\" in repository
        or ".." in PurePosixPath(repository).parts
    ):
        raise LokiRuntimeError("project repository is invalid")
    if not isinstance(workflows, dict) or len(workflows) > MAX_WORKFLOWS:
        raise LokiRuntimeError("project workflow collection is invalid")
    for workflow_name, workflow in workflows.items():
        if not isinstance(workflow_name, str) or not PROFILE_PATTERN.fullmatch(workflow_name):
            raise LokiRuntimeError("workflow name is invalid")
        _validate_workflow(workflow, profiles)


def _validate_workflow(workflow: object, profiles: dict[str, Any]) -> None:
    if not isinstance(workflow, dict) or set(workflow) != {
        "steps", "required_secrets", "timeout_seconds",
    }:
        raise LokiRuntimeError("project workflow policy is invalid")
    steps = workflow.get("steps")
    required = workflow.get("required_secrets")
    timeout = workflow.get("timeout_seconds")
    if (
        not isinstance(steps, list)
        or not steps
        or len(steps) > 64
        or not all(
            isinstance(step, list)
            and len(step) == 2
            and all(isinstance(item, str) for item in step)
            for step in steps
        )
    ):
        raise LokiRuntimeError("project workflow steps are invalid")
    if not isinstance(required, dict):
        raise LokiRuntimeError("project workflow required secrets are invalid")
    if isinstance(timeout, bool) or not isinstance(timeout, int) or not 1 <= timeout <= 86_400:
        raise LokiRuntimeError("project workflow timeout is invalid")
    referenced_profiles = {step[0] for step in steps} | set(required)
    for profile_name in referenced_profiles:
        _validate_profile_name(profile_name)
        if profile_name not in profiles:
            raise LokiRuntimeError("project workflow references an unknown profile")
    for profile_name, action_name in steps:
        _validate_action_name(action_name)
        if action_name not in profiles[profile_name]["actions"]:
            raise LokiRuntimeError("project workflow references an unknown action")
    for profile_name, names in required.items():
        if (
            not isinstance(names, list)
            or len(set(names)) != len(names)
            or not all(isinstance(name, str) for name in names)
        ):
            raise LokiRuntimeError("project workflow required secrets are invalid")
        available = profiles[profile_name]["secrets"]
        for name in names:
            _validate_secret_name(name)
            if name not in available:
                raise LokiRuntimeError("project workflow references an unknown secret")


def _validate_action(action: object, available_secrets: set[str]) -> None:
    if not isinstance(action, dict):
        raise LokiRuntimeError("action policy is invalid")
    command = action.get("command")
    cwd = action.get("cwd")
    selected = action.get("secrets")
    required = action.get("required_secrets", [])
    all_secrets = action.get("all_secrets", selected == [])
    timeout = action.get("timeout_seconds")
    maximum = action.get("max_output_bytes")
    materialize_env_file = action.get("materialize_env_file")
    materialize_env_path = action.get("materialize_env_path")
    docker_access = action.get("docker_access", False)
    dynamic_port = action.get("dynamic_port")
    singleton = action.get("singleton", False)
    lock_probe = action.get("lock_probe")
    local_callback = action.get("local_callback", False)
    public_environment = action.get("public_environment", [])
    if not isinstance(command, list) or not command or not all(isinstance(item, str) and item and "\x00" not in item for item in command):
        raise LokiRuntimeError("action command must be a non-empty argv array")
    if len(command) > 129 or sum(len(item) for item in command) > 16_384:
        raise LokiRuntimeError("action command is too large")
    executable = command[0]
    if executable not in EXECUTABLES:
        raise LokiRuntimeError("action executable is not allowed")
    WorkspaceTools._validate_exec_policy(executable, command[1:])
    if not isinstance(cwd, str) or not cwd or cwd.startswith("/") or "\\" in cwd or "\x00" in cwd or ".." in PurePosixPath(cwd).parts:
        raise LokiRuntimeError("action cwd must be workspace-relative")
    if not isinstance(selected, list) or not all(isinstance(item, str) for item in selected):
        raise LokiRuntimeError("action secrets must be an array")
    if not isinstance(all_secrets, bool):
        raise LokiRuntimeError("action all_secrets must be a boolean")
    if all_secrets and selected:
        raise LokiRuntimeError("action cannot combine all_secrets with selected secrets")
    if not all_secrets and not selected:
        raise LokiRuntimeError("action must select secrets or enable all_secrets")
    if len(set(selected)) != len(selected) or not set(selected) <= available_secrets:
        raise LokiRuntimeError("action references an unknown or duplicate secret")
    if not isinstance(required, list) or not all(isinstance(item, str) for item in required):
        raise LokiRuntimeError("action required_secrets must be an array")
    if len(set(required)) != len(required) or not set(required) <= available_secrets:
        raise LokiRuntimeError("action references an unknown or duplicate required secret")
    if not all_secrets and not set(required) <= set(selected):
        raise LokiRuntimeError("required secrets must be selected by the action")
    if not isinstance(timeout, int) or not 1 <= timeout <= 86_400:
        raise LokiRuntimeError("action timeout is invalid")
    if not isinstance(maximum, int) or not 4_096 <= maximum <= 16_777_216:
        raise LokiRuntimeError("action output limit is invalid")
    if materialize_env_file is not None:
        _validate_secret_name(materialize_env_file)
    if materialize_env_path is not None:
        if materialize_env_file is None:
            raise LokiRuntimeError("materialized env path requires a materialized env file")
        if (
            not isinstance(materialize_env_path, str)
            or not materialize_env_path
            or materialize_env_path.startswith("/")
            or "\\" in materialize_env_path
            or "\x00" in materialize_env_path
            or ".." in PurePosixPath(materialize_env_path).parts
        ):
            raise LokiRuntimeError("materialized env path must be workspace-relative")
    if not isinstance(docker_access, bool):
        raise LokiRuntimeError("action docker access must be a boolean")
    if dynamic_port is not None:
        if not isinstance(dynamic_port, dict) or set(dynamic_port) not in (
            {"preferred", "environment"},
            {"preferred", "environment", "origin_environment"},
        ):
            raise LokiRuntimeError("action dynamic port policy is invalid")
        preferred = dynamic_port.get("preferred")
        environment_name = dynamic_port.get("environment")
        origin_environment = dynamic_port.get("origin_environment")
        if (
            isinstance(preferred, bool)
            or not isinstance(preferred, int)
            or not 1024 <= preferred <= 65_535
            or preferred in {8765, 8766, 8767}
            or not isinstance(environment_name, str)
        ):
            raise LokiRuntimeError("action dynamic port policy is invalid")
        _validate_secret_name(environment_name)
        if origin_environment is not None:
            _validate_secret_name(origin_environment)
        if command.count("{LOKI_PORT}") > 1:
            raise LokiRuntimeError(
                "dynamic port action may contain at most one {LOKI_PORT} argument"
            )
    elif "{LOKI_PORT}" in command:
        raise LokiRuntimeError("action port placeholder requires a dynamic port policy")
    if not isinstance(local_callback, bool):
        raise LokiRuntimeError("action local callback policy must be a boolean")
    if local_callback and dynamic_port is None:
        raise LokiRuntimeError("local callback target requires a dynamic port policy")
    if (
        not isinstance(public_environment, list)
        or len(public_environment) > 64
        or len(set(public_environment)) != len(public_environment)
        or not all(isinstance(name, str) for name in public_environment)
    ):
        raise LokiRuntimeError("action public environment policy is invalid")
    for name in public_environment:
        _validate_secret_name(name)
    preview_environment = action.get("preview_environment", {})
    if not isinstance(preview_environment, dict) or len(preview_environment) > 7:
        raise LokiRuntimeError("invalid preview environment policy")
    for name, prefix in preview_environment.items():
        if name not in public_environment or not isinstance(prefix, str) or re.fullmatch(r"/[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+)*", prefix) is None:
            raise LokiRuntimeError("invalid preview environment route")
    if not isinstance(singleton, bool):
        raise LokiRuntimeError("action singleton must be a boolean")
    if lock_probe is not None:
        if not isinstance(lock_probe, str) or not lock_probe:
            raise LokiRuntimeError("action lock probe must be a relative path")
        probe_path = PurePosixPath(lock_probe)
        if probe_path.is_absolute() or ".." in probe_path.parts:
            raise LokiRuntimeError("action lock probe must stay inside action cwd")
def _selected_secret_names(action: dict[str, Any], secrets: dict[str, str]) -> list[str]:
    if action.get("all_secrets", not action["secrets"]):
        return sorted(secrets)
    return list(action["secrets"])


def _allocate_loopback_port(preferred: int) -> int:
    with _PORT_ALLOCATION_LOCK:
        now = time.monotonic()
        for port, expires_at in list(_PENDING_PORTS.items()):
            if expires_at <= now:
                del _PENDING_PORTS[port]
        for candidate in (preferred, *([0] * 32)):
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
                try:
                    listener.bind(("127.0.0.1", candidate))
                except OSError:
                    continue
                port = int(listener.getsockname()[1])
                if port in {8765, 8766, 8767} or port in _PENDING_PORTS:
                    continue
                _PENDING_PORTS[port] = now + PORT_ALLOCATION_HOLD_SECONDS
                return port
    raise LokiRuntimeError("unable to allocate a loopback development port")


def _release_pending_port(port: int) -> None:
    with _PORT_ALLOCATION_LOCK:
        _PENDING_PORTS.pop(port, None)


def _file_lock_is_held(path: Path) -> bool:
    exists = subprocess.run(
        ["/usr/sbin/runuser", "-u", "runner", "--", "/usr/bin/test", "-e", str(path)],
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL, check=False, timeout=5,
    )
    if exists.returncode == 1:
        return False
    if exists.returncode != 0:
        raise LokiRuntimeError("unable to inspect action runtime lock path")
    completed = subprocess.run(
        [
            "/usr/sbin/runuser", "-u", "runner", "--",
            "/usr/bin/flock", "--nonblock", str(path), "/usr/bin/true",
        ],
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL, check=False, timeout=5,
    )
    if completed.returncode == 0:
        return False
    if completed.returncode == 1:
        return True
    raise LokiRuntimeError("unable to inspect action runtime lock ownership")


def _action_cwd(action: dict[str, Any], override: object) -> Path:
    registered = _workspace_cwd(str(action["cwd"]))
    if override is None:
        return registered
    if (
        not isinstance(override, str)
        or not override
        or override.startswith("/")
        or "\\" in override
        or "\x00" in override
        or ".." in PurePosixPath(override).parts
    ):
        raise LokiRuntimeError("action cwd override must be workspace-relative")
    candidate = _workspace_cwd(override)
    if candidate == registered:
        return candidate
    if _git_common_directory(candidate) != _git_common_directory(registered):
        raise LokiRuntimeError("action cwd override must be a worktree of the registered repository")
    return candidate


def _git_common_directory(cwd: Path) -> Path:
    try:
        common = ProjectStateStore(WORKSPACE_ROOT, STATE_ROOT)._git_path(
            cwd, "--path-format=absolute", "--git-common-dir",
        )
    except PolicyError as error:
        raise LokiRuntimeError("action cwd override requires a Git worktree") from error
    except (OSError, subprocess.SubprocessError) as error:
        raise LokiRuntimeError("unable to verify action worktree") from error
    workspace = WORKSPACE_ROOT.resolve()
    if common != workspace and workspace not in common.parents:
        raise LokiRuntimeError("action Git metadata must stay inside the workspace")
    return common


def _preview_bindings(action: dict[str, Any], values: dict[str, str]) -> dict[str, Any]:
    routes: dict[str, int] = {}
    mappings: dict[str, str] = {}
    suffixes: dict[str, str] = {}
    required = []
    configured = action.get("preview_environment", {})
    for name in action.get("public_environment", []):
        parsed = urlsplit(values.get(name, ""))
        if parsed.hostname in {"127.0.0.1", "localhost", "::1", "0.0.0.0"}:
            required.append(name)
            if name not in configured:
                continue
            if parsed.scheme != "http" or parsed.username or parsed.password or parsed.query or parsed.fragment:
                raise LokiRuntimeError("invalid preview backend URL")
            prefix = configured[name]
            port = parsed.port or (443 if parsed.scheme == "https" else 80)
            if prefix in routes and routes[prefix] != port:
                raise LokiRuntimeError("preview route has conflicting backends")
            routes[prefix] = port
            mappings[name] = prefix
            suffixes[name] = parsed.path.rstrip("/")
    origin = (action.get("dynamic_port") or {}).get("origin_environment")
    if origin:
        mappings[origin] = "/"
        required.append(origin)
    return {"backend_routes": routes, "environment_routes": mappings,
            "environment_suffixes": suffixes, "required_environment": required}


def _validated_public_environment(raw: object, allowed_names: set[str]) -> dict[str, str]:
    if not isinstance(raw, dict):
        raise LokiRuntimeError("public preview environment must be an object")
    result: dict[str, str] = {}
    for name, value in raw.items():
        if name not in allowed_names or not isinstance(value, str):
            raise LokiRuntimeError("public preview environment is invalid")
        pattern = r"https://loki-[0-9a-f]{32}\.streamliner\.im(?:/[A-Za-z0-9._~!$&'()*+,;=:@%/-]*)?"
        if re.fullmatch(pattern, value) is None:
            raise LokiRuntimeError("public preview environment is invalid")
        result[name] = value
    return result


def _workspace_cwd(relative: str) -> Path:
    root = WORKSPACE_ROOT.resolve()
    target = (root / relative).resolve()
    if target != root and root not in target.parents:
        raise LokiRuntimeError("action cwd escapes workspace")
    if not target.is_dir():
        raise LokiRuntimeError("action cwd is not a directory")
    return target


def _resolved_command(command: list[str], cwd: Path) -> list[str]:
    executable, arguments = command[0], command[1:]
    resolved = EXECUTABLES[executable]
    if executable in {"node", "npm", "pnpm", "just", "actions-up"}:
        version = _node_version(cwd)
        prefix = ["/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", version]
        if executable == "pnpm":
            return [*prefix, "corepack", "pnpm", *arguments]
        return [*prefix, resolved, *arguments]
    return [resolved, *arguments]


def _node_version(cwd: Path) -> str:
    for filename in (".node-version", ".nvmrc"):
        candidate = cwd / filename
        if candidate.is_file() and not candidate.is_symlink():
            value = candidate.read_text(encoding="utf-8").strip()
            if value and len(value) <= 128 and not any(c in value for c in "\x00/\\"):
                return value
    versions = WORKSPACE_ROOT / ".loki/fnm/node-versions"
    installed = []
    if versions.is_dir():
        for candidate in versions.iterdir():
            match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", candidate.name)
            if candidate.is_dir() and match:
                installed.append((tuple(map(int, match.groups())), candidate.name))
    if not installed:
        raise LokiRuntimeError("no FNM-managed Node version is installed")
    return max(installed)[1]


if __name__ == "__main__":
    serve()
