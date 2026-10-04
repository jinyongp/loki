import argparse
import asyncio
import base64
import hashlib
import json
import os
from pathlib import Path
import signal
import sys
import time

class Client:
    def __init__(self, command, root):
        self.command, self.root = command, root
        self.pending, self.messages, self.stderr = {}, [], []
        self.next_id = 1

    async def start(self):
        env = {k: os.environ[k] for k in ('PATH', 'LANG', 'LD_LIBRARY_PATH', 'DISPLAY', 'WAYLAND_DISPLAY', 'XDG_RUNTIME_DIR') if k in os.environ}
        env.update(CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS='1', CHROME_DEVTOOLS_MCP_NO_UPDATE_CHECKS='1')
        for key, directory in [('XDG_DATA_HOME', 'data'), ('XDG_CONFIG_HOME', 'config'), ('XDG_CACHE_HOME', 'cache')]:
            env[key] = str(self.root / directory)
        env['LD_LIBRARY_PATH'] = str(self.root.parent / 'native-deps/root/usr/lib/x86_64-linux-gnu')
        env['PLAYWRIGHT_BROWSERS_PATH'] = str(self.root.parent / 'pw-browsers')
        self.process = await asyncio.create_subprocess_exec(*self.command, cwd=self.root, env=env, stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, start_new_session=True, limit=16 << 20)
        self.reader = asyncio.create_task(self.read())
        self.errors = asyncio.create_task(self.read_errors())

    async def send(self, value):
        self.process.stdin.write(json.dumps(value).encode() + b'\n')
        await self.process.stdin.drain()

    async def read(self):
        while line := await self.process.stdout.readline():
            try:
                value = json.loads(line)
            except json.JSONDecodeError:
                self.messages.append({'invalid_stdout': line.decode(errors='replace')[:500]})
                continue
            if 'method' in value and 'id' in value:
                self.messages.append({'server_request_method': value['method']})
                if value['method'] == 'roots/list':
                    await self.send({'jsonrpc': '2.0', 'id': value['id'], 'result': {'roots': [{'uri': self.root.as_uri(), 'name': 'loki-preflight'}]}})
                else:
                    await self.send({'jsonrpc': '2.0', 'id': value['id'], 'error': {'code': -32601, 'message': 'Unsupported probe client method'}})
            elif 'id' in value and value['id'] in self.pending:
                future = self.pending.pop(value['id'])
                if not future.done(): future.set_result(value)
            else:
                self.messages.append(value)
        for future in list(self.pending.values()):
            if not future.done(): future.set_exception(RuntimeError('MCP stdout ended before response'))

    async def read_errors(self):
        while line := await self.process.stderr.readline():
            self.stderr.append(line.decode(errors='replace').rstrip())

    async def request(self, method, params=None, timeout=45):
        number, self.next_id = self.next_id, self.next_id + 1
        future = asyncio.get_running_loop().create_future()
        self.pending[number] = future
        await self.send({'jsonrpc': '2.0', 'id': number, 'method': method, 'params': params or {}})
        try:
            return await asyncio.wait_for(future, timeout)
        except asyncio.TimeoutError as error:
            tool = (params or {}).get('name', '')
            raise RuntimeError(f'{method} {tool} timed out after {timeout}s') from error
        finally:
            self.pending.pop(number, None)

    async def initialize(self):
        response = await self.request('initialize', {'protocolVersion': '2025-06-18', 'capabilities': {'roots': {'listChanged': True}}, 'clientInfo': {'name': 'loki-0.2-preflight', 'version': '0.2.0'}})
        if 'error' in response: raise RuntimeError(str(response['error']))
        await self.send({'jsonrpc': '2.0', 'method': 'notifications/initialized'})
        return response['result']

    async def close(self):
        if self.process.returncode is None:
            self.process.stdin.close()
            try:
                await asyncio.wait_for(self.process.wait(), 5)
            except asyncio.TimeoutError:
                os.killpg(self.process.pid, signal.SIGTERM)
                try: await asyncio.wait_for(self.process.wait(), 5)
                except asyncio.TimeoutError:
                    os.killpg(self.process.pid, signal.SIGKILL)
                    await self.process.wait()
        # This group belongs to this probe; clean children even if the server exited.
        try: os.killpg(self.process.pid, signal.SIGTERM)
        except ProcessLookupError: pass
        await asyncio.gather(self.reader, self.errors, return_exceptions=True)

