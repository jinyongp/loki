"""Check the recorded prerequisite dossier; does not run product acceptance."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import sys

REPO = Path(__file__).resolve().parents[3]
EVIDENCE = REPO / 'docs/tools/evidence'


def load(name):
    return json.loads((EVIDENCE / name).read_text())


def require(condition, message):
    if not condition:
        raise ValueError(message)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--workstream-context', type=Path)
    args = parser.parse_args()
    index = load('index.json')
    for relative, expected in index['files'].items():
        path = REPO / relative
        require(path.is_file(), 'Missing evidence/source file: ' + relative)
        require(hashlib.sha256(path.read_bytes()).hexdigest() == expected,
                'Evidence/source changed since recording: ' + relative)

    metadata = load('metadata.json')
    lock = json.loads((REPO / 'scripts/maintainer/preflight/package-lock.json').read_text())
    pins = {'@playwright/mcp': '0.0.83', 'chrome-devtools-mcp': '1.10.1',
            'playwright': '1.64.0-alpha-1790635538000',
            'playwright-core': '1.64.0-alpha-1790635538000'}
    require(len(metadata['packages']) == 4, 'Unexpected npm dependency closure')
    for package in metadata['packages']:
        name = package['name']
        entry = lock['packages']['node_modules/' + name]
        require(package['version'] == entry['version'] == pins[name], 'Package pin mismatch: ' + name)
        require(package['integrity'] == entry['integrity'] and package['integrity'].startswith('sha512-'),
                'Package integrity mismatch: ' + name)
        require(package['notices'], 'Missing notice inventory: ' + name)
    require(metadata['versions']['node'] == 'v22.22.2', 'Unexpected tested Node')
    require(metadata['chrome']['version'] == '154.0.8037.92', 'Unexpected tested Chrome')
    require('linux-arm64' in metadata['chrome']['platforms'] and
            'win-arm64' not in metadata['chrome']['platforms'], 'Incorrect native browser platform claim')
    arm = load('chrome-linux-arm64.json')
    require(arm['head_status'] == 200 and arm['version'] == metadata['chrome']['version'],
            'Linux arm64 acquisition metadata was not verified')

    counts = {}
    for engine, expected_tools, expected_calls in [('playwright', 71, 30), ('devtools', 55, 25)]:
        report = load(engine + '.json')
        tools = report['catalog']['result']['tools']
        require(len(tools) == expected_tools and len({t['name'] for t in tools}) == expected_tools,
                'Unexpected/duplicate catalog: ' + engine)
        require(len(report['checks']) == expected_calls and 'error' not in report and
                all(c['passed'] for c in report['checks']), 'Failed fixture: ' + engine)
        require(sum(c['expected_error'] for c in report['checks']) == 2,
                'Missing invalid-call checks: ' + engine)
        require(report['initialize']['capabilities']['tools']['listChanged'], 'Missing discovery capability')
        require('resources' not in report['initialize']['capabilities'] and
                'prompts' not in report['initialize']['capabilities'], 'Unexpected protocol support claim')
        require(not any('invalid_stdout' in n for n in report['notifications']), 'Protocol stdout contamination')
        images = [x for c in report['checks'] for x in c['response'].get('result', {}).get('content', [])
                  if x.get('type') == 'image']
        require(images and all(x['bytes'] > 0 and len(x['sha256']) == 64 for x in images),
                'Missing decoded image evidence: ' + engine)
        counts[engine] = {'tools': len(tools), 'calls': len(report['checks'])}
        files = {f['path']: f['bytes'] for f in report['files']}
        needed = ['page.pdf', 'fixture.webm', 'output/fixture.txt', 'storage.json'] if engine == 'playwright' else [
            'screenshot.png', 'trace.json.gz', 'fixture.heapsnapshot', 'lighthouse/report.json']
        require(all(files.get(name, 0) > 0 for name in needed), 'Missing generated artifacts: ' + engine)

    optional = load('optional-browser.json')
    require(len(optional['checks']) == 16 and all(c['passed'] for c in optional['checks']),
            'Optional feature fixture did not pass')
    for name in ['execute_webmcp_tool', 'execute_3p_developer_tool']:
        c = next(c for c in optional['checks'] if c['name'] == name)
        texts = ''.join(x.get('text', '') for x in c['response']['result']['content'])
        require('"echo": "Loki"' in texts, 'Page-provided tool result mismatch: ' + name)

    protocol = load('protocol.json')
    require(protocol['unknown_method']['error']['code'] == -32601, 'Incorrect unknown-method behavior')
    require(len(protocol['default_catalog']['result']['tools']) == 25, 'Unexpected default Playwright catalog')
    require(len(load('devtools-default.json')['catalog']['result']['tools']) == 30,
            'Unexpected default DevTools catalog')
    default_names = {t['name'] for t in protocol['default_catalog']['result']['tools']}
    require('browser_run_code_unsafe' in default_names, 'Unsafe default capability finding missing')
    require(any(n.get('server_request_method') == 'roots/list' for n in protocol['notifications']),
            'Missing roots exchange')
    require(any(n.get('method') == 'notifications/tools/list_changed' for n in protocol['notifications']),
            'Missing dynamic discovery observation')
    require('error' not in protocol['post_cancel_snapshot'], 'Post-cancel responsiveness failed')

    baseline = load('go-baseline.json')
    require(baseline['tests_passed'] == 2360 and baseline['packages_passed'] == 76 and
            not baseline['failed'], 'Unexpected baseline result')
    legacy = load('legacy-browser.json')
    require(not legacy['failed'] and sum('Test' in x for x in legacy['passed']) == 5,
            'Existing browser integration failure')
    oci = load('oci-current-source.json')
    require(oci['script_completed'] and len(oci['passed']) == 6, 'Current-source OCI acceptance incomplete')
    require('loki OCI Job acceptance: passed' in (EVIDENCE / 'oci-current-source.log').read_text(),
            'Missing OCI acceptance log')
    privileged = load('privileged-linux.json')
    require(len(privileged['passed']) == 3 and all(x == 0 for x in privileged['exit_status'].values()),
            'Privileged synthetic fixture failure')
    project = load('project-e2e.json')
    require(project['passed'] == ['TestProjectExecutionContract'] and not project['failed'],
            'Existing project E2E failure')
    require(load('failures.json')['coordination']['error'] == 'unsupported devtools protocol version 6',
            'Known real-adapter failure was lost')
    platforms = load('platform-builds.json')
    require(all(r['exit_code'] == 0 for r in platforms['contract_compilation']), 'Contract cross-build failed')
    require(any(r['target'].startswith('darwin/') and r['exit_code'] != 0 for r in platforms['existing_host_builds']),
            'Known Linux-command/macOS failure was lost')
    cleanup = load('cleanup.json')
    require(not cleanup['chrome_processes_remaining'] and not cleanup['test_containers_remaining'] and
            cleanup['docker_query_exit'] == 0, 'Owned fixture cleanup was not confirmed')

    for path in (REPO / 'docs/tools').glob('*.md'):
        for target in re.findall(r'\]\(([^)]+)\)', path.read_text()):
            if '://' in target or target.startswith('#'):
                continue
            require((path.parent / target.split('#')[0]).exists(), 'Broken local document link: ' + target)
    require(index['pending_product_gates'] == ['A_REMOTE', 'A_HOST', 'A_ACCEPTANCE', 'A_CANDIDATE'],
            'External/product acceptance gaps were omitted')

    if args.workstream_context:
        context = json.loads(args.workstream_context.read_text())['data']
        documents = context['documents']
        require(documents['spec']['body'] == (REPO / 'docs/tools/spec.md').read_text(), 'Canonical spec differs')
        require(documents['plan']['body'] == (REPO / 'docs/tools/plan.md').read_text(), 'Canonical plan differs')
        require(len(documents['spec']['requirements']) == 11 and len(documents['spec']['acceptance']) == 13,
                'Incomplete requirement/acceptance coverage')
        require(set(documents['plan']['task_ids']) == {t['id'] for t in context['tasks']} and
                len(context['tasks']) == 13, 'Incomplete plan task references')
        require(set(documents['plan']['validation_ids']) == {v['id'] for v in context['validations']} and
                len(context['validations']) == 22 and sum(v['required'] for v in context['validations']) == 13,
                'Incomplete plan validation references or required final gates')
        require(context['item']['state'] == 'active', 'Product workstream must remain active')

    print(json.dumps({'prerequisite_evidence': 'pass', 'hashed_files': len(index['files']),
                      'browser': counts, 'optional_calls': 16, 'oci_cases': 6,
                      'pending_product_gates': index['pending_product_gates']}))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, OSError, StopIteration) as error:
        print('Prerequisite evidence verification failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
