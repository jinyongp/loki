param(
    [Parameter(Mandatory = $true)][string]$Candidate,
    [Parameter(Mandatory = $true)][string]$Tag
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Fail([string]$Message) {
    throw "Loki WSL acceptance: $Message"
}

function Invoke-NativeCapture([string]$Executable, [string[]]$Arguments) {
    $output = & $Executable @Arguments 2>&1
    $code = $LASTEXITCODE
    if ($code -ne 0) {
        Fail "$Executable exited with code $code. $($output -join [Environment]::NewLine)"
    }
    return ($output -join [Environment]::NewLine).Trim()
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

$templatePath = Join-Path $repo "tools\release\install.ps1.tmpl"
$template = Get-Content -LiteralPath $templatePath -Raw
$placeholders = @("@@LOKI_RELEASE_TAG@@", "@@LOKI_WSL_SHA256@@", "@@LOKI_WSL_LENGTH@@")
foreach ($placeholder in $placeholders) {
    if ($template.IndexOf($placeholder, [StringComparison]::Ordinal) -lt 0 -or $template.IndexOf($placeholder, [StringComparison]::Ordinal) -ne $template.LastIndexOf($placeholder, [StringComparison]::Ordinal)) {
        Fail "Windows installer template placeholder contract changed: $placeholder"
    }
}
$rendered = $template.Replace("@@LOKI_RELEASE_TAG@@", $Tag).Replace("@@LOKI_WSL_SHA256@@", [string]$evidence.wsl_appliance.sha256).Replace("@@LOKI_WSL_LENGTH@@", [string]$evidence.wsl_appliance.length)
if ($rendered.Contains("@@LOKI_")) { Fail "rendered Windows installer contains unresolved placeholders" }

$suffix = if ($env:GITHUB_RUN_ID) { "$env:GITHUB_RUN_ID-$env:GITHUB_RUN_ATTEMPT" } else { [Guid]::NewGuid().ToString("N").Substring(0, 12) }
$distributionName = "loki-accept-$suffix"
$installParent = Join-Path $env:RUNNER_TEMP "loki-wsl-acceptance"
$installLocation = Join-Path $installParent $distributionName
New-Item -ItemType Directory -Path $installParent -Force | Out-Null
$installer = Join-Path $env:RUNNER_TEMP "loki-install.ps1"
$utf8 = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText($installer, $rendered, $utf8)

$stateDir = Join-Path $env:LOCALAPPDATA (Join-Path "Loki" $distributionName)
$taskName = "Loki WSL ($distributionName)"
$env:LOKI_WSL_NAME = $distributionName
$env:LOKI_WSL_LOCATION = $installLocation
$env:LOKI_WSL_APPLIANCE_FILE = $wslPath
$env:LOKI_WSL_AUTOSTART = "1"

try {
    & $installer

    $whoami = Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--exec", "/usr/bin/id", "-un")
    $uid = Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--exec", "/usr/bin/id", "-u")
    if ($whoami -ne "ubuntu" -or $uid -ne "1000") { Fail "default WSL user is $whoami/$uid, expected ubuntu/1000" }

    $groups = Invoke-NativeCapture "wsl.exe" @("-d", $distributionName, "--user", "root", "--exec", "/usr/bin/id", "-nG", "ubuntu")
    if (($groups -split "\s+") -contains "docker") { Fail "default WSL user received Docker group authority" }

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

    Invoke-NativeCapture "wsl.exe" @("--terminate", $distributionName) | Out-Null
    Start-Sleep -Seconds 2
    $restartProcess = Start-Process -FilePath "$env:SystemRoot\System32\wsl.exe" -ArgumentList @("-d", $distributionName, "--user", "root", "--exec", "/usr/bin/sleep", "infinity") -WindowStyle Hidden -PassThru
    $recovered = $false
    for ($attempt = 0; $attempt -lt 90; $attempt++) {
        & wsl.exe -d $distributionName --user root --exec /usr/local/bin/loki host doctor --system *> $null
        if ($LASTEXITCODE -eq 0) { $recovered = $true; break }
        Start-Sleep -Seconds 2
    }
    if (-not $recovered) { Fail "Loki did not recover after WSL termination and restart" }

    Write-Host "Loki WSL exact-candidate acceptance passed for $distributionName"
}
finally {
    Remove-Item Env:LOKI_WSL_NAME -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_LOCATION -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_APPLIANCE_FILE -ErrorAction SilentlyContinue
    Remove-Item Env:LOKI_WSL_AUTOSTART -ErrorAction SilentlyContinue
    if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
    }
    $installed = @(& wsl.exe --list --quiet 2>$null) | ForEach-Object { "$_".Trim([char]0).Trim() } | Where-Object { $_ }
    if ($installed | Where-Object { $_.Equals($distributionName, [StringComparison]::OrdinalIgnoreCase) }) {
        & wsl.exe --terminate $distributionName 2>$null | Out-Null
        & wsl.exe --unregister $distributionName 2>$null | Out-Null
    }
    Remove-Item -LiteralPath $stateDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $installLocation -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $installer -Force -ErrorAction SilentlyContinue
}
