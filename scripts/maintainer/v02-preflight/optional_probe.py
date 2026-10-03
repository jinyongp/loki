import asyncio
import argparse
import json
import re
import struct
import zlib
import time
import sys
from pathlib import Path
from probe import Client, compact
from fixture import text, HTML

async def main():
    parser=argparse.ArgumentParser();parser.add_argument('--root',default='/tmp/loki-v02-preflight')
    base=Path(parser.parse_args().root).resolve(); root=base/('optional-'+str(time.time_ns())); root.mkdir(exist_ok=True)
    chrome=base/'chrome-linux64/chrome'
    command=['node',str(base/'node_modules/chrome-devtools-mcp/build/src/bin/chrome-devtools-mcp.js'),'--headless','--isolated','--executablePath',str(chrome),'--no-usage-statistics','--no-performance-crux','--categoryExtensions','--categoryPwa','--categoryExperimentalWebmcp','--categoryExperimentalThirdParty','--chromeArg=--enable-features=WebMCP']
    client=Client(command,root); report={'checks':[],'command':command}
    async def call(name,args=None):
        try:
            r=compact(await client.request('tools/call',{'name':name,'arguments':args or {}},timeout=30))
            passed='error' not in r and not r.get('result',{}).get('isError',False)
        except Exception as e:
            r={'exception':type(e).__name__,'message':str(e)}; passed=False
        report['checks'].append({'name':name,'passed':passed,'response':r})
        if passed and name in ('execute_webmcp_tool','execute_3p_developer_tool'):
            report['checks'][-1]['passed']='"echo": "Loki"' in text(r)
        print(json.dumps({'tool':name,'passed':passed}),flush=True)
        return r
    async def serve(reader,writer):
        try:
            req=await reader.readuntil(b'\r\n\r\n'); path=req.split(b' ')[1].decode(); mime='text/html'
            if path=='/manifest.json':
                body=json.dumps({'id':'/','name':'Loki isolated fixture','short_name':'Loki','start_url':'/','display':'browser','icons':[{'src':'/icon.png','sizes':'512x512','type':'image/png'}]}).encode();mime='application/manifest+json'
            elif path=='/icon.png':
                def chunk(kind,data):return struct.pack('>I',len(data))+kind+data+struct.pack('>I',zlib.crc32(kind+data)&0xffffffff)
                body=b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('>IIBBBBB',512,512,8,2,0,0,0))+chunk(b'IDAT',zlib.compress((b'\x00'+b'\x88\x66\xcc'*512)*512))+chunk(b'IEND',b'');mime='image/png'
            elif path=='/sw.js':body=b"self.addEventListener('fetch', e => e.respondWith(fetch(e.request)))";mime='application/javascript'
            else:
                extra="""<script>navigator.serviceWorker.register('/sw.js');
                window.addEventListener('devtoolstooldiscovery',e=>e.respondWith({name:'loki-fixture',tools:[{name:'loki_echo',description:'Synthetic fixture echo',inputSchema:{type:'object',properties:{value:{type:'string'}},required:['value']},execute:args=>({echo:args.value})}]}));
                const context=document.modelContext || navigator.modelContext;
                if(context) context.registerTool({name:'loki_echo',description:'Synthetic fixture echo',inputSchema:{type:'object',properties:{value:{type:'string'}},required:['value']},execute:args=>({echo:args.value})});</script>"""
                body=(HTML+extra).encode()
            writer.write(f'HTTP/1.1 200 OK\r\nContent-Type: {mime}\r\nContent-Length: {len(body)}\r\nConnection: close\r\n\r\n'.encode()+body);await writer.drain()
        finally: writer.close();await writer.wait_closed()
    server=await asyncio.start_server(serve,'127.0.0.1',0)
    url=f'http://127.0.0.1:{server.sockets[0].getsockname()[1]}/'
    try:
        await client.start();report['initialize']=await client.initialize()
        r=await call('new_page',{'url':url}); page=int(re.findall(r'^(\d+): .* \[selected\]',text(r),re.M)[-1])
        extension=root/'extension';extension.mkdir(exist_ok=True)
        (extension/'manifest.json').write_text(json.dumps({'manifest_version':3,'name':'Loki temporary fixture','version':'1.0','action':{'default_popup':'popup.html'}}))
        (extension/'popup.html').write_text('<h1>Loki temporary extension</h1>')
        r=await call('install_extension',{'path':str(extension)})
        match=re.search(r'\b[a-p]{32}\b',text(r))
        if match:
            extension_id=match.group(0)
            await call('list_extensions');await call('reload_extension',{'id':extension_id})
            await call('trigger_extension_action',{'id':extension_id});await call('uninstall_extension',{'id':extension_id})
        await call('install_pwa',{'manifestId':url,'installUrlOrBundleUrl':url,'displayMode':'browser'})
        await call('get_os_app_state',{'manifestId':url})
        await call('launch_pwa',{'manifestId':url})
        await call('uninstall_pwa',{'manifestId':url})
        await call('evaluate_script',{'pageId':page,'function':'() => ({modelContext:typeof navigator.modelContext,documentModelContext:typeof document.modelContext,modelContextTesting:typeof navigator.modelContextTesting})'})
        await call('list_webmcp_tools',{'pageId':page})
        await call('execute_webmcp_tool',{'pageId':page,'toolName':'loki_echo','input':'{"value":"Loki"}'})
        await call('list_3p_developer_tools',{'pageId':page})
        await call('execute_3p_developer_tool',{'pageId':page,'toolName':'loki_echo','params':'{"value":"Loki"}'})
        await call('close_page',{'pageId':page})
    finally:
        server.close();await server.wait_closed()
        if hasattr(client,'process'):await client.close()
        report['stderr']=client.stderr
        (base/'optional-report.json').write_text(json.dumps(report,indent=2)+'\n')
    return 0 if all(c['passed'] for c in report['checks']) else 1

if __name__=='__main__':sys.exit(asyncio.run(main()))
