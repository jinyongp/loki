param(
    [Parameter(Mandatory = $true)][string]$Candidate,
    [Parameter(Mandatory = $true)][string]$Tag,
    [ValidateSet("full", "integrations", "migration", "recovery")][string]$Scenario = "full"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Fail([string]$Message) {
    throw "Loki WSL acceptance: $Message"
}

function Invoke-NativeCapture([string]$Executable, [string[]]$Arguments) {
    $output = & $Executable @Arguments 2>&1
    $code = $LASTEXITCODE
    $text = (($output -join [Environment]::NewLine) -replace "`0", "").Trim()
    if ($code -ne 0) {
        Fail "$Executable exited with code $code. $text"
    }
    return $text
}

function Invoke-NativeStdoutCapture([string]$Executable, [string[]]$Arguments) {
    $output = & $Executable @Arguments 2>$null
    $code = $LASTEXITCODE
    $text = (($output -join [Environment]::NewLine) -replace "`0", "").Trim()
    if ($code -ne 0) {
        Fail "$Executable exited with code $code while capturing stdout."
    }
    return $text
}

function Assert-WindowsMCPReachability([string]$Endpoint, [string]$Token) {
    $uri = [Uri]$Endpoint
    if ($uri.Scheme -ne "http" -or $uri.Host -ne "127.0.0.1" -or $uri.Port -le 0) {
        Fail "Windows MCP endpoint is not the expected loopback HTTP endpoint."
    }

    $reachable = $false
    $tcp = [System.Net.Sockets.TcpClient]::new()
    try {
        $connect = $tcp.ConnectAsync($uri.Host, $uri.Port)
        if ($connect.Wait(10000) -and $tcp.Connected) { $reachable = $true }
    } catch {
        $reachable = $false
    } finally {
        $tcp.Dispose()
    }
    if (-not $reachable) { Fail "Windows cannot reach the WSL MCP endpoint through localhost forwarding." }

    $initialize = @{
        jsonrpc = "2.0"
        id = 1
        method = "initialize"
        params = @{
            protocolVersion = "2025-11-25"
            capabilities = @{}
            clientInfo = @{ name = "loki-wsl-acceptance"; version = "1" }
        }
    } | ConvertTo-Json -Depth 6 -Compress
    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri $Endpoint -Method Post -Headers @{ Authorization = "Bearer $Token"; Accept = "application/json, text/event-stream" } -ContentType "application/json" -Body $initialize -SkipHttpErrorCheck -TimeoutSec 10
    } catch {
        Fail "Windows could not complete an authenticated MCP initialize probe."
    }
    $status = [int]$response.StatusCode
    if ($status -lt 200 -or $status -ge 300) {
        Fail "Windows MCP initialize probe returned status $status."
    }
}

function Resolve-AccountSID([string]$Account) {
    if (-not $Account) { return "" }
    if ($Account -match "^S-1-") { return $Account }
    try {
        return ([Security.Principal.NTAccount]$Account).Translate([Security.Principal.SecurityIdentifier]).Value
    } catch {
        return ""
    }
}

function Get-RegisteredDistributions {
    return @(& wsl.exe --list --quiet 2>$null) |
        ForEach-Object { (("$_" -replace "`0", "")).Trim() } |
        Where-Object { $_ }
}

function Assert-WSLBootHealthy([string]$Distribution) {
    $userTarget = Invoke-NativeCapture "wsl.exe" @("-d", $Distribution, "--exec", "/usr/bin/systemctl", "--user", "is-active", "default.target")
    if ($userTarget -ne "active") { Fail "default WSL user systemd session is not active: $userTarget" }

    $failedUnits = Invoke-NativeCapture "wsl.exe" @("-d", $Distribution, "--user", "root", "--exec", "/usr/bin/systemctl", "--failed", "--no-legend", "--plain")
    if ($failedUnits) { Fail "WSL boot left failed systemd units: $failedUnits" }
}

