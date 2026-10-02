param(
    [int]$Attempts = 4,
    [int]$DelaySeconds = 15
)

$ErrorActionPreference = "Stop"
$PSNativeCommandUseErrorActionPreference = $false
if ($Attempts -lt 1 -or $Attempts -gt 5) { throw "WSL update attempts must be between 1 and 5" }
if ($DelaySeconds -lt 0 -or $DelaySeconds -gt 60) { throw "WSL update delay must be between 0 and 60 seconds" }

function Test-WSLReady {
    $version = (& wsl.exe --version 2>&1 | Out-String) -replace "`0", ""
    if ($LASTEXITCODE -ne 0) {
        Write-Host "WSL version probe unavailable (exit code $LASTEXITCODE): $($version.Trim())"
        return $false
    }
    $help = (& wsl.exe --help 2>&1 | Out-String) -replace "`0", ""
    # Match WSLClient.RequireInstallCapabilities: WSL help may return 1 or -1
    # even when it prints valid capabilities. Some hosts expose -1 unsigned.
    if ($LASTEXITCODE -notin @(0, 1, -1, 4294967295)) {
        Write-Host "WSL capability probe failed (exit code $LASTEXITCODE): $($help.Trim())"
        return $false
    }
    foreach ($option in @("--from-file", "--name", "--no-launch")) {
        if (-not $help.Contains($option)) {
            Write-Host "Installed WSL lacks required option $option."
            return $false
        }
    }
    Write-Host $version.Trim()
    return $true
}

function Test-TransientWSLDownloadError([string]$Output) {
    # The UpdatePackage 403 seen on hosted runners is a download failure, not
    # an operator authorization failure. Other access/configuration errors fail.
    if ($Output -match '(?i)access(?: is)? denied|permission denied|invalid (?:command|option|parameter)|restart.*required|reboot.*required') { return $false }
    return $Output -match '(?i)Wsl/UpdatePackage/0x80190193|\b(?:HTTP|status|error)[^\r\n]{0,40}\b(?:408|429|5\d\d)\b|0x80072(?:ee2|ee7|efd|efe)\b|\b(?:timed out|connection reset|temporary failure in name resolution)\b'
}

if (Test-WSLReady) {
    $global:LASTEXITCODE = 0
    Write-Host "Installed WSL supports Loki acceptance requirements; no update download needed."
    return
}

for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
    Write-Host "Updating WSL package (attempt $attempt/$Attempts)..."
    $output = (& wsl.exe --update --web-download 2>&1 | Out-String) -replace "`0", ""
    $exitCode = $LASTEXITCODE
    Write-Host $output.Trim()
    if (Test-WSLReady) {
        $global:LASTEXITCODE = 0
        Write-Host "WSL package preparation succeeded."
        return
    }
    if ($exitCode -eq 0) { throw "WSL update completed but required Loki acceptance options are unavailable" }
    if (-not (Test-TransientWSLDownloadError $output)) {
        throw "WSL environment preparation failed with a non-retryable error (exit code $exitCode)"
    }
    if ($attempt -lt $Attempts) {
        $delay = [int][Math]::Min(60, $DelaySeconds * [Math]::Pow(2, $attempt - 1))
        Write-Warning "WSL download attempt $attempt failed; retrying in $delay seconds."
        Start-Sleep -Seconds $delay
    }
}

throw "WSL environment preparation failed: external download unavailable after $Attempts attempts"
