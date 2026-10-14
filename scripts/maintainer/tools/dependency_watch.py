"""Advisory upstream discovery and one reusable GitHub issue; never update pins."""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone, date
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import time
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

from dependency_inventory import inventory, policy

START = '<!-- loki-dependency-watch:v1 -->'
END = '<!-- /loki-dependency-watch:v1 -->'
STATE = '<!-- loki-dependency-state:'
TITLE = 'Dependency update proposals'
LABEL = 'dependency-watch'
BOT = 'github-actions[bot]'


class MetadataRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, url):
        if urlsplit(url).scheme != 'https':
            raise ValueError('Upstream redirect weakened HTTPS')
        redirected = super().redirect_request(request, response, code, message, headers, url)
        if redirected is not None and urlsplit(url).netloc != urlsplit(request.full_url).netloc:
            redirected.remove_header('Authorization')
        return redirected


class Upstream:
    def __init__(self):
        self.cache = {}

    def document(self, url, headers=None, raw=False):
        key = (url, tuple(sorted((headers or {}).items())), raw)
        if key in self.cache:
            return self.cache[key]
        request = Request(url, headers={'User-Agent': 'loki-dependency-watch', **(headers or {})})
        for attempt in range(3):
            try:
                with build_opener(MetadataRedirect()).open(request, timeout=15) as response:
                    data = response.read((16 << 20)+1)
                if len(data) > 16 << 20:
                    raise ValueError('Upstream metadata exceeds limit')
                value = data if raw else json.loads(data)
                self.cache[key] = value
                return value
            except HTTPError as error:
                if attempt == 2 or error.code not in {429, 500, 502, 503, 504}:
                    raise
            except (TimeoutError, URLError):
                if attempt == 2:
                    raise
            time.sleep(attempt+1)

    def github(self, path):
        token = os.environ.get('GH_TOKEN', '')
        return self.document('https://api.github.com/'+path, {'Authorization': 'Bearer '+token} if token else None)

    def npm(self, name, version='latest'):
        return self.document('https://registry.npmjs.org/'+quote(name, safe='')+'/'+quote(version, safe=''))

    def candidate(self, item):
        kind, source = item['kind'], item['source']
        if kind == 'go':
            release = next(x for x in self.document(source) if x['stable'])
            return dict(version=release['version'].removeprefix('go'), url='https://go.dev/doc/devel/release')
        if kind == 'node':
            release = next(x for x in self.document(source) if '-' not in x['version'] and (item.get('channel', 'current') != 'lts' or x['lts']))
            return dict(version=release['version'].removeprefix('v'), url='https://nodejs.org/en/blog/release', channel='LTS' if release['lts'] else 'Current')
        if kind == 'chrome':
            release = self.document(source)['channels']['Stable']
            return dict(version=release['version'], url='https://chromereleases.googleblog.com/search/label/Stable%20updates')
        if kind == 'npm':
            release = self.npm(source)
            return dict(version=release['version'], url='https://www.npmjs.com/package/'+source+'/v/'+release['version'], published=release.get('time', {}).get('modified'))
        if kind == 'gomod':
            escaped = ''.join('!'+c.lower() if c.isupper() else c for c in source)
            release = self.document('https://proxy.golang.org/'+quote(escaped, safe='/!')+'/@latest')
            return dict(version=release['Version'], url='https://pkg.go.dev/'+source+'@'+release['Version'], published=release.get('Time'))
        if kind in {'github', 'action'}:
            release = self.github('repos/'+source+'/releases/latest')
            if release['draft'] or release['prerelease']:
                raise ValueError('Upstream did not return a stable release')
            version = release['tag_name']
            result = dict(version=version, url=release['html_url'], published=release.get('published_at'))
            if kind == 'action':
                ref = self.github('repos/'+source+'/git/ref/tags/'+quote(version, safe=''))['object']
                if ref['type'] == 'tag':
                    ref = self.github('repos/'+source+'/git/tags/'+ref['sha'])['object']
                if ref['type'] != 'commit' or not re.fullmatch('[a-f0-9]{40}', ref['sha']):
                    raise ValueError('Action release does not resolve to a commit')
                result['version'], result['release'] = ref['sha'], version
            return result
        if kind == 'ffmpeg':
            mcp = self.npm(source)
            core = self.npm('playwright-core', mcp['dependencies']['playwright-core'])
            archive_url = core['dist']['tarball']
            if not archive_url.startswith('https://registry.npmjs.org/'):
                raise ValueError('Playwright input left its primary registry')
            data = self.document(archive_url, raw=True)
            integrity = core['dist']['integrity']
            if integrity != 'sha512-'+base64.b64encode(hashlib.sha512(data).digest()).decode():
                raise ValueError('Playwright metadata archive integrity mismatch')
            with tarfile.open(fileobj=io.BytesIO(data), mode='r:gz') as archive:
                browser_file = archive.extractfile('package/browsers.json')
                if browser_file is None:
                    raise ValueError('Playwright browsers metadata missing')
                browsers = json.load(io.BytesIO(browser_file.read(1 << 20)))['browsers']
            return dict(version=next(x['revision'] for x in browsers if x['name']=='ffmpeg'), url='https://github.com/microsoft/playwright-mcp/releases', channel='Coupled to @playwright/mcp '+mcp['version'])
        if kind == 'ubuntu-package':
            url = 'https://api.launchpad.net/1.0/ubuntu/+archive/primary?'+urlencode({'ws.op': 'getPublishedBinaries', 'binary_name': source, 'exact_match': 'true', 'status': 'Published', 'distro_arch_series': 'https://api.launchpad.net/1.0/ubuntu/'+item['series']+'/'+item['arch'], 'ws.size': 100})
            document = self.document(url)
            if document.get('next_collection_link'):
                raise ValueError('Ubuntu package metadata exceeds bounded query')
            entries = document['entries']
            if not entries:
                raise ValueError('No published package on the accepted Ubuntu series')
            selected = entries[0]
            for entry in entries[1:]:
                if debian_newer(entry['binary_package_version'], selected['binary_package_version']):
                    selected = entry
            return dict(version=selected['binary_package_version'], url='https://launchpad.net/ubuntu/'+item['series']+'/+package/'+source, published=selected.get('date_published'), channel='Ubuntu '+item['series'])
        if kind == 'image':
            token = self.document('https://auth.docker.io/token?'+urlencode({'service': 'registry.docker.io', 'scope': 'repository:'+source+':pull'}))['token']
            headers = {'Authorization': 'Bearer '+token, 'Accept': 'application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json'}
            index = self.document('https://registry-1.docker.io/v2/'+source+'/manifests/'+quote(item['tag'], safe=''), headers)
            native = next(x for x in index['manifests'] if x.get('platform', {}).get('os')=='linux' and x['platform'].get('architecture')==item['arch'])
            if not re.fullmatch('sha256:[a-f0-9]{64}', native['digest']):
                raise ValueError('Invalid registry digest')
            return dict(version=native['digest'], url='https://hub.docker.com/r/'+source+'/tags', channel='Existing tag '+item['tag'])
        raise ValueError('Unknown dependency kind: '+kind)


