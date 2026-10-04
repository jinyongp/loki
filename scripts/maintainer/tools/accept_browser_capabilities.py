#!/usr/bin/env python3
"""Final optional browser capability acceptance using isolated synthetic pages."""
import argparse
import base64
import json
from pathlib import Path
import re

from accept_browser_candidate import accept, protocol
from mcp_probe import Client, result

CAPABILITIES = ['unsafe-code', 'vision', 'pdf', 'devtools', 'network', 'storage',
                'testing', 'tracing', 'config', 'extensions', 'pwa', 'webmcp',
                'third-party', 'memory']


async def optional_protocol(binary, root, workspace, invoke):
    await protocol(binary, root, workspace, invoke)
    invoke('tools', 'enable', 'browser', '--capabilities', ','.join(CAPABILITIES))
    client = Client([str(binary), '--root', str(root), 'tools', 'serve', 'browser',
                     '--workspace', str(workspace), '--engine', 'both'], workspace)
    await client.start()
    try:
        await client.initialize()
        tools = {tool['name']:tool for tool in result(await client.request('tools/list'))['tools']}
        required = {'browser_run_code_unsafe', 'browser_mouse_move_xy', 'browser_pdf_save',
                    'browser_localstorage_set', 'browser_verify_text_visible',
                    'browser_route', 'browser_start_tracing', 'browser_get_config',
                    'list_extensions', 'install_pwa', 'list_webmcp_tools',
                    'list_3p_developer_tools', 'take_heapsnapshot'}
        if not required <= tools.keys():
            raise ValueError('Optional capability discovery is incomplete: ' + str(required - tools.keys()))

        async def call(name, arguments=None):
            return result(await client.request('tools/call', {'name':name, 'arguments':arguments or {}}))

        # Interception supplies the complete response before any network dial.
        await call('browser_route', {'pattern':'http://loki-native.invalid/**',
                   'body':'<html><title>Loki optional fixture</title><h1>Loki optional fixture</h1></html>',
                   'contentType':'text/html'})
        await call('browser_navigate', {'url':'http://loki-native.invalid/fixture'})
        await call('browser_verify_text_visible', {'text':'Loki optional fixture'})
        await call('browser_mouse_move_xy', {'x':20, 'y':20})
        await call('browser_get_config')
        await call('browser_localstorage_set', {'key':'fixture', 'value':'isolated value'})
        storage = await call('browser_localstorage_get', {'key':'fixture'})
        if 'isolated value' not in json.dumps(storage):
            raise ValueError('Optional storage did not preserve synthetic data')
        unsafe = await call('browser_run_code_unsafe', {'code':'async page => await page.title()'})
        if 'Loki optional fixture' not in json.dumps(unsafe):
            raise ValueError('Explicit unsafe-code capability did not execute its synthetic fixture')
        await call('browser_pdf_save')
        await call('browser_storage_state')
        await call('browser_start_tracing')
        await call('browser_verify_text_visible', {'text':'Loki optional fixture'})
        await call('browser_stop_tracing')
        await call('browser_start_video', {'size':{'width':320, 'height':240}})
        await call('browser_wait_for', {'time':1})
        await call('browser_stop_video')
        engine = next(value for value in tools['loki_browser_files']['inputSchema']['properties']['engine']['enum'] if value.endswith('/playwright'))
        listing = await call('loki_browser_files', {'engine':engine, 'action':'list'})
        entries = json.loads(next(item['text'] for item in listing['content'] if item['type'] == 'text'))['files']
        for extension, signature in [('.pdf', b'%PDF'), ('.webm', b'\x1aE\xdf\xa3')]:
            files = [entry for entry in entries if entry['name'].endswith(extension)]
            if len(files) != 1:
                raise ValueError('Optional output missing or ambiguous: ' + extension)
            resource = result(await client.request('resources/read', {'uri':files[0]['uri']}))
            if not base64.b64decode(resource['contents'][0]['blob']).startswith(signature):
                raise ValueError('Optional output has invalid bytes: ' + extension)
        traces = [entry for entry in entries if entry['name'].endswith('.trace') and entry['bytes'] > 0]
        if len(traces) != 1:
            raise ValueError('Optional tracing did not create its owned action log')
        trace = result(await client.request('resources/read', {'uri':traces[0]['uri']}))
        events = base64.b64decode(trace['contents'][0]['blob']).decode().splitlines()
        if not events or not all(isinstance(json.loads(event), dict) for event in events):
            raise ValueError('Optional trace action log has invalid events')
        await call('browser_unroute')

        opened = await call('new_page', {'url':'data:text/html,<h1>Loki optional DevTools fixture</h1>'})
        text = '\n'.join(item.get('text', '') for item in opened['content'] if item['type'] == 'text')
        selected = re.findall(r'^(\d+): .* \[selected\]', text, re.MULTILINE)
        if len(selected) != 1:
            raise ValueError('Optional DevTools page identity is ambiguous')
        page_id = int(selected[0])
        await call('list_extensions')
        await call('list_webmcp_tools', {'pageId':page_id})
        await call('list_3p_developer_tools', {'pageId':page_id})

        # A changed selection must revoke the cached privileged tool immediately.
        invoke('tools', 'enable', 'browser', '--capabilities', '')
        denied = await client.request('tools/call', {'name':'browser_run_code_unsafe', 'arguments':{'code':'async page => await page.title()'}})
        if 'error' not in denied and not denied.get('result', {}).get('isError'):
            raise ValueError('Cached optional capability survived revocation')
        invoke('tools', 'disable', 'browser')
        print(json.dumps({'optional_browser_acceptance':'pass', 'capability_discovery':CAPABILITIES,
                          'executed':['vision pointer', 'pdf bytes', 'storage', 'testing', 'network interception',
                                      'tracing', 'video bytes', 'config', 'unsafe-code and revocation',
                                      'extensions listing', 'webmcp listing', 'third-party listing'],
                          'remaining':['interactive annotation UI', 'extension installation', 'PWA installation',
                                       'WebMCP invocation', 'third-party invocation', 'memory snapshot analysis']}))
    finally:
        await client.close()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate', required=True, type=Path)
    accept(parser.parse_args().candidate.resolve(), protocol_check=optional_protocol)
