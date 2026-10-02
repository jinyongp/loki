Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$path = Join-Path $PSScriptRoot "accept-wsl.ps1"
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw "WSL acceptance has PowerShell syntax errors" }

# Exercise the real parameter contract without starting Windows or WSL.
$parameters = [scriptblock]::Create($ast.ParamBlock.Extent.Text + "`nreturn `$Scenario")
if ((& $parameters -Candidate "fixture" -Tag "v0.0.0") -ne "full") {
    throw "Local WSL acceptance must run the full chain by default"
}
foreach ($scenario in @("full", "integrations", "migration", "recovery")) {
    if ((& $parameters -Candidate "fixture" -Tag "v0.0.0" -Scenario $scenario) -ne $scenario) {
        throw "WSL acceptance rejected scenario $scenario"
    }
}
$rejected = $false
try { & $parameters -Candidate "fixture" -Tag "v0.0.0" -Scenario "unknown" | Out-Null }
catch [Management.Automation.ParameterBindingException] { $rejected = $true }
if (-not $rejected) { throw "Unknown WSL scenarios must fail before any installation" }

$guards = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.IfStatementAst] -and
        $node.Clauses[0].Item1.Extent.Text.Contains('$Scenario')
}, $true))
if ($guards.Count -ne 3) { throw "WSL acceptance must have exactly three scenario groups" }
foreach ($scenario in @("full", "integrations", "migration", "recovery")) {
    $selected = 0
    foreach ($guard in $guards) {
        $condition = [scriptblock]::Create("param(`$Scenario)`nreturn (" + $guard.Clauses[0].Item1.Extent.Text + ")")
        if (& $condition $scenario) { $selected++ }
    }
    $expected = if ($scenario -eq "full") { 3 } else { 1 }
    if ($selected -ne $expected) { throw "WSL scenario $scenario selects $selected groups, expected $expected" }
}

function Get-ScenarioScope($Node) {
    for ($parent = $Node.Parent; $null -ne $parent; $parent = $parent.Parent) {
        if ($parent -is [Management.Automation.Language.IfStatementAst] -and
            $parent.Clauses[0].Item1.Extent.Text.Contains('$Scenario')) {
            $condition = [scriptblock]::Create("param(`$Scenario)`nreturn (" + $parent.Clauses[0].Item1.Extent.Text + ")")
            foreach ($scenario in @("integrations", "migration", "recovery")) {
                if (& $condition $scenario) { return $scenario }
            }
        }
    }
    return "common"
}

# Keep state-dependent failures together and baseline authority checks common.
$expectedScopes = @{
    'candidate WSL SHA-256 changed' = 'common'
    'candidate Windows frontend SHA-256 changed' = 'common'
    'Windows MCP port preflight mutated WSL before failing' = 'common'
    'default WSL user received Docker group authority' = 'common'
    'default WSL user received passwordless root authority' = 'common'
    'Windows MCP token ACL still inherits permissions' = 'common'
    'autostart task requests elevated run level' = 'common'
    'accept-managed-integrations.ps1' = 'integrations'
    'v0.1.19-style healthy adoption failed' = 'migration'
    'v0.1.19 keepalive task field' = 'migration'
    'frontend replacement unexpectedly succeeded while canonical EXE was locked' = 'migration'
    'failed locked frontend replacement published new ownership' = 'migration'
    'frontend replacement retry failed after releasing the injected lock' = 'migration'
    'healthy non-interactive installer rerun failed' = 'migration'
    'non-interactive stale reinstall succeeded without explicit approval' = 'migration'
    'denied stale reinstall removed Windows state' = 'migration'
    'explicit non-interactive stale reinstall failed' = 'migration'
    'legacy orphan recovery failed' = 'recovery'
    'doctor accepted missing WSL boot prerequisites' = 'recovery'
    'doctor accepted a nonpersistent WSL user session' = 'recovery'
    'legacy orphan recovery Windows token does not match the reinstalled appliance' = 'recovery'
    'Loki did not recover when the owned keepalive task was started' = 'recovery'
    'verified Windows uninstall failed' = 'recovery'
    'verified uninstall removed the shared Windows frontend' = 'recovery'
    'fresh reinstall after verified uninstall failed' = 'recovery'
}
$commands = @($ast.FindAll({ param($node) $node -is [Management.Automation.Language.CommandAst] }, $true))
foreach ($fragment in $expectedScopes.Keys) {
    $matches = @($commands | Where-Object { $_.Extent.Text.Contains($fragment) })
    if ($matches.Count -eq 0) { throw "WSL acceptance lost check: $fragment" }
    foreach ($command in $matches) {
        if ((Get-ScenarioScope $command) -ne $expectedScopes[$fragment]) {
            throw "WSL acceptance check moved to the wrong scenario: $fragment"
        }
    }
}

