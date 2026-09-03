from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path, PurePosixPath
import os
import stat

from mcp.server.mcpserver.exceptions import ToolError


class PolicyError(ToolError, ValueError):
    """Raised when a requested workspace operation violates policy."""


DENIED_COMPONENTS = frozenset({".git", ".ssh", ".gnupg"})
DENIED_FILENAMES = frozenset({
    ".env",
    ".env.local",
    ".env.production",
    ".netrc",
    ".npmrc",
    ".pypirc",
})
DENIED_SUFFIXES = frozenset({".key", ".pem", ".p12", ".pfx"})


@dataclass(frozen=True)
class WorkspacePolicy:
    root: Path

    def __post_init__(self) -> None:
        root = self.root.resolve(strict=True)
        if not root.is_dir():
            raise PolicyError("workspace root is not a directory")
        object.__setattr__(self, "root", root)

    def resolve(self, requested: str, *, must_exist: bool = True) -> Path:
        if not isinstance(requested, str) or "\x00" in requested or "\\" in requested:
            raise PolicyError("invalid path")

        parsed = PurePosixPath(requested or ".")
        if parsed.is_absolute():
            raise PolicyError("absolute paths are not allowed")

        parts = tuple(part for part in parsed.parts if part not in {"", "."})
        if any(part == ".." for part in parts):
            raise PolicyError("parent traversal is not allowed")
        self._check_denied(parts)

        current = self.root
        for part in parts:
            current = current / part
            if current.is_symlink():
                raise PolicyError("symbolic links are not allowed")
            if current.exists():
                mode = current.lstat().st_mode
                if not (stat.S_ISREG(mode) or stat.S_ISDIR(mode)):
                    raise PolicyError("special files are not allowed")

        if must_exist and not current.exists():
            raise FileNotFoundError(requested)
        if not must_exist:
            self._validate_existing_parent(current.parent)
        return current

    def resolve_cwd(self, requested: str) -> Path:
        """Resolve a relative or sandbox-visible /workspace working directory."""
        if isinstance(requested, str) and "\x00" not in requested and "\\" not in requested:
            parsed = PurePosixPath(requested or ".")
            if parsed.is_absolute():
                try:
                    requested = parsed.relative_to(PurePosixPath("/workspace")).as_posix() or "."
                except ValueError as error:
                    raise PolicyError("absolute cwd must be within /workspace") from error
        return self.resolve(requested)

    def relative(self, path: Path) -> str:
        return path.relative_to(self.root).as_posix() or "."

    def allowed_entry(self, path: Path) -> bool:
        try:
            relative = path.relative_to(self.root)
            self._check_denied(relative.parts)
            if path.is_symlink():
                return False
            mode = path.lstat().st_mode
            return stat.S_ISREG(mode) or stat.S_ISDIR(mode)
        except (OSError, PolicyError, ValueError):
            return False

    def _validate_existing_parent(self, parent: Path) -> None:
        current = parent
        missing: list[Path] = []
        while not current.exists():
            missing.append(current)
            if current == self.root:
                break
            current = current.parent
        if self.root not in (current, *current.parents):
            raise PolicyError("path escapes workspace")
        if current.is_symlink() or not current.is_dir():
            raise PolicyError("parent must be a real directory")
        for path in reversed(missing):
            if path.is_symlink():
                raise PolicyError("symbolic links are not allowed")

    @staticmethod
    def _check_denied(parts: tuple[str, ...]) -> None:
        for part in parts:
            lowered = part.lower()
            if lowered in DENIED_COMPONENTS or lowered in DENIED_FILENAMES:
                raise PolicyError(f"access denied: {part}")
            if any(lowered.endswith(suffix) for suffix in DENIED_SUFFIXES):
                raise PolicyError(f"access denied: {part}")


def atomic_write(path: Path, content: str, *, overwrite: bool = True) -> None:
    atomic_write_bytes(path, content.encode("utf-8"), overwrite=overwrite)


def atomic_write_bytes(path: Path, content: bytes, *, overwrite: bool = True) -> None:
    descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    temporary: Path | None = None
    try:
        for attempt in range(100):
            candidate = path.parent / f".{path.name}.loki-{os.getpid()}-{attempt}"
            try:
                fd = os.open(candidate, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
                temporary = candidate
                break
            except FileExistsError:
                continue
        else:
            raise OSError("unable to allocate temporary file")

        with os.fdopen(fd, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        if overwrite:
            os.replace(temporary, path)
        else:
            os.link(temporary, path)
            temporary.unlink()
        temporary = None
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
        if temporary is not None:
            temporary.unlink(missing_ok=True)
