"""Exercise verified self-upgrade from an owned native CLI to official 0.2.2."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import zipfile


def accept(candidate):
    receipt = json.loads((candidate/'manager-receipt.json').read_text())
    archive = candidate/receipt['archive']
    if hashlib.sha256(archive.read_bytes()).hexdigest() != receipt['archive_sha256']:
        raise ValueError('manager candidate differs from accepted receipt')
    with tempfile.TemporaryDirectory(prefix='loki-cli-upgrade-') as temporary:
        scratch = Path(temporary)
        binary = scratch/receipt['binary']
        with zipfile.ZipFile(archive) as packed:
            binary.write_bytes(packed.read(receipt['binary']))
        binary.chmod(0o700)
        root, bin_directory = scratch/'management', scratch/'bin'
        subprocess.run([str(binary), '--root', str(root), 'install', '--bin-dir', str(bin_directory)], check=True)
        installed = bin_directory/receipt['binary']
        sentinel = root/'user-sentinel'
        sentinel.write_bytes(b'retained user data')
        state = root/'control/state.json'
        before = state.read_bytes()
        command = [str(installed), '--root', str(root), 'upgrade', '--version', '0.2.2', '--force']
        cancelled = subprocess.run(command, input='n\n', capture_output=True, text=True, check=True)
        if 'Current: 0.2.3' not in cancelled.stdout or 'Target:  0.2.2' not in cancelled.stdout or 'Upgrade cancelled.' not in cancelled.stdout:
            raise ValueError('native release selection/confirmation failed')
        if state.read_bytes() != before:
            raise ValueError('cancelled upgrade changed state')
        upgraded = subprocess.run(command+['--yes'], capture_output=True, text=True)
        if upgraded.returncode:
            raise ValueError('Native self-upgrade failed:\n'+upgraded.stdout+upgraded.stderr)
        if 'Loki 0.2.2 installed.' not in upgraded.stdout:
            raise ValueError('native CLI self-publication failed')
        actual = subprocess.check_output([str(installed), 'version'], text=True).strip()
        if actual != 'loki 0.2.2' or json.loads(state.read_text())['config']['release'] != '0.2.2':
            raise ValueError('native verified target release was not published')
        owner = json.loads(Path(str(installed)+'.loki-owner.json').read_text())
        if owner['release'] != '0.2.2' or owner['sha256'] != hashlib.sha256(installed.read_bytes()).hexdigest():
            raise ValueError('native CLI owner record differs from target')
        subprocess.run([str(binary), '--root', str(root), 'install', '--bin-dir', str(bin_directory)], check=True)
        if subprocess.check_output([str(installed), 'version'], text=True).strip() != 'loki 0.2.3':
            raise ValueError('native CLI return to candidate failed')
        if sentinel.read_bytes() != b'retained user data' or list(bin_directory.glob('*.previous')):
            raise ValueError('native CLI did not retain user data or clean journal-owned backups')
        print(json.dumps({'native_cli_upgrade': 'pass', 'os': receipt['os'], 'arch': receipt['arch'],
                          'previous_release': '0.2.2', 'candidate_release': '0.2.3'}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--candidate', type=Path, required=True)
    accept(parser.parse_args().candidate.resolve())
