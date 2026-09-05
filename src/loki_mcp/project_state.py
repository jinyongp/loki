from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from threading import RLock
from typing import Any
import hashlib
import json
import os
import pwd
import grp
import re
import shutil
import subprocess
import tempfile
import unicodedata

from .policy import PolicyError


STATE_ROOT = Path(os.environ.get("LOKI_PROJECT_STATE", "/var/lib/loki/project-state"))
WORKSPACE_ROOT = Path(os.environ.get("LOKI_PROJECT_WORKSPACE", "/workspace"))
HOST_WORKSPACE_ROOT = Path(
    os.environ.get("LOKI_PROJECT_HOST_WORKSPACE", "/srv/workspace/loki")
)
WORKSTREAM_PATTERN = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+){2,}$")
ARTIFACTS = frozenset({"spec.md", "plan.md", "validation.md"})
MAX_ARTIFACT_BYTES = 2_097_152


def resolve_workspace_git_path(value: str, workspace_root: Path) -> Path:
    """Map Git's sandbox-visible absolute paths into the caller's workspace view.

    The runtime mounts the same tree read-only at /workspace so Git can follow
    linked-worktree metadata written inside the MCP sandbox. Bind mounts do not
    canonicalize through Path.resolve(), so normalize the prefix explicitly.
    Resolve symlinks afterwards; callers still enforce workspace containment.
    """
    path = Path(value)
    logical_root = Path("/workspace")
    if path.is_relative_to(logical_root):
        path = workspace_root / path.relative_to(logical_root)
    return path.resolve()


@dataclass(frozen=True)
class ProjectIdentity:
    project_id: str
    repository_root: Path
    worktree_root: Path
    logical_common_directory: str
    state_directory: Path
    worktree_id: str


