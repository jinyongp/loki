package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsInstallerTemplateOwnsWSLBootstrapWithoutUpdatingWSL(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "tools", "release", "install.ps1.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, placeholder := range []string{
		"@@LOKI_RELEASE_TAG@@",
		"@@LOKI_WSL_SHA256@@",
		"@@LOKI_WSL_LENGTH@@",
	} {
		if strings.Count(body, placeholder) != 1 {
			t.Errorf("Windows installer placeholder %q count = %d, want 1", placeholder, strings.Count(body, placeholder))
		}
	}

	for _, required := range []string{
		`$env:LOKI_WSL_NAME`,
		`$env:LOKI_WSL_LOCATION`,
		`$env:LOKI_WSL_AUTOSTART`,
		`$env:LOKI_WSL_APPLIANCE_FILE`,
		`$env:LOKI_MCP_PORT`,
		`Copy-Item -LiteralPath $localAppliance -Destination $appliance`,
		`"--from-file", $appliance, "--name", $distributionName, "--no-launch"`,
		`Get-FileHash -LiteralPath $appliance -Algorithm SHA256`,
		`$file.Length -ne $applianceLength`,
		`Run 'wsl --update' manually, then retry.`,
		"-replace \"`0\", \"\"",
		`[int[]]$AllowedExitCodes = @(0)`,
		`$AllowedExitCodes -notcontains $result.ExitCode`,
		`Invoke-NativeCapture "wsl.exe" @("--help") @(-1, 0, 1)`,
		`function Invoke-NativeResult`,
		`function Get-ProvisionJournalText`,
		`function Test-LoopbackPortAvailable`,
		`function Get-LoopbackPortOwner`,
		`function Get-PortConflictMessage`,
		`function Get-ProvisionFailureMessage`,
		`Get-NetTCPConnection -LocalPort $Port -State Listen`,
		`LOKI_MCP_PORT must be an integer between 1024 and 65535.`,
		`Choose another unused local port and rerun:`,
		`$ErrorActionPreference = "Continue"`,
		`2> $stderrPath`,
		`Write-Host "[Loki] ERROR [$script:currentStage]: $($_.Exception.Message)"`,
		`$createdDistribution = $false`,
		`$createdStateDir = $false`,
		`$createdTask = $false`,
		`$installationComplete = $false`,
		`Windows Loki state already exists for '$distributionName'`,
		`Step "Checking Windows and WSL prerequisites..."`,
		`Test-LoopbackPortAvailable $mcpPort`,
		`MCP local-origin port: $mcpPort`,
		`Step "Preparing Loki $releaseTag WSL appliance..."`,
		`Step "Verifying WSL appliance integrity..."`,
		`Step "Registering WSL distribution '$distributionName'..."`,
		`Step "Starting WSL and provisioning Docker + Loki runtime..."`,
		`[Loki] This can take several minutes.`,
		`"/usr/lib/loki-appliance/configure-install", [string]$mcpPort`,
		`MCP local-origin port configured: $mcpPort`,
		`"/usr/bin/systemctl", "show"`,
		`"--property=NRestarts"`,
		`"--property=ExecMainStatus"`,
		`$maxProvisionRestarts = 3`,
		`Provisioning attempt failed; systemd restart`,
		`First-boot provisioning state:`,
		`Loki appliance provisioning failed repeatedly`,
		`did not complete within 10 minutes`,
		`Step "Verifying Loki host health..."`,
		`Step "Reading MCP connection information..."`,
		`Step "Writing protected Windows MCP connection files..."`,
		`Step "Configuring Windows startup integration..."`,
		`[Loki] Collecting diagnostics before rollback...`,
		`if ($diagnostics) {`,
		`[Loki] Rolling back incomplete installation...`,
		`[Loki] Rollback complete. Removed incomplete WSL distribution '$distributionName'.`,
		`[Loki] You can rerun the installer with the same command.`,
		`Invoke-NativeResult "wsl.exe" @("--terminate", $distributionName)`,
		`Invoke-NativeResult "wsl.exe" @("--unregister", $distributionName)`,
		`Remove-Item -LiteralPath $installLocation -Recurse -Force -ErrorAction SilentlyContinue`,
		`Unregister-ScheduledTask -TaskName $taskName -Confirm:$false`,
		`[Loki] Installation complete.`,
		`$global:LASTEXITCODE = 0`,
		`$global:LASTEXITCODE = 1`,
		`"host", "status", "--system", "--json"`,
		`"host", "doctor", "--system"`,
		`"host", "connection", "--system", "--json"`,
		`schema_version = 1`,
		`local_origin = [ordered]@{`,
		`url = [string]$connection.local_origin.url`,
		`reachability = [string]$connection.local_origin.reachability`,
		`type = [string]$connection.local_origin.authentication.type`,
		`endpoint = [string]$connection.local_origin.url`,
		`authentication = "bearer-token-file"`,
		`token_file = $tokenFile`,
		`MCP local origin:`,
		`Reachability: loopback only`,
		`External access: user-managed`,
		`Loki does not create or manage a public MCP endpoint.`,
		`"/var/lib/loki/lifecycle/mcp-token"`,
		`Join-Path $env:LOCALAPPDATA`,
		`Invoke-NativeCapture "icacls.exe"`,
		`[Security.Principal.WindowsIdentity]::GetCurrent().Name`,
		`New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero)`,
		`New-ScheduledTaskTrigger -AtLogOn -User $identity`,
		`"-d $distributionName --exec /usr/bin/sleep infinity"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Windows installer lacks %q", required)
		}
	}

	for _, forbidden := range []string{
		`Invoke-NativeCapture "wsl.exe" @("--update")`,
		`& wsl.exe --update`,
		`--name Loki`,
		`"/usr/bin/systemctl", "is-failed"`,
		`& wsl.exe -d $distributionName --user root --exec /usr/bin/test`,
		`Write-Host $token`,
		`Write-Output $token`,
		`$diagnostics.Count`,
		`First-boot provisioning is still running`,
		`"/usr/bin/systemctl", "is-active", "--quiet"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Windows installer contains forbidden %q", forbidden)
		}
	}
}

