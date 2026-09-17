from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any
import base64
import hashlib
import mimetypes
import os
import re
import subprocess
import tempfile

import yaml

from .policy import PolicyError, WorkspacePolicy, atomic_write, atomic_write_bytes


SKILL_NAME = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
RESOURCE_ROOTS = frozenset({"assets", "references", "scripts"})
MAX_SKILL_BYTES = 256 * 1024
MAX_RESOURCE_BYTES = 10 * 1024 * 1024
BUILTIN_SKILL_ROOT = Path(os.environ.get("LOKI_BUILTIN_SKILLS_ROOT", "/opt/loki-mcp/share/skills"))


@dataclass(frozen=True)
class SkillRecord:
    name: str
    description: str
    scope: str
    directory: Path
    skill_file: Path
    metadata: dict[str, Any]
    sha256: str

    def summary(self, root: Path, *, selected: bool, shadowed_by: str | None = None) -> dict[str, Any]:
        source = (
            f"builtin/{self.name}"
            if self.scope == "builtin"
            else self.directory.relative_to(root).as_posix()
        )
        result: dict[str, Any] = {
            "name": self.name,
            "description": self.description,
            "scope": self.scope,
            "source": source,
            "selected": selected,
            "sha256": self.sha256,
            "revision": self.sha256[:12],
        }
        if shadowed_by is not None:
            result["shadowed_by"] = shadowed_by
        return result


