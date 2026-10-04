"""Bind public installers to the exact accepted manager and catalog bytes."""
import hashlib
import json
from pathlib import Path


def render(assets, output, release='0.2.1'):
    pins = {}
    for path in assets.iterdir():
        if path.name.startswith('loki-manager-') and path.suffix == '.zip' or path.name.startswith('loki-catalog-') and path.suffix == '.json':
            pins[path.name] = hashlib.sha256(path.read_bytes()).hexdigest()
    templates = Path(__file__).resolve().parent
    output.mkdir(parents=True, exist_ok=True)
    shell = (templates / 'public-install.sh.tmpl').read_text().replace('@@RELEASE@@', release).replace('@@PINS@@', '\n'.join(name+' '+digest for name, digest in sorted(pins.items())))
    (output / 'install.sh').write_text(shell)
    pins['loki-install.sh'] = hashlib.sha256(shell.encode()).hexdigest()
    powershell = (templates / 'public-install.ps1.tmpl').read_text().replace('@@RELEASE@@', release).replace('@@PINS@@', json.dumps(pins, sort_keys=True))
    (output / 'install.ps1').write_text(powershell)
    (assets / 'loki-install.sh').write_text(shell)
    (assets / 'loki-install.ps1').write_text(powershell)
    (output / '.nojekyll').write_text('')
    (output / 'index.html').write_text('<!doctype html><title>Loki</title><p>Install Loki with install.sh or install.ps1.</p>')
