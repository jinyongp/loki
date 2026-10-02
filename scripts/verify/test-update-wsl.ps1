Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Exercise the real helper using command substitutes, without modifying WSL.
function wsl.exe {
    $command = $args[0]
    $global:lokiWSLPreparationFixture.Calls.Add($command)
    $global:LASTEXITCODE = 0
    switch ($command) {
        '--version' {
            if ($global:lokiWSLPreparationFixture.VersionFailure -and $global:lokiWSLPreparationFixture.Updates -eq 0) {
                $global:LASTEXITCODE = 1
            }
            Write-Output "WSL version: 3.0.0"
        }
        '--help' {
            if ($global:lokiWSLPreparationFixture.HelpFailure -and $global:lokiWSLPreparationFixture.Updates -eq 0) {
                $global:LASTEXITCODE = 1
            }
            if ($global:lokiWSLPreparationFixture.Ready) { Write-Output '--from-file --name --no-launch' }
            else { Write-Output '--name --no-launch' }
        }
        '--update' {
            if (($args -join ' ') -ne '--update --web-download') { throw 'Unexpected WSL update arguments' }
            $global:lokiWSLPreparationFixture.Updates++
            if ($global:lokiWSLPreparationFixture.Updates -le $global:lokiWSLPreparationFixture.Failures) {
                $global:LASTEXITCODE = 1
                if ($global:lokiWSLPreparationFixture.ReadyAfterFailure) { $global:lokiWSLPreparationFixture.Ready = $true }
                Write-Output $global:lokiWSLPreparationFixture.Error
            } else {
                $global:lokiWSLPreparationFixture.Ready = -not $global:lokiWSLPreparationFixture.Incompatible
                Write-Output 'Update completed'
            }
        }
        default { throw "Unexpected WSL command: $command" }
    }
}

function Start-Sleep {
    param([int]$Seconds)
    $global:lokiWSLPreparationFixture.Delays.Add($Seconds)
}

$download403 = 'Forbidden (403). Error code: Wsl/UpdatePackage/0x80190193'
$cases = @(
    @{ Name = 'installed-ready'; Ready = $true; Updates = 0 },
    @{ Name = 'upgrade'; Updates = 1 },
    @{ Name = 'version-probe-failed'; Ready = $true; VersionFailure = $true; Updates = 1 },
    @{ Name = 'help-probe-failed'; Ready = $true; HelpFailure = $true; Updates = 1 },
    @{ Name = 'download-403'; Failures = 1; Error = $download403; Updates = 2; Delays = '15' },
    @{ Name = 'nul-encoded-403'; Failures = 1; Error = ($download403.ToCharArray() -join "`0"); Updates = 2; Delays = '15' },
    @{ Name = 'http-503'; Failures = 1; Error = 'HTTP status: 503'; Updates = 2; Delays = '15' },
    @{ Name = 'http-429'; Failures = 1; Error = 'HTTP status: 429'; Updates = 2; Delays = '15' },
    @{ Name = 'timeout'; Failures = 1; Error = 'Connection timed out'; Updates = 2; Delays = '15' },
    @{ Name = 'authorization'; Failures = 4; Error = 'Access denied'; Updates = 1; Reject = $true },
    @{ Name = 'authorization-with-transient'; Failures = 4; Error = 'Access denied. HTTP status: 503'; Updates = 1; Reject = $true },
    @{ Name = 'unclassified-403'; Failures = 4; Error = 'Forbidden (403)'; Updates = 1; Reject = $true },
    @{ Name = 'invalid-option'; Failures = 4; Error = 'Invalid command line option'; Updates = 1; Reject = $true },
    @{ Name = 'exhausted'; Failures = 4; Error = $download403; Updates = 4; Delays = '15,30,60'; Reject = $true },
    @{ Name = 'incompatible-update'; Updates = 1; Incompatible = $true; Reject = $true },
    @{ Name = 'ambiguous-success'; Failures = 1; Error = $download403; ReadyAfterFailure = $true; Updates = 1 }
)

foreach ($case in $cases) {
    $global:lokiWSLPreparationFixture = @{
        Ready = [bool]$case['Ready']; Failures = [int]$case['Failures']; Error = [string]$case['Error']
        Incompatible = [bool]$case['Incompatible']; ReadyAfterFailure = [bool]$case['ReadyAfterFailure']
        VersionFailure = [bool]$case['VersionFailure']; HelpFailure = [bool]$case['HelpFailure']
        Calls = [Collections.Generic.List[string]]::new(); Updates = 0
        Delays = [Collections.Generic.List[int]]::new()
    }
    $rejected = $false
    $failure = ''
    try { & (Join-Path $PSScriptRoot 'update-wsl.ps1') }
    catch { $rejected = $true; $failure = $_.Exception.Message }
    if ($rejected -ne [bool]$case['Reject'] -or $global:lokiWSLPreparationFixture.Updates -ne $case.Updates -or
        ($global:lokiWSLPreparationFixture.Delays -join ',') -ne [string]$case['Delays']) {
        throw "WSL preparation case $($case.Name): rejected=$rejected updates=$($global:lokiWSLPreparationFixture.Updates) delays=$($global:lokiWSLPreparationFixture.Delays -join ',') error=$failure"
    }
    if (-not $rejected -and $LASTEXITCODE -ne 0) { throw 'Successful WSL preparation left a failed native exit code' }
}

foreach ($arguments in @(@{ Attempts = 0 }, @{ Attempts = 6 }, @{ DelaySeconds = -1 }, @{ DelaySeconds = 61 })) {
    $global:lokiWSLPreparationFixture.Calls.Clear()
    $rejected = $false
    try { & (Join-Path $PSScriptRoot 'update-wsl.ps1') @arguments }
    catch { $rejected = $true }
    if (-not $rejected -or $global:lokiWSLPreparationFixture.Calls.Count -ne 0) { throw 'Invalid WSL retry limits reached the native command' }
}
Remove-Variable -Name lokiWSLPreparationFixture -Scope Global
Write-Host 'WSL preparation success, failure classification and bounded retry tests passed'
