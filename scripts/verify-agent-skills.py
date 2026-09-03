#!/usr/bin/env python3
from __future__ import annotations

import asyncio
import grp
import hashlib
import json
import os
from pathlib import Path
import pwd
import shutil
import subprocess
import time

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client


TOKEN_PATH = Path("/etc/loki/token")
MCP_URL = "http://127.0.0.1:8765/mcp"
WORKSPACE = Path("/srv/workspace/loki")
TEST_DIRECTORY = WORKSPACE / ".loki" / "agent-skills-e2e"
PROJECT_STATE_ROOT = Path("/var/lib/loki/project-state/projects")


async def wait_for_server() -> None:
    deadline = time.monotonic() + 15
    while True:
        try:
            reader, writer = await asyncio.open_connection("127.0.0.1", 8765)
            writer.close()
            await writer.wait_closed()
            return
        except OSError:
            if time.monotonic() >= deadline:
                raise RuntimeError("Loki MCP did not become ready within 15 seconds")
            await asyncio.sleep(0.1)


async def call(session: ClientSession, name: str, arguments: dict[str, object]) -> dict[str, object]:
    result = await session.call_tool(name, arguments)
    if result.is_error:
        raise RuntimeError(f"{name} failed: {result.content}")
    value = result.structured_content
    if not isinstance(value, dict):
        raise RuntimeError(f"{name} returned no structured result")
    return value


