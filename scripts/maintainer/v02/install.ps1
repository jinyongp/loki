param(
    [string]$BinDirectory = (Join-Path $env:LOCALAPPDATA 'Programs\Loki'),
    [string]$ManagementRoot,
    [switch]$AddToUserPath
)
$ErrorActionPreference = 'Stop'
$binary = Join-Path $PSScriptRoot 'loki.exe'
$arguments = @()
if ($ManagementRoot) { $arguments += @('--root', $ManagementRoot) }
$arguments += @('install', '--bin-dir', $BinDirectory)
& $binary @arguments
if ($LASTEXITCODE -ne 0) { throw 'Loki management installation failed; see the command error above.' }
if ($AddToUserPath) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $parts = @($userPath -split ';' | Where-Object { $_ })
    if ($parts -notcontains $BinDirectory) {
        [Environment]::SetEnvironmentVariable('Path', (($parts + $BinDirectory) -join ';'), 'User')
    }
    Write-Host 'Open a new terminal to use the updated user PATH.'
}
