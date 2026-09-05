#!/usr/bin/env python3
"""Update only preview bindings on an existing central action using authenticated MCP."""
import argparse
import asyncio
from pathlib import Path
import json

import httpx2
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client


async def configure(profile, action_name, bindings):
    async with httpx2.AsyncClient(headers={
        "Authorization": "Bearer " + Path("/etc/loki/token").read_text().strip()
    }) as client:
        async with streamable_http_client("http://127.0.0.1:8765/mcp", http_client=client) as streams:
            async with ClientSession(*streams) as session:
                await session.initialize()
                async def call(name, arguments):
                    result = await session.call_tool(name, arguments)
                    if result.is_error:
                        raise RuntimeError(str(result.content))
                    return result.structured_content
                profile_data = await call("secret_inspect", {"action": "profile", "profile": profile})
                policy = profile_data["action_policies"][action_name]
                keys = ("cwd", "command", "secrets", "all_secrets", "required_secrets",
                        "timeout_seconds", "max_output_bytes", "materialize_env_file",
                        "materialize_env_path", "docker_access", "singleton", "lock_probe", "local_callback")
                arguments = {key: policy[key] for key in keys}
                dynamic = policy.get("dynamic_port")
                if dynamic:
                    arguments.update(preferred_port=dynamic["preferred"],
                                     port_environment=dynamic["environment"],
                                     origin_environment=dynamic.get("origin_environment"))
                # Explicit bindings are the complete set of browser-facing backend URLs.
                arguments.update(operation="set", profile=profile, action_name=action_name,
                                 public_environment=list(bindings), preview_environment=bindings)
                await call("action", arguments)
                readback = await call("secret_inspect", {"action": "profile", "profile": profile})
                assert readback["action_policies"][action_name]["preview_environment"] == bindings
                print(json.dumps({"profile": profile, "action": action_name, "preview_environment": bindings}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile")
    parser.add_argument("action")
    parser.add_argument("bindings", nargs="*", help="PUBLIC_ENV=/preview/path")
    args = parser.parse_args()
    bindings = dict(item.split("=", 1) for item in args.bindings)
    asyncio.run(configure(args.profile, args.action, bindings))
