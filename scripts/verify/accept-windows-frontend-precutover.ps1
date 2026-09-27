param(
    [Parameter(Mandatory = $true)][string]$Candidate,
    [Parameter(Mandatory = $true)][string]$Tag
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Fail([string]$Message) {
    throw "Loki Windows frontend pre-cutover acceptance: $Message"
}

function Invoke-NativeResult([string]$Executable, [string[]]$Arguments) {
    $output = @(& $Executable @Arguments 2>&1)
    $code = $LASTEXITCODE
    return [pscustomobject]@{
        Code = $code
        Text = (($output -join [Environment]::NewLine) -replace [char]0, "").Trim()
    }
}

function Invoke-NativeSuccess([string]$Executable, [string[]]$Arguments) {
    $result = Invoke-NativeResult $Executable $Arguments
    if ($result.Code -ne 0) {
        Fail "$Executable exited with code $($result.Code). $($result.Text)"
    }
    return $result.Text
}

function Get-RegisteredDistributions {
    return @(& wsl.exe --list --quiet 2>$null) |
        ForEach-Object { (("$_" -replace [char]0, "")).Trim() } |
        Where-Object { $_ }
}

function Wait-LokiHealthy([string]$Distribution, [int]$Attempts = 90) {
    for ($attempt = 0; $attempt -lt $Attempts; $attempt++) {
        & wsl.exe -d $Distribution --user root --exec /usr/local/bin/loki host doctor --system *> $null
        if ($LASTEXITCODE -eq 0) {
            return
        }
        Start-Sleep -Seconds 2
    }
    Fail "Loki did not become healthy for distribution $Distribution"
}

function Get-UserPathState {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment", $false)
    if ($null -eq $key) {
        return [pscustomobject]@{ Exists = $false; Value = $null; Kind = $null }
    }
    try {
        $names = @($key.GetValueNames())
        if (-not ($names -contains "Path")) {
            return [pscustomobject]@{ Exists = $false; Value = $null; Kind = $null }
        }
        return [pscustomobject]@{
            Exists = $true
            Value = [string]$key.GetValue("Path", "", [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
            Kind = $key.GetValueKind("Path")
        }
    } finally {
        $key.Dispose()
    }
}

function Set-UserPathState($State) {
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment", $true)
    if ($null -eq $key) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey("Environment", $true)
    }
    try {
        if (-not $State.Exists) {
            $key.DeleteValue("Path", $false)
            return
        }
        $key.SetValue("Path", [string]$State.Value, $State.Kind)
    } finally {
        $key.Dispose()
    }
}

function Read-UserPath {
    $state = Get-UserPathState
    if (-not $state.Exists) { return "" }
    return [string]$state.Value
}

function Write-TestUserPath([string]$Value, $OriginalState) {
    $kind = if ($OriginalState.Exists) { $OriginalState.Kind } else { [Microsoft.Win32.RegistryValueKind]::String }
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment", $true)
    if ($null -eq $key) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey("Environment", $true)
    }
    try {
        $key.SetValue("Path", $Value, $kind)
    } finally {
        $key.Dispose()
    }
}

function Assert-PrivateAcl([string]$Path) {
    $acl = Get-Acl -LiteralPath $Path
    if (-not $acl.AreAccessRulesProtected) {
        Fail "ACL inheritance remains enabled for $Path"
    }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    foreach ($rule in $acl.Access) {
        if ($rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) { continue }
        $principal = $rule.IdentityReference.Value
        if ($principal -ne $identity -and $principal -notmatch "(^|\\)SYSTEM$") {
            Fail "unexpected ACL principal $principal on $Path"
        }
    }
}

function Get-KeepaliveTaskContract([string]$TaskName) {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction Stop
    if (@($task.Actions).Count -ne 1) {
        Fail "keepalive task must have exactly one action"
    }
    return [pscustomobject]@{
        Execute = [string]$task.Actions[0].Execute
        Arguments = [string]$task.Actions[0].Arguments
        Description = [string]$task.Description
        RunLevel = [string]$task.Principal.RunLevel
        UserId = [string]$task.Principal.UserId
    }
}

function Assert-KeepaliveTaskUnchanged($Before, $After, [string]$Distribution) {
    foreach ($field in @("Execute", "Arguments", "Description", "RunLevel", "UserId")) {
        if ([string]$Before.$field -cne [string]$After.$field) {
            Fail "legacy keepalive task field $field changed during frontend adoption"
        }
    }
    $expectedArguments = "-d $Distribution --exec /usr/bin/sleep infinity"
    if ($After.Arguments -cne $expectedArguments) {
        Fail "keepalive task arguments are not the exact legacy contract"
    }
    $expectedExecutable = Join-Path $env:SystemRoot "System32\wsl.exe"
    if (-not $After.Execute.Equals($expectedExecutable, [StringComparison]::OrdinalIgnoreCase)) {
        Fail "keepalive task executable is not the exact System32 wsl.exe path"
    }
    if ($After.RunLevel -ne "Limited") {
        Fail "keepalive task requests elevated run level"
    }
}

function Assert-CandidateFile($Evidence, [string]$CandidateRoot, [string]$Label) {
    $path = Join-Path $CandidateRoot (([string]$Evidence.path) -replace "/", [IO.Path]::DirectorySeparatorChar)
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        Fail "$Label is missing from the candidate"
    }
    $info = Get-Item -LiteralPath $path
    if ($info.Length -ne [Int64]$Evidence.length) {
        Fail "$Label length changed"
    }
    $digest = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($digest -ne [string]$Evidence.sha256) {
        Fail "$Label SHA-256 changed"
    }
    return $path
}

