param(
    [Parameter(Mandatory = $true)][string]$Frontend,
    [Parameter(Mandatory = $true)][string]$Distribution,
    [Parameter(Mandatory = $true)][string]$ConnectionFile
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$connection = Get-Content -LiteralPath $ConnectionFile -Raw | ConvertFrom-Json
$endpoint = [string]$connection.local_origin.url
$token = [IO.File]::ReadAllText([string]$connection.local_origin.authentication.token_file).Trim()

function Invoke-IntegrationCLI([string[]]$Arguments) {
    $output = @(& $Frontend @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "Managed integration command failed: $($Arguments -join ' '): $($output -join [Environment]::NewLine)"
    }
    return ($output -join [Environment]::NewLine).Trim()
}

function Get-IntegrationStatus([string]$Name) {
    return (Invoke-IntegrationCLI @("integration", "status", "--distribution", $Distribution, "--json", $Name) | ConvertFrom-Json)
}

function Invoke-MCP([hashtable]$Session, [string]$Method, [hashtable]$Parameters, [switch]$Notification) {
    $body = @{ jsonrpc = "2.0"; method = $Method; params = $Parameters }
    if (-not $Notification) { $body.id = [Guid]::NewGuid().ToString() }
    $headers = @{ Authorization = "Bearer $token"; Accept = "application/json, text/event-stream"; "MCP-Protocol-Version" = "2025-11-25" }
    if ($Session.id) { $headers["Mcp-Session-Id"] = $Session.id }
    $response = Invoke-WebRequest -Uri $endpoint -Method Post -Headers $headers -ContentType "application/json" -Body ($body | ConvertTo-Json -Depth 20 -Compress) -SkipHttpErrorCheck -TimeoutSec 60
    if ([int]$response.StatusCode -lt 200 -or [int]$response.StatusCode -ge 300) {
        throw "MCP $Method returned HTTP $([int]$response.StatusCode): $($response.Content)"
    }
    if ($Method -eq "initialize") {
        $Session.id = $response.Headers["Mcp-Session-Id"] -join ""
        if (-not $Session.id) { throw "MCP initialize did not return the required stateful session" }
    }
    if ($Notification) { return }
    $text = ([string]$response.Content).Trim()
    $payload = $null
    if ($text.StartsWith("{")) {
        $payload = $text | ConvertFrom-Json -AsHashtable
    } else {
        foreach ($line in ($text -split "\r?\n")) {
            if ($line.StartsWith("data:")) {
                $candidate = $line.Substring(5).Trim() | ConvertFrom-Json -AsHashtable
                if ($candidate.id -eq $body.id) { $payload = $candidate; break }
            }
        }
    }
    if ($null -eq $payload -or $payload.id -ne $body.id) { throw "MCP $Method returned no matching response" }
    if ($payload.ContainsKey("error")) { throw "MCP $Method failed: $($payload.error | ConvertTo-Json -Compress)" }
    return $payload.result
}

function Connect-MCP {
    $session = @{ id = "" }
    Invoke-MCP $session "initialize" @{ protocolVersion = "2025-11-25"; capabilities = @{}; clientInfo = @{ name = "loki-integration-acceptance"; version = "1" } } | Out-Null
    Invoke-MCP $session "notifications/initialized" @{} -Notification
    return $session
}

function Invoke-LokiTool([hashtable]$Session, [string]$Name, [hashtable]$Arguments, [switch]$ExpectError) {
    $result = Invoke-MCP $Session "tools/call" @{ name = $Name; arguments = $Arguments }
    $failed = $result.ContainsKey("isError") -and $result.isError
    if ($ExpectError) {
        if (-not $failed) { throw "Unavailable integration unexpectedly accepted $Name" }
        return
    }
    if ($failed) { throw "MCP tool $Name failed: $($result.content | ConvertTo-Json -Depth 8 -Compress)" }
    if ($result.ContainsKey("structuredContent")) { return $result.structuredContent }
    foreach ($item in $result.content) {
        if ($item.type -eq "text") { return ($item.text | ConvertFrom-Json -AsHashtable) }
    }
    throw "MCP tool $Name returned no structured result"
}

function Invoke-GitJob([hashtable]$Session, [string]$Cwd, [string[]]$Argv, [switch]$ExpectFailure) {
    $job = Invoke-LokiTool $Session "job" @{ action = "start"; request_id = [Guid]::NewGuid().ToString(); cwd = $Cwd; argv = $Argv; network = "none"; timeout_seconds = 60 }
    for ($attempt = 0; $attempt -lt 90; $attempt++) {
        $status = Invoke-LokiTool $Session "job" @{ action = "inspect"; job_id = $job.job_id }
        if ($status.state -eq "terminal" -and $status.cleanup -in @("complete", "not_required")) {
            $output = Invoke-LokiTool $Session "job" @{ action = "output"; job_id = $job.job_id }
            if ($status.outcome -ne "exited" -or -not $status.ContainsKey("exit_code")) { throw "Job did not execute normally: $($status | ConvertTo-Json -Compress) $($output.output)" }
            if (($status.exit_code -eq 0) -eq [bool]$ExpectFailure) { throw "Unexpected Git Job exit $($status.exit_code): $($output.output)" }
            return $output.output
        }
        Start-Sleep -Seconds 1
    }
    throw "Git Job did not finish and clean up: $($job.job_id)"
}

$list = Invoke-IntegrationCLI @("integration", "list", "--distribution", $Distribution, "--json") | ConvertFrom-Json
if (@($list.integrations).Count -ne 3) { throw "Fresh integration list is incomplete" }
foreach ($item in $list.integrations) {
    if ($item.enabled -or $item.ready) { throw "Fresh installation granted optional authority: $($item.name)" }
}
$session = Connect-MCP
$catalog = @((Invoke-MCP $session "tools/list" @{}).tools | ForEach-Object { $_.name } | Sort-Object) -join "`n"
Invoke-LokiTool $session "browser_session" @{ action = "start" } -ExpectError
Invoke-LokiTool $session "github_read" @{ action = "repository"; target = "example-org/integration-fixture" } -ExpectError

Write-Host "Verifying managed browser enable, MCP use, and disable"
Invoke-IntegrationCLI @("integration", "enable", "--distribution", $Distribution, "browser") | Out-Null
if (-not (Get-IntegrationStatus "browser").ready) { throw "Enabled browser is not ready" }
$session = Connect-MCP
$enabledCatalog = @((Invoke-MCP $session "tools/list" @{}).tools | ForEach-Object { $_.name } | Sort-Object) -join "`n"
if ($catalog -cne $enabledCatalog) { throw "Enabling browser changed the MCP tool catalog" }
$browser = Invoke-LokiTool $session "browser_session" @{ action = "start" }
if ($browser.status -ne "running") { throw "Browser did not start Chromium" }
Invoke-LokiTool $session "browser_session" @{ action = "stop" } | Out-Null
Invoke-IntegrationCLI @("integration", "disable", "--distribution", $Distribution, "browser") | Out-Null
$session = Connect-MCP
Invoke-LokiTool $session "browser_session" @{ action = "start" } -ExpectError

Write-Host "Verifying managed signing through isolated MCP Jobs"
Invoke-IntegrationCLI @("integration", "setup", "signing", "--distribution", $Distribution, "--identity-name", "Loki Acceptance", "--identity-email", "signing@example.test") | Out-Null
$signing = Get-IntegrationStatus "signing"
if (-not $signing.ready -or -not $signing.fingerprint) { throw "Signing setup did not expose a ready public identity" }
$originalFingerprint = $signing.fingerprint
$fixture = ".loki-integration-acceptance-" + [Guid]::NewGuid().ToString("N")
$session = Connect-MCP
Invoke-GitJob $session "." @("/usr/bin/git", "init", "-q", "--initial-branch=main", $fixture) | Out-Null
Invoke-GitJob $session $fixture @("/usr/bin/git", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "managed signing fixture") | Out-Null
Invoke-GitJob $session $fixture @("/usr/bin/git", "verify-commit", "HEAD") | Out-Null
Invoke-IntegrationCLI @("integration", "disable", "--distribution", $Distribution, "signing") | Out-Null
$session = Connect-MCP
Invoke-GitJob $session $fixture @("/usr/bin/ssh-add", "-l") -ExpectFailure | Out-Null
Invoke-IntegrationCLI @("integration", "enable", "--distribution", $Distribution, "signing") | Out-Null
if ((Get-IntegrationStatus "signing").fingerprint -cne $originalFingerprint) { throw "Signing enable replaced the persisted key" }
$session = Connect-MCP
Invoke-GitJob $session $fixture @("/usr/bin/git", "verify-commit", "HEAD") | Out-Null
Invoke-IntegrationCLI @("integration", "rotate", "signing", "--distribution", $Distribution, "--identity-name", "Loki Acceptance", "--identity-email", "signing@example.test") | Out-Null
if ((Get-IntegrationStatus "signing").fingerprint -ceq $originalFingerprint) { throw "Signing rotation kept the old key" }
$session = Connect-MCP
Invoke-GitJob $session $fixture @("/usr/bin/git", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "rotated signing fixture") | Out-Null
Invoke-GitJob $session $fixture @("/usr/bin/git", "verify-commit", "HEAD") | Out-Null
Invoke-IntegrationCLI @("integration", "remove", "--distribution", $Distribution, "signing") | Out-Null
$removed = Get-IntegrationStatus "signing"
if ($removed.configured -or $removed.enabled -or $removed.ready) { throw "Signing removal retained authority" }
$session = Connect-MCP
Invoke-GitJob $session $fixture @("/usr/bin/ssh-add", "-l") -ExpectFailure | Out-Null
Invoke-GitJob $session "." @("/bin/rm", "-rf", $fixture) | Out-Null
Invoke-LokiTool $session "github_read" @{ action = "repository"; target = "example-org/integration-fixture" } -ExpectError
$finalCatalog = @((Invoke-MCP $session "tools/list" @{}).tools | ForEach-Object { $_.name } | Sort-Object) -join "`n"
if ($catalog -cne $finalCatalog) { throw "Integration lifecycle changed the MCP tool catalog" }
Write-Host "Managed integration exact-candidate acceptance passed"