def numeric(value):
    match = re.match(r'^v?(\d+(?:\.\d+)*)(?:$|-)', value)
    return tuple(map(int, match[1].split('.'))) if match else None


def debian_newer(candidate, current):
    result = subprocess.run(['dpkg', '--compare-versions', candidate, 'gt', current], capture_output=True, timeout=10)
    if result.returncode not in {0, 1}:
        raise ValueError('Invalid Debian version metadata')
    return result.returncode == 0


def collect(items, selected_policy, upstream=None, today=None):
    upstream, today = upstream or Upstream(), today or datetime.now(timezone.utc).date()
    def check(item):
        row = dict(item)
        try:
            candidate = upstream.candidate(dict(item, channel=selected_policy.get('channels', {}).get(item['id'], 'current')))
            version = candidate['version']
            if not isinstance(version, str) or not version or len(version) > 200:
                raise ValueError('Invalid candidate version')
            row.update(candidate=candidate, status='current')
            # Stable upstream APIs may return an older supported line. Never propose a downgrade.
            changed = any(version.removeprefix('v') != current.removeprefix('v') for current in item['current'])
            if item['kind'] == 'ubuntu-package':
                changed = any(debian_newer(version, current) for current in item['current'])
            elif item['kind'] not in {'image', 'action'}:
                changed = changed and '-' not in version and numeric(version) is not None and any(numeric(version) > numeric(current) for current in item['current'] if numeric(current) is not None)
            if changed:
                row['status'] = 'review'
                hold = selected_policy['holds'].get(item['id'], {})
                if hold.get('version') == version or hold.get('until') and today <= date.fromisoformat(hold['until']):
                    row.update(status='held', reason=hold['reason'])
        except Exception as error:
            # Metadata failures are unknown, never evidence that the pin is current.
            row.update(status='unknown', error=type(error).__name__+(': HTTP '+str(error.code) if isinstance(error, HTTPError) else ': '+str(error)[:180]))
        return row
    with ThreadPoolExecutor(max_workers=6) as pool:
        rows = list(pool.map(check, items))
    return dict(schema=1, checked_at=datetime.now(timezone.utc).isoformat(), rows=rows)


