"""Render a bootstrap correction against unchanged immutable release assets."""
import argparse
import hashlib
from pathlib import Path
import urllib.request

from render_public_installers import render

BASE = 'https://github.com/jinyongp/loki/releases/download/v0.2.1/'


def download(name):
    with urllib.request.urlopen(BASE + name, timeout=120) as response:
        return response.read()


def prepare(output):
    assets = output / 'assets'
    assets.mkdir(parents=True, exist_ok=True)
    sums = dict((name, digest) for digest, name in
                (line.split() for line in download('SHA256SUMS').decode().splitlines()))
    for name, digest in sums.items():
        if ((name.startswith('loki-manager-') and name.endswith('.zip')) or
                (name.startswith('loki-catalog-') and name.endswith('.json'))):
            raw = download(name)
            if hashlib.sha256(raw).hexdigest() != digest:
                raise ValueError('immutable release asset checksum differs: ' + name)
            (assets / name).write_bytes(raw)
    render(assets, output / 'pages')
    original = download('loki-install.ps1')
    if hashlib.sha256(original).hexdigest() != sums['loki-install.ps1']:
        raise ValueError('original release PowerShell installer checksum differs')
    (output / 'original.ps1').write_bytes(original)
    # Windows delegates WSL installation to this immutable release attachment.
    # A shell bootstrap change needs its own release/delegation update.
    shell = (output / 'pages/install.sh').read_bytes()
    if hashlib.sha256(shell).hexdigest() != sums['loki-install.sh']:
        raise ValueError('bootstrap-only publication must preserve the release shell installer')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    prepare(parser.parse_args().output.resolve())
