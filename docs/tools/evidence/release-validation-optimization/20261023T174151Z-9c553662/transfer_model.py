"""Research model of sealing/verification placement. No publisher or network calls."""
import copy
import hashlib
import io
import json
from pathlib import Path
import unittest

def receipt(data):return {'bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()}

def verify(files,inventory):
    if set(files)!=set(inventory):raise ValueError('exact file set differs')
    for name,data in files.items():
        if receipt(data)!=inventory[name]:raise ValueError('asset bytes differ')

def seal(original,lock_digest,accepted_lock):
    inventory={name:receipt(data) for name,data in original.items()}
    if inventory['loki-release-lock.json']['sha256']!=lock_digest:raise ValueError('proof lock differs')
    inventory['loki-release-lock.json']=receipt(accepted_lock)
    checksums=''.join(inventory[n]['sha256']+'  '+n+'\n' for n in sorted(inventory)).encode()
    inventory['SHA256SUMS']=receipt(checksums)
    return inventory,{'loki-release-lock.json':accepted_lock,'SHA256SUMS':checksums}

def finish(files,inventory,accepted,publish):
    actual=dict(files);actual.update(accepted)
    verify(actual,inventory)
    return ['tag','release','pages'] if publish else []

class SealingPlacement(unittest.TestCase):
    def setUp(self):
        self.original={'archive.zip':b'exact artifact','loki-release-lock.json':b'unsealed lock','catalog.json':b'catalog'}
        self.inventory,self.accepted=seal(self.original,receipt(b'unsealed lock')['sha256'],b'exact accepted lock')
    def test_validation_only_still_checks_bytes_and_has_no_public_effects(self):
        self.assertEqual(finish(self.original,self.inventory,self.accepted,False),[])
        bad=dict(self.original,**{'archive.zip':b'wrong artifact'})
        with self.assertRaises(ValueError):finish(bad,self.inventory,self.accepted,False)
    def test_publication_is_after_exact_byte_validation(self):
        self.assertEqual(finish(self.original,self.inventory,self.accepted,True),['tag','release','pages'])
        for files in ({k:v for k,v in self.original.items() if k!='archive.zip'},dict(self.original,**{'extra.zip':b'extra'}),dict(self.original,**{'archive.zip':b'wrong artifact'})):
            for publish in (True,False):
                with self.subTest(publish=publish),self.assertRaises(ValueError):finish(files,self.inventory,self.accepted,publish)
    def test_preseal_proof_cannot_authorize_different_lock(self):
        with self.assertRaisesRegex(ValueError,'proof lock'):seal(self.original,'f'*64,b'accepted')
        bad=dict(self.accepted,**{'loki-release-lock.json':b'changed accepted lock'})
        with self.assertRaises(ValueError):finish(self.original,self.inventory,bad,True)
    def test_repeat_and_public_recovery_use_identical_final_bytes(self):
        before=copy.deepcopy(self.original);accepted_before=copy.deepcopy(self.accepted)
        first=finish(self.original,self.inventory,self.accepted,True)
        self.assertEqual(finish(self.original,self.inventory,self.accepted,True),first)
        public=dict(self.original);public.update(self.accepted)
        verify(public,self.inventory)
        self.assertEqual(finish(public,self.inventory,self.accepted,False),[])
        self.assertEqual(self.original,before);self.assertEqual(self.accepted,accepted_before)

if __name__=='__main__':
    output=io.StringIO();result=unittest.TextTestRunner(stream=output,verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(SealingPlacement))
    path=Path(__file__).with_name('transfer-model-results.json')
    with path.open('x') as f:json.dump({'tests':result.testsRun,'failures':len(result.failures),'errors':len(result.errors),'output':output.getvalue(),'scope':'Pure model; existing gate proof checks tested separately; no actual Actions/Releaseway/native product acceptance'},f,indent=2);f.write('\n')
    print(output.getvalue());raise SystemExit(not result.wasSuccessful())