def escape(value):
    return str(value).replace('\n', ' ').replace('\r', ' ').replace('|', '\\|').replace('`', '').replace('<', '&lt;').replace('>', '&gt;')


def previous_rows(body):
    match = re.search(re.escape(STATE)+r'(.*?) -->', body, re.S)
    if not match:
        return {}
    value = json.loads(match[1])
    if value.get('schema') != 1 or not isinstance(value.get('rows'), list) or len(value['rows']) > 200:
        raise ValueError('Invalid prior dependency report; preserving issue')
    return {row['id']: row for row in value['rows']}


def render(report, old_body=''):
    previous = previous_rows(old_body)
    rows = []
    for value in report['rows']:
        row = dict(value)
        if row['status'] == 'unknown' and row['id'] in previous and previous[row['id']]['current'] == row['current']:
            if 'candidate' in previous[row['id']]:
                row['candidate'] = previous[row['id']]['candidate']
        if row['status'] != 'current':
            rows.append(row)
    rows.sort(key=lambda row: row['id'])
    counts = {status: sum(x['status']==status for x in report['rows']) for status in ['current', 'review', 'held', 'unknown']}
    lines = [START, 'New versions are review proposals. Accepted pins remain valid for Loki releases.', '',
             f"To review: {counts['review']} · Held: {counts['held']} · Unavailable: {counts['unknown']} · Unchanged: {counts['current']}", '',
             'Adopt selected proposals through a reviewed PR: verify supported platforms, compatibility and runtime, and update linked receipts and locks together.']
    for status, heading in [('review', 'To review'), ('held', 'On hold'), ('unknown', 'Lookup unavailable')]:
        selected = [row for row in rows if row['status'] == status]
        if not selected:
            continue
        lines += ['', '## '+heading]
        for row in selected:
            candidate = row.get('candidate', {})
            version = candidate.get('release', candidate.get('version', 'unknown'))
            url = candidate.get('url', '')
            target = escape(version)
            if len(target) > 30:
                target = target.removeprefix('sha256:')[:12]
            if re.match(r'^https://[^\s()<>]+$', url):
                target = '['+target+']('+url+')'
            current = []
            for accepted in row['current']:
                label = escape(accepted if len(accepted) <= 30 else accepted.removeprefix('sha256:')[:12])
                if row['kind'] == 'action':
                    current.append('['+label+'](https://github.com/'+row['source']+'/commit/'+accepted+')')
                else:
                    current.append('`'+label+'`')
            name = row['id'].split(':', 1)[-1] if ':' in row['id'] else row['id']
            proposal = 'Last known proposal' if status == 'unknown' else 'Proposed'
            lines += ['', '### '+escape(name), '', 'Accepted: '+', '.join(current)+' → '+proposal+': '+target]
            metadata = []
            if candidate.get('published'):
                metadata.append('Published '+escape(candidate['published'][:10]))
            if candidate.get('channel'):
                metadata.append(escape(candidate['channel']))
            if metadata:
                lines += ['', ' · '.join(metadata)]
            if row.get('reason') or row.get('error'):
                lines += ['', escape(row.get('reason', row.get('error')))]
            detailed = len(row['paths']) > 1 or any(len(value)>30 for value in row['current']) or len(candidate.get('version', '')) > 30
            if detailed:
                lines += ['', '<details>', '<summary>Pin locations and full references</summary>', '']
                for accepted in row['current']:
                    lines.append('- Accepted: `'+escape(accepted)+'`')
                if candidate.get('version'):
                    lines.append('- Candidate reference: `'+escape(candidate['version'])+'`')
                lines += ['']+['- `'+escape(path)+'`' for path in row['paths']]+['', '</details>']
            else:
                lines += ['', 'Pins: '+', '.join('`'+escape(path)+'`' for path in row['paths'])]
    lines += ['', '[Update and hold policy](https://github.com/jinyongp/loki/blob/main/docs/tools/dependency-updates.md)', '',
              STATE+json.dumps(dict(schema=1, rows=rows), sort_keys=True, separators=(',', ':')).replace('-->', '--\\u003e')+' -->', END]
    managed = '\n'.join(lines)
    if old_body:
        if old_body.count(START) != 1 or old_body.count(END) != 1:
            raise ValueError('Ambiguous managed issue section; preserving issue')
        a, b = old_body.index(START), old_body.index(END)+len(END)
        body = old_body[:a]+managed+old_body[b:]
    else:
        body = managed+'\n\nMaintainer notes (preserved by the watcher):\n'
    if len(body.encode()) > 60000:
        raise ValueError('Dependency issue exceeds safe body size; preserving issue')
    return body, counts


