"""Remove explicitly selected obsolete publications after replacement verification."""
import json
import os
import re
import subprocess
from urllib.parse import quote


def api(path, method='GET'):
    return subprocess.check_output(['gh','api','--method',method,path],text=True)


repository = os.environ['GITHUB_REPOSITORY']
owner, name = repository.split('/')
identity = json.loads(api('repos/'+repository))
packages = json.loads(os.environ.get('OBSOLETE_PACKAGES','[]'))
if not isinstance(packages,list) or len(packages)>10:
    raise ValueError('cleanup requires a bounded explicit package list')
for package in packages:
    if not isinstance(package,str) or not package.startswith(name+'/') or not re.fullmatch(r'[a-z0-9/-]+',package):
        raise ValueError('cleanup package is outside this repository namespace')
    endpoint = 'users/'+owner+'/packages/container/'+quote(package,safe='')
    info = json.loads(api(endpoint))
    if (info.get('repository') or {}).get('id') != identity['id']:
        raise ValueError('obsolete package is not owned by this repository')
    api(endpoint,'DELETE')
    print('Removed obsolete package:',package)
tag = os.environ.get('OBSOLETE_RELEASE','')
if tag:
    if not re.fullmatch(r'v0\.2\.\d+',tag) or tag == 'v0.2.1':
        raise ValueError('obsolete release must be an explicitly selected prior 0.2 release')
    current = json.loads(api('repos/'+repository+'/releases/latest'))
    if current['tag_name'] != 'v0.2.1':
        raise ValueError('replacement stable release is not latest')
    subprocess.run(['gh','release','delete',tag,'--repo',repository,'--yes','--cleanup-tag'],check=True)