def compact(value):
    if isinstance(value, list): return [compact(x) for x in value]
    if isinstance(value, dict):
        result = {k: compact(v) for k, v in value.items() if k != 'data'}
        if value.get('type') == 'image' and 'data' in value:
            data = base64.b64decode(value['data'])
            result.update(bytes=len(data), sha256=hashlib.sha256(data).hexdigest())
        elif 'data' in value: result['data'] = compact(value['data'])
        return result
    return value

async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--engine', choices=['playwright', 'devtools'], required=True)
    parser.add_argument('--root', default='/tmp/loki-tools-preflight')
    parser.add_argument('--all-caps', action='store_true')
    parser.add_argument('--sandbox', action='store_true', default=True, help='Chrome sandbox stays enabled for these probes')
    parser.add_argument('--catalog-only', action='store_true')
    parser.add_argument('--fixture', action='store_true')
    parser.add_argument('--devtools-ui', action='store_true')
    args = parser.parse_args()
    base = Path(args.root).resolve()
    profile = ('full' if args.all_caps else 'default') + ('-sandbox' if args.sandbox else '') + ('-ui' if args.devtools_ui else '')
    root = base / (args.engine + '-' + profile + '-' + str(time.time_ns()))
    root.mkdir(exist_ok=True)
    chrome = base / 'chrome-linux64/chrome'
    if args.engine == 'playwright':
        command = ['node', str(base / 'node_modules/@playwright/mcp/cli.js'), '--headless', '--isolated', '--executable-path', str(chrome), '--output-dir', str(root / 'output'), '--image-responses', 'allow']
        if args.all_caps: command += ['--caps=vision,pdf,devtools,network,storage,testing']
        if args.sandbox: command += ['--sandbox']
    else:
        command = ['node', str(base / 'node_modules/chrome-devtools-mcp/build/src/bin/chrome-devtools-mcp.js'), '--headless', '--isolated', '--executablePath', str(chrome), '--no-usage-statistics', '--no-performance-crux']
        if args.all_caps: command += ['--categoryExtensions', '--categoryPwa', '--categoryExperimentalWebmcp', '--categoryExperimentalThirdParty', '--memoryDebugging']
        if args.devtools_ui: command += ['--experimentalDevtools']
    client = Client(command, root)
    report = {'engine': args.engine, 'profile': profile, 'command': command, 'checks': []}
    try:
        await client.start()
        report['initialize'] = await client.initialize()
        response = await client.request('tools/list')
        report['catalog'] = response
        tools = response.get('result', {}).get('tools', [])
        print(json.dumps({'engine': args.engine, 'profile': profile, 'server': report['initialize'].get('serverInfo'), 'capabilities': report['initialize'].get('capabilities'), 'tools': [t['name'] for t in tools]}), flush=True)
        if args.fixture:
            from fixture import exercise
            await exercise(client, args.engine, root, report)
        elif not args.catalog_only:
            name, arguments = ('browser_navigate', {'url': 'data:text/html,<h1>Loki%20preflight</h1>'}) if args.engine == 'playwright' else ('new_page', {'url': 'data:text/html,<h1>Loki%20preflight</h1>'})
            result = compact(await client.request('tools/call', {'name': name, 'arguments': arguments}))
            report['checks'].append({'name': name, 'arguments': arguments, 'response': result})
            print(json.dumps(result)[:4500], flush=True)
    except Exception as error:
        report['error'] = type(error).__name__ + ': ' + str(error)
        print(json.dumps({'error': str(error)}), flush=True)
    finally:
        if hasattr(client, 'process'): await client.close()
        report['stderr'] = client.stderr
        report['notifications'] = compact(client.messages)
        (base / (args.engine + '-' + profile + '-report.json')).write_text(json.dumps(report, indent=2) + '\n')
    if 'error' in report or any(c.get('passed') is False for c in report['checks']) or any('invalid_stdout' in m for m in client.messages):
        return 1
    return 0

if __name__ == '__main__': sys.exit(asyncio.run(main()))