# Execute the actual root-only boot helpers with fake native commands. These
# checks run without WSL and keep default-user probes from masking the failure.
foreach ($name in @("Fail", "Invoke-NativeCapture", "Assert-WSLBootHealthy", "Wait-LokiHealthy", "Assert-WSLRootBootHealthy")) {
    $definition = $ast.Find({
        param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name
    }, $true)
    if ($null -eq $definition) { throw "WSL acceptance lost helper $name" }
    . ([scriptblock]::Create($definition.Extent.Text))
}

$script:bootCalls = [Collections.Generic.List[string]]::new()
$script:bootLinger = "yes"
$script:bootSessions = ""
$script:bootDoctorCalls = 0
$script:bootDoctorFailures = 0
function Stop-ScheduledTask {
    param([string]$TaskName, [string]$ErrorAction)
    $script:bootCalls.Add("stop $TaskName")
}
function Start-ScheduledTask {
    param([string]$TaskName, [string]$ErrorAction)
    $script:bootCalls.Add("start $TaskName")
}
function Start-Sleep {
    param([int]$Seconds)
    $script:bootCalls.Add("sleep $Seconds")
}
function wsl.exe {
    $script:bootCalls.Add("wsl " + ($args -join " "))
    $global:LASTEXITCODE = 0
    if ($args[0] -eq "--terminate") { return }
    if ($args -contains "doctor") {
        $script:bootDoctorCalls++
        if ($script:bootDoctorCalls -le $script:bootDoctorFailures) { $global:LASTEXITCODE = 1 }
        return
    }
    if ($args -contains "/usr/bin/loginctl") {
        if ($args -contains "--property=Linger") { return $script:bootLinger }
        if ($args -contains "--property=Sessions") { return $script:bootSessions }
    }
    if ($args -contains "/usr/sbin/runuser") { return "active" }
    if ($args -contains "check") { return }
    throw "Unexpected WSL boot probe: $args"
}

foreach ($case in @("healthy", "delayed", "missing-linger", "login-session", "not-ready")) {
    $script:bootCalls.Clear()
    $script:bootDoctorCalls = 0
    $script:bootDoctorFailures = switch ($case) { "delayed" { 2 }; "not-ready" { 90 }; default { 0 } }
    $script:bootLinger = if ($case -eq "missing-linger") { "no" } else { "yes" }
    $script:bootSessions = if ($case -eq "login-session") { "7" } else { "" }
    $failure = ""
    try { Assert-WSLRootBootHealthy "fixture-distro" "fixture-task" }
    catch { $failure = $_.Exception.Message }
    $expectedFailure = switch ($case) {
        "missing-linger" { "does not persist without a login" }
        "login-session" { "unexpectedly opened an Ubuntu login session" }
        "not-ready" { "did not become healthy" }
        default { "" }
    }
    if (($expectedFailure -and -not $failure.Contains($expectedFailure)) -or (-not $expectedFailure -and $failure)) {
        throw "Root-only WSL case ${case}: unexpected result $failure"
    }
    if ($script:bootCalls[0] -ne "stop fixture-task" -or
        $script:bootCalls[1] -ne "wsl --terminate fixture-distro" -or
        $script:bootCalls[$script:bootCalls.Count - 1] -ne "start fixture-task") {
        throw "Root-only WSL case ${case}: keepalive stop/terminate/restore ordering changed"
    }
    foreach ($call in $script:bootCalls) {
        if ($call.StartsWith("wsl -d ") -and -not $call.Contains(" --user root --exec ")) {
            throw "Root-only WSL boot opened a default-user session: $call"
        }
    }
    if ($case -eq "delayed" -and $script:bootDoctorCalls -ne 3) {
        throw "Root-only WSL boot did not wait for delayed readiness"
    }
}
Write-Host "WSL scenario routing and state dependencies passed"