class ProjectStateStore:
    def __init__(self, workspace_root: Path = WORKSPACE_ROOT, state_root: Path = STATE_ROOT) -> None:
        self.workspace_root = workspace_root.resolve()
        self.state_root = state_root
        self._lock = RLock()

    def status(self, cwd: Path) -> dict[str, Any]:
        identity = self.resolve(cwd)
        active = self._active_workstream(identity)
        return {
            **self._identity_metadata(identity),
            "initialized": identity.state_directory.is_dir(),
            "active_workstream": active,
            "task_store": "central",
        }

    def list_workstreams(self, cwd: Path) -> dict[str, Any]:
        identity = self.resolve(cwd)
        root = identity.state_directory / "workstreams"
        items = []
        if root.is_dir():
            for directory in sorted(root.iterdir(), key=lambda item: item.name):
                manifest = directory / "manifest.json"
                if not directory.is_dir() or directory.is_symlink() or not manifest.is_file():
                    continue
                try:
                    value = json.loads(manifest.read_text(encoding="utf-8"))
                except (OSError, json.JSONDecodeError):
                    continue
                items.append({
                    "slug": directory.name,
                    "goal": value.get("goal"),
                    "depth": value.get("depth"),
                    "active": directory.name == self._active_workstream(identity),
                })
        return {**self._identity_metadata(identity), "workstreams": items}

    def initialize_workstream(
        self,
        cwd: Path,
        *,
        goal: str,
        slug_base: str | None,
        slug: str | None,
        depth: str,
        intent_source_kind: str,
        intent_source: str | None,
    ) -> dict[str, Any]:
        identity = self.resolve(cwd)
        clean_goal = " ".join(unicodedata.normalize("NFKC", goal).split())
        if not clean_goal or len(clean_goal.encode("utf-8")) > 4_096:
            raise PolicyError("workstream goal is empty or too large")
        if depth not in {"light", "standard", "high-risk"}:
            raise PolicyError("workstream depth must be light, standard, or high-risk")
        if intent_source_kind not in {"plan-local", "authoritative"}:
            raise PolicyError("intent source kind must be plan-local or authoritative")
        if intent_source_kind == "authoritative" and not intent_source:
            raise PolicyError("authoritative intent requires an exact source")
        if intent_source_kind == "plan-local" and intent_source is not None:
            raise PolicyError("plan-local intent does not accept an external source")
        goal_hash = hashlib.sha256(clean_goal.casefold().encode("utf-8")).hexdigest()
        resolved_slug = self._slug(slug, slug_base, goal_hash)
        manifest_value = {
            "schema": 1,
            "slug": resolved_slug,
            "goal": clean_goal,
            "goal_hash": goal_hash,
            "depth": depth,
            "intent_source_kind": intent_source_kind,
            "intent_source": intent_source,
            "task_project": resolved_slug,
        }
        with self._lock:
            self._ensure_project(identity)
            directory = identity.state_directory / "workstreams" / resolved_slug
            manifest = directory / "manifest.json"
            created = not directory.exists()
            if created:
                directory.mkdir(mode=0o700)
                self._write_json(manifest, manifest_value)
            else:
                if directory.is_symlink() or not manifest.is_file():
                    raise PolicyError("workstream directory is unmanaged")
                try:
                    existing = json.loads(manifest.read_text(encoding="utf-8"))
                except json.JSONDecodeError as error:
                    raise PolicyError("workstream manifest is invalid") from error
                if existing != manifest_value:
                    raise PolicyError("workstream identity does not match the existing manifest")
            self._write_binding(identity, resolved_slug)
        return {
            **self._identity_metadata(identity),
            "slug": resolved_slug,
            "created": created,
            "active": True,
            "artifacts": sorted(ARTIFACTS),
        }

    def bind_workstream(self, cwd: Path, slug: str) -> dict[str, Any]:
        identity = self.resolve(cwd)
        directory = self._workstream_directory(identity, slug)
        if not (directory / "manifest.json").is_file():
            raise PolicyError("unknown workstream")
        with self._lock:
            self._write_binding(identity, slug)
        return {**self._identity_metadata(identity), "slug": slug, "active": True}

    def read_artifact(
        self, cwd: Path, slug: str | None, filename: str,
    ) -> dict[str, Any]:
        identity = self.resolve(cwd)
        path, resolved_slug = self._artifact_path(identity, slug, filename)
        if not path.is_file() or path.is_symlink():
            raise PolicyError("workstream artifact does not exist")
        content = path.read_text(encoding="utf-8")
        return {
            **self._identity_metadata(identity),
            "slug": resolved_slug,
            "filename": filename,
            "content": content,
            "sha256": hashlib.sha256(content.encode("utf-8")).hexdigest(),
        }

    def write_artifact(
        self,
        cwd: Path,
        slug: str | None,
        filename: str,
        content: str,
        expected_sha256: str | None,
    ) -> dict[str, Any]:
        encoded = content.encode("utf-8")
        if len(encoded) > MAX_ARTIFACT_BYTES:
            raise PolicyError("workstream artifact is too large")
        identity = self.resolve(cwd)
        path, resolved_slug = self._artifact_path(identity, slug, filename)
        with self._lock:
            if path.exists():
                if path.is_symlink() or not path.is_file():
                    raise PolicyError("workstream artifact path is unsafe")
                current = hashlib.sha256(path.read_bytes()).hexdigest()
                if expected_sha256 is None:
                    raise PolicyError("expected_sha256 is required when updating an artifact")
                if current != expected_sha256:
                    raise PolicyError("workstream artifact changed since it was read")
            elif expected_sha256 is not None:
                raise PolicyError("workstream artifact does not exist for the expected revision")
            self._atomic_write(path, encoded)
        return {
            **self._identity_metadata(identity),
            "slug": resolved_slug,
            "filename": filename,
            "sha256": hashlib.sha256(encoded).hexdigest(),
            "created": expected_sha256 is None,
        }

    def migrate_legacy(self, cwd: Path) -> dict[str, Any]:
        identity = self.resolve(cwd)
        source = identity.worktree_root / ".tasks"
        if not source.is_dir() or source.is_symlink():
            raise PolicyError("legacy .tasks directory does not exist or is unsafe")
        if any(path.is_symlink() for path in source.rglob("*")):
            raise PolicyError("legacy .tasks directory contains a symbolic link")
        destination = identity.state_directory
        if destination.exists():
            raise PolicyError("central project state already exists")
        self._ensure_state_parent(destination.parent)
        staging = Path(tempfile.mkdtemp(prefix=f".{identity.project_id}.", dir=destination.parent))
        try:
            (staging / "taskwarrior").mkdir(mode=0o700)
            source_data = source / "data"
            if source_data.is_dir():
                shutil.copytree(source_data, staging / "taskwarrior" / "data")
            else:
                (staging / "taskwarrior" / "data").mkdir(mode=0o700)
            self._atomic_write(
                staging / "taskwarrior" / "taskrc",
                (
                    f"data.location={destination}/taskwarrior/data\n"
                    "confirmation=1\n"
                    "hooks=0\n"
                ).encode("utf-8"),
            )
            source_items = source / "items"
            if source_items.is_dir():
                shutil.copytree(source_items, staging / "workstreams")
            else:
                (staging / "workstreams").mkdir(mode=0o700)
            (staging / "bindings").mkdir(mode=0o700)
            migrated_workstreams = [
                path for path in (staging / "workstreams").iterdir()
                if path.is_dir() and not path.is_symlink()
            ]
            if len(migrated_workstreams) == 1:
                self._write_json(
                    staging / "bindings" / f"{identity.worktree_id}.json",
                    {"slug": migrated_workstreams[0].name},
                )
            self._write_json(staging / "project.json", self._identity_metadata(identity))
            copied = self._tree_digest(staging / "taskwarrior" / "data")
            original = self._tree_digest(source_data) if source_data.is_dir() else copied
            if copied != original:
                raise PolicyError("Taskwarrior migration verification failed")
            if source_items.is_dir() and self._tree_digest(
                staging / "workstreams"
            ) != self._tree_digest(source_items):
                raise PolicyError("workstream artifact migration verification failed")
            if os.geteuid() == 0:
                uid = pwd.getpwnam("runner").pw_uid
                gid = grp.getgrnam("workspace").gr_gid
                for path in [staging / "taskwarrior", *(staging / "taskwarrior").rglob("*")]:
                    os.chown(path, uid, gid, follow_symlinks=False)
                    os.chmod(path, 0o700 if path.is_dir() else 0o600)
                os.chown(staging / "taskwarrior", 0, gid, follow_symlinks=False)
                os.chmod(staging / "taskwarrior", 0o710)
                os.chown(staging / "taskwarrior" / "taskrc", 0, gid, follow_symlinks=False)
                os.chmod(staging / "taskwarrior" / "taskrc", 0o640)
                os.chown(staging, 0, gid, follow_symlinks=False)
                os.chmod(staging, 0o710)
            os.replace(staging, destination)
            if source_data.is_dir():
                shutil.rmtree(source_data)
            if source_items.is_dir():
                shutil.rmtree(source_items)
            (source / "taskrc").unlink(missing_ok=True)
            try:
                source.rmdir()
                legacy_removed = True
                retained = []
            except OSError:
                legacy_removed = False
                retained = sorted(path.name for path in source.iterdir())
        except Exception:
            if staging.exists():
                shutil.rmtree(staging)
            raise
        return {
            **self._identity_metadata(identity),
            "migrated": True,
            "legacy_removed": legacy_removed,
            "worktree_local_entries_retained": retained,
            "workstreams": len(list((destination / "workstreams").iterdir())),
        }

    def resolve(self, cwd: Path) -> ProjectIdentity:
        candidate = cwd.resolve()
        if candidate != self.workspace_root and self.workspace_root not in candidate.parents:
            raise PolicyError("project cwd escapes workspace")
        worktree = self._git_path(candidate, "--show-toplevel")
        common = self._git_path(candidate, "--path-format=absolute", "--git-common-dir")
        if worktree != self.workspace_root and self.workspace_root not in worktree.parents:
            raise PolicyError("project worktree escapes workspace")
        if common != self.workspace_root and self.workspace_root not in common.parents:
            raise PolicyError("project Git metadata escapes workspace")
        relative_common = common.relative_to(self.workspace_root)
        logical_common = str(PurePosixPath("/workspace", *relative_common.parts))
        project_id = hashlib.sha256(logical_common.encode("utf-8")).hexdigest()[:32]
        relative_worktree = worktree.relative_to(self.workspace_root).as_posix()
        worktree_id = hashlib.sha256(relative_worktree.encode("utf-8")).hexdigest()[:24]
        repository_root = common.parent if common.name == ".git" else worktree
        return ProjectIdentity(
            project_id=project_id,
            repository_root=repository_root,
            worktree_root=worktree,
            logical_common_directory=logical_common,
            state_directory=self.state_root / "projects" / project_id,
            worktree_id=worktree_id,
        )

    def _ensure_project(self, identity: ProjectIdentity) -> None:
        root = identity.state_directory
        if root.is_symlink():
            raise PolicyError("central project state path is unsafe")
        self._ensure_state_parent(root.parent)
        root.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(root, 0o730)
        for relative in ("taskwarrior", "workstreams", "bindings"):
            path = root / relative
            if path.is_symlink():
                raise PolicyError("central project state path is unsafe")
            path.mkdir(parents=True, exist_ok=True, mode=0o700)
        taskwarrior = root / "taskwarrior"
        os.chmod(taskwarrior, 0o730)
        taskdata = taskwarrior / "data"
        if not taskdata.exists() and self._is_managed_runtime():
            completed = subprocess.run(
                [
                    "/usr/sbin/runuser", "-u", "runner", "--",
                    "/usr/bin/mkdir", "-m", "0700", str(taskdata),
                ],
                env={"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"},
                capture_output=True, check=False,
            )
            if completed.returncode != 0:
                raise PolicyError("could not initialize the Taskwarrior data store")
        else:
            taskdata.mkdir(parents=True, exist_ok=True, mode=0o700)
        taskrc = root / "taskwarrior" / "taskrc"
        if not taskrc.exists():
            self._atomic_write(
                taskrc,
                (
                    f"data.location={root}/taskwarrior/data\n"
                    "confirmation=1\n"
                    "hooks=0\n"
                ).encode(),
            )
        os.chmod(taskrc, 0o640)
        project = root / "project.json"
        if not project.exists():
            self._write_json(project, self._identity_metadata(identity))
        if os.geteuid() == 0 and not self._is_managed_runtime():
            try:
                uid = pwd.getpwnam("runner").pw_uid
                gid = grp.getgrnam("workspace").gr_gid
            except KeyError:
                return
            os.chown(root, 0, gid, follow_symlinks=False)
            os.chmod(root, 0o710)
            for path in (
                root / "taskwarrior",
                root / "taskwarrior" / "data",
                root / "taskwarrior" / "taskrc",
            ):
                os.chown(path, uid, gid, follow_symlinks=False)
                os.chmod(path, 0o700 if path.is_dir() else 0o640)
            os.chown(taskwarrior, 0, gid, follow_symlinks=False)
            os.chmod(taskwarrior, 0o710)
            os.chown(taskrc, 0, gid, follow_symlinks=False)

    def _is_managed_runtime(self) -> bool:
        return (
            self.workspace_root in {
                WORKSPACE_ROOT.resolve(), HOST_WORKSPACE_ROOT.resolve(),
            }
            and self.state_root == STATE_ROOT
        )

    @staticmethod
    def _ensure_state_parent(parent: Path) -> None:
        parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        if parent.is_symlink():
            raise PolicyError("central project state root is unsafe")
        if os.geteuid() == 0:
            try:
                gid = grp.getgrnam("workspace").gr_gid
            except KeyError:
                return
            for path in (parent.parent, parent):
                try:
                    os.chown(path, 0, gid, follow_symlinks=False)
                except PermissionError:
                    metadata = path.stat()
                    if metadata.st_uid != 0 or metadata.st_gid != gid:
                        raise
                os.chmod(path, 0o2710)

    def _identity_metadata(self, identity: ProjectIdentity) -> dict[str, Any]:
        return {
            "project_id": identity.project_id,
            "repository": identity.repository_root.relative_to(self.workspace_root).as_posix(),
            "worktree": identity.worktree_root.relative_to(self.workspace_root).as_posix(),
            "worktree_id": identity.worktree_id,
        }

    def _active_workstream(self, identity: ProjectIdentity) -> str | None:
        binding = identity.state_directory / "bindings" / f"{identity.worktree_id}.json"
        try:
            value = json.loads(binding.read_text(encoding="utf-8"))
        except (FileNotFoundError, json.JSONDecodeError):
            return None
        slug = value.get("slug")
        return slug if isinstance(slug, str) else None

    def _write_binding(self, identity: ProjectIdentity, slug: str) -> None:
        self._ensure_project(identity)
        self._write_json(
            identity.state_directory / "bindings" / f"{identity.worktree_id}.json",
            {"slug": slug},
        )

    def _workstream_directory(self, identity: ProjectIdentity, slug: str) -> Path:
        if WORKSTREAM_PATTERN.fullmatch(slug) is None or len(slug) > 63:
            raise PolicyError("invalid workstream slug")
        return identity.state_directory / "workstreams" / slug

    def _artifact_path(
        self, identity: ProjectIdentity, slug: str | None, filename: str,
    ) -> tuple[Path, str]:
        if filename not in ARTIFACTS:
            raise PolicyError("workstream artifact must be spec.md, plan.md, or validation.md")
        resolved_slug = slug or self._active_workstream(identity)
        if resolved_slug is None:
            raise PolicyError("worktree has no active workstream")
        directory = self._workstream_directory(identity, resolved_slug)
        if not (directory / "manifest.json").is_file():
            raise PolicyError("unknown workstream")
        return directory / filename, resolved_slug

    @staticmethod
    def _slug(slug: str | None, slug_base: str | None, goal_hash: str) -> str:
        if slug is not None:
            if WORKSTREAM_PATTERN.fullmatch(slug) is None or len(slug) > 63:
                raise PolicyError("explicit workstream slug is invalid")
            return slug
        if slug_base is None or WORKSTREAM_PATTERN.fullmatch(slug_base) is None:
            raise PolicyError("slug_base must contain at least three lowercase words")
        value = f"{slug_base}-{goal_hash[:10]}"
        if len(value) > 63:
            raise PolicyError("workstream slug exceeds 63 characters")
        return value

    def _git_path(self, cwd: Path, *arguments: str) -> Path:
        # The runtime deliberately lacks DAC override. Linked-worktree metadata
        # created with the MCP's private umask is readable only by runner.
        prefix = (
            ["/usr/sbin/runuser", "-u", "runner", "--"]
            if os.geteuid() == 0 and self._is_managed_runtime() else []
        )
        completed = subprocess.run(
            [
                *prefix,
                "/usr/bin/git", "-c", "safe.directory=*", "-C", str(cwd),
                "rev-parse", *arguments,
            ],
            env={"PATH": "/usr/bin:/bin", "HOME": "/tmp", "LANG": "C.UTF-8"},
            capture_output=True, text=True, encoding="utf-8", errors="replace",
            timeout=10, check=False,
        )
        if completed.returncode != 0:
            raise PolicyError("project state requires a Git worktree")
        return resolve_workspace_git_path(completed.stdout.strip(), self.workspace_root)

    @staticmethod
    def _atomic_write(path: Path, content: bytes) -> None:
        descriptor, name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
        temporary = Path(name)
        try:
            with os.fdopen(descriptor, "wb") as handle:
                handle.write(content)
                handle.flush()
                os.fsync(handle.fileno())
            os.chmod(temporary, 0o600)
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)

    def _write_json(self, path: Path, value: dict[str, Any]) -> None:
        self._atomic_write(
            path,
            (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode(),
        )

    @staticmethod
    def _tree_digest(root: Path) -> str:
        digest = hashlib.sha256()
        if not root.exists():
            return digest.hexdigest()
        for path in sorted(item for item in root.rglob("*") if item.is_file()):
            digest.update(path.relative_to(root).as_posix().encode())
            digest.update(b"\0")
            digest.update(path.read_bytes())
            digest.update(b"\0")
        return digest.hexdigest()
