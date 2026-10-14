"""Verify accepted pin consistency offline; newer upstream versions never gate CI."""
import re
from dependency_inventory import ROOT, inventory, literals, policy, read


def verify(root=ROOT):
    config = read(root/'packaging/tools/release.json')
    package = read(root/'modules/browser/package.json')
    lock = read(root/'package-lock.json')['packages']
    acquisition = literals(root/'scripts/maintainer/tools/acquire_browser_inputs.py')['VERSIONS']
    assembly = literals(root/'scripts/maintainer/tools/build_browser_bundle.py')['VERSIONS']
    if acquisition != assembly:
        raise ValueError('Browser acquisition and assembly pins differ')
    engines = [read(root/'package.json'), package, lock[''], lock['modules/browser']]
    if any(item.get('engines', {}).get('node') != acquisition['node'] for item in engines):
        raise ValueError('Node engine pins differ between manifests, lock and acquisition')
    for name, version in package['dependencies'].items():
        if lock['node_modules/'+name]['version'] != version or lock['modules/browser']['dependencies'][name] != version:
            raise ValueError('npm manifest and lock pins differ: '+name)
    constants = dict(re.findall(r'^const (\w+) = "([^"]+)"', (root/'modules/browser/browser.go').read_text(), re.M))
    expected = {'NodeVersion': acquisition['node'], 'ChromeVersion': acquisition['chrome'],
                'PlaywrightVersion': package['dependencies']['@playwright/mcp'],
                'DevToolsVersion': package['dependencies']['chrome-devtools-mcp'],
                'PlaywrightCoreVersion': lock['node_modules/playwright-core']['version']}
    if any(constants.get(key) != version for key, version in expected.items()):
        raise ValueError('Runtime browser version checks differ from accepted inputs')
    for path in (root/'packaging/tools/inputs').glob('browser-*-*.json'):
        for name, asset in read(path).get('assets', {}).items():
            if asset['version'] != acquisition[name] or not re.fullmatch('[a-f0-9]{64}', asset.get('sha256', '')) or not isinstance(asset.get('bytes'), int) or asset['bytes'] <= 0:
                raise ValueError('Invalid or inconsistent browser receipt: '+path.name)
    minimum = re.search(r'^go (\S+)', (root/'go.mod').read_text(), re.M).group(1)
    if tuple(map(int, config['go'].split('.'))) < tuple(map(int, minimum.split('.'))):
        raise ValueError('Pinned build Go is older than the module requirement')
    workflow = (root/'.github/workflows/release.yml').read_text()
    if set(re.findall(r"go-version:\s*['\"]?([0-9.]+)", workflow)) != {config['go']}:
        raise ValueError('Release workflow Go pins differ from the build configuration')
    owners = literals(root/'scripts/maintainer/tools/build_go_vendor.py')['OWNERS']
    for path in (root/'packaging/tools/inputs').glob('*-source-linux-*.json'):
        value = read(path)
        if value.get('owner') in owners:
            accepted = owners[value['owner']]
            if any(value['source'][key] != accepted[key] for key in ['version', 'commit']):
                raise ValueError('Vendor source receipt differs from accepted pin: '+path.name)
    items = inventory(root)
    for item in items:
        if item['kind'] not in {'action', 'image'} and len(item['current']) != 1:
            raise ValueError('Dependency pins differ between targets: '+item['id'])
    selected_policy = policy(root/'packaging/tools/dependency-policy.json')
    if set(selected_policy['holds']) - {item['id'] for item in items}:
        raise ValueError('Dependency policy names an unused dependency')
    print('Accepted dependency pins are internally consistent (offline).', flush=True)


if __name__ == '__main__':
    verify()
