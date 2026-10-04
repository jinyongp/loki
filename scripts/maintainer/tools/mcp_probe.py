"""Bounded stdio client used only by final native candidate acceptance."""
import asyncio
import json


class Client:
    def __init__(self, command, workspace):
        self.command, self.workspace = command, workspace
        self.pending, self.next_id, self.diagnostics = {}, 1, []

    async def start(self):
        self.process = await asyncio.create_subprocess_exec(*self.command, cwd=self.workspace, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, limit=16 << 20)
        self.reader = asyncio.create_task(self.read())
        self.errors = asyncio.create_task(self.read_errors())

    async def send(self, body):
        self.process.stdin.write(json.dumps(body).encode()+b"\n")
        await self.process.stdin.drain()

    async def read(self):
        try:
            while line := await self.process.stdout.readline():
                value = json.loads(line)
                if "method" in value and "id" in value:
                    response = {"jsonrpc":"2.0", "id":value["id"]}
                    if value["method"] == "roots/list":
                        response["result"] = {"roots":[{"uri":self.workspace.as_uri(), "name":"Native acceptance fixture"}]}
                    else:
                        response["error"] = {"code":-32601, "message":"Unsupported acceptance client method"}
                    await self.send(response)
                elif "id" in value:
                    future = self.pending.get(value["id"])
                    if future is not None and not future.done():
                        future.set_result(value)
        finally:
            for future in self.pending.values():
                if not future.done():
                    future.set_exception(RuntimeError("MCP connection ended before its response"))

    async def read_errors(self):
        total = 0
        while line := await self.process.stderr.readline():
            total += len(line)
            if total <= 64 << 10:
                self.diagnostics.append(line.decode(errors="replace").rstrip())

    async def request(self, method, params=None, timeout=120):
        number, self.next_id = self.next_id, self.next_id+1
        future = asyncio.get_running_loop().create_future()
        self.pending[number] = future
        try:
            await self.send({"jsonrpc":"2.0", "id":number, "method":method, "params":params or {}})
            return await asyncio.wait_for(future, timeout)
        finally:
            self.pending.pop(number, None)

    async def initialize(self):
        result = await self.request("initialize", {"protocolVersion":"2025-11-25", "capabilities":{"roots":{"listChanged":True}}, "clientInfo":{"name":"loki-native-candidate-acceptance", "version":"0.2.2"}})
        if "error" in result:
            raise ValueError("native candidate rejected MCP initialization")
        await self.send({"jsonrpc":"2.0", "method":"notifications/initialized"})

    async def close(self):
        if self.process.returncode is None:
            self.process.stdin.close()
            try:
                await asyncio.wait_for(self.process.wait(), 10)
            except asyncio.TimeoutError:
                self.process.kill()
                await self.process.wait()
                raise ValueError("native MCP process did not close after stdin ended")
        await asyncio.gather(self.reader, self.errors)


def result(response):
    if "error" in response or response.get("result", {}).get("isError"):
        details = response.get("error") or [item.get("text", "") for item in response.get("result", {}).get("content", []) if item.get("type") == "text"]
        raise ValueError("native candidate rejected an expected successful MCP operation: " + json.dumps(details, ensure_ascii=True)[:8192])
    return response["result"]
