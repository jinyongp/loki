"""Isolated research checks against existing functions; never launches products."""
import asyncio
import base64
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[5]
sys.path.insert(0,str(ROOT/'scripts/maintainer/tools'))
import accept_browser_candidate as browser
import accept_browser_capabilities as capabilities
import release_pipeline as pipeline
import release_gate
from test_release_pipeline import fixture
from plan_release import select

class OptionalReached(Exception):pass

class FakeClient:
    fault=None
    def __init__(self,*args):self.disabled=False
    async def start(self):pass
    async def initialize(self):pass
    async def close(self):pass
    async def request(self,method,params=None):
        p=params or {};name=p.get('name');args=p.get('arguments',{})
        def text(value):return {'result':{'content':[{'type':'text','text':value}]}}
        png=base64.b64encode(b'\x89PNG\r\n\x1a\nfixture').decode()
        if method=='tools/list':
            tools=[{'name':'browser_navigate'},{'name':'navigate_page'},{'name':'loki_browser_files','inputSchema':{'properties':{'engine':{'enum':['fixture/playwright']}}}}]
            if self.fault=='duplicate':tools.append(tools[0])
            if self.fault=='unsafe':tools.append({'name':'browser_run_code_unsafe'})
            return {'result':{'tools':tools}}
        if method=='resources/read':
            data=b'wrong' if self.fault=='resource' else b'Native session transfer fixture\n'
            return {'result':{'contents':[{'blob':base64.b64encode(data).decode()}]}}
        if name=='browser_navigate' and self.disabled:
            return {'result':{}} if self.fault=='revocation' else {'error':{'code':-1}}
        if name in ('browser_snapshot','take_snapshot'):
            return text('wrong' if self.fault==name else 'Loki native candidate fixture')
        if name=='new_page':return text('unselected' if self.fault=='selection' else '1: fixture [selected]')
        if name=='loki_browser_files':
            if args['action']=='stage':return text(json.dumps({'uri':'fixture:'}))
            if args['action']=='list':return text(json.dumps({'files':[] if self.fault=='missing_png' else [{'name':'shot.png'}]}))
            if args['action']=='read':return {'result':{'content':[{'type':'image','data':base64.b64encode(b'bad').decode() if self.fault=='png' else png}]}}
        if name=='browser_take_screenshot':return {'result':{'content':[{'type':'image','data':png}]}}
        return {'result':{}}

class BrowserEquivalence(unittest.TestCase):
    def invoke_variant(self,retained,fault):
        client=FakeClient();client.fault=fault
        def invoke(*args):client.disabled=True
        def optional_boundary(*args):raise OptionalReached()
        with patch.object(browser,'Client',return_value=client),patch.object(capabilities,'Client',side_effect=optional_boundary):
            operation=capabilities.optional_protocol if retained else browser.protocol
            try:asyncio.run(operation(Path('/not-executed'),Path('/not-executed'),Path('/not-executed'),invoke))
            except OptionalReached:return 'basic-pass'
            except ValueError as e:return str(e)
        return 'basic-pass'
    def test_real_retained_callback_detects_same_nine_faults(self):
        self.assertEqual(self.invoke_variant(False,None),'basic-pass')
        self.assertEqual(self.invoke_variant(True,None),'basic-pass')
        for fault in ('duplicate','unsafe','resource','revocation','browser_snapshot','take_snapshot','selection','missing_png','png'):
            with self.subTest(fault=fault):
                first=self.invoke_variant(False,fault);retained=self.invoke_variant(True,fault)
                self.assertNotEqual(first,'basic-pass');self.assertEqual(first,retained)

class Materialization(unittest.TestCase):
    def test_manager_ignores_other_families_and_reuse_is_verified(self):
        for local in (True,False):
            with self.subTest(local=local),tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp);built=root/'built';(built/'assets').mkdir(parents=True)
                data=b'exact manager';sha=__import__('hashlib').sha256(data).hexdigest()
                record={'url':'https://github.com/jinyongp/loki/releases/download/v0.2.4/manager.zip','bytes':len(data),'sha256':sha,'receipt':{'archive':'manager.zip'}}
                pipeline.write(built/'assets/loki-release-lock.json',{'units':{'manager:linux:amd64':{'artifact':record}}})
                if local:(built/'manager.zip').write_bytes(data)
                (built/'unused-full.zip').write_bytes(b'ignored')
                def download(actual,dest):
                    self.assertEqual(actual,record);dest.write_bytes(data)
                with patch.object(pipeline,'native',return_value=('linux','amd64')),patch.object(pipeline,'download',side_effect=download) as remote:
                    dest=pipeline.materialize({},built,root/'out','manager')
                    self.assertEqual((dest/'manager.zip').read_bytes(),data)
                    self.assertEqual(remote.call_count,0 if local else 1)
    def test_present_corrupt_manager_is_not_hidden_by_public_fallback(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);built=root/'built';(built/'assets').mkdir(parents=True)
            pipeline.write(built/'assets/loki-release-lock.json',{'units':{'manager:linux:amd64':{'artifact':{'url':'https://example.invalid/manager.zip','bytes':3,'sha256':'a'*64}}}})
            (built/'manager.zip').write_bytes(b'bad')
            with patch.object(pipeline,'native',return_value=('linux','amd64')),patch.object(pipeline,'download') as remote:
                with self.assertRaisesRegex(ValueError,'manager differs'):pipeline.materialize({},built,root/'out','manager')
                remote.assert_not_called()

