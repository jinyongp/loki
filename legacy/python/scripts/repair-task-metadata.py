"""Root-only, bounded repair of legacy central Taskwarrior metadata, never DB contents."""
from pathlib import Path
import grp
import os
import re


def repair(root: Path = Path("/var/lib/loki/project-state/projects")) -> int:
    if os.geteuid() != 0:
        raise PermissionError("task metadata repair requires root")
    if root.is_symlink():
        raise ValueError("unsafe state root")
    if not root.exists():
        return 0
    gid = grp.getgrnam("workspace").gr_gid
    count = 0
    for project in root.iterdir():
        if not re.fullmatch(r"[a-f0-9]{32}", project.name):
            continue
        paths = [(project, 0o710), (project / "taskwarrior", 0o710),
                 (project / "taskwarrior" / "taskrc", 0o640)]
        for path, mode in paths:
            if path.is_symlink() or not path.exists():
                raise ValueError("unsafe or incomplete central task metadata")
        for path, mode in paths:
            os.chown(path, 0, gid, follow_symlinks=False)
            os.chmod(path, mode, follow_symlinks=False)
        count += 1
    return count


if __name__ == "__main__":
    print(f"repaired_task_metadata={repair()}")
