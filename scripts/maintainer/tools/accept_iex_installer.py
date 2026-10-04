"""Run the advertised zero-argument pipeline in native Windows PowerShell 5.1."""
import argparse
import json
from pathlib import Path
import platform
import subprocess
import tempfile

from accept_cli_installer import check_empty


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def accept(publication):
    with tempfile.TemporaryDirectory(prefix='loki-iex-') as temporary:
        scratch = Path(temporary)
        user_path = subprocess.check_output([
            'powershell.exe', '-NoProfile', '-Command',
            "[Environment]::GetEnvironmentVariable('Path','User')"], text=True).rstrip('\r\n')
        script = publication / 'pages/install.ps1'

        def run(name):
            case = scratch / name
            case.mkdir()
            workspace = case / 'workspace'
            workspace.mkdir()
            codex = case / 'codex'
            codex.mkdir()
            config = codex / 'config.toml'
            preserved = '# Existing user settings\nmodel = "fixture"\n[mcp_servers.other]\ncommand = "other"\n'
            config.write_text(preserved)
            wrapper = case / 'check.ps1'
            wrapper.write_text("""$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSVersion.Major -ne 5) { throw 'Acceptance requires Windows PowerShell 5.1' }
$env:LOCALAPPDATA = %s
$env:APPDATA = %s
$env:CODEX_HOME = %s
Set-Location -LiteralPath %s
function Read-Host { throw 'CLI installation must complete without selection prompts' }
function wsl.exe { throw 'CLI installation must not configure WSL' }
function Invoke-RestMethod { param([string]$Uri)
    if ($Uri -ne 'https://jinyongp.dev/loki/install.ps1') { throw 'Unexpected public installer URL' }
    return [IO.File]::ReadAllText(%s)
}
function Invoke-WebRequest { param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)
    if (-not $Uri.StartsWith('https://github.com/jinyongp/loki/releases/download/v0.2.1/')) { throw 'Unexpected release URL' }
    Copy-Item -LiteralPath (Join-Path %s ([Uri]$Uri).Segments[-1]) -Destination $OutFile
}
# Caller variables must not implicitly select tools or an execution host.
$Tools = 'full'
$HostKind = 'wsl'
irm https://jinyongp.dev/loki/install.ps1 | iex
irm https://jinyongp.dev/loki/install.ps1 | iex
""" % (quote(case / 'local'), quote(case / 'app'), quote(codex), quote(workspace),
       quote(script), quote(publication / 'assets')))
            result = subprocess.run(['powershell.exe', '-NoProfile', '-NonInteractive',
                                     '-ExecutionPolicy', 'Bypass', '-File', str(wrapper)],
                                    capture_output=True, text=True, timeout=600)
            print(result.stdout)
            if result.returncode:
                print(result.stderr)
            return result, case, config, preserved

        try:
            result, case, config, preserved = run('cli-only')
            if result.returncode:
                raise ValueError('zero-argument CLI IEX failed')
            expected = ['Installing Loki...', 'Loki 0.2.1 installed.', 'Next: loki --help'] * 2
            if result.stdout.splitlines() != expected or result.stderr.strip():
                raise ValueError('IEX success output is not concise: ' + result.stdout + result.stderr)
            binary = case / 'local/Programs/Loki/bin/loki.exe'
            root = case / 'app/loki'
            version = subprocess.check_output([str(binary), '--root', str(root), 'version'], text=True)
            if version.strip() != 'loki 0.2.1':
                raise ValueError('IEX installed the wrong manager')
            check_empty(root)
            if config.read_text() != preserved:
                raise ValueError('CLI IEX modified Codex settings')
            arch = 'arm64' if platform.machine().lower() in ('arm64', 'aarch64') else 'amd64'
            manager = publication / ('assets/loki-manager-0.2.1-windows-' + arch + '.zip')
            manager.write_bytes(b'corrupt download')
            result, _, _, _ = run('corrupt-download')
            if result.returncode == 0 or 'SHA-256 mismatch' not in result.stderr:
                raise ValueError('IEX accepted a corrupt manager download')
            print(json.dumps({'iex_acceptance': 'pass', 'powershell': '5.1',
                              'prompts': 'none', 'tools': 'empty', 'codex': 'unchanged',
                              'repeat_install': 'pass', 'corrupt_download': 'rejected'}))
        finally:
            restore = scratch / 'restore.ps1'
            restore.write_text("[Environment]::SetEnvironmentVariable('Path', " + quote(user_path) + ", 'User')\n")
            subprocess.run(['powershell.exe', '-NoProfile', '-File', str(restore)], check=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--publication', required=True, type=Path)
    args = parser.parse_args()
    accept(args.publication.resolve())