function Wait-LokiHealthy([string]$Distribution, [int]$Attempts = 90) {
    for ($attempt = 0; $attempt -lt $Attempts; $attempt++) {
        & wsl.exe -d $Distribution --user root --exec /usr/local/bin/loki host doctor --system *> $null
        if ($LASTEXITCODE -eq 0) {
            Assert-WSLBootHealthy $Distribution
            return
        }
        Start-Sleep -Seconds 2
    }
    Fail "Loki did not become healthy for distribution $Distribution"
}

function Invoke-InstallerNonInteractive([string]$Path) {
    $escaped = $Path.Replace("'", "''")
    $output = @(& pwsh -NoProfile -NonInteractive -Command "& '$escaped'; exit `$global:LASTEXITCODE")
    $code = $LASTEXITCODE
    foreach ($line in $output) {
        Write-Host $line
    }
    return $code
}

$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$candidateRoot = (Resolve-Path $Candidate).Path
$evidencePath = Join-Path $candidateRoot "evidence.json"
$evidence = Get-Content -LiteralPath $evidencePath -Raw | ConvertFrom-Json
$relativeWSL = [string]$evidence.wsl_appliance.path
$wslPath = Join-Path $candidateRoot ($relativeWSL -replace "/", [IO.Path]::DirectorySeparatorChar)
if (-not (Test-Path -LiteralPath $wslPath -PathType Leaf)) { Fail "candidate WSL artifact is missing" }
$wslInfo = Get-Item -LiteralPath $wslPath
if ($wslInfo.Length -ne [Int64]$evidence.wsl_appliance.length) { Fail "candidate WSL length changed" }
$actualSha = (Get-FileHash -LiteralPath $wslPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualSha -ne [string]$evidence.wsl_appliance.sha256) { Fail "candidate WSL SHA-256 changed" }

$relativeFrontend = [string]$evidence.windows_frontend.path
$frontendPath = Join-Path $candidateRoot ($relativeFrontend -replace "/", [IO.Path]::DirectorySeparatorChar)
if (-not (Test-Path -LiteralPath $frontendPath -PathType Leaf)) { Fail "candidate Windows frontend is missing" }
$frontendInfo = Get-Item -LiteralPath $frontendPath
if ($frontendInfo.Length -ne [Int64]$evidence.windows_frontend.length) { Fail "candidate Windows frontend length changed" }
$frontendSha = (Get-FileHash -LiteralPath $frontendPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($frontendSha -ne [string]$evidence.windows_frontend.sha256) { Fail "candidate Windows frontend SHA-256 changed" }

$templatePath = Join-Path $repo "tools\release\install.ps1.tmpl"
$template = Get-Content -LiteralPath $templatePath -Raw
$placeholders = @("@@LOKI_RELEASE_TAG@@", "@@LOKI_WINDOWS_FRONTEND_SHA256@@", "@@LOKI_WINDOWS_FRONTEND_LENGTH@@")
foreach ($placeholder in $placeholders) {
    if ($template.IndexOf($placeholder, [StringComparison]::Ordinal) -lt 0 -or $template.IndexOf($placeholder, [StringComparison]::Ordinal) -ne $template.LastIndexOf($placeholder, [StringComparison]::Ordinal)) {
        Fail "Windows installer template placeholder contract changed: $placeholder"
    }
}
$rendered = $template.Replace("@@LOKI_RELEASE_TAG@@", $Tag).Replace("@@LOKI_WINDOWS_FRONTEND_SHA256@@", [string]$evidence.windows_frontend.sha256).Replace("@@LOKI_WINDOWS_FRONTEND_LENGTH@@", [string]$evidence.windows_frontend.length)
if ($rendered.Contains("@@LOKI_")) { Fail "rendered Windows installer contains unresolved placeholders" }

$suffix = if ($env:GITHUB_RUN_ID) { "$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT" } else { [Guid]::NewGuid().ToString("N").Substring(0, 12) }
$distributionName = "loki-accept-$suffix-$Scenario"
$installParent = Join-Path $env:RUNNER_TEMP "loki-wsl-acceptance"
$installLocation = Join-Path $installParent $distributionName
New-Item -ItemType Directory -Path $installParent -Force | Out-Null
$installer = Join-Path $env:RUNNER_TEMP "loki-install.ps1"
$utf8 = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText($installer, $rendered, $utf8)

$stateDir = Join-Path $env:LOCALAPPDATA (Join-Path "Loki" $distributionName)
$taskName = "Loki WSL ($distributionName)"
$programRoot = Join-Path $env:LOCALAPPDATA "Programs\Loki"
$canonicalFrontend = Join-Path $programRoot "bin\loki.exe"
$frontendOwnershipFile = Join-Path $programRoot "ownership.json"
$env:LOKI_WSL_NAME = $distributionName
$env:LOKI_WSL_LOCATION = $installLocation
$env:LOKI_WSL_APPLIANCE_FILE = $wslPath
$env:LOKI_WINDOWS_FRONTEND_FILE = $frontendPath
$env:LOKI_WSL_AUTOSTART = "1"

$defaultPortBlocker = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 18765)
$defaultPortBlockerStarted = $false
try {
    try {
        $defaultPortBlocker.Start()
        $defaultPortBlockerStarted = $true
    } catch [System.Net.Sockets.SocketException] {
        if ($_.Exception.SocketErrorCode -ne [System.Net.Sockets.SocketError]::AddressAlreadyInUse) {
            throw
        }
    }
    Remove-Item Env:LOKI_MCP_PORT -ErrorAction SilentlyContinue
    $global:LASTEXITCODE = 0
    & $installer
    $portProbeCode = $LASTEXITCODE
    if ($portProbeCode -eq 0) { Fail "Windows installer accepted an occupied default MCP port" }
    $installedAfterPortProbe = @(& wsl.exe --list --quiet 2>$null) | ForEach-Object { (("$_" -replace "`0", "")).Trim() } | Where-Object { $_ }
    if ($installedAfterPortProbe | Where-Object { $_.Equals($distributionName, [StringComparison]::OrdinalIgnoreCase) }) {
        Fail "Windows MCP port preflight mutated WSL before failing"
    }
    if (Test-Path -LiteralPath $installLocation) {
        Fail "Windows MCP port preflight created the custom WSL location"
    }
} finally {
    if ($defaultPortBlockerStarted) {
        $defaultPortBlocker.Stop()
    }
}

