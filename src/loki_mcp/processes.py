from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from threading import Lock, Thread
from typing import Any, BinaryIO
import os
import secrets
import signal
import subprocess
import time

from .config import CommandSpec, ServerConfig
from .policy import PolicyError, WorkspacePolicy


@dataclass
class ManagedProcess:
    session_id: str
    name: str
    process: subprocess.Popen[bytes]
    started_at: float
    max_output_bytes: int
    timeout_seconds: int
    group: str | None = None
    instance_key: str | None = None
    metadata: dict[str, Any] = field(default_factory=dict)
    buffer: bytearray = field(default_factory=bytearray)
    base_offset: int = 0
    completed_at: float | None = None
    timed_out: bool = False
    lock: Lock = field(default_factory=Lock)

    def append(self, chunk: bytes) -> None:
        with self.lock:
            self.buffer.extend(chunk)
            overflow = len(self.buffer) - self.max_output_bytes
            if overflow > 0:
                del self.buffer[:overflow]
                self.base_offset += overflow

    def mark_complete(self) -> None:
        with self.lock:
            self.completed_at = time.time()

    def snapshot(self, offset: int | None, limit: int) -> dict[str, Any]:
        with self.lock:
            requested = self.base_offset if offset is None else max(0, int(offset))
            lost = requested < self.base_offset
            actual = max(requested, self.base_offset)
            index = actual - self.base_offset
            chunk = bytes(self.buffer[index:index + limit])
            next_offset = actual + len(chunk)
            running = self.process.poll() is None
            return {
                "session_id": self.session_id,
                "name": self.name,
                "status": "running" if running else "exited",
                "exit_code": None if running else self.process.returncode,
                "timed_out": self.timed_out,
                "started_at": datetime.fromtimestamp(self.started_at, timezone.utc).isoformat(),
                "output": chunk.decode("utf-8", "replace"),
                "offset": actual,
                "next_offset": next_offset,
                "available_from": self.base_offset,
                "output_lost": lost,
                "has_more": next_offset < self.base_offset + len(self.buffer),
                **self.metadata,
            }


