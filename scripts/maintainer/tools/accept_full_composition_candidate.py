#!/usr/bin/env python3
"""Exercise full browser, jobs and local modules through a native owned candidate.

Uses synthetic files and credentials. Does not publish endpoints, contact GitHub,
or certify external sharing, account signing enrollment or desktop image rendering.
Run with the administrator identity used for the protected full host.
"""
import argparse
import asyncio
import json
from pathlib import Path
import time
import uuid

from accept_browser_candidate import protocol as browser_protocol
from accept_full_workspace_candidate import accept
from mcp_probe import Client, result


async def protocol(binary, root, workspace, invoke):
    command = [str(binary), "--host", "local", "--root", str(root), "tools", "serve"]
    client = Client(command, workspace)
    await client.start()
    async def call(name, arguments):
        body = result(await client.request("tools/call", {"name":name, "arguments":arguments}))
        if "structuredContent" in body:
            return body["structuredContent"]
        return json.loads(next(item["text"] for item in body["content"] if item["type"] == "text"))
    try:
        await client.initialize()
        definitions = result(await client.request("tools/list"))["tools"]
        names = {item["name"] for item in definitions}
        required = {"workspace_read", "workspace_edit", "job", "git_inspect", "secret_inspect", "secret_write", "project_coordination", "browser_navigate", "navigate_page"}
        if len(names) != len(definitions) or not required <= names:
            raise ValueError("full composition omitted selected local tools")
        await call("workspace_edit", {"action":"create", "path":"full-fixture.txt", "content":"Owned composition fixture\n"})
        content = await call("workspace_read", {"action":"file", "path":"full-fixture.txt"})
        if "Owned composition fixture" not in json.dumps(content):
            raise ValueError("full workspace readback differs")
        for network in ("none", "dependency-install"):
            script = f"printf 'composition-job\\n'; /opt/loki/bin/git init repo; cd repo; printf 'fixture\\n' > fixture-{network}.txt; /opt/loki/bin/git add fixture-{network}.txt; /opt/loki/bin/git commit -m 'Acceptance fixture'; /opt/loki/bin/git verify-commit HEAD"
            request = {"action":"start", "request_id":str(uuid.uuid4()), "cwd":".", "argv":["/bin/sh", "-ec", script], "timeout_seconds":30, "network":network}
            started = await call("job", request)
            replay = await call("job", request)
            if replay["job_id"] != started["job_id"] or not replay["replayed"]:
                raise ValueError("full job retry created a second job")
            deadline = time.monotonic()+60
            while time.monotonic() < deadline:
                status = await call("job", {"action":"inspect", "job_id":started["job_id"]})
                if status["state"] == "terminal":
                    break
                await asyncio.sleep(.25)
            output = await call("job", {"action":"output", "job_id":started["job_id"]})
            if status.get("exit_code") != 0 or "composition-job" not in output.get("output", ""):
                raise ValueError("full job failed: " + json.dumps({"status":status, "output":output}))
        git = await call("git_inspect", {"action":"status", "cwd":"repo"})
        if git.get("exit_code") != 0:
            raise ValueError("native Git inspection failed")
        index = await call("git_inspect", {"action":"index", "cwd":"repo"})
        patch = "diff --git a/fixture-none.txt b/fixture-none.txt\n--- a/fixture-none.txt\n+++ b/fixture-none.txt\n@@ -1 +1 @@\n-fixture\n+updated fixture\n"
        staged = await call("git_stage", {"action":"patch", "cwd":"repo", "patch":patch, "expected_index_sha256":index["index_sha256"]})
        if staged.get("index_sha256") == index["index_sha256"]:
            raise ValueError("full partial staging did not consume its isolated stdin")
        state = await call("secret_inspect", {"action":"status"})
        await call("secret_write", {"action":"create_profile", "profile":"fixture", "request_id":str(uuid.uuid4()), "expected_revision":state["revision"]})
        state = await call("secret_inspect", {"action":"status"})
        await call("secret_write", {"action":"generate", "profile":"fixture", "secret":"FIXTURE_TOKEN", "bytes":32, "request_id":str(uuid.uuid4()), "expected_revision":state["revision"]})
        metadata = await call("secret_inspect", {"action":"profile", "profile":"fixture"})
        if metadata.get("secret_names") != ["FIXTURE_TOKEN"] or metadata.get("configured_secret_count") != 1:
            raise ValueError("synthetic secret metadata differs")
        await call("workspace_edit", {"action":"create", "path":"devtools.toml", "content":'profile = "fixture"\n'})
        coordination = await call("project_coordination", {"action":"current", "cwd":"."})
        if coordination.get("profile") != "fixture" or not coordination.get("complete"):
            raise ValueError("local coordination read failed")
        invoke("tools", "disable", "workspace")
        denied = await client.request("tools/call", {"name":"workspace_read", "arguments":{"action":"file", "path":"full-fixture.txt"}})
        if "error" not in denied and not denied.get("result", {}).get("isError"):
            raise ValueError("cached workspace access survived disabled selection")
    finally:
        await client.close()
    await browser_protocol(binary, root, workspace, invoke, launch=command)
    print(json.dumps({"full_composition_operations":"pass", "checks":["confined jobs with none and dependency-install profiles", "idempotent job start", "generated test signing key", "signed commits and signature verification", "partial staging through isolated stdin", "synthetic secret generation", "local coordination", "both browser engines", "owned screenshot resources and PNG bytes", "cached workspace and browser revocation"]}), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True, type=Path)
    accept(parser.parse_args().candidate.resolve(), modules=("workspace", "execution", "git", "secrets", "coordination", "browser"), protocol_check=protocol)