function Render-LegacyInstaller([string]$Repo, $Evidence, [string]$ReleaseTag, [string]$Output) {
    $templatePath = Join-Path $Repo "tools\release\install.ps1.tmpl"
    $template = Get-Content -LiteralPath $templatePath -Raw
    foreach ($placeholder in @("@@LOKI_RELEASE_TAG@@", "@@LOKI_WSL_SHA256@@", "@@LOKI_WSL_LENGTH@@")) {
        if ($template.IndexOf($placeholder, [StringComparison]::Ordinal) -lt 0) {
            Fail "pre-cutover PowerShell template no longer has legacy placeholder $placeholder"
        }
    }
    if ($template.Contains("@@LOKI_WINDOWS_FRONTEND_") -or $template.Contains("bootstrap install")) {
        Fail "VAL-012 must run before the public PowerShell bootstrap cutover"
    }
    $rendered = $template.Replace("@@LOKI_RELEASE_TAG@@", $ReleaseTag)
    $rendered = $rendered.Replace("@@LOKI_WSL_SHA256@@", [string]$Evidence.wsl_appliance.sha256)
    $rendered = $rendered.Replace("@@LOKI_WSL_LENGTH@@", [string]$Evidence.wsl_appliance.length)
    if ($rendered.Contains("@@LOKI_")) {
        Fail "rendered legacy installer contains unresolved placeholders"
    }
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($Output, $rendered, $utf8)
}

function Rewrite-LegacyOwnershipRelease([string]$OwnershipPath) {
    $ownership = Get-Content -LiteralPath $OwnershipPath -Raw | ConvertFrom-Json
    $ownership.release_tag = "v0.1.19"
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($OwnershipPath, ($ownership | ConvertTo-Json -Depth 8), $utf8)
}

function Assert-StateShape([string]$StateDir) {
    $names = @(Get-ChildItem -LiteralPath $StateDir -Force | ForEach-Object { $_.Name } | Sort-Object)
    $expected = @("connection.json", "mcp-token", "ownership.json")
    if (($names -join "|") -cne ($expected -join "|")) {
        Fail "legacy per-distribution state shape changed: $($names -join ', ')"
    }
}

function Invoke-LegacyInstaller([string]$Installer) {
    $result = Invoke-NativeResult "pwsh.exe" @("-NoProfile", "-NonInteractive", "-File", $Installer)
    if ($result.Text) { Write-Host $result.Text }
    return $result.Code
}

$repo = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$candidateRoot = (Resolve-Path $Candidate).Path
$evidence = Get-Content -LiteralPath (Join-Path $candidateRoot "evidence.json") -Raw | ConvertFrom-Json
if ([int]$evidence.version -ne 5) { Fail "candidate evidence is not schema v5" }

$wslPath = Assert-CandidateFile $evidence.wsl_appliance $candidateRoot "WSL appliance"
$frontendPath = Assert-CandidateFile $evidence.windows_frontend $candidateRoot "Windows frontend"
$frontendInfo = Get-Item -LiteralPath $frontendPath
$frontendSHA = (Get-FileHash -LiteralPath $frontendPath -Algorithm SHA256).Hash.ToLowerInvariant()