$portSelector = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$portSelector.Start()
$mcpPort = ([System.Net.IPEndPoint]$portSelector.LocalEndpoint).Port
$portSelector.Stop()
$env:LOKI_MCP_PORT = [string]$mcpPort

try {
    $global:LASTEXITCODE = 0
    & $installer
    if ($LASTEXITCODE -ne 0) { Fail "Windows thin installer failed with code $LASTEXITCODE" }

    $whoami = Invoke-NativeStdoutCapture "wsl.exe" @("-d", $distributionName, "--exec", "/usr/bin/id", "-un")
    $uid = Invoke-NativeStdoutCapture "wsl.exe" @("-d", $distributionName, "--exec", "/usr/bin/id", "-u")
    if ($whoami -ne "ubuntu" -or $uid -ne "1000") { Fail "default WSL user is $whoami/$uid, expected ubuntu/1000" }
    Assert-WSLBootHealthy $distributionName

    $groups = Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/usr/bin/id", "-nG", "ubuntu")
    if (($groups -split "\s+") -contains "docker") { Fail "default WSL user received Docker group authority" }

    & wsl.exe -d $distributionName --exec /bin/sh -c "if command -v sudo >/dev/null 2>&1 && sudo -n /usr/local/bin/loki version >/dev/null 2>&1; then exit 23; fi" *> $null
    if ($LASTEXITCODE -eq 23) { Fail "default WSL user received passwordless root authority" }
    if ($LASTEXITCODE -ne 0) { Fail "passwordless root-authority probe failed with code $LASTEXITCODE" }

    $workspaceOwner = Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/usr/bin/stat", "-c", "%U:%G", "/home/ubuntu/workspace")
    if ($workspaceOwner -ne "ubuntu:ubuntu") { Fail "workspace ownership is $workspaceOwner" }

    Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/usr/local/bin/loki", "host", "status", "--system", "--json") | Out-Null
    Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/usr/local/bin/loki", "host", "doctor", "--system") | Out-Null

    $tokenFile = Join-Path $stateDir "mcp-token"
    $connectionFile = Join-Path $stateDir "connection.json"
    if (-not (Test-Path -LiteralPath $tokenFile -PathType Leaf) -or -not (Test-Path -LiteralPath $connectionFile -PathType Leaf)) { Fail "Windows MCP connection material is missing" }
    $windowsToken = [IO.File]::ReadAllText($tokenFile).Trim()
    $internalToken = Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/bin/cat", "/var/lib/loki/lifecycle/mcp-token")
    if ($windowsToken -ne $internalToken) { Fail "Windows MCP token copy does not match the installed appliance" }
    $windowsConnection = Get-Content -LiteralPath $connectionFile -Raw | ConvertFrom-Json
    if ([int]$windowsConnection.schema_version -ne 1 -or
        [string]$windowsConnection.local_origin.transport -ne "streamable-http" -or
        [string]$windowsConnection.local_origin.reachability -ne "loopback" -or
        [string]$windowsConnection.local_origin.authentication.type -ne "bearer-token-file" -or
        [string]$windowsConnection.local_origin.authentication.token_file -ne $tokenFile -or
        [string]$windowsConnection.distribution -ne $distributionName) {
        Fail "Windows MCP connection metadata does not match the accepted appliance."
    }
    $localOrigin = [Uri][string]$windowsConnection.local_origin.url
    if ($localOrigin.Host -ne "127.0.0.1" -or $localOrigin.Port -ne $mcpPort -or $localOrigin.AbsolutePath -ne "/mcp") {
        Fail "Windows MCP local origin does not use the selected port."
    }
    Assert-WindowsMCPReachability ([string]$windowsConnection.local_origin.url) $windowsToken

    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $acl = Get-Acl -LiteralPath $tokenFile
    if (-not $acl.AreAccessRulesProtected) { Fail "Windows MCP token ACL still inherits permissions" }
    foreach ($rule in $acl.Access) {
        if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) { continue }
        $value = $rule.IdentityReference.Value
        if ($value -ne $identity -and $value -notmatch "(^|\\)SYSTEM$") { Fail "unexpected Windows MCP token ACL principal: $value" }
    }

    $task = Get-ScheduledTask -TaskName $taskName -ErrorAction Stop
    if (-not $task.Actions.Arguments.Contains($distributionName) -or -not $task.Actions.Arguments.Contains("/usr/bin/sleep infinity")) { Fail "autostart task action does not target the accepted distribution" }
    if ([string]$task.Settings.ExecutionTimeLimit -ne "PT0S") { Fail "autostart task has a finite execution time limit" }
    $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $triggerSid = Resolve-AccountSID ([string]$task.Triggers[0].UserId)
    $principalSid = Resolve-AccountSID ([string]$task.Principal.UserId)
    if ($triggerSid -ne $currentSid) { Fail "autostart task trigger is not bound to the current user" }
    if ($principalSid -ne $currentSid) { Fail "autostart task principal is not the current user" }
    if ([string]$task.Principal.RunLevel -ne "Limited") { Fail "autostart task requests elevated run level" }

    $ownershipFile = Join-Path $stateDir "ownership.json"
    if (-not (Test-Path -LiteralPath $ownershipFile -PathType Leaf)) { Fail "Windows ownership manifest is missing" }
    $ownership = Get-Content -LiteralPath $ownershipFile -Raw | ConvertFrom-Json
    if ([int]$ownership.schema_version -ne 1 -or
        [string]$ownership.distribution -ne $distributionName -or
        [string]$ownership.release_tag -ne $Tag -or
        [string]$ownership.task_name -ne $taskName -or
        [int]$ownership.mcp_port -ne $mcpPort) {
        Fail "Windows ownership manifest does not match the accepted installation"
    }

    if ($Scenario -in @("full", "integrations")) {
        & (Join-Path $PSScriptRoot "accept-managed-integrations.ps1") -Frontend $canonicalFrontend -Distribution $distributionName -ConnectionFile $connectionFile
    }

    if ($Scenario -in @("full", "migration")) {
        # Convert the accepted installation into the exact Windows ownership shape
        # produced by the v0.1.19 baseline while keeping the current candidate
        # appliance bytes. This isolates state/task migration compatibility from
        # appliance-version compatibility.
        $keepaliveBeforeMigration = [pscustomobject]@{
            Execute = [string]$task.Actions[0].Execute
            Arguments = [string]$task.Actions[0].Arguments
            Description = [string]$task.Description
            RunLevel = [string]$task.Principal.RunLevel
            PrincipalSID = $principalSid
            TriggerSID = $triggerSid
            ExecutionTimeLimit = [string]$task.Settings.ExecutionTimeLimit
        }
        $ownership.release_tag = "v0.1.19"
        [IO.File]::WriteAllText($ownershipFile, ($ownership | ConvertTo-Json -Depth 8), $utf8)
        Remove-Item -LiteralPath $programRoot -Recurse -Force -ErrorAction Stop

        $migrationCode = Invoke-InstallerNonInteractive $installer
        if ($migrationCode -ne 0) { Fail "v0.1.19-style healthy adoption failed with code $migrationCode" }
        if (-not (Test-Path -LiteralPath $canonicalFrontend -PathType Leaf) -or
            -not (Test-Path -LiteralPath $frontendOwnershipFile -PathType Leaf)) {
            Fail "v0.1.19-style adoption did not restore the canonical Windows frontend"
        }
        if ((Get-FileHash -LiteralPath $canonicalFrontend -Algorithm SHA256).Hash.ToLowerInvariant() -ne $frontendSha) {
            Fail "adopted canonical frontend does not match the exact candidate bytes"
        }
        $adoptedOwnership = Get-Content -LiteralPath $ownershipFile -Raw | ConvertFrom-Json
        if ([string]$adoptedOwnership.release_tag -ne "v0.1.19") {
            Fail "healthy v0.1.19-style adoption rewrote the appliance ownership proof"
        }
        $taskAfterMigration = Get-ScheduledTask -TaskName $taskName -ErrorAction Stop
        $keepaliveAfterMigration = [pscustomobject]@{
            Execute = [string]$taskAfterMigration.Actions[0].Execute
            Arguments = [string]$taskAfterMigration.Actions[0].Arguments
            Description = [string]$taskAfterMigration.Description
            RunLevel = [string]$taskAfterMigration.Principal.RunLevel
            PrincipalSID = Resolve-AccountSID ([string]$taskAfterMigration.Principal.UserId)
            TriggerSID = Resolve-AccountSID ([string]$taskAfterMigration.Triggers[0].UserId)
            ExecutionTimeLimit = [string]$taskAfterMigration.Settings.ExecutionTimeLimit
        }
        foreach ($field in @("Execute", "Arguments", "Description", "RunLevel", "PrincipalSID", "TriggerSID", "ExecutionTimeLimit")) {
            if ([string]$keepaliveBeforeMigration.$field -cne [string]$keepaliveAfterMigration.$field) {
                Fail "v0.1.19 keepalive task field $field changed during adoption"
            }
        }

        # Force a canonical frontend replacement while holding the existing EXE
        # delete-locked. The first attempt must fail closed; retry after releasing
        # the lock must repair ownership and succeed without manual cleanup.
        $frontendOwnership = Get-Content -LiteralPath $frontendOwnershipFile -Raw | ConvertFrom-Json
        $frontendOwnership.release_tag = "v0.0.1"
        $frontendOwnership.source_revision = "0000000000000000000000000000000000000001"
        [IO.File]::WriteAllText($frontendOwnershipFile, ($frontendOwnership | ConvertTo-Json -Depth 8), $utf8)
        $frontendLock = [IO.File]::Open($canonicalFrontend, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
        try {
            $lockedRetryCode = Invoke-InstallerNonInteractive $installer
            if ($lockedRetryCode -eq 0) { Fail "frontend replacement unexpectedly succeeded while canonical EXE was locked" }
            $stillOldFrontendOwnership = Get-Content -LiteralPath $frontendOwnershipFile -Raw | ConvertFrom-Json
            if ([string]$stillOldFrontendOwnership.release_tag -ne "v0.0.1") {
                Fail "failed locked frontend replacement published new ownership"
            }
        }
        finally {
            $frontendLock.Dispose()
        }
        $unlockedRetryCode = Invoke-InstallerNonInteractive $installer
        if ($unlockedRetryCode -ne 0) { Fail "frontend replacement retry failed after releasing the injected lock" }

        Remove-Item Env:LOKI_WSL_REINSTALL -ErrorAction SilentlyContinue
        $healthyRerunCode = Invoke-InstallerNonInteractive $installer
        if ($healthyRerunCode -ne 0) { Fail "healthy non-interactive installer rerun failed with code $healthyRerunCode" }
        if (-not ((Get-RegisteredDistributions) | Where-Object { $_.Equals($distributionName, [StringComparison]::OrdinalIgnoreCase) })) {
            Fail "healthy installer rerun removed the Loki distribution"
        }

        Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/bin/rm", "-rf", "/var/lib/loki/lifecycle") | Out-Null
        & wsl.exe -d $distributionName --user root --exec /usr/local/bin/loki host doctor --system *> $null
        if ($LASTEXITCODE -eq 0) { Fail "stale-recovery fixture unexpectedly remained healthy" }

        Remove-Item Env:LOKI_WSL_REINSTALL -ErrorAction SilentlyContinue
        $staleDeniedCode = Invoke-InstallerNonInteractive $installer
        if ($staleDeniedCode -eq 0) { Fail "non-interactive stale reinstall succeeded without explicit approval" }
        if (-not ((Get-RegisteredDistributions) | Where-Object { $_.Equals($distributionName, [StringComparison]::OrdinalIgnoreCase) })) {
            Fail "denied stale reinstall removed the Loki distribution"
        }
        if (-not (Test-Path -LiteralPath $stateDir -PathType Container)) {
            Fail "denied stale reinstall removed Windows state"
        }
        if (-not (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) {
            Fail "denied stale reinstall removed the startup task"
        }

        $env:LOKI_WSL_REINSTALL = "1"
        $staleApprovedCode = Invoke-InstallerNonInteractive $installer
        Remove-Item Env:LOKI_WSL_REINSTALL -ErrorAction SilentlyContinue
        if ($staleApprovedCode -ne 0) { Fail "explicit non-interactive stale reinstall failed with code $staleApprovedCode" }
        Wait-LokiHealthy $distributionName
        if (-not (Test-Path -LiteralPath $ownershipFile -PathType Leaf)) {
            Fail "stale reinstall did not recreate Windows ownership state"
        }
    }

    if ($Scenario -in @("full", "recovery")) {
        $flatLegacyConnection = [ordered]@{
            endpoint = "http://127.0.0.1:18765/mcp"
            transport = "streamable-http"
            authentication = "bearer-token-file"
            token_file = $tokenFile
            distribution = $distributionName
        }
        [IO.File]::WriteAllText($connectionFile, ($flatLegacyConnection | ConvertTo-Json -Depth 4), $utf8)
        Remove-Item -LiteralPath $ownershipFile -Force
        Invoke-NativeCapture "wsl.exe" @("--terminate", $distributionName) | Out-Null
        Invoke-NativeCapture "wsl.exe" @("--unregister", $distributionName) | Out-Null
        if ((Get-RegisteredDistributions) | Where-Object { $_.Equals($distributionName, [StringComparison]::OrdinalIgnoreCase) }) {
            Fail "legacy orphan fixture still has a registered WSL distribution"
        }
        if (-not (Test-Path -LiteralPath $stateDir -PathType Container)) {
            Fail "legacy orphan fixture lost Windows connection state"
        }
        if (-not (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) {
            Fail "legacy orphan fixture lost the startup task"
        }

        Remove-Item Env:LOKI_WSL_LOCATION -ErrorAction SilentlyContinue
        Remove-Item Env:LOKI_WSL_REINSTALL -ErrorAction SilentlyContinue
        $orphanRecoveryCode = Invoke-InstallerNonInteractive $installer
        if ($orphanRecoveryCode -ne 0) { Fail "legacy orphan recovery failed with code $orphanRecoveryCode" }
        Wait-LokiHealthy $distributionName
        if (-not (Test-Path -LiteralPath $ownershipFile -PathType Leaf)) {
            Fail "legacy orphan recovery did not publish a new ownership manifest"
        }
        if (-not (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) {
            Fail "legacy orphan recovery did not recreate the startup task"
        }
        $windowsToken = [IO.File]::ReadAllText($tokenFile).Trim()
        $windowsConnection = Get-Content -LiteralPath $connectionFile -Raw | ConvertFrom-Json
        $internalToken = Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/bin/cat", "/var/lib/loki/lifecycle/mcp-token")
        if ($windowsToken -ne $internalToken) {
            Fail "legacy orphan recovery Windows token does not match the reinstalled appliance"
        }
        Assert-WindowsMCPReachability ([string]$windowsConnection.local_origin.url) $windowsToken

        Invoke-NativeCapture "wsl.exe" @("--terminate", $distributionName) | Out-Null
        Start-Sleep -Seconds 2
        Start-ScheduledTask -TaskName $taskName -ErrorAction Stop
        $recovered = $false
        for ($attempt = 0; $attempt -lt 90; $attempt++) {
            & wsl.exe -d $distributionName --user root --exec /usr/local/bin/loki host doctor --system *> $null
            if ($LASTEXITCODE -eq 0) { $recovered = $true; break }
            Start-Sleep -Seconds 2
        }
        if (-not $recovered) { Fail "Loki did not recover when the owned keepalive task was started as a logon/reboot simulation" }
        Assert-WSLBootHealthy $distributionName
        Assert-WindowsMCPReachability ([string]$windowsConnection.local_origin.url) $windowsToken

        $uninstallOutput = @(& $canonicalFrontend uninstall --distribution $distributionName --approve 2>&1)
        $uninstallCode = $LASTEXITCODE
        foreach ($line in $uninstallOutput) { Write-Host $line }
        if ($uninstallCode -ne 0) { Fail "verified Windows uninstall failed with code $uninstallCode" }
        if ((Get-RegisteredDistributions) | Where-Object { $_.Equals($distributionName, [StringComparison]::OrdinalIgnoreCase) }) {
            Fail "verified uninstall left the WSL distribution registered"
        }
        if (Test-Path -LiteralPath $stateDir) { Fail "verified uninstall left per-distribution Windows state" }
        if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) { Fail "verified uninstall left the WSL keepalive task" }
        if (-not (Test-Path -LiteralPath $canonicalFrontend -PathType Leaf) -or
            -not (Test-Path -LiteralPath $frontendOwnershipFile -PathType Leaf)) {
            Fail "verified uninstall removed the shared Windows frontend"
        }

        $freshReinstallCode = Invoke-InstallerNonInteractive $installer
        if ($freshReinstallCode -ne 0) { Fail "fresh reinstall after verified uninstall failed with code $freshReinstallCode" }
        Wait-LokiHealthy $distributionName
        if (-not (Test-Path -LiteralPath $stateDir -PathType Container) -or
            -not (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue)) {
            Fail "fresh reinstall did not restore owned per-distribution state and keepalive task"
        }
    }

    Write-Host "Loki WSL exact-candidate $Scenario acceptance passed for $distributionName on MCP port $mcpPort"
}
finally {
    Remove-Item Env:LOKI_WSL_NAME -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_LOCATION -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_APPLIANCE_FILE -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WINDOWS_FRONTEND_FILE -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_AUTOSTART -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_MCP_PORT -ErrorAction SilentlyContinue
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    }
    $installed = @(& wsl.exe --list --quiet 2>$null) | ForEach-Object { (("$_" -replace "`0", "")).Trim() } | Where-Object { $_ }
    if ($installed | Where-Object { $_.Equals($distributionName, [StringComparison]::OrdinalIgnoreCase) }) {
        & wsl.exe --terminate $distributionName 2>$null | Out-Null
        & wsl.exe --unregister $distributionName 2>$null | Out-Null
    }
    Remove-Item -LiteralPath $stateDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $installLocation -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $installer -Force -ErrorAction SilentlyContinue
}
