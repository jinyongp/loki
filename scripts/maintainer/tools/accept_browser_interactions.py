"""Actual extension actions and headless user annotation acceptance."""
import base64
import functools
import http.server
import json
from pathlib import Path
import re
import threading
import tempfile
import shutil
import urllib.error
import urllib.request

from mcp_probe import result


def text(value):
    return '\n'.join(item.get('text', '') for item in value.get('content', []) if item.get('type') == 'text')


async def interactions(client, root, workspace):
    async def call(name, arguments=None):
        return result(await client.request('tools/call', {'name': name, 'arguments': arguments or {}}))

    class QuietHandler(http.server.SimpleHTTPRequestHandler):
        def log_message(self, *_args):
            pass

    site = Path(tempfile.mkdtemp(prefix='interaction-fixture-', dir=workspace))
    (site / 'index.html').write_text('<!doctype html><meta charset="utf-8"><title>Loki interaction fixture</title><h1>Loki interaction fixture</h1>', encoding='utf-8')
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), functools.partial(QuietHandler, directory=str(site)))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    origin = f'http://127.0.0.1:{server.server_address[1]}'
    try:
        opened = await call('new_page', {'url': origin})
        page_id = selected_page(opened)
        profiles = root / 'data' / 'browser' / 'profiles'
        active = list(profiles.glob('*/devtools/session-*'))
        if len(active) != 1:
            raise ValueError('Expected exactly one active owned DevTools profile')
        owned = root / 'data' / 'browser' / 'results' / active[0].relative_to(profiles)
        extension = owned / 'interaction-extension'
        extension.mkdir()
        (extension / 'manifest.json').write_text(json.dumps({
            'manifest_version': 3, 'name': 'Loki interaction fixture', 'version': '1.0',
            'permissions': ['scripting'], 'host_permissions': ['http://127.0.0.1/*'],
            'background': {'service_worker': 'background.js'}, 'action': {'default_title': 'Loki fixture'},
        }), encoding='utf-8')
        (extension / 'background.js').write_text("globalThis.fixtureClicks=[];chrome.action.onClicked.addListener(async tab=>{globalThis.fixtureClicks.push({id:tab.id,url:tab.url});try{await chrome.scripting.executeScript({target:{tabId:tab.id},func:()=>{document.documentElement.dataset.lokiAction='LOKI_ACTION_OK';}})}catch(error){globalThis.fixtureError=String(error);}});", encoding='utf-8')
        installed = await call('install_extension', {'path': str(extension)})
        match = re.search(r'Id: ([a-p]{32})', text(installed))
        if not match:
            raise ValueError('Extension installation did not return an ID')
        extension_id = match[1]
        try:
            for reload in [False, True]:
                if reload:
                    await call('reload_extension', {'id': extension_id})
                await call('evaluate_script', {'pageId': page_id, 'function': '() => {delete document.documentElement.dataset.lokiAction;}'})
                await call('trigger_extension_action', {'id': extension_id})
                observed = await call('evaluate_script', {'pageId': page_id, 'function': "async () => {for(let i=0;i<50&&!document.documentElement.dataset.lokiAction;i++)await new Promise(r=>setTimeout(r,100));return document.documentElement.dataset.lokiAction;}"})
                if 'LOKI_ACTION_OK' not in text(observed):
                    pages = text(await call('list_pages'))
                    workers = re.findall(r'(sw-\d+): chrome-extension://'+extension_id+'/', pages)
                    diagnostic = ''
                    if workers:
                        diagnostic = text(await call('evaluate_script', {'serviceWorkerId':workers[0], 'function':'() => ({clicks:globalThis.fixtureClicks,error:globalThis.fixtureError})'}))
                    raise ValueError('Extension action did not change the page after ' + ('reload' if reload else 'install')+'\n'+pages+'\n'+diagnostic)
        finally:
            await call('uninstall_extension', {'id': extension_id})
        if extension_id in text(await call('list_extensions')):
            raise ValueError('Uninstalled fixture extension is still listed')

        await call('browser_navigate', {'url': origin})
        start = await call('browser_annotate')
        url = annotation_url(start)
        # Reject cross-origin writes and malformed images without completing
        # the session, then exercise the real UI's mouse and submit handlers.
        for body, headers, expected in [
            ({'cancel': True}, {'Origin': 'https://untrusted.invalid'}, 403),
            ({'annotations': [], 'feedback': 'fixture', 'image': 'invalid'}, {}, 400),
        ]:
            request = urllib.request.Request(url + 'submit', data=json.dumps(body).encode(), headers={'Content-Type': 'application/json', **headers})
            try:
                urllib.request.urlopen(request, timeout=5).close()
                raise ValueError('Invalid annotation request was accepted')
            except urllib.error.HTTPError as error:
                if error.code != expected:
                    raise
        await call('browser_navigate', {'url': url})
        await call('browser_run_code_unsafe', {'code': '''async page => {
            await page.waitForFunction(()=>document.querySelector('#canvas').width>300);
            await page.locator('#label').fill('LOKI_ANNOTATION_OK');
            const box=await page.locator('#canvas').boundingBox();
            await page.mouse.move(box.x+20,box.y+20);await page.mouse.down();
            await page.mouse.move(box.x+200,box.y+70,{steps:8});await page.mouse.up();
            await page.locator('#feedback').fill('Loki interaction feedback');
            await page.getByRole('button',{name:'Submit annotations',exact:true}).click();
            await page.getByRole('status').filter({hasText:'Submitted.'}).waitFor();
        }'''})
        annotated = await call('browser_annotate')
        if 'LOKI_ANNOTATION_OK' not in text(annotated) or 'Loki interaction feedback' not in text(annotated):
            raise ValueError('Submitted annotation and feedback were not returned')
        listing = json.loads(text(await call('loki_browser_files', {'engine': 'browser/playwright', 'action': 'list'})))['files']
        images = [item for item in listing if 'annotations' in item['name'] and item['name'].endswith('.png')]
        if len(images) != 1:
            raise ValueError('Expected one annotated PNG')
        readback = await call('loki_browser_files', {'engine': 'browser/playwright', 'action': 'read', 'name': images[0]['name']})
        if not any(item.get('type') == 'image' and base64.b64decode(item['data']).startswith(b'\x89PNG\r\n\x1a\n') for item in readback['content']):
            raise ValueError('Annotated PNG did not return through owned file transfer')

        await call('browser_navigate', {'url': origin})
        url = annotation_url(await call('browser_annotate'))
        await call('browser_navigate', {'url': url})
        await call('browser_run_code_unsafe', {'code': "async page => {await page.getByRole('button',{name:'Cancel',exact:true}).click();await page.getByRole('status').filter({hasText:'Cancelled.'}).waitFor();}"})
        if 'Annotation cancelled.' not in text(await call('browser_annotate')):
            raise ValueError('Cancelled annotation was not acknowledged')
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
        shutil.rmtree(site)


def selected_page(value):
    ids = re.findall(r'^(\d+): .* \[selected\]', text(value), re.MULTILINE)
    if len(ids) != 1:
        raise ValueError('Could not identify the selected fixture page')
    return int(ids[0])


def annotation_url(value):
    match = re.search(r'http://127\.0\.0\.1:\d+/[a-f0-9]{48}/', text(value))
    if not match:
        raise ValueError('Annotation did not return an owned loopback screen')
    return match[0]