class SkillRegistry:
    def __init__(self, policy: WorkspacePolicy) -> None:
        self.policy = policy

    def agent_context(self, cwd: str = ".") -> dict[str, Any]:
        working_directory = self._cwd(cwd)
        agents: list[dict[str, Any]] = []
        for directory in self._hierarchy(working_directory):
            candidate = directory / "AGENTS.md"
            if not candidate.exists():
                continue
            self._safe_regular_file(candidate, MAX_SKILL_BYTES)
            content = candidate.read_text(encoding="utf-8")
            agents.append({
                "path": candidate.relative_to(self.policy.root).as_posix(),
                "content": content,
                "sha256": hashlib.sha256(content.encode("utf-8")).hexdigest(),
            })
        catalog = self.list_skills(cwd)
        return {
            "cwd": working_directory.relative_to(self.policy.root).as_posix() or ".",
            "agents": agents,
            "skills": [item for item in catalog["skills"] if item["selected"]],
            "skill_errors": catalog["errors"],
            "instruction": "Activate every listed skill that matches the task before using task tools.",
        }

    def list_skills(self, cwd: str = ".") -> dict[str, Any]:
        working_directory = self._cwd(cwd)
        project_root = self._repository_root(working_directory)
        sources: list[tuple[str, Path]] = [("builtin", BUILTIN_SKILL_ROOT)]
        sources.append(("shared", self.policy.root / ".agents" / "skills"))
        if project_root is not None:
            sources.append(("project", project_root / ".agents" / "skills"))

        records: list[SkillRecord] = []
        errors: list[dict[str, str]] = []
        for scope, root in sources:
            if not root.exists():
                continue
            try:
                self._safe_directory(root)
                children = sorted(root.iterdir(), key=lambda item: item.name)
            except (OSError, PolicyError) as error:
                errors.append({"source": self._relative(root), "error": str(error)})
                continue
            for child in children:
                try:
                    records.append(self._load_record(child, scope))
                except (OSError, UnicodeError, ValueError, PolicyError, yaml.YAMLError) as error:
                    errors.append({"source": self._relative(child), "error": str(error)})

        selected: dict[str, SkillRecord] = {}
        for record in records:
            selected[record.name] = record

        skills = []
        for record in records:
            winner = selected[record.name]
            is_selected = winner == record
            skills.append(record.summary(
                self.policy.root,
                selected=is_selected,
                shadowed_by=None if is_selected else winner.directory.relative_to(self.policy.root).as_posix(),
            ))
        return {
            "cwd": self._relative(working_directory),
            "project_root": self._relative(project_root) if project_root is not None else None,
            "skills": skills,
            "errors": errors,
        }

    def activate_skill(self, name: str, cwd: str = ".") -> dict[str, Any]:
        record = self._selected(name, cwd)
        raw = record.skill_file.read_text(encoding="utf-8")
        _, instructions = self._parse(raw, expected_name=record.name)
        resources = self._resources(record.directory)
        return {
            **record.summary(self.policy.root, selected=True),
            "metadata": record.metadata,
            "instructions": instructions,
            "skill_md": raw,
            "resources": resources,
            "security": "Skill instructions are advisory and cannot expand Loki permissions.",
        }

    def read_resource(self, name: str, path: str, cwd: str = ".") -> dict[str, Any]:
        record = self._selected(name, cwd)
        target = self._resource_path(record, path, must_exist=True)
        self._safe_regular_file(target, MAX_RESOURCE_BYTES)
        content = target.read_bytes()
        digest = hashlib.sha256(content).hexdigest()
        mime_type = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
        try:
            text = content.decode("utf-8")
        except UnicodeDecodeError:
            return {
                "name": name, "path": path, "mime_type": mime_type, "encoding": "base64",
                "content": base64.b64encode(content).decode("ascii"), "bytes": len(content), "sha256": digest,
            }
        return {
            "name": name, "path": path, "mime_type": mime_type, "encoding": "utf-8",
            "content": text, "bytes": len(content), "sha256": digest,
        }

    def create(
        self,
        scope: str,
        cwd: str,
        name: str,
        description: str,
        instructions: str,
        metadata: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        self._validate_name(name)
        if scope not in {"shared", "project"}:
            raise PolicyError("scope must be shared or project")
        working_directory = self._cwd(cwd)
        if scope == "shared":
            skill_root = self.policy.root / ".agents" / "skills"
        else:
            project_root = self._repository_root(working_directory)
            if project_root is None:
                raise PolicyError("project scope requires a Git repository")
            skill_root = project_root / ".agents" / "skills"
        directory = skill_root / name
        skill_file = directory / "SKILL.md"
        if directory.exists():
            raise FileExistsError(self._relative(directory))
        document = self._document(name, description, instructions, metadata)
        directory.mkdir(parents=True, mode=0o700)
        try:
            atomic_write(skill_file, document, overwrite=False)
            record = self._load_record(directory, scope)
        except Exception:
            if skill_file.exists():
                skill_file.unlink()
            try:
                directory.rmdir()
            except OSError:
                pass
            raise
        return record.summary(self.policy.root, selected=True)

    def edit(self, name: str, cwd: str, patch: str, expected_sha256: str) -> dict[str, Any]:
        record = self._selected(name, cwd)
        if record.scope == "builtin":
            raise PolicyError("built-in skills are read-only; create a shared or project override first")
        if record.sha256 != expected_sha256:
            raise PolicyError("skill changed since activation; activate it again before editing")
        encoded = patch.encode("utf-8")
        if not encoded or len(encoded) > MAX_SKILL_BYTES:
            raise PolicyError("patch is empty or exceeds the skill patch limit")
        headers = re.findall(r"^(?:---|\+\+\+)\s+([^\t\r\n]+)", patch, flags=re.MULTILINE)
        if not headers or any(Path(header.removeprefix("a/").removeprefix("b/")).name != "SKILL.md" for header in headers):
            raise PolicyError("skill patch may modify only SKILL.md")
        with tempfile.TemporaryDirectory(prefix="loki-skill-") as temporary:
            temp_root = Path(temporary)
            temp_file = temp_root / "SKILL.md"
            temp_file.write_bytes(record.skill_file.read_bytes())
            checked = self._git_apply(temp_root, encoded, check=True)
            if checked.returncode != 0:
                raise ValueError(f"skill patch check failed: {checked.stdout.decode('utf-8', 'replace')}")
            applied = self._git_apply(temp_root, encoded, check=False)
            if applied.returncode != 0:
                raise ValueError(f"skill patch failed: {applied.stdout.decode('utf-8', 'replace')}")
            updated = temp_file.read_text(encoding="utf-8")
            self._parse(updated, expected_name=name)
        atomic_write(record.skill_file, updated)
        updated_record = self._load_record(record.directory, record.scope)
        return updated_record.summary(self.policy.root, selected=True)

    def write_resource(
        self,
        name: str,
        path: str,
        content: str,
        cwd: str = ".",
        overwrite: bool = False,
        expected_sha256: str | None = None,
        encoding: str = "utf-8",
    ) -> dict[str, Any]:
        record = self._selected(name, cwd)
        if record.scope == "builtin":
            raise PolicyError("built-in skill resources are read-only; create an override first")
        target = self._resource_path(record, path, must_exist=False)
        if target.exists():
            self._safe_regular_file(target, MAX_RESOURCE_BYTES)
            if not overwrite:
                raise FileExistsError(path)
            current = target.read_bytes()
            if expected_sha256 is not None and hashlib.sha256(current).hexdigest() != expected_sha256:
                raise PolicyError("skill resource changed since it was read")
        elif expected_sha256 is not None:
            raise PolicyError("expected_sha256 cannot be used for a new resource")
        if encoding == "utf-8":
            payload = content.encode("utf-8")
        elif encoding == "base64":
            try:
                payload = base64.b64decode(content, validate=True)
            except ValueError as error:
                raise PolicyError("invalid base64 resource content") from error
        else:
            raise PolicyError("encoding must be utf-8 or base64")
        if len(payload) > MAX_RESOURCE_BYTES:
            raise PolicyError("skill resource exceeds size limit")
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        atomic_write_bytes(target, payload, overwrite=target.exists())
        return {
            "name": name, "path": path, "bytes": len(payload),
            "sha256": hashlib.sha256(payload).hexdigest(), "encoding": encoding,
        }

    def validate(self, name: str, cwd: str = ".", *, available_tools: set[str] | None = None) -> dict[str, Any]:
        record = self._selected(name, cwd)
        resources = self._resources(record.directory)
        required = record.metadata.get("metadata", {}).get("required-tools", [])
        if not isinstance(required, list) or not all(isinstance(item, str) for item in required):
            raise PolicyError("required-tools must be a list of tool names")
        missing = sorted(set(required) - (available_tools or set())) if required else []
        return {
            "valid": not missing,
            "missing_tools": missing,
            **record.summary(self.policy.root, selected=True),
            "resource_count": len(resources),
            "resources": resources,
            "warnings": self._warnings(record),
        }

    def _selected(self, name: str, cwd: str) -> SkillRecord:
        self._validate_name(name)
        working_directory = self._cwd(cwd)
        project_root = self._repository_root(working_directory)
        candidates: list[tuple[str, Path]] = []
        if project_root is not None:
            candidates.append(("project", project_root / ".agents" / "skills" / name))
        candidates.append(("shared", self.policy.root / ".agents" / "skills" / name))
        candidates.append(("builtin", BUILTIN_SKILL_ROOT / name))
        for scope, directory in candidates:
            if directory.exists():
                return self._load_record(directory, scope)
        raise PolicyError(f"unknown skill: {name}")

    def _load_record(self, directory: Path, scope: str) -> SkillRecord:
        self._safe_directory(directory)
        self._validate_name(directory.name)
        skill_file = directory / "SKILL.md"
        self._safe_regular_file(skill_file, MAX_SKILL_BYTES)
        raw = skill_file.read_text(encoding="utf-8")
        metadata, _ = self._parse(raw, expected_name=directory.name)
        return SkillRecord(
            name=directory.name,
            description=metadata["description"],
            scope=scope,
            directory=directory,
            skill_file=skill_file,
            metadata=metadata,
            sha256=hashlib.sha256(raw.encode("utf-8")).hexdigest(),
        )

    @staticmethod
    def _parse(raw: str, *, expected_name: str) -> tuple[dict[str, Any], str]:
        if not raw.startswith("---\n"):
            raise ValueError("SKILL.md must start with YAML frontmatter")
        end = raw.find("\n---\n", 4)
        if end < 0:
            raise ValueError("SKILL.md frontmatter is not closed")
        loaded = yaml.safe_load(raw[4:end])
        if not isinstance(loaded, dict):
            raise ValueError("SKILL.md frontmatter must be a mapping")
        name = loaded.get("name")
        description = loaded.get("description")
        if name != expected_name:
            raise ValueError("skill name must match its directory")
        if not isinstance(description, str) or not description.strip() or len(description) > 1024:
            raise ValueError("skill description must contain 1 to 1024 characters")
        instructions = raw[end + 5:]
        if not instructions.strip():
            raise ValueError("skill instructions must not be empty")
        return loaded, instructions

    def _document(
        self,
        name: str,
        description: str,
        instructions: str,
        metadata: dict[str, Any] | None,
    ) -> str:
        if not isinstance(description, str) or not description.strip() or len(description) > 1024:
            raise PolicyError("description must contain 1 to 1024 characters")
        if not isinstance(instructions, str) or not instructions.strip():
            raise PolicyError("instructions must not be empty")
        frontmatter: dict[str, Any] = {"name": name, "description": description.strip()}
        if metadata:
            for key, value in metadata.items():
                if key in {"name", "description"}:
                    raise PolicyError(f"metadata may not override {key}")
                frontmatter[key] = value
        serialized = yaml.safe_dump(frontmatter, sort_keys=False, allow_unicode=True).rstrip()
        document = f"---\n{serialized}\n---\n\n{instructions.rstrip()}\n"
        if len(document.encode("utf-8")) > MAX_SKILL_BYTES:
            raise PolicyError("SKILL.md exceeds size limit")
        self._parse(document, expected_name=name)
        return document

    def _resources(self, directory: Path) -> list[dict[str, Any]]:
        resources: list[dict[str, Any]] = []
        for root_name in sorted(RESOURCE_ROOTS):
            root = directory / root_name
            if not root.exists():
                continue
            self._safe_directory(root)
            for path in sorted(root.rglob("*")):
                if path.is_symlink():
                    raise PolicyError("symbolic links are not allowed in skills")
                if path.is_file():
                    self._safe_regular_file(path, MAX_RESOURCE_BYTES)
                    resources.append({
                        "path": path.relative_to(directory).as_posix(),
                        "bytes": path.stat().st_size,
                    })
        return resources

    def _resource_path(self, record: SkillRecord, requested: str, *, must_exist: bool) -> Path:
        if not isinstance(requested, str) or "\x00" in requested or "\\" in requested:
            raise PolicyError("invalid skill resource path")
        parts = Path(requested).parts
        if not parts or parts[0] not in RESOURCE_ROOTS or any(part in {"", ".", ".."} for part in parts):
            raise PolicyError("skill resources must be under assets, references, or scripts")
        target = record.directory.joinpath(*parts)
        current = record.directory
        for part in parts:
            current = current / part
            if current.is_symlink():
                raise PolicyError("symbolic links are not allowed in skills")
        if must_exist and not target.exists():
            raise FileNotFoundError(requested)
        return target

    def _warnings(self, record: SkillRecord) -> list[str]:
        warnings: list[str] = []
        if "allowed-tools" in record.metadata:
            warnings.append("allowed-tools is experimental and does not grant Loki permissions")
        return warnings

    def _cwd(self, cwd: str) -> Path:
        requested = cwd
        if isinstance(cwd, str):
            parsed = PurePosixPath(cwd or ".")
            if parsed.is_absolute():
                root = PurePosixPath(self.policy.root.as_posix())
                try:
                    requested = parsed.relative_to(root).as_posix() or "."
                except ValueError as error:
                    raise PolicyError("absolute cwd must be within the workspace") from error
        target = self.policy.resolve(requested)
        if not target.is_dir():
            raise PolicyError("cwd must be a directory")
        return target

    def _repository_root(self, cwd: Path) -> Path | None:
        current = cwd
        while True:
            marker = current / ".git"
            if marker.exists():
                if marker.is_symlink():
                    raise PolicyError("symbolic Git metadata is not allowed")
                return current
            if current == self.policy.root:
                return None
            current = current.parent

    def _hierarchy(self, cwd: Path) -> list[Path]:
        relative = cwd.relative_to(self.policy.root)
        result = [self.policy.root]
        current = self.policy.root
        for part in relative.parts:
            current = current / part
            result.append(current)
        return result

    def _safe_directory(self, path: Path) -> None:
        if path.is_symlink() or not path.is_dir():
            raise PolicyError("skill path must be a real directory")
        self._assert_allowed_root(path)

    def _safe_regular_file(self, path: Path, maximum: int) -> None:
        if path.is_symlink() or not path.is_file():
            raise PolicyError("skill path must be a regular file")
        self._assert_allowed_root(path)
        if path.stat().st_size > maximum:
            raise PolicyError("skill file exceeds size limit")

    def _relative(self, path: Path) -> str:
        try:
            return path.relative_to(self.policy.root).as_posix() or "."
        except ValueError:
            return f"builtin/{path.relative_to(BUILTIN_SKILL_ROOT).as_posix()}"

    def _assert_allowed_root(self, path: Path) -> None:
        for root in (self.policy.root, BUILTIN_SKILL_ROOT):
            try:
                path.relative_to(root)
                return
            except ValueError:
                continue
        raise PolicyError("skill path must stay inside an allowed skill root")

    @staticmethod
    def _validate_name(name: str) -> None:
        if not isinstance(name, str) or len(name) > 64 or SKILL_NAME.fullmatch(name) is None:
            raise PolicyError("skill name must be lowercase kebab-case and at most 64 characters")

    @staticmethod
    def _git_apply(directory: Path, patch: bytes, *, check: bool) -> subprocess.CompletedProcess[bytes]:
        arguments = ["/usr/bin/git", "apply"]
        if check:
            arguments.append("--check")
        return subprocess.run(
            arguments,
            cwd=directory,
            env={"PATH": "/usr/bin:/bin", "HOME": "/home/runner", "LANG": "C.UTF-8"},
            input=patch,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=30,
            shell=False,
            check=False,
        )