class GitHubIssues:
    def __init__(self, repository):
        if not re.fullmatch(r'[\w.-]+/[\w.-]+', repository):
            raise ValueError('Invalid repository')
        self.repository = repository

    def api(self, path, data=None, method=None):
        args = ['gh', 'api', 'repos/'+self.repository+'/'+path]
        if method:
            args += ['--method', method]
        if data is not None:
            args += ['--input', '-']
        done = subprocess.run(args, input=json.dumps(data) if data is not None else None, capture_output=True, text=True, timeout=45, check=True)
        return json.loads(done.stdout) if done.stdout.strip() else {}

    def issues(self):
        owned = []
        for page in range(1, 21):
            batch = self.api('issues?'+urlencode(dict(state='all', creator=BOT, per_page=100, page=page)))
            owned += [issue for issue in batch if not issue.get('pull_request') and issue['user']['login']==BOT and START in (issue.get('body') or '')]
            if len(batch) < 100:
                return sorted(owned, key=lambda issue: issue['number'])
        raise ValueError('Issue lookup pagination limit; refusing to create a duplicate')

    def ensure_label(self):
        try:
            self.api('labels/'+LABEL)
        except subprocess.CalledProcessError as error:
            if 'HTTP 404' not in error.stderr:
                raise
            self.api('labels', dict(name=LABEL, color='0366d6', description='Advisory dependency update tracking'))

    def create(self, body):
        self.ensure_label()
        return self.api('issues', dict(title=TITLE, body=body, labels=[LABEL]))

    def update(self, number, data):
        return self.api('issues/'+str(number), data, 'PATCH')


def publish(report, github):
    issues = github.issues()
    existing = issues[0] if issues else None
    old_body = existing.get('body', '') if existing else ''
    # A fixed concurrency group prevents new races. If older runs left duplicates,
    # retain their human notes once in the canonical issue before closing them.
    for duplicate in issues[1:]:
        marker = f'<!-- dependency-notes-from:{duplicate["number"]} -->'
        if duplicate['state'] == 'open' and marker not in old_body:
            duplicate_body = duplicate.get('body') or ''
            if duplicate_body.count(START) != 1 or duplicate_body.count(END) != 1:
                raise ValueError('Ambiguous duplicate issue; preserve it for manual review')
            a, b = duplicate_body.index(START), duplicate_body.index(END)+len(END)
            notes = duplicate_body[:a]+duplicate_body[b:]
            old_body += '\n\n'+marker+'\nPreserved notes from #'+str(duplicate['number'])+':\n'+notes
    body, counts = render(report, old_body)
    if existing is None:
        if not counts['review']:
            return dict(action='none', reason='No actionable update proposals', counts=counts)
        created = github.create(body)
        return dict(action='created', number=created['number'], url=created['html_url'], counts=counts)
    state = 'open' if counts['review'] or counts['unknown'] and existing['state']=='open' else 'closed'
    changes = {}
    if body != existing.get('body'):
        changes['body'] = body
    if state != existing['state']:
        changes['state'] = state
    if changes:
        github.update(existing['number'], changes)
    # Canonical update succeeded before any duplicate is closed.
    for duplicate in issues[1:]:
        if duplicate['state'] == 'open':
            github.update(duplicate['number'], {'state': 'closed'})
    return dict(action='updated' if changes else 'unchanged', number=existing['number'], url=existing['html_url'], counts=counts)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--publish', action='store_true')
    parser.add_argument('--repository', default=os.environ.get('GITHUB_REPOSITORY', 'jinyongp/loki'))
    args = parser.parse_args()
    report = collect(inventory(), policy())
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2)+'\n')
    result = publish(report, GitHubIssues(args.repository)) if args.publish else {'action': 'dry-run'}
    print(json.dumps(result, indent=2))
    if os.environ.get('GITHUB_STEP_SUMMARY'):
        body, _ = render(report)
        with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as output:
            output.write(body.split(STATE)[0]+'\n\n'+json.dumps(result)+'\n')
    if any(row['status']=='unknown' for row in report['rows']):
        raise SystemExit('Some upstream sources could not be inspected; see report. Release CI is unaffected.')


if __name__ == '__main__':
    main()
