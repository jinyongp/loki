"""Probe actual fallback rules using real closures and isolated changed-path input."""
import copy
from functools import lru_cache
import json
from pathlib import Path
import subprocess
import sys
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[5]
sys.path.insert(0,str(ROOT/'scripts/maintainer/tools'))
import plan_release as selection

original_closure=selection.go_closure
cached=lru_cache(None)(lambda packages,system,arch:original_closure(list(packages),system,arch))
with patch.object(selection,'go_closure',side_effect=lambda packages,system,arch:cached(tuple(packages),system,arch)):
    current,inputs=selection.fingerprints('0.2.4')
old={'schema':2,'contract':selection.CONTRACT,'release':'0.2.4','source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),'units':{k:dict(v,version='0.2.4',producer={'source_commit':'a'*40},acceptance={'passed':True}) for k,v in current.items()}}
original_output=selection.subprocess.check_output
results=[]
cases=[('test-only','scripts/maintainer/tools/test_release_pipeline.py',False),('fixture-unknown','internal/host/lifecycle/testdata/native-migration/main.go',True),('recipe-unknown','packaging/tools/inputs/candidate-linux-amd64-full.json',True),('deleted-baseline-unknown','removed.fixture',True)]
for label,path,forced in cases:
    baseline=copy.deepcopy(old)
    if label=='deleted-baseline-unknown':baseline['unmapped_inputs']=[path]
    def commands(command,*args,**kwargs):
        if command[:3]==['git','diff','--name-only']:return path+'\n'
        return original_output(command,*args,**kwargs)
    with patch.object(selection,'fingerprints',return_value=(current,inputs)),patch.object(selection.subprocess,'check_output',side_effect=commands):
        plan=selection.plan('0.2.4',baseline)
    all_forced=all(u['reason']=='forced' for u in plan['units'].values())
    assert all_forced==forced,(label,all_forced)
    assert forced or all(not plan['matrices'][m] for m in ('manager','browser','full'))
    results.append({'case':label,'changed_path':path,'all_forced':all_forced,'build_counts':{m:len(plan['matrices'][m]) for m in ('manager','browser','full')}})
changed=copy.deepcopy(current);changed['manager:linux:amd64']['bridge']='changed'
plan=selection.select('0.2.4',changed,old)
assert not any(plan['matrices'][m] for m in ('manager','browser','full'))
assert len(plan['matrices']['browser-checks'])==len(plan['matrices']['full-checks'])==1
results.append({'case':'bridge-checks-without-build-artifacts','build_counts':[0,0,0],'browser_checks':1,'full_checks':1})
Path(__file__).with_name('selection-experiment-results.json').write_text(json.dumps({'tests':len(results),'all_passed':True,'go_runtime':subprocess.check_output(['go','version'],text=True).strip(),'scope':'Real target closures; changed paths are mocked, no source/product mutation','cases':results},indent=2)+'\n')
print(json.dumps(results,indent=2))