class ProcessManager:
    def __init__(self, config: ServerConfig, policy: WorkspacePolicy) -> None:
        self.config = config
        self.policy = policy
        self._lock = Lock()
        self._processes: dict[str, ManagedProcess] = {}

    def start(self, name: str) -> dict[str, Any]:
        spec = self.config.processes.get(name)
        if spec is None:
            raise PolicyError(f"unknown process: {name}")
        return self._start_spec(name, spec)

    def start_check(self, name: str) -> dict[str, Any]:
        spec = self.config.checks.get(name)
        if spec is None:
            raise PolicyError(f"unknown check: {name}")
        return self._start_spec(name, spec)

    def _start_spec(self, name: str, spec: CommandSpec) -> dict[str, Any]:
        cwd = self.policy.resolve(spec.cwd)
        if not cwd.is_dir():
            raise PolicyError("command cwd must be a directory")

        return self.start_command(
            name=name,
            command=list(spec.command),
            cwd=cwd,
            environment=self._environment(spec),
            timeout_seconds=spec.timeout_seconds,
            max_output_bytes=spec.max_output_bytes,
        )

    def start_command(
        self,
        *,
        name: str,
        command: list[str],
        cwd: Path,
        environment: dict[str, str],
        timeout_seconds: int,
        max_output_bytes: int,
        group: str | None = None,
        max_group_processes: int | None = None,
        instance_key: str | None = None,
        metadata: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        with self._lock:
            self._prune_locked()
            running_items = [
                item for item in self._processes.values() if item.process.poll() is None
            ]
            if instance_key is not None:
                existing = next(
                    (item for item in running_items if item.instance_key == instance_key),
                    None,
                )
                if existing is not None:
                    return {**existing.snapshot(offset=None, limit=0), "reused": True}
            running = len(running_items)
            group_running = sum(item.group == group for item in running_items) if group else 0
            if running >= self.config.max_processes:
                detail = f"total {running}/{self.config.max_processes}"
                if group is not None and max_group_processes is not None:
                    detail += f", profile {group} {group_running}/{max_group_processes}"
                raise PolicyError(f"maximum concurrent process count reached ({detail})")
            if (
                group is not None
                and max_group_processes is not None
                and group_running >= max_group_processes
            ):
                raise PolicyError(
                    "maximum concurrent process count reached for profile "
                    f"{group} ({group_running}/{max_group_processes}; "
                    f"total {running}/{self.config.max_processes})"
                )
            session_id = secrets.token_urlsafe(12)
            process = subprocess.Popen(
                command,
                cwd=cwd,
                env=environment,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                start_new_session=True,
                shell=False,
            )
            managed = ManagedProcess(
                session_id=session_id,
                name=name,
                process=process,
                started_at=time.time(),
                max_output_bytes=max_output_bytes,
                timeout_seconds=timeout_seconds,
                group=group,
                instance_key=instance_key,
                metadata=dict(metadata or {}),
            )
            self._processes[session_id] = managed
            Thread(target=self._drain, args=(managed,), name=f"loki-{session_id}", daemon=True).start()
            Thread(target=self._watchdog, args=(managed,), name=f"loki-timeout-{session_id}", daemon=True).start()
        return managed.snapshot(offset=None, limit=0)

    def usage(self) -> dict[str, Any]:
        with self._lock:
            self._prune_locked()
            running = [
                item for item in self._processes.values() if item.process.poll() is None
            ]
        groups: dict[str, int] = {}
        for item in running:
            if item.group is not None:
                groups[item.group] = groups.get(item.group, 0) + 1
        return {"total": len(running), "groups": dict(sorted(groups.items()))}

    def find_running(self, instance_key: str) -> dict[str, Any] | None:
        with self._lock:
            self._prune_locked()
            existing = next(
                (
                    item for item in self._processes.values()
                    if item.instance_key == instance_key and item.process.poll() is None
                ),
                None,
            )
            if existing is None:
                return None
            return {**existing.snapshot(offset=None, limit=0), "reused": True}

    def read(self, session_id: str, offset: int | None = None, limit: int = 65_536) -> dict[str, Any]:
        managed = self._get(session_id)
        bounded_limit = max(1, min(int(limit), self.config.max_output_bytes))
        return managed.snapshot(offset, bounded_limit)

    def list(self) -> dict[str, Any]:
        with self._lock:
            self._prune_locked()
            processes = list(self._processes.values())
        return {"processes": [item.snapshot(offset=None, limit=0) for item in processes]}

    def stop(self, session_id: str) -> dict[str, Any]:
        managed = self._get(session_id)
        self._terminate_group(managed)
        return managed.snapshot(offset=None, limit=0)

    def close(self) -> None:
        with self._lock:
            processes = list(self._processes.values())
        for managed in processes:
            self._terminate_group(managed, grace_seconds=3)

    @staticmethod
    def _group_exists(process_group: int) -> bool:
        try:
            os.killpg(process_group, 0)
            return True
        except ProcessLookupError:
            return False

    @classmethod
    def _terminate_group(cls, managed: ManagedProcess, grace_seconds: float = 5) -> None:
        process_group = managed.process.pid
        try:
            os.killpg(process_group, signal.SIGTERM)
        except ProcessLookupError:
            return
        deadline = time.monotonic() + grace_seconds
        while cls._group_exists(process_group) and time.monotonic() < deadline:
            time.sleep(0.05)
        if cls._group_exists(process_group):
            try:
                os.killpg(process_group, signal.SIGKILL)
            except ProcessLookupError:
                pass
        if managed.process.poll() is None:
            try:
                managed.process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                pass

    def _get(self, session_id: str) -> ManagedProcess:
        with self._lock:
            self._prune_locked()
            managed = self._processes.get(session_id)
        if managed is None:
            raise PolicyError("unknown process session")
        return managed

    def _prune_locked(self) -> None:
        cutoff = time.time() - self.config.process_retention_seconds
        expired = [
            session_id for session_id, item in self._processes.items()
            if item.completed_at is not None and item.completed_at < cutoff
        ]
        for session_id in expired:
            del self._processes[session_id]

        completed = sorted(
            (item for item in self._processes.values() if item.completed_at is not None),
            key=lambda item: item.completed_at or 0,
        )
        while len(self._processes) > 32 and completed:
            item = completed.pop(0)
            self._processes.pop(item.session_id, None)

    @staticmethod
    def _environment(spec: CommandSpec) -> dict[str, str]:
        environment = {
            "HOME": "/tmp",
            "TMPDIR": "/tmp",
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "PATH": "/opt/loki-mcp/venv/bin:/usr/bin:/bin",
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_OPTIONAL_LOCKS": "0",
        }
        environment.update(spec.environment)
        return environment

    @staticmethod
    def _drain(managed: ManagedProcess) -> None:
        stream: BinaryIO | None = managed.process.stdout
        if stream is None:
            managed.process.wait()
            managed.mark_complete()
            return
        try:
            while True:
                chunk = os.read(stream.fileno(), 65_536)
                if not chunk:
                    break
                managed.append(chunk)
        finally:
            stream.close()
            managed.process.wait()
            managed.mark_complete()

    @staticmethod
    def _watchdog(managed: ManagedProcess) -> None:
        try:
            managed.process.wait(timeout=managed.timeout_seconds)
            return
        except subprocess.TimeoutExpired:
            with managed.lock:
                managed.timed_out = True
        try:
            os.killpg(managed.process.pid, signal.SIGTERM)
            managed.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(managed.process.pid, signal.SIGKILL)
                managed.process.wait(timeout=5)
            except ProcessLookupError:
                return
        except ProcessLookupError:
            return
