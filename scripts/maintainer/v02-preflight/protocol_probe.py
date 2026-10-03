import asyncio
import argparse
import json
from pathlib import Path
import time
from probe import Client, compact

async def main():
    parser=argparse.ArgumentParser();parser.add_argument('--root',default='/tmp/loki-v02-preflight')
    base = Path(parser.parse_args().root).resolve()
    root = base/('protocol-'+str(time.time_ns()))
    root.mkdir(exist_ok=True)
    command=['node',str(base/'node_modules/@playwright/mcp/cli.js'),'--headless','--isolated','--sandbox','--executable-path',str(base/'chrome-linux64/chrome')]
    client=Client(command,root)
    report={'scope':'Upstream stdio protocol, cancellation, progress and invalid calls; not the Windows app path','command':command}
    try:
        await client.start()
        report['initialize']=await client.initialize()
        report['navigate']=compact(await client.request('tools/call',{'name':'browser_navigate','arguments':{'url':'data:text/html,<h1>Loki protocol fixture</h1>'}}))
        request_id=client.next_id
        wait=asyncio.create_task(client.request('tools/call',{'name':'browser_wait_for','arguments':{'time':15},'_meta':{'progressToken':'loki-cancel-probe'}},timeout=20))
        await asyncio.sleep(1)
        start=time.monotonic()
        await client.send({'jsonrpc':'2.0','method':'notifications/cancelled','params':{'requestId':request_id,'reason':'Preflight cancellation probe'}})
        try:
            response=await asyncio.wait_for(asyncio.shield(wait),3)
            report['cancel_response']=compact(response)
            report['cancel_response_within_3s']=True
        except asyncio.TimeoutError:
            report['cancel_response_within_3s']=False
        report['post_cancel_snapshot']=compact(await client.request('tools/call',{'name':'browser_snapshot','arguments':{}},timeout=20))
        report['post_cancel_latency_seconds']=round(time.monotonic()-start,3)
        if not wait.done(): wait.cancel()
        await asyncio.gather(wait,return_exceptions=True)
        report['unknown_method']=await client.request('loki/nonexistent',{})
        report['default_catalog']=await client.request('tools/list')
    finally:
        if hasattr(client,'process'): await client.close()
        report['notifications']=compact(client.messages)
        report['stderr']=client.stderr
        (base/'protocol-report.json').write_text(json.dumps(report,indent=2)+'\n')
        print(json.dumps({'cancel_response_within_3s':report.get('cancel_response_within_3s'),'post_cancel_latency_seconds':report.get('post_cancel_latency_seconds'),'default_tools':len(report.get('default_catalog',{}).get('result',{}).get('tools',[])),'progress_notifications':sum(x.get('method')=='notifications/progress' for x in client.messages),'unknown_method_error':report.get('unknown_method',{}).get('error')}))

if __name__=='__main__': asyncio.run(main())
