"""Native regressions for post-audit capture, docked size, and recorder replay."""
import asyncio
import base64
import functools
import http.server
import json
import os
from pathlib import Path
import re
import struct
import sys
import tempfile
import threading

from mcp_probe import result


def text(value):
    return '\n'.join(item.get('text', '') for item in value.get('content', []) if item.get('type') == 'text')


def selected(value):
    return int(re.findall(r'^(\d+): .* \[selected\]', text(value), re.MULTILINE)[-1])


async def regressions(client, workspace, bundle):
    async def call(name, arguments=None):
        return result(await client.request('tools/call', {'name': name, 'arguments': arguments or {}}, timeout=90))

    class QuietHandler(http.server.SimpleHTTPRequestHandler):
        def log_message(self, *_args):
            pass

    with tempfile.TemporaryDirectory(prefix='browser-regression-', dir=workspace) as temporary:
        site = Path(temporary)
        (site / 'index.html').write_text('''<!doctype html><html lang="en"><meta charset="utf-8"><title>Browser regression</title>
<h1>Browser regression</h1><label>Name <input id="name"></label><label>Literal <input id="literal"></label>
<label>Dialog literal <input id="dialog-literal"></label>
<label>Upload <input id="upload" type="file" multiple></label>
<label>Clear upload <input id="clear-upload" type="file" multiple></label>
<label>Popup upload <input id="popup-upload" type="file" onchange="window.open('popup.html')"></label>
<label>Manual upload <input id="manual-upload" type="file" onchange="answers.push(prompt('Manual upload'))"></label>
<button id="accept" onclick="answers.push(prompt('Accept fixture'))">Accept prompt</button>
<button id="dismiss" onclick="answers.push(prompt('Dismiss fixture'))">Dismiss prompt</button>
<button id="count" onclick="counter.textContent=Number(counter.textContent)+1">Count</button><output id="counter">0</output>
<script>window.answers=[];window.popupClicks=0</script></html>''', encoding='utf-8')
        (site / 'popup.html').write_text('''<!doctype html><title>Upload popup</title>
<button onclick="opener.popupClicks++">Complete upload</button>''', encoding='utf-8')
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), functools.partial(QuietHandler, directory=str(site)))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        origin = f'http://127.0.0.1:{server.server_port}/'
        try:
            listing = json.loads(text(await call('loki_browser_files', {'engine': 'browser/devtools', 'action': 'list'})))
            owned = Path(listing['directory'])
            page = selected(await call('new_page', {'url': origin}))
            await call('resize_page', {'pageId': page, 'width': 960, 'height': 720})
            await call('emulate', {'pageId': page, 'colorScheme': 'dark'})
            size = await call('evaluate_script', {'pageId': page, 'function': '() => ({width:innerWidth,height:innerHeight})'})
            if json.loads(re.search(r'```json\n(.*?)\n```', text(size), re.S).group(1)) != {'width': 960, 'height': 720}:
                raise ValueError('Docked DevTools changed the requested viewport')
            background = selected(await call('new_page', {'url': origin}))
            await call('lighthouse_audit', {'pageId': page, 'mode': 'snapshot', 'outputDirPath': str(owned / 'regression-lighthouse')})
            await call('take_screenshot', {'pageId': page, 'filePath': str(owned / 'regression-after-audit.png')})
            image = await call('loki_browser_files', {'engine': 'browser/devtools', 'action': 'read', 'name': 'regression-after-audit.png'})
            png = next(base64.b64decode(item['data']) for item in image['content'] if item['type'] == 'image')
            if not png.startswith(b'\x89PNG\r\n\x1a\n') or struct.unpack('>II', png[16:24]) != (960, 720):
                raise ValueError('Post-audit capture did not preserve the requested PNG size')
            await call('close_page', {'pageId': background})
            await call('close_page', {'pageId': page})
            await call('list_pages')

            paths = []
            for name in ['report.fill(.txt', 'data.fill(test).csv', "upload 'quoted'.txt"]:
                staged = json.loads(text(await call('loki_browser_files', {'engine': 'browser/playwright', 'action': 'stage', 'name': name, 'data': base64.b64encode(b'replay fixture').decode()})))
                paths.append(staged['execution_host_path'])
            await call('browser_navigate', {'url': origin})
            await call('browser_start_recording')
            await call('browser_fill_form', {'fields': [
                {'name': 'Name', 'target': '#name', 'type': 'textbox', 'value': 'Recorded name'},
                {'name': 'Literal', 'target': '#literal', 'type': 'textbox', 'value': r'C:\fakepath\plain.txt'},
                {'name': 'Dialog literal', 'target': '#dialog-literal', 'type': 'textbox', 'value': 'Search for dialog.dismiss() in the source'},
            ]})
            await call('browser_click', {'target': '#upload'})
            await call('browser_file_upload', {'paths': paths})
            await call('browser_click', {'target': '#clear-upload'})
            await call('browser_file_upload', {'paths': paths[:1]})
            await call('browser_click', {'target': '#clear-upload'})
            await call('browser_file_upload', {'paths': []})
            await call('browser_click', {'target': '#popup-upload'})
            await call('browser_file_upload', {'paths': paths[1:2]})
            tabs = text(await call('browser_tabs', {'action': 'list'}))
            popup_index = int(re.findall(r'^- (\d+):.*\(' + re.escape(origin + 'popup.html') + r'\)', tabs, re.M)[0])
            main_index = int(re.findall(r'^- (\d+):.*\(' + re.escape(origin) + r'\)', tabs, re.M)[0])
            await call('browser_tabs', {'action': 'select', 'index': popup_index})
            await call('browser_click', {'target': 'button'})
            await call('browser_tabs', {'action': 'select', 'index': main_index})
            await call('browser_click', {'target': '#accept'})
            await call('browser_handle_dialog', {'accept': True, 'promptText': 'Actual response'})
            await call('browser_click', {'target': '#dismiss'})
            await call('browser_handle_dialog', {'accept': False})
            recorded = text(await call('browser_stop_recording'))
            code = re.search(r'```js\n(.*?)\n```', recorded, re.S).group(1)
            if 'omitted' in recorded or '.setInputFiles(' not in code or 'dialog.accept("Actual response")' not in code:
                raise ValueError('Recorder lost an upload or actual dialog outcome: ' + recorded)

            # Replay outside the MCP modal-state observer so the recorded dialog
            # listeners, rather than another tool call, must complete the flow.
            runtime = json.loads((bundle / 'runtime.json').read_text())
            async def replay_recording(code, expected):
                replay = site / 'replay.cjs'
                settings = {'bundle': str(bundle), 'chrome': str(bundle / runtime['chrome']), 'origin': origin, 'code': code}
                replay.write_text('const settings=' + json.dumps(settings) + ';\n' + '''const {chromium}=require(settings.bundle+'/node_modules/playwright');
(async()=>{const browser=await chromium.launch({headless:true,chromiumSandbox:true,executablePath:settings.chrome});
try{const page=await browser.newPage();await page.goto(settings.origin);
await new (Object.getPrototypeOf(async function(){}).constructor)('page',settings.code)(page);
console.log(JSON.stringify(await page.evaluate(()=>({name:document.querySelector('#name').value,literal:document.querySelector('#literal').value,dialogLiteral:document.querySelector('#dialog-literal').value,files:[...document.querySelector('#upload').files].map(f=>f.name),clearFiles:document.querySelector('#clear-upload').files.length,popupFiles:[...document.querySelector('#popup-upload').files].map(f=>f.name),popupClicks,answers}))));
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exitCode=1});''', encoding='utf-8')
                process = await asyncio.create_subprocess_exec(str(bundle / runtime['node']), str(replay), stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
                try:
                    stdout, stderr = await asyncio.wait_for(process.communicate(), 30)
                except BaseException:
                    if process.returncode is None:
                        process.kill()
                        await process.wait()
                    raise
                if process.returncode or json.loads(stdout.splitlines()[-1]) != expected:
                    raise ValueError('Recorded code did not replay actual final state: ' + stdout.decode() + stderr.decode())

            await replay_recording(code, {'name': 'Recorded name', 'literal': r'C:\fakepath\plain.txt', 'dialogLiteral': 'Search for dialog.dismiss() in the source', 'files': [Path(path).name for path in paths], 'clearFiles': 0, 'popupFiles': [Path(paths[1]).name], 'popupClicks': 1, 'answers': ['Actual response', None]})

            # An upload outside browser_file_upload has no recoverable host path.
            # Omitting it must not apply its answer to the next retained dialog.
            for first_accept in [False, True]:
                await call('browser_navigate', {'url': origin})
                await call('browser_start_recording')
                await call('browser_run_code_unsafe', {'code': 'async page => await page.locator("#manual-upload").setInputFiles(' + json.dumps(paths[:1]) + ')'})
                await call('browser_handle_dialog', {'accept': first_accept, 'promptText': 'First answer'})
                await call('browser_click', {'target': '#accept'})
                await call('browser_handle_dialog', {'accept': not first_accept, 'promptText': 'Second answer'})
                recorded = text(await call('browser_stop_recording'))
                if 'A recorded file upload was omitted' not in recorded or 'A recorded dialog action was omitted' in recorded:
                    raise ValueError('Manual upload omission affected a recoverable dialog: ' + recorded)
                code = re.search(r'```js\n(.*?)\n```', recorded, re.S).group(1)
                await replay_recording(code, {'name': '', 'literal': '', 'dialogLiteral': '', 'files': [], 'clearFiles': 0, 'popupFiles': [], 'popupClicks': 0, 'answers': [None if first_accept else 'Second answer']})

            # The debugger belongs to this exact MCP context. Attach a fixture
            # client through Playwright's owned registry to create a real pause,
            # then use the public MCP resume tool to unblock its pending click.
            listing = json.loads(text(await call('loki_browser_files', {'engine': 'browser/playwright', 'action': 'list'})))
            profile = Path(listing['directory'].replace(os.sep + 'results' + os.sep, os.sep + 'profiles' + os.sep)).resolve()
            cache = Path(os.environ.get('XDG_CACHE_HOME', str(Path.home() / '.cache')))
            if sys.platform == 'darwin':
                cache = Path.home() / 'Library' / 'Caches'
            elif sys.platform == 'win32':
                cache = Path(os.environ['LOCALAPPDATA'])
            registry = Path(os.environ.get('PWTEST_SERVER_REGISTRY', str(cache / 'ms-playwright' / 'b')))
            descriptor = None
            newest = -1
            for candidate in registry.iterdir():
                try:
                    value = json.loads(candidate.read_text())
                except (OSError, ValueError):
                    continue
                user_data = value.get('browser', {}).get('userDataDir')
                if user_data and Path(user_data).resolve() == profile:
                    endpoint = value.get('endpoint', value.get('pipeName'))
                    if sys.platform != 'win32' and endpoint and not endpoint.startswith(('ws://', 'wss://')) and not Path(endpoint).exists():
                        continue
                    modified = candidate.stat().st_mtime_ns
                    if modified > newest:
                        descriptor = candidate
                        newest = modified
            if descriptor is None:
                raise ValueError('Owned fixture debugger descriptor was not found')
            fixture = Path(__file__).with_name('browser_pause_fixture.cjs')
            paused = await asyncio.create_subprocess_exec(str(bundle / runtime['node']), str(fixture), str(bundle), str(descriptor), origin, stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE)
            try:
                ready = await asyncio.wait_for(paused.stdout.readline(), 15)
                if ready.strip() != b'PAUSED':
                    stderr = await asyncio.wait_for(paused.stderr.read(), 2)
                    raise ValueError('Fixture did not reach a real debugger pause: ' + stderr.decode())
                result(await client.request('tools/call', {'name': 'browser_resume', 'arguments': {'step': False}}, timeout=10))
                stdout, stderr = await asyncio.wait_for(paused.communicate(), 15)
                resumed = json.loads(stdout.splitlines()[-1]) if stdout else {}
                if paused.returncode or resumed != {'resumed': True, 'counter': '1', 'paused': False}:
                    raise ValueError('MCP resume did not complete the paused action: ' + stdout.decode() + stderr.decode())
            finally:
                if paused.returncode is None:
                    paused.kill()
                    await paused.wait()
            await call('browser_close')
            print(json.dumps({'browser_regressions': 'pass', 'viewport': [960, 720], 'post_audit_capture': 'pass', 'follow_up_calls': 'pass', 'recorded_upload_and_dialog_replay': 'pass', 'unusual_upload_names': 'pass', 'cleared_upload': 'pass', 'upload_popup': 'pass', 'omitted_upload_dialog_alignment': 'pass', 'actual_debugger_resume': 'pass'}))
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
