"""Run the advertised zero-argument pipeline in native Windows PowerShell 5.1."""
import argparse
import json
from pathlib import Path
import subprocess
import tempfile


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def accept(publication, original=None):
    with tempfile.TemporaryDirectory(prefix='loki-iex-') as temporary:
        scratch = Path(temporary)
        user_path = subprocess.check_output([
            'powershell.exe', '-NoProfile', '-Command',
            "[Environment]::GetEnvironmentVariable('Path','User')"], text=True).rstrip('\r\n')
        script = publication / 'pages/install.ps1'

        def run(name, answers, installer=script):
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
$script:answers = New-Object 'System.Collections.Generic.Queue[string]'
%s
function Read-Host { param([string]$Prompt)
    if ($script:answers.Count -eq 0) { throw "Unexpected prompt: $Prompt" }
    $value = $script:answers.Dequeue()
    Write-Host "$Prompt -> $value"
    return $value
}
function Invoke-RestMethod { param([string]$Uri)
    if ($Uri -ne 'https://jinyongp.dev/loki/install.ps1') { throw 'Unexpected public installer URL' }
    return [IO.File]::ReadAllText(%s)
}
function Invoke-WebRequest { param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)
    if (-not $Uri.StartsWith('https://github.com/jinyongp/loki/releases/download/v0.2.1/')) { throw 'Unexpected release URL' }
    Copy-Item -LiteralPath (Join-Path %s ([Uri]$Uri).Segments[-1]) -Destination $OutFile
}
# Reproduce the empty values from an existing interactive caller as well.
$Tools = ''
$HostKind = ''
irm https://jinyongp.dev/loki/install.ps1 | iex
if ($script:answers.Count -ne 0) { throw 'Installer did not complete the interactive selections' }
""" % (quote(case / 'local'), quote(case / 'app'), quote(codex), quote(workspace),
       '\n'.join('$script:answers.Enqueue(' + quote(answer) + ')' for answer in answers),
       quote(installer), quote(publication / 'assets')))
            result = subprocess.run(['powershell.exe', '-NoProfile', '-NonInteractive',
                                     '-ExecutionPolicy', 'Bypass', '-File', str(wrapper)],
                                    capture_output=True, text=True, timeout=600)
            print(result.stdout)
            if result.returncode:
                print(result.stderr)
            return result, case, config, preserved

        try:
            if original:
                result, _, _, _ = run('original', ['none'], original)
                if result.returncode == 0 or 'ValidateSetFailure' not in result.stderr:
                    raise ValueError('original public installer did not reproduce ValidateSetFailure')
                print('Original public IEX ValidateSetFailure reproduced.')
            for name, answers in [('management', ['none']), ('browser', ['browser', 'native'])]:
                result, case, config, preserved = run(name, answers)
                if result.returncode:
                    raise ValueError('zero-argument IEX failed: ' + name)
                binary = case / 'local/Programs/Loki/bin/loki.exe'
                root = case / 'app/loki'
                version = subprocess.check_output([str(binary), '--root', str(root), 'version'], text=True)
                if version.strip() != 'loki 0.2.1':
                    raise ValueError('IEX installed the wrong manager')
                configured = config.read_text()
                if not configured.startswith(preserved):
                    raise ValueError('IEX changed existing Codex settings')
                if name == 'management' and configured != preserved:
                    raise ValueError('management-only IEX modified Codex settings')
                if name == 'browser' and '[mcp_servers.loki_browser]' not in configured:
                    raise ValueError('browser IEX did not connect Codex')
            result, _, _, _ = run('invalid-selection', ['invalid'])
            if result.returncode == 0 or 'Choose browser, full or none.' not in result.stderr:
                raise ValueError('IEX accepted an invalid interactive tool selection')
            manager = publication / 'assets/loki-manager-0.2.1-windows-amd64.zip'
            manager.write_bytes(b'corrupt download')
            result, _, _, _ = run('corrupt-download', ['none'])
            if result.returncode == 0 or 'SHA-256 mismatch' not in result.stderr:
                raise ValueError('IEX accepted a corrupt manager download')
            print(json.dumps({'iex_acceptance': 'pass', 'powershell': '5.1',
                              'management': 'pass', 'browser_native': 'pass',
                              'invalid_selection': 'rejected', 'corrupt_download': 'rejected'}))
        finally:
            restore = scratch / 'restore.ps1'
            restore.write_text("[Environment]::SetEnvironmentVariable('Path', " + quote(user_path) + ", 'User')\n")
            subprocess.run(['powershell.exe', '-NoProfile', '-File', str(restore)], check=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--publication', required=True, type=Path)
    parser.add_argument('--original', type=Path)
    args = parser.parse_args()
    accept(args.publication.resolve(), args.original.resolve() if args.original else None)
