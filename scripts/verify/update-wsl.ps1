param(
    [int]$Attempts = 3,
    [int]$DelaySeconds = 10
)

$ErrorActionPreference = "Stop"
if ($Attempts -lt 1 -or $Attempts -gt 5) { throw "WSL update attempts must be between 1 and 5" }
if ($DelaySeconds -lt 0 -or $DelaySeconds -gt 60) { throw "WSL update delay must be between 0 and 60 seconds" }

for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
    Write-Host "Updating WSL package (attempt $attempt/$Attempts)..."
    & wsl.exe --update --web-download
    $exitCode = $LASTEXITCODE
    if ($exitCode -eq 0) {
        $global:LASTEXITCODE = 0
        Write-Host "WSL package update succeeded."
        return
    }
    if ($attempt -lt $Attempts) {
        Write-Warning "WSL package update attempt $attempt failed with exit code $exitCode; retrying."
        Start-Sleep -Seconds $DelaySeconds
    }
}

throw "WSL package update unavailable after $Attempts attempts"