$directVersion = Invoke-NativeResult $frontendPath @("version", "--json")
if ($directVersion.Code -ne 0) {
    Fail "downloaded candidate frontend did not execute directly: $($directVersion.Text)"
}
$versionPayload = $directVersion.Text | ConvertFrom-Json
if ([string]$versionPayload.release_binding.release_tag -ne $Tag) {
    Fail "downloaded candidate frontend release binding does not match $Tag"
}
if ([string]$versionPayload.release_binding.wsl_appliance.sha256 -ne [string]$evidence.wsl_appliance.sha256) {
    Fail "downloaded candidate frontend WSL binding does not match candidate evidence"
}

$suffix = if ($env:GITHUB_RUN_ID) { "$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT" } else { [Guid]::NewGuid().ToString("N").Substring(0, 12) }
$distribution = "loki-frontend-$suffix"
$taskName = "Loki WSL ($distribution)"
$installRoot = Join-Path $env:RUNNER_TEMP "loki-frontend-precutover"
$installLocation = Join-Path $installRoot $distribution
$stateDir = Join-Path $env:LOCALAPPDATA (Join-Path "Loki" $distribution)
$programRoot = Join-Path $env:LOCALAPPDATA "Programs\Loki"
$binDir = Join-Path $programRoot "bin"
$canonicalFrontend = Join-Path $binDir "loki.exe"
$frontendOwnership = Join-Path $programRoot "ownership.json"
$legacyInstaller = Join-Path $env:RUNNER_TEMP "loki-precutover-legacy.ps1"
$originalUserPath = Get-UserPathState

New-Item -ItemType Directory -Path $installRoot -Force | Out-Null
Render-LegacyInstaller $repo $evidence $Tag $legacyInstaller

$portSelector = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$portSelector.Start()
$mcpPort = ([System.Net.IPEndPoint]$portSelector.LocalEndpoint).Port
$portSelector.Stop()

$env:LOKI_WSL_NAME = $distribution
$env:LOKI_WSL_LOCATION = $installLocation
$env:LOKI_WSL_APPLIANCE_FILE = $wslPath
$env:LOKI_WSL_AUTOSTART = "1"
$env:LOKI_MCP_PORT = [string]$mcpPort