class GateCases(unittest.TestCase):
    def cohort(self,root):
        current,old=fixture();plan=select('0.2.5',current,old);plan['installer_fingerprint']='fixture'
        lock={'release':plan['release'],'source_commit':plan['source_commit'],'units':copy.deepcopy(old['units'])}
        for key,u in plan['units'].items():
            if u['action']=='build':lock['units'][key]=dict(u,producer={'source_commit':plan['source_commit']})
        pipeline.write(root/'assets/loki-release-lock.json',lock)
        (root/'assets/fixture.zip').write_bytes(b'exact test bytes')
        digest=pipeline.digest(root/'assets/loki-release-lock.json')
        for t in plan['matrices']['manager-checks']:
            proof=release_gate.evidence(plan,'manager',t['os'],t['arch'],digest);proof['run_attempt']='1'
            pipeline.write(root/'checks'/t['os']/t['arch']/'acceptance.json',proof)
        results={name:'success' for name in ('plan','source-checks','assemble','manager-build','manager-checks')}
        results.update({name:'skipped' for name in ('browser-build','full-build','browser-checks','full-checks')})
        return plan,results
    def test_actual_gate_rejects_identity_proof_and_status_faults(self):
        for fault in ('none','missing','different_sha','different_run','future_attempt','lock_digest','required_failed','required_cancelled','unselected_cancelled'):
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as tmp,patch.dict(os.environ,{'GITHUB_RUN_ID':'42','GITHUB_RUN_ATTEMPT':'2'}):
                root=Path(tmp);plan,results=self.cohort(root)
                proofpath=next((root/'checks').rglob('acceptance.json'));proof=pipeline.read(proofpath)
                fields={'different_sha':('source_commit','b'*40),'different_run':('run_id','99'),'future_attempt':('run_attempt','3'),'lock_digest':('lock_sha256','a'*64)}
                if fault=='missing':proofpath.unlink()
                elif fault in fields:
                    k,v=fields[fault];proof[k]=v;pipeline.write(proofpath,proof)
                elif fault=='required_failed':results['manager-checks']='failure'
                elif fault=='required_cancelled':results['manager-checks']='cancelled'
                elif fault=='unselected_cancelled':results['browser-checks']='cancelled'
                if fault=='none':
                    release_gate.gate(plan,root,root/'checks',results)
                    self.assertTrue((root/'assets/SHA256SUMS').is_file())
                else:
                    with self.assertRaises(ValueError):release_gate.gate(plan,root,root/'checks',results)
    def test_recovery_preserves_original_sealed_bytes(self):
        with tempfile.TemporaryDirectory() as tmp,patch.dict(os.environ,{'GITHUB_RUN_ID':'42','GITHUB_RUN_ATTEMPT':'2'}):
            root=Path(tmp);plan,results=self.cohort(root);plan['publication_recovery']=[{'fixture':'public assets'}]
            before=(root/'assets/loki-release-lock.json').read_bytes()
            release_gate.gate(plan,root,root/'checks',results)
            self.assertEqual((root/'assets/loki-release-lock.json').read_bytes(),before)
    def test_inventory_model_rejects_missing_extra_and_corrupt_bytes(self):
        def verify(files,inventory):
            if set(files)!=set(inventory):raise ValueError('file set')
            for name,data in files.items():
                if len(data)!=inventory[name][0] or __import__('hashlib').sha256(data).hexdigest()!=inventory[name][1]:raise ValueError('digest')
        files={'asset.zip':b'exact'};inventory={'asset.zip':(5,__import__('hashlib').sha256(b'exact').hexdigest())}
        verify(files,inventory)
        for changed in ({},{**files,'extra.zip':b'x'},{'asset.zip':b'wrong'}):
            with self.assertRaises(ValueError):verify(changed,inventory)

if __name__=='__main__':
    stream=io.StringIO();result=unittest.TextTestRunner(stream=stream,verbosity=2).run(unittest.defaultTestLoader.loadTestsFromModule(sys.modules[__name__]))
    out=Path(__file__).with_name('contract-experiment-results.json')
    out.write_text(json.dumps({'tests':result.testsRun,'failures':len(result.failures),'errors':len(result.errors),'output':stream.getvalue(),'scope':'Real existing functions with mocked process/network boundaries; inventory verification is a model, not implemented product behavior'},indent=2)+'\n')
    print(stream.getvalue());sys.exit(not result.wasSuccessful())
