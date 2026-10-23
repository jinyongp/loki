import concurrent.futures
import argparse
import datetime as dt
import hashlib
import json
import pathlib
import subprocess
import sys
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[5]
OUT = ROOT/'docs/tools/evidence/release-validation-optimization/20261023T174151Z-9c553662'
REPO = 'repos/jinyongp/loki'

def api(path):
    return json.loads(subprocess.check_output(['gh','api',path],timeout=30))

def save(name,value):
    with (OUT/name).open('x') as stream:
        stream.write(json.dumps(value,ensure_ascii=False,indent=2)+'\n')

def seconds(start,end):
    if not start or not end:return None
    value=(dt.datetime.fromisoformat(end.replace('Z','+00:00'))-dt.datetime.fromisoformat(start.replace('Z','+00:00'))).total_seconds()
    return value if value>=0 else None

def job(j):
    keys=('id','name','status','conclusion','created_at','started_at','completed_at','run_attempt','runner_name')
    result={k:j.get(k) for k in keys}
    result['seconds']=seconds(j.get('started_at'),j.get('completed_at')) if j.get('conclusion')!='skipped' else None
    result['queue_seconds']=seconds(j.get('created_at'),j.get('started_at'))
    result['steps']=[{**{k:s.get(k) for k in ('number','name','status','conclusion','started_at','completed_at')},'seconds':seconds(s.get('started_at'),s.get('completed_at')) if s.get('conclusion')!='skipped' else None} for s in j.get('steps',[])]
    return result

def collect(run_id):
    r=api(f'{REPO}/actions/runs/{run_id}')
    result={k:r.get(k) for k in ('id','name','event','status','conclusion','head_sha','created_at','run_started_at','updated_at','run_attempt')}
    result['attempts']=[]
    for attempt in range(1,r['run_attempt']+1):
        j=api(f'{REPO}/actions/runs/{run_id}/attempts/{attempt}/jobs?per_page=100')
        assert j['total_count']<=100, 'Pagination required'
        result['attempts'].append({'attempt':attempt,'jobs':[job(x) for x in j['jobs']]})
    arts=api(f'{REPO}/actions/runs/{run_id}/artifacts?per_page=100')
    assert arts['total_count']<=100
    result['artifacts']=[{k:a.get(k) for k in ('id','name','size_in_bytes','digest','expired','created_at')} for a in arts['artifacts']]
    save(f'run-{run_id}.json',result)
    return result

class ReducerTests(unittest.TestCase):
    def test_elapsed_not_parallel_sum(self):
        self.assertEqual(seconds('2026-01-01T00:00:00Z','2026-01-01T00:01:30Z'),90)
        self.assertIsNone(seconds('2026-01-01T00:02:00Z','2026-01-01T00:01:30Z'))
        self.assertIsNone(seconds(None,'2026-01-01T00:01:30Z'))
    def test_skipped_synthetic_timestamps_not_cost(self):
        self.assertIsNone(job({'conclusion':'skipped','started_at':'2026-01-01T00:00:00Z','completed_at':'2026-01-01T00:01:00Z'})['seconds'])

def main():
    global OUT
    parser=argparse.ArgumentParser()
    parser.add_argument('--output',type=pathlib.Path)
    parser.add_argument('--test-only',action='store_true')
    args=parser.parse_args()
    suite=unittest.defaultTestLoader.loadTestsFromTestCase(ReducerTests)
    result=unittest.TextTestRunner(verbosity=1).run(suite)
    assert result.wasSuccessful()
    if args.test_only:return
    if args.output:OUT=args.output.resolve()
    OUT.mkdir(parents=True,exist_ok=True)
    if list(OUT.glob('run-*.json')) or (OUT/'history-summary.json').exists():
        raise SystemExit('Existing evidence is immutable; select a fresh --output directory.')
    runs=[39984988033,39981008103,39977174119,38996724241,38851251342,38777137633]
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:records=list(pool.map(collect,runs))
    summaries=[]
    for r in records:
        unique={}
        for a in r['attempts']:
            for j in a['jobs']:
                unique.setdefault((j['name'],j['started_at'],j['completed_at']),j)
        latest=r['attempts'][-1]['jobs']
        summaries.append({'run':r['id'],'sha':r['head_sha'],'status':r['status'],'conclusion':r['conclusion'],'attempts':r['run_attempt'],'observed_runner_seconds':sum(j['seconds'] or 0 for j in unique.values()),'stages':[{'name':j['name'],'seconds':j['seconds'],'steps':[{'name':s['name'],'seconds':s['seconds']} for s in j['steps'] if (s['seconds'] or 0)>=30]} for j in latest if j['name'].split(' (')[0] in {'assemble','gate','publish','source-checks','full-checks','browser-checks'}],'assets_bytes':next((a['size_in_bytes'] for a in r['artifacts'] if a['name']=='publication-assets'),None)})
    save('history-summary.json',{'as_of_utc':dt.datetime.now(dt.timezone.utc).isoformat(),'source_sha':subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),'reducer_tests':result.testsRun,'runs':summaries,'limits':'Historic runs differ in source/workload/cache. In-progress steps have no completed duration. No causal candidate time comparison.'})
    print(json.dumps({'runs':len(summaries),'output':str(OUT),'reducer_tests':result.testsRun},ensure_ascii=False))

if __name__=='__main__':main()