async def verify() -> None:
    await wait_for_server()
    resolved = TEST_DIRECTORY.resolve()
    if resolved != WORKSPACE / ".loki" / "agent-skills-e2e":
        raise RuntimeError("unsafe Agent Skills verification directory")
    if TEST_DIRECTORY.exists():
        shutil.rmtree(TEST_DIRECTORY)
    project_state_directory: Path | None = None
    try:
        TEST_DIRECTORY.mkdir(parents=True)
        runner = pwd.getpwnam("runner")
        workspace_group = grp.getgrnam("workspace")
        os.chown(TEST_DIRECTORY, runner.pw_uid, workspace_group.gr_gid)
        tracked = TEST_DIRECTORY / "tracked.txt"
        tracked.write_text(
            "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n",
            encoding="utf-8",
        )
        os.chown(tracked, runner.pw_uid, workspace_group.gr_gid)
        git_environment = {"HOME": "/home/runner", "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8"}
        for arguments in (
            ["init", "-q"],
            ["add", "tracked.txt"],
            [
                "-c", "user.name=Loki E2E",
                "-c", "user.email=loki-e2e@example.invalid",
                "-c", "commit.gpgsign=false",
                "-c", "core.hooksPath=/dev/null",
                "commit", "-qm", "test: initialize fixture",
            ],
        ):
            subprocess.run(
                ["/usr/sbin/runuser", "-u", "runner", "--", "git", "-C", str(TEST_DIRECTORY), *arguments],
                env=git_environment, check=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            )
        logical_common = "/workspace/.loki/agent-skills-e2e/.git"
        project_state_directory = PROJECT_STATE_ROOT / hashlib.sha256(
            logical_common.encode("utf-8")
        ).hexdigest()[:32]
        if project_state_directory.exists():
            shutil.rmtree(project_state_directory)

        token = TOKEN_PATH.read_text(encoding="utf-8").strip()
        headers = {"Authorization": f"Bearer {token}"}
        async with httpx2.AsyncClient(headers=headers) as http_client:
            async with streamable_http_client(MCP_URL, http_client=http_client) as streams:
                async with ClientSession(*streams) as session:
                    await session.initialize()
                    descriptors = await session.list_tools()
                    names = {tool.name for tool in descriptors.tools}
                    expected = {
                        "agent_context", "skill_read", "skill_write", "git_inspect",
                        "git_stage", "workspace_read", "workspace_edit", "restore_workspace_file",
                        "project",
                    }
                    assert expected <= names

                    context = await call(session, "agent_context", {"cwd": ".loki/agent-skills-e2e"})
                    selected_names = {skill["name"] for skill in context["skills"]}
                    assert {
                        "close", "dev-docs", "git-commit", "humanize-korean", "minify",
                        "planning", "queue", "review-loop", "survey", "taskwarrior",
                        "verify", "workstream",
                    } <= selected_names
                    assert "solidjs-development" not in selected_names
                    assert "astro-solid-integration" not in selected_names
                    absolute_context = await call(
                        session, "agent_context", {"cwd": "/workspace/.loki/agent-skills-e2e"},
                    )
                    assert absolute_context["cwd"] == ".loki/agent-skills-e2e"
                    escaped_context = await session.call_tool(
                        "agent_context", {"cwd": "/etc"},
                    )
                    assert escaped_context.is_error
                    assert "absolute cwd must be within the workspace" in str(
                        escaped_context.content
                    )
                    activated = await call(
                        session, "skill_read", {"action": "activate", "name": "git-commit", "cwd": ".loki/agent-skills-e2e"},
                    )
                    assert activated["scope"] == "shared"
                    assert "git_stage" in activated["instructions"]
                    commit_context = await call(
                        session, "git_inspect", {
                            "action": "commit_context",
                            "cwd": ".loki/agent-skills-e2e",
                        },
                    )
                    assert commit_context["configured"] is True
                    assert len(commit_context["template"]["sha256"]) == 64
                    assert commit_context["template"]["content"]
                    config_read = await call(session, "command_run", {
                        "action": "exec", "executable": "git",
                        "arguments": [
                            "config", "--show-origin", "--get-all", "commit.template",
                        ],
                        "cwd": ".loki/agent-skills-e2e",
                    })
                    assert config_read["exit_code"] == 0, config_read
                    assert "gitmessage" in config_read["output"]

                    task_cwd = ".loki/agent-skills-e2e"
                    initialized = await call(session, "project", {
                        "action": "init", "cwd": task_cwd,
                        "goal": "Verify shared project state across worktrees",
                        "slug_base": "verify-project-state",
                        "depth": "standard",
                        "intent_source_kind": "plan-local",
                    })
                    assert initialized["created"] is True
                    plan = await call(session, "project", {
                        "action": "write", "cwd": task_cwd,
                        "workstream": initialized["slug"], "filename": "plan.md",
                        "content": "# Verification plan\n",
                    })
                    read_plan = await call(session, "project", {
                        "action": "read", "cwd": task_cwd,
                        "workstream": initialized["slug"], "filename": "plan.md",
                    })
                    assert read_plan["sha256"] == plan["sha256"]
                    task_version = await call(session, "command_run", {
                        "action": "exec", "executable": "task", "arguments": ["--version"], "cwd": task_cwd,
                    })
                    assert task_version["exit_code"] == 0, task_version
                    added_task = await call(session, "command_run", {
                        "action": "exec", "executable": "task",
                        "arguments": ["add", "Verify Loki Taskwarrior", "project:loki-agent-skills", "+validation"],
                        "cwd": task_cwd,
                    })
                    assert added_task["exit_code"] == 0, added_task
                    listed_tasks = await call(session, "command_run", {
                        "action": "exec", "executable": "task",
                        "arguments": ["project:loki-agent-skills", "list"],
                        "cwd": task_cwd,
                    })
                    assert listed_tasks["exit_code"] == 0, listed_tasks
                    assert "Verify Loki Taskwarrior" in listed_tasks["output"]

                    cwd = ".loki/agent-skills-e2e"
                    original = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
                    updated = "ONE\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nTEN\n"
                    read = await call(session, "workspace_read", {
                        "action": "file", "path": ".loki/agent-skills-e2e/tracked.txt",
                    })
                    replaced = await call(session, "workspace_edit", {
                        "action": "replace", "path": ".loki/agent-skills-e2e/tracked.txt",
                        "old": original,
                        "new": updated,
                        "expected_sha256": read["sha256"],
                    })
                    revisions = await call(session, "workspace_read", {
                        "action": "revisions", "path": ".loki/agent-skills-e2e/tracked.txt",
                    })
                    assert revisions["revisions"][0]["revision"] == replaced["previous_revision"]
                    revision_diff = await call(session, "workspace_read", {
                        "action": "revision_diff", "path": ".loki/agent-skills-e2e/tracked.txt",
                        "revision": replaced["previous_revision"],
                    })
                    assert "+ONE" in revision_diff["diff"] and "+TEN" in revision_diff["diff"]
                    before = await call(session, "git_inspect", {"action": "index", "cwd": cwd})
                    patch = """diff --git a/tracked.txt b/tracked.txt
--- a/tracked.txt
+++ b/tracked.txt
@@ -1,4 +1,4 @@
-one
+ONE
 two
 three
 four
"""
                    staged = await call(session, "git_stage", {
                        "action": "patch", "patch": patch, "cwd": cwd,
                        "expected_index_sha256": before["index_sha256"],
                    })
                    cached = await call(session, "git_inspect", {"action": "diff", "staged": True, "cwd": cwd})
                    unstaged = await call(session, "git_inspect", {"action": "diff", "staged": False, "cwd": cwd})
                    assert "+ONE" in cached["output"] and "+TEN" not in cached["output"]
                    assert "+TEN" in unstaged["output"]
                    await call(session, "git_stage", {
                        "action": "patch", "patch": patch, "cwd": cwd, "reverse": True,
                        "expected_index_sha256": staged["index_sha256"],
                    })

                    created = await call(session, "skill_write", {
                        "action": "create", "scope": "project", "cwd": cwd, "name": "fixture-skill",
                        "description": "Exercise project Skill lifecycle.",
                        "instructions": "Read the fixture reference.",
                    })
                    await call(session, "skill_write", {
                        "action": "resource", "name": "fixture-skill", "cwd": cwd,
                        "path": "references/fixture.md", "content": "fixture\n",
                    })
                    valid = await call(session, "skill_read", {"action": "validate", "name": "fixture-skill", "cwd": cwd})
                    assert valid["valid"] is True and valid["resource_count"] == 1
                    assert len(created["sha256"]) == 64

        configured = subprocess.run(
            ["/usr/sbin/runuser", "-u", "runner", "--", "git", "config", "--get", "commit.template"],
            env=git_environment, text=True, capture_output=True, check=True,
        ).stdout.strip()
        assert configured == "/home/runner/.dotfiles/git/templates/gitmessage"
        assert Path(configured).is_file()
        assert Path("/home/runner/.dotfiles/git/hooks/commit-msg").is_file()
        print(json.dumps({
            "agent_skill_tools": len(expected),
            "builtin_skills": 12,
            "partial_staging": "hunk-stage-and-reverse",
            "commit_template": configured,
        }, separators=(",", ":")))
    finally:
        if project_state_directory is not None and project_state_directory.exists():
            shutil.rmtree(project_state_directory)
        if TEST_DIRECTORY.exists():
            shutil.rmtree(TEST_DIRECTORY)


if __name__ == "__main__":
    asyncio.run(verify())
