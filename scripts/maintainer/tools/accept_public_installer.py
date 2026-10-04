"""Native public installer acceptance with exact local release candidates."""
import argparse
from release_config import RELEASE
import asyncio
import hashlib
import json
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

from accept_browser_candidate import protocol
from accept_iex_installer import accept as accept_iex
from render_public_installers import render


def accept(candidate, browser=False):
    manager = candidate / 'manager' if browser else candidate
    receipt = json.loads((manager / 'manager-receipt.json').read_text())
    archive = manager / receipt['archive']
    if hashlib.sha256(archive.read_bytes()).hexdigest() != receipt['archive_sha256']:
        raise ValueError('installer candidate differs from its manager receipt')
    with tempfile.TemporaryDirectory(prefix='loki-installer-accept-') as temporary:
        scratch = Path(temporary)
        assets, pages = scratch / 'assets', scratch / 'pages'
        assets.mkdir()
        shutil.copyfile(archive, assets / archive.name)
        if browser:
            catalog = json.loads((candidate / 'release/catalog.json').read_text())
            target = catalog['artifacts'][0]['target']
            shutil.copyfile(candidate / 'release/catalog.json', assets / ('loki-catalog-'+target['os']+'-'+target['arch']+'-project-host.json'))
            shutil.copytree(candidate / 'release/archives', assets / 'archives')
        render(assets, pages)
        root, binary_dir, workspace, config = scratch / 'management', scratch / 'bin', scratch / 'workspace', scratch / 'config.toml'
        workspace.mkdir()
        original = '# Keep user configuration\nmodel = "fixture"\n[mcp_servers.other]\ncommand = "other"\n'
        config.write_text(original)
        marker = workspace / 'keep.txt'
        marker.write_text('User project fixture')
        if platform.system() == 'Windows':
            arguments = ['powershell.exe','-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',str(pages / 'install.ps1'),'-BinDirectory',str(binary_dir),'-ManagementRoot',str(root),'-SourceDirectory',str(assets)]
            path_before = subprocess.check_output(['powershell.exe','-NoProfile','-Command',"[Environment]::GetEnvironmentVariable('Path','User')"],text=True)
        else:
            arguments = ['sh',str(pages / 'install.sh'),'--bin-dir',str(binary_dir),'--root',str(root),'--source-dir',str(assets)]
            path_before = None
        try:
            if platform.system() == 'Windows':
                accept_iex(scratch)
            subprocess.run(arguments, check=True)
            binary = binary_dir / receipt['binary']
            def invoke(*args):
                return subprocess.check_output([str(binary),'--root',str(root),*args],text=True)
            if invoke('version').strip() != 'loki '+RELEASE:
                raise ValueError('public installer reports the wrong release')
            state = json.loads((root / 'control/state.json').read_text())
            if state['installed'] or state['config']['tools'] or config.read_text() != original:
                raise ValueError('CLI bootstrap configured tools or modified Codex settings')
            if browser:
                # Tool configuration is an explicit post-install CLI operation.
                invoke('tools', 'configure', '--mode', 'project-host')
                invoke('tools', 'install', '--catalog', str(candidate / 'release/catalog.json'),
                       '--archives', str(candidate / 'release/archives'), 'browser')
                invoke('tools', 'enable', 'browser')
                invoke('tools', 'connect', '--workspace', str(workspace), '--config', str(config), 'codex')
                configured = config.read_text()
                if not configured.startswith(original) or '[mcp_servers.loki_browser]' not in configured:
                    raise ValueError('public installer did not preserve/connect Codex configuration')
                invoke('tools','connect','--workspace',str(workspace),'--config',str(config),'codex')
                if config.read_text() != configured:
                    raise ValueError('repeated Codex setup changed the user configuration')
                asyncio.run(protocol(binary,root,workspace,invoke))
                invoke('tools','remove','browser')
            elif config.read_text() != original:
                raise ValueError('management-only install modified Codex configuration')
            if marker.read_text() != 'User project fixture':
                raise ValueError('public installation altered user project files')
            (assets / archive.name).write_bytes(b'corrupt release download')
            rejected = subprocess.run(arguments,capture_output=True,text=True)
            if rejected.returncode == 0 or 'SHA-256 mismatch' not in rejected.stdout+rejected.stderr:
                raise ValueError('public installer accepted corrupt native manager bytes')
            print(json.dumps({'public_installer_acceptance':'pass','target':{'os':receipt['os'],'arch':receipt['arch']},'browser':browser,'configuration':'preserved','corrupt_download':'rejected'}))
        finally:
            if path_before is not None:
                restore = scratch / 'restore-path.ps1'
                encoded = path_before.rstrip('\r\n').replace("'", "''")
                restore.write_text("[Environment]::SetEnvironmentVariable('Path', '"+encoded+"', 'User')\n")
                subprocess.run(['powershell.exe','-NoProfile','-File',str(restore)],check=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate',required=True,type=Path)
    parser.add_argument('--browser',action='store_true')
    args = parser.parse_args()
    accept(args.candidate.resolve(),args.browser)
