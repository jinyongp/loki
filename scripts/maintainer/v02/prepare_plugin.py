#!/usr/bin/env python3
"""Materialize a local plugin with an explicit execution-host connection.

Preparation writes source configuration only. Installation, native startup and
desktop SSH acceptance are separate operations.
"""
import argparse
import json
from pathlib import Path, PurePosixPath, PureWindowsPath
import shutil


def prepare(output, command, mode, workspace, engine, host, address, distribution, management_root):
    if not command or any(ord(c) < 32 for c in command):
        raise ValueError("manager command requires an explicit executable without control characters")
    if host == "ssh" and not address or host == "wsl" and not distribution:
        raise ValueError("remote execution host requires its explicit destination")
    args = []
    if host != "local":
        args += ["--host", host]
        args += ["--address", address] if host == "ssh" else ["--distribution", distribution]
    if management_root:
        args += ["--root", management_root]
    args += ["tools", "serve"]
    name = "loki-tools"
    if mode == "project-host":
        if not workspace or not (PurePosixPath(workspace).is_absolute() or PureWindowsPath(workspace).is_absolute()):
            raise ValueError("browser workspace must be an absolute path on the execution host")
        name = "loki-browser"
        args += ["browser", "--workspace", workspace, "--engine", engine]
    elif workspace:
        raise ValueError("full mode uses its configured managed workspace")
    for value in args:
        if any(ord(c) < 32 for c in value):
            raise ValueError("connection arguments cannot contain control characters")
    source = Path(__file__).resolve().parents[3] / "plugins" / name
    shutil.copytree(source, output)
    configuration = {"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {name:{"type":"stdio", "command":command, "args":args}}}
    (output / "mcp.json").write_text(json.dumps(configuration, indent=2)+"\n", encoding="utf-8")
    # Codex's local package loader uses this overlay. The portable manifest
    # stays at the package root; both presentations have the same identity.
    portable = json.loads((output / "plugin.json").read_text(encoding="utf-8"))
    overlay = {key:portable[key] for key in ("name", "version", "description", "author")}
    overlay.update({"skills":"./skills/", "mcpServers":"./mcp.json", "requires_local_executor":True, "interface":portable["extensions"]["com.openai"]["interface"]})
    (output / ".codex-plugin").mkdir()
    (output / ".codex-plugin" / "plugin.json").write_text(json.dumps(overlay, indent=2)+"\n", encoding="utf-8")
    print(output, flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--command", default="loki")
    parser.add_argument("--mode", required=True, choices=("project-host", "full"))
    parser.add_argument("--workspace")
    parser.add_argument("--engine", default="both", choices=("playwright", "devtools", "both"))
    parser.add_argument("--host", default="local", choices=("local", "wsl", "ssh"))
    parser.add_argument("--address", default="")
    parser.add_argument("--distribution", default="")
    parser.add_argument("--management-root", default="")
    args = parser.parse_args()
    prepare(args.output, args.command, args.mode, args.workspace, args.engine, args.host, args.address, args.distribution, args.management_root)