func TestWindowsInstallerRollsBackOnlyFreshResourcesCreatedByCurrentInvocation(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "tools", "release", "install.ps1.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	existingConflict := strings.Index(body, `A WSL distribution named '$distributionName' already exists.`)
	if !strings.Contains(body, `/usr/bin/systemctl status loki-appliance-provision.service --no-pager`) ||
		!strings.Contains(body, `/usr/bin/journalctl -u loki-appliance-provision.service --no-pager -n 80`) ||
		!strings.Contains(body, `The appliance binary, when present, is /usr/lib/loki-appliance/loki; /usr/local/bin/loki may not exist until provisioning succeeds.`) {
		t.Fatal("Windows installer existing-distribution guidance does not cover incomplete appliance provisioning")
	}
	if strings.Contains(body, `inspect it with 'wsl -d $distributionName --user root -- /usr/local/bin/loki host doctor --system'`) {
		t.Fatal("Windows installer still assumes the published CLI exists for interrupted installs")
	}
	stateConflict := strings.Index(body, `Windows Loki state already exists for '$distributionName'`)
	install := strings.Index(body, `$installArgs = @("--install", "--from-file"`)
	markCreated := strings.Index(body, `$createdDistribution = $true`)
	rollbackGuard := strings.Index(body, `if ($createdDistribution -and -not $installationComplete)`)
	unregister := strings.Index(body, `Invoke-NativeResult "wsl.exe" @("--unregister", $distributionName)`)
	if existingConflict < 0 || stateConflict < 0 || install < 0 || markCreated < 0 || rollbackGuard < 0 || unregister < 0 {
		t.Fatal("Windows installer transactional rollback markers are missing")
	}
	if existingConflict > install || stateConflict > install {
		t.Fatal("Windows installer mutates WSL before pre-existing resource conflicts are rejected")
	}
	if markCreated < install {
		t.Fatal("Windows installer claims ownership of the distribution before successful registration")
	}
	if unregister < rollbackGuard {
		t.Fatal("Windows installer can unregister a distribution outside the current-invocation rollback guard")
	}
	if strings.Count(body, `@("--unregister", $distributionName)`) != 1 {
		t.Fatal("Windows installer must have exactly one guarded automatic unregister path")
	}
}