try {
    $legacyCode = Invoke-LegacyInstaller $legacyInstaller
    if ($legacyCode -ne 0) {
        Fail "legacy PowerShell baseline install failed with code $legacyCode"
    }
    Wait-LokiHealthy $distribution

    $ownershipPath = Join-Path $stateDir "ownership.json"
    if (-not (Test-Path -LiteralPath $ownershipPath -PathType Leaf)) {
        Fail "legacy ownership manifest is missing"
    }
    Rewrite-LegacyOwnershipRelease $ownershipPath
    Assert-StateShape $stateDir

    $legacyOwnership = Get-Content -LiteralPath $ownershipPath -Raw | ConvertFrom-Json
    if ([string]$legacyOwnership.release_tag -ne "v0.1.19") {
        Fail "legacy ownership baseline was not rewritten to v0.1.19 identity"
    }
    $keepaliveBefore = Get-KeepaliveTaskContract $taskName

    if (Test-Path -LiteralPath $programRoot) {
        Remove-Item -LiteralPath $programRoot -Recurse -Force
    }

    New-Item -ItemType Directory -Path $binDir -Force | Out-Null
    $foreignBytes = [Text.Encoding]::UTF8.GetBytes("foreign frontend acceptance sentinel")
    [IO.File]::WriteAllBytes($canonicalFrontend, $foreignBytes)
    $pathBeforeForeignProbe = Read-UserPath
    $foreignProbe = Invoke-NativeResult $frontendPath @(
        "bootstrap", "install",
        "--distribution", $distribution,
        "--location", $installLocation,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($foreignProbe.Code -eq 0) {
        Fail "bootstrap replaced an unverified canonical frontend"
    }
    if ([Text.Encoding]::UTF8.GetString([IO.File]::ReadAllBytes($canonicalFrontend)) -ne "foreign frontend acceptance sentinel") {
        Fail "foreign canonical frontend changed after refusal"
    }
    if ((Read-UserPath) -cne $pathBeforeForeignProbe) {
        Fail "foreign frontend refusal mutated persistent user PATH"
    }
    Remove-Item -LiteralPath $programRoot -Recurse -Force

    $sentinelBefore = "C:\LokiAcceptanceBefore"
    $sentinelAfter = "C:\LokiAcceptanceAfter"
    $basePath = if ($originalUserPath.Exists -and [string]$originalUserPath.Value) { [string]$originalUserPath.Value } else { "" }
    $testPath = if ($basePath) { "$sentinelBefore;$basePath;$sentinelAfter" } else { "$sentinelBefore;$sentinelAfter" }
    Write-TestUserPath $testPath $originalUserPath

    $bootstrap = Invoke-NativeResult $frontendPath @(
        "bootstrap", "install",
        "--distribution", $distribution,
        "--location", $installLocation,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($bootstrap.Code -ne 0) {
        Fail "trusted frontend bootstrap failed: $($bootstrap.Text)"
    }
    if (-not (Test-Path -LiteralPath $canonicalFrontend -PathType Leaf)) {
        Fail "canonical Windows frontend was not installed"
    }
    if (-not (Test-Path -LiteralPath $frontendOwnership -PathType Leaf)) {
        Fail "frontend ownership manifest was not installed"
    }
    if ((Get-FileHash -LiteralPath $canonicalFrontend -Algorithm SHA256).Hash.ToLowerInvariant() -ne $frontendSHA) {
        Fail "canonical frontend bytes differ from the accepted candidate"
    }
    $managedPath = Read-UserPath
    $managedParts = @($managedPath.Split(";") | Where-Object { $_.Equals($binDir, [StringComparison]::OrdinalIgnoreCase) })
    if ($managedParts.Count -ne 1) {
        Fail "persistent user PATH does not contain exactly one canonical Loki bin entry"
    }
    if (-not $managedPath.StartsWith("$sentinelBefore;", [StringComparison]::Ordinal) -or
        $managedPath.IndexOf(";$sentinelAfter", [StringComparison]::Ordinal) -lt 0) {
        Fail "persistent user PATH did not preserve unrelated entries"
    }
    Assert-PrivateAcl $programRoot
    Assert-PrivateAcl $binDir
    Assert-PrivateAcl $canonicalFrontend
    Assert-PrivateAcl $frontendOwnership

    $frontendOwnershipPayload = Get-Content -LiteralPath $frontendOwnership -Raw | ConvertFrom-Json
    if ([string]$frontendOwnershipPayload.release_tag -ne $Tag -or
        [string]$frontendOwnershipPayload.sha256 -ne $frontendSHA -or
        [Int64]$frontendOwnershipPayload.length -ne $frontendInfo.Length -or
        [string]$frontendOwnershipPayload.architecture -ne "windows-amd64" -or
        -not ([string]$frontendOwnershipPayload.canonical_path).Equals($canonicalFrontend, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([string]$frontendOwnershipPayload.path_entry).Equals($binDir, [StringComparison]::OrdinalIgnoreCase)) {
        Fail "frontend ownership manifest does not bind the canonical candidate"
    }

    $keepaliveAfter = Get-KeepaliveTaskContract $taskName
    Assert-KeepaliveTaskUnchanged $keepaliveBefore $keepaliveAfter $distribution
    Assert-StateShape $stateDir
    $adoptedOwnership = Get-Content -LiteralPath $ownershipPath -Raw | ConvertFrom-Json
    if ([string]$adoptedOwnership.release_tag -ne "v0.1.19") {
        Fail "healthy frontend adoption rewrote the legacy WSL ownership manifest"
    }

    $sameVersion = Invoke-NativeResult $frontendPath @(
        "bootstrap", "install",
        "--distribution", $distribution,
        "--location", $installLocation,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($sameVersion.Code -ne 0) {
        Fail "same-version frontend bootstrap repair failed: $($sameVersion.Text)"
    }

    $ownedForLock = Get-Content -LiteralPath $frontendOwnership -Raw | ConvertFrom-Json
    $ownedForLock.release_tag = "v0.0.1"
    $ownedForLock.source_revision = "0000000000000000000000000000000000000001"
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($frontendOwnership, ($ownedForLock | ConvertTo-Json -Depth 8), $utf8)
    $lock = [IO.File]::Open($canonicalFrontend, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        $locked = Invoke-NativeResult $frontendPath @(
            "bootstrap", "install",
            "--distribution", $distribution,
            "--location", $installLocation,
            "--mcp-port", [string]$mcpPort,
            "--autostart", "true",
            "--appliance-file", $wslPath
        )
        if ($locked.Code -eq 0) {
            Fail "frontend replacement unexpectedly succeeded while canonical executable was delete-locked"
        }
        $stillOwned = Get-Content -LiteralPath $frontendOwnership -Raw | ConvertFrom-Json
        if ([string]$stillOwned.release_tag -ne "v0.0.1") {
            Fail "failed locked replacement published new frontend ownership"
        }
    } finally {
        $lock.Dispose()
    }
    $unlocked = Invoke-NativeResult $frontendPath @(
        "bootstrap", "install",
        "--distribution", $distribution,
        "--location", $installLocation,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($unlocked.Code -ne 0) {
        Fail "frontend replacement did not recover after executable lock was released: $($unlocked.Text)"
    }

    Remove-Item -LiteralPath $frontendOwnership -Force
    $interrupted = Invoke-NativeResult $frontendPath @(
        "bootstrap", "install",
        "--distribution", $distribution,
        "--location", $installLocation,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($interrupted.Code -ne 0 -or -not (Test-Path -LiteralPath $frontendOwnership -PathType Leaf)) {
        Fail "exact-byte interrupted frontend ownership recovery failed"
    }

    $statusText = Invoke-NativeSuccess $canonicalFrontend @("status", "--distribution", $distribution)
    if (-not $statusText.Contains("Frontend: $Tag") -or -not $statusText.Contains("Status: installed")) {
        Fail "Windows status output does not distinguish frontend and appliance state"
    }
    $statusJSON = Invoke-NativeSuccess $canonicalFrontend @("status", "--distribution", $distribution, "--json") | ConvertFrom-Json
    if ([int]$statusJSON.schema_version -ne 1 -or
        [string]$statusJSON.frontend.release_tag -ne $Tag -or
        [int]$statusJSON.status.schema_version -ne 1) {
        Fail "Windows status JSON does not preserve the operator schema boundary"
    }
    Invoke-NativeSuccess $canonicalFrontend @("doctor", "--distribution", $distribution) | Out-Null

    $connection = Invoke-NativeSuccess $canonicalFrontend @("connection", "--distribution", $distribution, "--json") | ConvertFrom-Json
    if ([int]$connection.schema_version -ne 1 -or
        [string]$connection.distribution -ne $distribution -or
        [string]$connection.local_origin.transport -ne "streamable-http" -or
        [string]$connection.local_origin.reachability -ne "loopback") {
        Fail "delegated Windows connection output does not match the expected schema"
    }
    $tokenPath = Join-Path $stateDir "mcp-token"
    $windowsToken = [IO.File]::ReadAllText($tokenPath).Trim()
    $liveToken = Invoke-NativeSuccess "wsl.exe" @("-d", $distribution, "--user", "root", "--exec", "/bin/cat", "/var/lib/loki/lifecycle/mcp-token")
    if ($windowsToken -ne $liveToken) {
        Fail "Windows connection refresh did not copy the live appliance token"
    }

    Invoke-NativeSuccess $canonicalFrontend @("update", "status", "--distribution", $distribution) | Out-Null
    $backup = Invoke-NativeSuccess $canonicalFrontend @("backup", "--distribution", $distribution) | ConvertFrom-Json
    $backupID = [string]$backup.id
    if (-not $backupID) {
        Fail "delegated backup did not return a backup id"
    }
    [IO.File]::WriteAllText($tokenPath, "stale-windows-token", $utf8)
    $restore = Invoke-NativeResult $canonicalFrontend @("restore", "--distribution", $distribution, $backupID)
    if ($restore.Code -ne 0) {
        Fail "delegated restore failed: $($restore.Text)"
    }
    $refreshedToken = [IO.File]::ReadAllText($tokenPath).Trim()
    $liveTokenAfterRestore = Invoke-NativeSuccess "wsl.exe" @("-d", $distribution, "--user", "root", "--exec", "/bin/cat", "/var/lib/loki/lifecycle/mcp-token")
    if ($refreshedToken -ne $liveTokenAfterRestore -or $refreshedToken -eq "stale-windows-token") {
        Fail "restore did not refresh the Windows token replica from the live appliance"
    }

    Invoke-NativeSuccess "wsl.exe" @("-d", $distribution, "--user", "root", "--exec", "/bin/rm", "-rf", "/var/lib/loki/lifecycle") | Out-Null
    & wsl.exe -d $distribution --user root --exec /usr/local/bin/loki host doctor --system *> $null
    if ($LASTEXITCODE -eq 0) {
        Fail "stale recovery fixture unexpectedly remained healthy"
    }

    $denied = Invoke-NativeResult $canonicalFrontend @(
        "install",
        "--distribution", $distribution,
        "--location", $installLocation,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($denied.Code -eq 0) {
        Fail "non-interactive stale reinstall succeeded without explicit approval"
    }
    if (-not ((Get-RegisteredDistributions) | Where-Object { $_.Equals($distribution, [StringComparison]::OrdinalIgnoreCase) })) {
        Fail "denied stale reinstall removed the distribution"
    }
    Assert-StateShape $stateDir
    Get-ScheduledTask -TaskName $taskName -ErrorAction Stop | Out-Null

    $approved = Invoke-NativeResult $canonicalFrontend @(
        "install",
        "--distribution", $distribution,
        "--location", $installLocation,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath,
        "--reinstall"
    )
    if ($approved.Code -ne 0) {
        Fail "explicit stale reinstall failed: $($approved.Text)"
    }
    Wait-LokiHealthy $distribution

    $connectionFile = Join-Path $stateDir "connection.json"
    $flatLegacy = [ordered]@{
        endpoint = "http://127.0.0.1:18765/mcp"
        transport = "streamable-http"
        authentication = "bearer-token-file"
        token_file = $tokenPath
        distribution = $distribution
    }
    [IO.File]::WriteAllText($connectionFile, ($flatLegacy | ConvertTo-Json -Depth 4), $utf8)
    Remove-Item -LiteralPath $ownershipPath -Force
    Invoke-NativeSuccess "wsl.exe" @("--terminate", $distribution) | Out-Null
    Invoke-NativeSuccess "wsl.exe" @("--unregister", $distribution) | Out-Null
    Remove-Item Env:LOKI_WSL_LOCATION -ErrorAction SilentlyContinue

    $orphan = Invoke-NativeResult $canonicalFrontend @(
        "install",
        "--distribution", $distribution,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($orphan.Code -ne 0) {
        Fail "verified legacy orphan recovery failed: $($orphan.Text)"
    }
    Wait-LokiHealthy $distribution
    Assert-StateShape $stateDir

    $uninstall = Invoke-NativeResult $canonicalFrontend @("uninstall", "--distribution", $distribution, "--approve")
    if ($uninstall.Code -ne 0) {
        Fail "verified Windows uninstall failed: $($uninstall.Text)"
    }
    if ((Get-RegisteredDistributions) | Where-Object { $_.Equals($distribution, [StringComparison]::OrdinalIgnoreCase) }) {
        Fail "uninstall left the WSL distribution registered"
    }
    if (Test-Path -LiteralPath $stateDir) {
        Fail "uninstall left per-distribution Windows state"
    }
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        Fail "uninstall left the legacy keepalive task"
    }
    if (-not (Test-Path -LiteralPath $canonicalFrontend -PathType Leaf) -or
        -not (Test-Path -LiteralPath $frontendOwnership -PathType Leaf)) {
        Fail "uninstall removed the shared Windows frontend"
    }

    $reinstall = Invoke-NativeResult $canonicalFrontend @(
        "install",
        "--distribution", $distribution,
        "--mcp-port", [string]$mcpPort,
        "--autostart", "true",
        "--appliance-file", $wslPath
    )
    if ($reinstall.Code -ne 0) {
        Fail "fresh reinstall after uninstall failed: $($reinstall.Text)"
    }
    Wait-LokiHealthy $distribution
    Invoke-NativeSuccess $canonicalFrontend @("doctor", "--distribution", $distribution) | Out-Null

    Write-Host "Loki Windows frontend pre-cutover acceptance passed for $distribution on MCP port $mcpPort"
}
finally {
    Remove-Item Env:LOKI_WSL_NAME -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_LOCATION -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_APPLIANCE_FILE -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_AUTOSTART -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_MCP_PORT -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_REINSTALL -ErrorAction SilentlyContinue

    if (Test-Path -LiteralPath $canonicalFrontend -PathType Leaf) {
        & $canonicalFrontend uninstall --distribution $distribution --approve *> $null
    }
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    }
    if ((Get-RegisteredDistributions) | Where-Object { $_.Equals($distribution, [StringComparison]::OrdinalIgnoreCase) }) {
        & wsl.exe --terminate $distribution 2>$null | Out-Null
        & wsl.exe --unregister $distribution 2>$null | Out-Null
    }
    Remove-Item -LiteralPath $stateDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $installLocation -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $programRoot -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $legacyInstaller -Force -ErrorAction SilentlyContinue
    Set-UserPathState $originalUserPath
}
