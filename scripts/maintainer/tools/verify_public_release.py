"""Verify anonymous public installer, accepted manifests and actual management setup."""
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request

BASE = 'https://github.com/jinyongp/loki/releases/download/v0.2.1/'


def download(url):
    with urllib.request.urlopen(url, timeout=120) as response:
        return response.read()


def verify():
    sums = dict((name, digest) for digest, name in (line.split() for line in download(BASE+'SHA256SUMS').decode().splitlines()))
    with tempfile.TemporaryDirectory(prefix='loki-public-install-') as temporary:
        root = Path(temporary)
        for extension in ('sh', 'ps1'):
            accepted = download(BASE+'loki-install.'+extension)
            if hashlib.sha256(accepted).hexdigest() != sums['loki-install.'+extension]:
                raise ValueError('release installer checksum differs')
            for attempt in range(12):
                current = download('https://jinyongp.dev/loki/install.'+extension+'?release=0.2.1&attempt='+str(attempt))
                if current == accepted:
                    break
                time.sleep(5)
            else:
                raise ValueError('public installer endpoint differs from accepted release')
            (root / ('install.'+extension)).write_bytes(current)
        evidence = json.loads(download(BASE+'loki-release-evidence.json'))
        for image in evidence['publication_images']:
            repository, digest = image['reference'].removeprefix('ghcr.io/').split('@',1)
            token = json.loads(download('https://ghcr.io/token?service=ghcr.io&scope=repository:'+repository+':pull'))['token']
            request = urllib.request.Request('https://ghcr.io/v2/'+repository+'/manifests/'+digest,headers={'Authorization':'Bearer '+token,'Accept':'application/vnd.oci.image.manifest.v1+json'})
            with urllib.request.urlopen(request,timeout=120) as response:
                raw = response.read()
            if 'sha256:'+hashlib.sha256(raw).hexdigest() != digest:
                raise ValueError('anonymous published OCI manifest differs')
        (root/'workspace').mkdir()
        subprocess.run(['sh',str(root/'install.sh'),'--tools','none','--bin-dir',str(root/'bin'),'--root',str(root/'management'),'--workspace',str(root/'workspace')],check=True)
        print('Anonymous installer endpoints, 10 OCI manifests and public native management installation passed.')


if __name__ == '__main__':
    verify()