func TestWindowsInstallerPreflightsConflictsBeforeDistributionMutation(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "tools", "release", "install.ps1.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	conflict := strings.Index(body, `A WSL distribution named '$distributionName' already exists.`)
	taskConflict := strings.Index(body, `Scheduled Task '$taskName' already exists.`)
	portConflict := strings.Index(body, `if (-not (Test-LoopbackPortAvailable $mcpPort))`)
	prepare := strings.Index(body, `Step "Preparing Loki $releaseTag WSL appliance..."`)
	install := strings.Index(body, `$installArgs = @("--install", "--from-file"`)
	if conflict < 0 || taskConflict < 0 || portConflict < 0 || prepare < 0 || install < 0 {
		t.Fatal("Windows installer conflict/install markers are missing")
	}
	if conflict > prepare || taskConflict > prepare || portConflict > prepare || prepare > install {
		t.Fatal("Windows installer downloads or mutates WSL before conflict preflight")
	}
}

func TestWindowsWSLAcceptanceRequiresExactCandidateAndRecovery(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "verify", "accept-wsl.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		"evidence.wsl_appliance.path",
		"evidence.wsl_appliance.length",
		"evidence.wsl_appliance.sha256",
		"loki-accept-$suffix",
		"LOKI_WSL_APPLIANCE_FILE",
		"loki-install-rollback.ps1",
		"Windows installer rollback injection marker changed",
		"synthetic acceptance failure after WSL registration",
		"Windows installer rollback probe unexpectedly succeeded",
		"Windows installer rollback left the failed WSL distribution registered",
		"Windows installer rollback left the failed custom WSL location behind",
		"Windows installer retry after rollback failed with code",
		"function Invoke-NativeStdoutCapture",
		"function Assert-WindowsMCPReachability",
		"System.Net.Sockets.TcpClient",
		"Windows cannot reach the WSL MCP endpoint through localhost forwarding",
		`protocolVersion = "2025-11-25"`,
		`method = "initialize"`,
		"Windows MCP initialize probe returned status",
		"Windows installer accepted an occupied default MCP port",
		"Windows MCP port preflight mutated WSL before failing",
		"LOKI_MCP_PORT",
		"$mcpPort = ([System.Net.IPEndPoint]$portSelector.LocalEndpoint).Port",
		"local_origin.authentication.type",
		"Windows MCP local origin does not use the selected port.",
		"Assert-WindowsMCPReachability ([string]$windowsConnection.local_origin.url) $windowsToken",
		`$whoami = Invoke-NativeStdoutCapture "wsl.exe"`,
		`$uid = Invoke-NativeStdoutCapture "wsl.exe"`,
		`if ($whoami -ne "ubuntu" -or $uid -ne "1000")`,
		"default WSL user received Docker group authority",
		"default WSL user received passwordless root authority",
		"/home/ubuntu/workspace",
		`"host", "doctor", "--system"`,
		"Windows MCP token copy does not match",
		"AreAccessRulesProtected",
		"ExecutionTimeLimit",
		"autostart task trigger is not bound to the current user",
		"autostart task principal is not the current user",
		"autostart task requests elevated run level",
		`wsl.exe --terminate $distributionName`,
		"Loki did not recover after WSL termination and restart",
		`wsl.exe --unregister $distributionName`,
		"-replace \"`0\", \"\"",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Windows WSL acceptance lacks %q", required)
		}
	}
}
