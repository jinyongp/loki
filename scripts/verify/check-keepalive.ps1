param(
    [string]$ArtifactDirectory = $PSScriptRoot,
    [string]$Distribution = 'loki-mcp'
)

$ErrorActionPreference = 'Stop'
if ($Distribution -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { throw 'Invalid WSL distribution name' }
$work = Join-Path $env:TEMP ('loki-keepalive-check-' + [Guid]::NewGuid().ToString('N'))
$taskName = 'Loki Keepalive Check (' + [Guid]::NewGuid().ToString('N') + ')'
$registered = $false
$originalCompanion = $env:LOKI_KEEPALIVE_TEST_COMPANION
try {
    New-Item -ItemType Directory -Path $work | Out-Null
    foreach ($name in @('loki-keepalive.exe', 'host-tests.exe', 'process-tests.exe')) {
        Copy-Item -LiteralPath (Join-Path $ArtifactDirectory $name) -Destination (Join-Path $work $name)
    }
    Write-Host 'Checking owned-task migration and companion publication...'
    & (Join-Path $work 'host-tests.exe') '-test.v' '-test.run' '^(TestWindowsKeepaliveTaskScript|TestWindowsKeepalivePayloadPublication|TestWindowsKeepalivePayloadRejectsConsoleAndForeignFiles)$'
    if ($LASTEXITCODE -ne 0) { throw 'Keepalive migration/publication tests failed' }

    $companion = Join-Path $work 'loki-keepalive.exe'
    $env:LOKI_KEEPALIVE_TEST_COMPANION = $companion
    Write-Host 'Checking console suppression, caller exit, and WSL exit-code propagation...'
    & (Join-Path $work 'process-tests.exe') '-test.v' '-test.run' '^TestCompanionHasNoConsoleAndOutlivesCaller$'
    if ($LASTEXITCODE -ne 0) { throw 'Keepalive console/lifetime test failed' }

    # Exercise the real scheduler and real WSL, using a separate task that expires
    # after 30 seconds. Existing Loki tasks, binaries and connections are untouched.
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $action = New-ScheduledTaskAction -Execute $companion -Argument ('--distribution ' + $Distribution)
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Seconds 30) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    $principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
    Register-ScheduledTask -TaskName $taskName -Action $action -Settings $settings -Principal $principal -Description 'Temporary Loki keepalive verification.' | Out-Null
    $registered = $true
    Start-ScheduledTask -TaskName $taskName
    $running = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        Start-Sleep -Milliseconds 200
        if ([string](Get-ScheduledTask -TaskName $taskName).State -eq 'Running') {
            $running = $true
            break
        }
    }
    if (-not $running) {
        $info = Get-ScheduledTaskInfo -TaskName $taskName
        throw ('Temporary keepalive did not remain running; exit code: ' + $info.LastTaskResult)
    }
    Start-Sleep -Seconds 3
    if ([string](Get-ScheduledTask -TaskName $taskName).State -ne 'Running') { throw 'Temporary keepalive exited early' }
    Write-Host 'PASS: native tests passed; console-free keepalive is running through Task Scheduler.'
    Write-Host 'Visual check: no additional terminal window should have appeared.'
} finally {
    if ($registered) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    }
    $env:LOKI_KEEPALIVE_TEST_COMPANION = $originalCompanion
    if (Test-Path -LiteralPath $work) {
        # Give scheduler-owned processes time to exit before deleting the copies.
        for ($attempt = 0; $attempt -lt 20; $attempt++) {
            try { Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction Stop; break }
            catch { if ($attempt -eq 19) { Write-Warning ('Temporary test files remain at ' + $work) }; Start-Sleep -Milliseconds 250 }
        }
    }
}
