"""Read accepted dependency pins without querying or executing upstream code."""
import ast
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
POLICY = ROOT / 'packaging/tools/dependency-policy.json'


def read(path):
    return json.loads(path.read_text())


def literals(path):
    values = {}
    for node in ast.parse(path.read_text()).body:
        if isinstance(node, ast.Assign):
            for target in node.targets:
                if isinstance(target, ast.Name):
                    try:
                        values[target.id] = ast.literal_eval(node.value)
                    except (ValueError, TypeError):
                        pass
    return values


def inventory(root=ROOT):
    items = {}

    def add(key, kind, current, source, path, **extra):
        item = items.setdefault(key, dict(id=key, kind=kind, current=[], source=source, paths=[], **extra))
        if item['kind'] != kind or item['source'] != source:
            raise ValueError('Conflicting dependency source: '+key)
        if current not in item['current']:
            item['current'].append(current)
        relative = path.relative_to(root).as_posix()
        if relative not in item['paths']:
            item['paths'].append(relative)

    config = root/'packaging/tools/release.json'
    add('go', 'go', read(config)['go'], 'https://go.dev/dl/?mode=json', config)
    package = root/'modules/browser/package.json'
    for name, version in read(package)['dependencies'].items():
        add('npm:'+name, 'npm', version, name, package)
    for path in sorted((root/'packaging/tools/inputs').glob('browser-*-*.json')):
        for name, asset in read(path).get('assets', {}).items():
            kind, source = {'node': ('node', 'https://nodejs.org/dist/index.json'),
                            'chrome': ('chrome', 'https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json'),
                            'ffmpeg': ('ffmpeg', '@playwright/mcp')}[name]
            add(name, kind, asset['version'], source, path)
    owners = root/'scripts/maintainer/tools/build_go_vendor.py'
    for owner, value in literals(owners)['OWNERS'].items():
        repository = {'github': 'cli/cli', 'coordination': 'jinyongp/devtools'}[owner]
        add('tool:'+value['program'], 'github', value['version'], repository, owners)
    for path in sorted((root/'packaging/tools/inputs').glob('workspace-linux-*.json')):
        for value in read(path).get('inputs', []):
            add('tool:ripgrep', 'github', value['version'], 'BurntSushi/ripgrep', path)
    for path in sorted((root/'packaging/tools/inputs').glob('git-packages-linux-*.json')):
        recipe = read(path)
        for package in recipe['packages']:
            if package['name'] == 'git':
                arch = recipe['target']['arch']
                add('tool:git:'+arch, 'ubuntu-package', package['version'], 'git', path, arch=arch, series={'ubuntu-24.04': 'noble'}[recipe['distribution']])
    for path in sorted((root/'.github/workflows').glob('*.yml')):
        text = path.read_text()
        for repository, sha in re.findall(r'uses:\s+([\w.-]+/[\w.-]+)(?:/[\w./-]+)?@([a-f0-9]{40})\b', text):
            add('action:'+repository, 'action', sha, repository, path)
        for version in re.findall(r'version:\s*[\'\"]?(v\d+\.\d+\.\d+)', text):
            if 'docker/setup-buildx-action@' in text:
                add('tool:buildx', 'github', version, 'docker/buildx', path)
    for path in sorted((root/'packaging/tools/inputs').glob('images-linux-*.json')):
        recipe = read(path)
        arch = recipe['target']['arch']
        for role, value in recipe['registry_provenance'].items():
            repository, digest = value['reference'].removeprefix('docker.io/').split('@', 1)
            tag = value['version']
            add(f'image:{repository}:{tag}:{arch}', 'image', digest, repository, path, tag=tag, arch=arch)
            if role == 'buildkit':
                add('tool:buildkit', 'github', tag, 'moby/buildkit', path)
    # Direct Go dependencies are proposals; transitive changes remain lockfile/PR review.
    mod = root/'go.mod'
    for name, version in re.findall(r'^\s*([^\s/][^\s]*)\s+(v\d+\.[^\s]+)\s*$', mod.read_text(), re.M):
        add('gomod:'+name, 'gomod', version, name, mod)
    return [dict(item, current=sorted(item['current']), paths=sorted(item['paths'])) for _, item in sorted(items.items())]


def policy(path=POLICY):
    value = read(path)
    if value.get('schema') != 1 or set(value) - {'schema', 'holds', 'channels'}:
        raise ValueError('Invalid dependency policy')
    if not isinstance(value.get('holds'), dict):
        raise ValueError('Dependency holds must be keyed by inventory ID')
    if value.get('channels', {}).get('node', 'current') not in {'current', 'lts'} or set(value.get('channels', {})) - {'node'}:
        raise ValueError('Unsupported dependency channel')
    from datetime import date
    for key, hold in value['holds'].items():
        if not isinstance(hold, dict) or not hold.get('reason') or set(hold) - {'reason', 'until', 'version'}:
            raise ValueError('Invalid dependency hold: '+key)
        if not hold.get('until') and not hold.get('version'):
            raise ValueError('A hold needs an expiry date or exact candidate: '+key)
        if hold.get('until'):
            date.fromisoformat(hold['until'])
    return value
