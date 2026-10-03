import asyncio
import hashlib
import json
import re
from pathlib import Path
from probe import compact

HTML = '''<!doctype html><html><head><title>Loki fixture</title><link rel="manifest" href="/manifest.json"></head><body>
<h1>Loki preflight fixture</h1><label>Name <input id="name"></label>
<button id="increment" onclick="document.querySelector('#count').textContent=Number(document.querySelector('#count').textContent)+1">Increment</button><output id="count">0</output>
<label>Upload file <input id="upload" type="file" onchange="document.querySelector('#uploaded').textContent=this.files[0].name"></label><output id="uploaded"></output>
<a id="download" href="/download.txt" download="fixture.txt">Download fixture</a>
<script>console.log('loki fixture console');fetch('/api').then(r=>r.json()).then(v=>window.fixtureApi=v);</script></body></html>'''

def text(response):
    return '\n'.join(x.get('text','') for x in response.get('result',{}).get('content',[]) if x.get('type')=='text')

async def exercise(client, engine, root, report):
    async def serve(reader, writer):
        try:
            request = await reader.readuntil(b'\r\n\r\n')
            path = request.split(b' ')[1].decode().split('?')[0]
            mime, extra = 'text/html', ''
            if path == '/api': body, mime = b'{"fixture":true}', 'application/json'
            elif path == '/mock': body, mime = b'original', 'text/plain'
            elif path == '/download.txt': body, mime, extra = b'Loki fixture download\n', 'text/plain', 'Content-Disposition: attachment; filename="fixture.txt"\r\n'
            elif path == '/manifest.json':
                body, mime = b'{"id":"/","name":"Loki fixture","short_name":"Loki","start_url":"/","display":"standalone"}', 'application/manifest+json'
            else: body = HTML.encode()
            writer.write((f'HTTP/1.1 200 OK\r\nContent-Type: {mime}\r\nContent-Length: {len(body)}\r\n{extra}Connection: close\r\n\r\n').encode()+body)
            await writer.drain()
        finally:
            writer.close()
            await writer.wait_closed()
    server = await asyncio.start_server(serve, '127.0.0.1', 0)
    url = 'http://127.0.0.1:' + str(server.sockets[0].getsockname()[1]) + '/'
    report['fixture_url'] = url
    (root/'upload.txt').write_text('Synthetic Loki upload\n')
    async def call(name, arguments=None, expect_error=False, verify=None):
        result = compact(await client.request('tools/call', {'name':name, 'arguments':arguments or {}, '_meta':{'progressToken':'preflight-'+name}}, timeout=60))
        error = 'error' in result or result.get('result',{}).get('isError',False)
        passed = bool(error) == expect_error
        if passed and verify is not None: passed = bool(verify(result))
        report['checks'].append({'name':name,'arguments':arguments or {},'expected_error':expect_error,'passed':passed,'response':result})
        print(json.dumps({'engine':engine,'tool':name,'passed':passed,'error':error}),flush=True)
        return result
    try:
        if engine == 'playwright':
            await call('browser_navigate', {'url':url})
            await call('browser_snapshot', verify=lambda r:'Loki preflight fixture' in text(r))
            await call('browser_type', {'target':'#name','text':'Loki'})
            await call('browser_click', {'target':'#increment'})
            await call('browser_evaluate', {'function':"() => ({name:document.querySelector('#name').value,count:document.querySelector('#count').textContent,api:window.fixtureApi})"}, verify=lambda r:'"count": "1"' in text(r) and '"name": "Loki"' in text(r))
            await call('browser_take_screenshot', {'scale':'css'}, verify=lambda r:any(x.get('type')=='image' and x.get('bytes',0)>0 for x in r.get('result',{}).get('content',[])))
            await call('browser_click', {'target':'#upload'})
            await call('browser_file_upload', {'paths':[str(root/'upload.txt')]})
            await call('browser_evaluate', {'function':"() => document.querySelector('#uploaded').textContent"}, verify=lambda r:'upload.txt' in text(r))
            await call('browser_click', {'target':'#download'})
            await call('browser_cookie_set', {'name':'loki_fixture','value':'yes','domain':'127.0.0.1','path':'/'})
            await call('browser_cookie_list', verify=lambda r:'loki_fixture' in text(r))
            await call('browser_localstorage_set', {'key':'loki_fixture','value':'yes'})
            await call('browser_localstorage_get', {'key':'loki_fixture'}, verify=lambda r:'yes' in text(r))
            await call('browser_storage_state', {'filename':'storage.json'})
            await call('browser_route', {'pattern':'**/mock','body':'mocked','contentType':'text/plain'})
            await call('browser_evaluate', {'function':"async () => await (await fetch('/mock')).text()"}, verify=lambda r:'mocked' in text(r))
            await call('browser_network_requests')
            await call('browser_console_messages')
            await call('browser_pdf_save', {'filename':'page.pdf'})
            await call('browser_verify_text_visible', {'text':'Loki preflight fixture'})
            await call('browser_start_tracing')
            await call('browser_click', {'target':'#increment'})
            await call('browser_stop_tracing')
            await call('browser_run_code_unsafe', {'code':'async (page) => await page.title()'}, verify=lambda r:'Loki fixture' in text(r))
            await call('browser_start_video', {'filename':'fixture.webm'})
            await call('browser_stop_video')
            await call('browser_click', {}, expect_error=True)
            await call('loki_nonexistent_tool', {}, expect_error=True)
            await call('browser_close')
        else:
            response=await call('new_page', {'url':url})
            selected=re.findall(r'^(\d+): .* \[selected\]',text(response),re.MULTILINE)
            if not selected: raise RuntimeError('No selected page ID')
            page=int(selected[-1]); common={'pageId':page}
            snapshot=await call('take_snapshot',common)
            def uid(role, label):
                match=re.search(r'uid=([\w_-]+) '+role+r' "'+re.escape(label),text(snapshot))
                if not match: raise RuntimeError('Missing fixture snapshot target '+role+' '+label)
                return match.group(1)
            await call('fill', {**common,'uid':uid('textbox','Name'),'value':'Loki'})
            await call('click', {**common,'uid':uid('button','Increment')})
            await call('evaluate_script', {**common,'function':"() => ({name:document.querySelector('#name').value,count:document.querySelector('#count').textContent})"}, verify=lambda r:'"count":"1"' in text(r).replace(' ','') and 'Loki' in text(r))
            await call('take_screenshot',common, verify=lambda r:any(x.get('type')=='image' and x.get('bytes',0)>0 for x in r.get('result',{}).get('content',[])))
            await call('take_screenshot',{**common,'filePath':str(root/'screenshot.png')})
            await call('upload_file', {**common,'uid':uid('button','Upload file'),'filePaths':[str(root/'upload.txt')]})
            await call('evaluate_script',{**common,'function':"() => document.querySelector('#uploaded').textContent"},verify=lambda r:'upload.txt' in text(r))
            await call('list_network_requests',common)
            await call('list_console_messages',common)
            await call('emulate', {**common,'viewport':'800x600x1','colorScheme':'dark'})
            await call('get_css_styles',{**common,'uid':uid('button','Increment')})
            await call('performance_start_trace',{**common,'reload':False,'autoStop':False})
            await call('evaluate_script',{**common,'function':'() => { for(let i=0;i<10000;i++) Math.sqrt(i); return true; }'})
            await call('performance_stop_trace',{**common,'filePath':str(root/'trace.json.gz')})
            await call('take_heapsnapshot',{**common,'filePath':str(root/'fixture.heapsnapshot')})
            await call('get_heapsnapshot_summary',{'filePath':str(root/'fixture.heapsnapshot')})
            await call('list_extensions')
            await call('list_webmcp_tools',common)
            await call('list_3p_developer_tools',common)
            await call('lighthouse_audit',{**common,'mode':'snapshot','outputDirPath':str(root/'lighthouse')})
            await call('click',{},expect_error=True)
            await call('loki_nonexistent_tool',{},expect_error=True)
            await call('close_page',common)
    finally:
        server.close(); await server.wait_closed()
        report['files']=[{'path':str(p.relative_to(root)), 'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest()} for p in root.rglob('*') if p.is_file() and not any(x in ('cache','data','config','home') for x in p.relative_to(root).parts)]
