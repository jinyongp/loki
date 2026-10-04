"""Verify anonymous public installer, accepted manifests and actual management setup."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request

from prepare_bootstrap import prepare
from release_config import RELEASE

BASE = 'https://github.com/jinyongp/loki/releases/download/v'+RELEASE+'/'


def download(url):
    with urllib.request.urlopen(url, timeout=120) as response:
        return response.read()


def verify(expected_directory=None):
    sums = dict((name, digest) for digest, name in (line.split() for line in download(BASE+'SHA256SUMS').decode().splitlines()))
    with tempfile.TemporaryDirectory(prefix='loki-public-install-') as temporary:
        root = Path(temporary)
        if expected_directory is None:
            prepare(root / 'bootstrap')
            expected_directory = root / 'bootstrap/pages'
        for extension in ('sh', 'ps1'):
            accepted = download(BASE+'loki-install.'+extension)
            if hashlib.sha256(accepted).hexdigest() != sums['loki-install.'+extension]:
                raise ValueError('release installer checksum differs')
            # Public bootstraps can receive independently accepted corrections.
            # Their manager/catalog pins still bind immutable release bytes.
            expected = (expected_directory / ('install.'+extension)).read_bytes()
            for attempt in range(12):
                current = download('https://jinyongp.dev/loki/install.'+extension+'?release='+RELEASE+'&attempt='+str(attempt))
                if current == expected:
                    break
                time.sleep(5)
            else:
                raise ValueError('public installer endpoint differs from accepted bootstrap')
            (root / ('install.'+extension)).write_bytes(current)
        evidence = json.loads(download(BASE+'loki-release-lock.json'))
        for unit in evidence['units'].values():
            if unit['kind'] != 'image':
                continue
            image = unit['image']
            repository, digest = image['reference'].removeprefix('ghcr.io/').split('@',1)
            token = json.loads(download('https://ghcr.io/token?service=ghcr.io&scope=repository:'+repository+':pull'))['token']
            request = urllib.request.Request('https://ghcr.io/v2/'+repository+'/manifests/'+digest,headers={'Authorization':'Bearer '+token,'Accept':'application/vnd.oci.image.manifest.v1+json'})
            with urllib.request.urlopen(request,timeout=120) as response:
                raw = response.read()
            if 'sha256:'+hashlib.sha256(raw).hexdigest() != digest:
                raise ValueError('anonymous published OCI manifest differs')
        subprocess.run(['sh',str(root/'install.sh'),'--bin-dir',str(root/'bin'),'--root',str(root/'management')],check=True)
        state = json.loads((root/'management/control/state.json').read_text())
        if state['installed'] or state['config']['tools']:
            raise ValueError('public bootstrap installed or enabled tools')
        for args in [[], ['tools'], ['integrations'], ['tools', 'install', '--help']]:
            result = subprocess.run([str(root/'bin/loki'), '--root', str(root/'management'), *args], capture_output=True, text=True)
            if result.returncode or result.stderr.strip() or 'Usage:' not in result.stdout or 'Examples:' not in result.stdout:
                raise ValueError('published CLI readable help failed')
        binary = str(root/'bin/loki')
        management = str(root/'management')
        checked = subprocess.check_output([binary, '--root', management, 'upgrade', '--check'], text=True)
        if 'Current: '+RELEASE not in checked or 'Target:  '+RELEASE not in checked:
            raise ValueError('public CLI upgrade release lookup failed')
        before = (root/'management/control/state.json').read_bytes()
        subprocess.run([binary, '--root', management, 'upgrade', '--version', RELEASE, '--force', '--yes'], check=True)
        if (root/'management/control/state.json').read_bytes() != before:
            raise ValueError('public CLI reinstall changed management configuration')
        print('Anonymous installer endpoints, 10 OCI manifests, native installation and CLI self-upgrade passed.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--expected-directory', type=Path)
    verify(parser.parse_args().expected_directory)
