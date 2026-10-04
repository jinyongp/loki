"""Native CLI bootstrap acceptance; tool configuration remains a later step."""
import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def check_empty(root):
    state = json.loads((root / 'control/state.json').read_text())
    if state['installed'] or state['config']['tools']:
        raise ValueError('CLI bootstrap installed or selected tools')
    if (root / 'catalogs').exists() or (root / 'tools').exists():
        raise ValueError('CLI bootstrap acquired tool assets')


def accept(publication):
    windows = platform.system() == 'Windows'
    target_os = {'Windows': 'windows', 'Darwin': 'darwin', 'Linux': 'linux'}[platform.system()]
    target_arch = {'x86_64': 'amd64', 'amd64': 'amd64', 'arm64': 'arm64', 'aarch64': 'arm64'}[platform.machine().lower()]
    with tempfile.TemporaryDirectory(prefix='loki-cli-install-') as temporary:
        scratch = Path(temporary)
        assets = scratch / 'assets'
        assets.mkdir()
        archive = 'loki-manager-0.2.2-' + target_os + '-' + target_arch + '.zip'
        shutil.copyfile(publication / 'assets' / archive, assets / archive)
        root, binary_dir, workspace = scratch / 'management', scratch / 'bin', scratch / 'workspace'
        workspace.mkdir()
        marker = workspace / 'keep.txt'
        marker.write_text('User project fixture')
        codex = scratch / 'codex'
        codex.mkdir()
        config = codex / 'config.toml'
        preserved = '# Keep user configuration\nmodel = "fixture"\n[mcp_servers.other]\ncommand = "other"\n'
        config.write_text(preserved)
        environment = os.environ.copy()
        environment['CODEX_HOME'] = str(codex)
        if windows:
            args = ['powershell.exe', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass',
                    '-File', str(publication / 'pages/install.ps1'), '-BinDirectory', str(binary_dir),
                    '-ManagementRoot', str(root), '-SourceDirectory', str(assets)]
            user_path = subprocess.check_output(['powershell.exe', '-NoProfile', '-Command',
                      "[Environment]::GetEnvironmentVariable('Path','User')"], text=True).rstrip('\r\n')
        else:
            args = ['sh', str(publication / 'pages/install.sh'), '--bin-dir', str(binary_dir),
                    '--root', str(root), '--source-dir', str(assets)]
            user_path = None
        try:
            for _ in range(2):
                result = subprocess.run(args, check=True, stdin=subprocess.DEVNULL, env=environment,
                                        cwd=workspace, timeout=120, capture_output=True, text=True)
                expected_next = 'loki' if windows or str(binary_dir) in environment.get('PATH', '').split(os.pathsep) else str(binary_dir / 'loki')
                expected = ['Installing Loki...', 'Loki 0.2.2 installed.', 'Next: ' + expected_next + ' --help']
                if result.stdout.splitlines() != expected or result.stderr.strip():
                    raise ValueError('Installer success output is not concise: ' + result.stdout + result.stderr)
                check_empty(root)
                if config.read_text() != preserved or marker.read_text() != 'User project fixture':
                    raise ValueError('CLI bootstrap changed Codex or project files')
            binary = binary_dir / ('loki.exe' if windows else 'loki')
            version = subprocess.check_output([str(binary), '--root', str(root), 'version'], text=True)
            if version.strip() != 'loki 0.2.2':
                raise ValueError('CLI bootstrap reports wrong version')
            conflict = scratch / 'unowned-bin'
            conflict.mkdir()
            unowned = conflict / binary.name
            unowned.write_bytes(b'user-owned file')
            conflict_args = [str(conflict) if item == str(binary_dir) else item for item in args]
            rejected = subprocess.run(conflict_args, capture_output=True, text=True, stdin=subprocess.DEVNULL,
                                      env=environment, cwd=workspace, timeout=120)
            failure = ' '.join((rejected.stdout + rejected.stderr).split())
            if rejected.returncode == 0 or 'existing loki command' not in failure or 'choose another bin directory' not in failure or unowned.read_bytes() != b'user-owned file':
                raise ValueError('Nested installer failure details were lost')
            (assets / archive).write_bytes(b'corrupt download')
            rejected = subprocess.run(args, capture_output=True, text=True, stdin=subprocess.DEVNULL,
                                      env=environment, cwd=workspace, timeout=120)
            if rejected.returncode == 0 or 'SHA-256 mismatch' not in rejected.stdout + rejected.stderr:
                raise ValueError('CLI bootstrap accepted corrupt release bytes')
            print(json.dumps({'cli_installer_acceptance': 'pass', 'os': target_os, 'arch': target_arch,
                              'tools': 'empty', 'codex': 'unchanged', 'repeat_install': 'pass',
                              'corrupt_download': 'rejected', 'success_output': 'three lines',
                              'nested_failure': 'visible'}))
        finally:
            if user_path is not None:
                restore = scratch / 'restore.ps1'
                restore.write_text("[Environment]::SetEnvironmentVariable('Path', " + quote(user_path) + ", 'User')\n")
                subprocess.run(['powershell.exe', '-NoProfile', '-File', str(restore)], check=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--publication', required=True, type=Path)
    accept(parser.parse_args().publication.resolve())
