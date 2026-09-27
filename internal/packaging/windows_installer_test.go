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
		`function Get-LokiWindowsStateOwnership`,
		`function Get-LokiStartupTaskOwnership`,
		`function Get-LokiDistributionState`,
		`function Write-LokiOwnershipManifest`,
		`ownership.json`,
		`/usr/lib/loki-appliance/release-manifest.json`,
		`/usr/lib/loki-appliance/loki", "version"`,
		`Loki is already installed and healthy.`,
		`is still provisioning.`,
		`exists but is not a verified Loki appliance.`,
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
		`Windows state exists at '$stateDir' but does not prove Loki ownership.`,
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

	distroClassification := strings.Index(body, `$preflight = Get-LokiPreflightSnapshot`)
	if !strings.Contains(body, `/usr/bin/systemctl status loki-appliance-provision.service --no-pager`) ||
		!strings.Contains(body, `/usr/lib/loki-appliance/release-manifest.json`) ||
		!strings.Contains(body, `/usr/lib/loki-appliance/loki", "version"`) {
		t.Fatal("Windows installer existing-distribution classification does not verify appliance identity and provisioning state")
	}
	stateConflict := strings.Index(body, `Windows state exists at '$stateDir' but does not prove Loki ownership.`)
	install := strings.Index(body, `$installArgs = @("--install", "--from-file"`)
	markCreated := strings.Index(body, `$createdDistribution = $true`)
	rollbackGuard := strings.Index(body, `if ($createdDistribution -and -not $installationComplete)`)
	recoveryApproval := strings.Index(body, `Confirm-LokiStaleReinstall $distroState $windowsState $startupTask`)
	recoveryUnregister := strings.Index(body, `$unregisterResult = Invoke-NativeResult "wsl.exe" @("--unregister", $distributionName)`)
	rollbackUnregister := strings.LastIndex(body, `$unregisterResult = Invoke-NativeResult "wsl.exe" @("--unregister", $distributionName)`)
	if distroClassification < 0 || stateConflict < 0 || install < 0 || markCreated < 0 || rollbackGuard < 0 ||
		recoveryApproval < 0 || recoveryUnregister < 0 || rollbackUnregister < 0 {
		t.Fatal("Windows installer transactional rollback/recovery markers are missing")
	}
	if distroClassification > install || stateConflict > install {
		t.Fatal("Windows installer mutates WSL before pre-existing resource conflicts are rejected")
	}
	if markCreated < install {
		t.Fatal("Windows installer claims ownership of the distribution before successful registration")
	}
	if recoveryUnregister < recoveryApproval || recoveryUnregister > install {
		t.Fatal("Windows installer stale-distro unregister is not gated by recovery approval before fresh install")
	}
	if rollbackUnregister < rollbackGuard {
		t.Fatal("Windows installer fresh-install rollback unregister is outside the current-invocation rollback guard")
	}
	if strings.Count(body, `@("--unregister", $distributionName)`) != 2 {
		t.Fatal("Windows installer must have exactly one approved recovery unregister and one fresh-install rollback unregister")
	}
}

func TestWindowsInstallerPreflightsConflictsBeforeDistributionMutation(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "tools", "release", "install.ps1.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	distroConflict := strings.Index(body, `exists but is not a verified Loki appliance.`)
	taskConflict := strings.Index(body, `Scheduled Task '$taskName' exists but does not match Loki's managed startup action.`)
	stateConflict := strings.Index(body, `Windows state exists at '$stateDir' but does not prove Loki ownership.`)
	portConflict := strings.Index(body, `if (-not (Test-LoopbackPortAvailable $mcpPort))`)
	prepare := strings.Index(body, `Step "Preparing Loki $releaseTag WSL appliance..."`)
	install := strings.Index(body, `$installArgs = @("--install", "--from-file"`)
	if distroConflict < 0 || taskConflict < 0 || stateConflict < 0 || portConflict < 0 || prepare < 0 || install < 0 {
		t.Fatal("Windows installer conflict/install markers are missing")
	}
	if distroConflict > prepare || taskConflict > prepare || stateConflict > prepare || portConflict > prepare || prepare > install {
		t.Fatal("Windows installer downloads or mutates WSL before conflict preflight")
	}
}

func TestWindowsInstallerRecoveryRequiresOwnershipAndExplicitDestructiveApproval(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "tools", "release", "install.ps1.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, required := range []string{
		`$env:LOKI_WSL_REINSTALL`,
		`function Test-InteractiveConsole`,
		`Read-Host "Reinstall Loki? [y/N]"`,
		`Non-interactive recovery will not unregister a WSL distribution without explicit approval.`,
		`function Apply-LokiPreservedSettings`,
		`mcp_port = [int]$mcpPort`,
		`function Remove-LokiOwnedStartupTask`,
		`function Remove-LokiOwnedWindowsState`,
		`Refusing to remove unverified Scheduled Task`,
		`Refusing to remove unverified Windows state`,
		`WSL still reports '$distributionName' after unregister; owned Windows state was not removed.`,
		`Removing verified orphaned Loki Windows integration...`,
		`$localOriginProperty = $connection.PSObject.Properties["local_origin"]`,
		`$flatAllowed = @("endpoint", "transport", "authentication", "token_file", "distribution")`,
		`legacy flat connection state does not prove Loki ownership`,
		`legacy flat connection state is not the published Loki endpoint`,
		`$connection.PSObject.Properties["authentication"]`,
		`$connection.PSObject.Properties["token_file"]`,
		`$connection.PSObject.Properties["endpoint"]`,
		`$connection.PSObject.Properties["transport"]`,
		`Assert-LokiFreshPreflight $preflight`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Windows installer recovery lacks %q", required)
		}
	}

	interactive := strings.Index(body, `if (Test-InteractiveConsole)`)
	nonInteractiveApproval := strings.Index(body, `if ($reinstallRequested)`)
	confirm := strings.Index(body, `Confirm-LokiStaleReinstall $distroState $windowsState $startupTask`)
	removeTask := strings.Index(body, `Remove-LokiOwnedStartupTask $startupTask`)
	terminate := strings.Index(body, `$terminateResult = Invoke-NativeResult "wsl.exe" @("--terminate", $distributionName)`)
	unregister := strings.Index(body, `$unregisterResult = Invoke-NativeResult "wsl.exe" @("--unregister", $distributionName)`)
	verifyAbsent := strings.Index(body, `$stillPresent = @($afterUnregister`)
	removeState := strings.Index(body, `Remove-LokiOwnedWindowsState $windowsState`)
	fresh := strings.Index(body, `Assert-LokiFreshPreflight $preflight`)
	prepare := strings.Index(body, `Step "Preparing Loki $releaseTag WSL appliance..."`)
	if interactive < 0 || nonInteractiveApproval < 0 || confirm < 0 || removeTask < 0 || terminate < 0 ||
		unregister < 0 || verifyAbsent < 0 || removeState < 0 || fresh < 0 || prepare < 0 {
		t.Fatal("Windows installer recovery ordering markers are missing")
	}
	if interactive > nonInteractiveApproval {
		t.Fatal("interactive recovery must prompt before considering non-interactive approval")
	}
	if !(confirm < removeTask && removeTask < terminate && terminate < unregister && unregister < verifyAbsent &&
		verifyAbsent < removeState && removeState < fresh && fresh < prepare) {
		t.Fatal("Windows installer stale recovery ordering is unsafe")
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
		"Windows ownership manifest is missing",
		"healthy non-interactive installer rerun failed with code",
		"non-interactive stale reinstall succeeded without explicit approval",
		"denied stale reinstall removed the Loki distribution",
		"explicit non-interactive stale reinstall failed with code",
		"legacy orphan fixture still has a registered WSL distribution",
		"$flatLegacyConnection = [ordered]@{",
		"endpoint = \"http://127.0.0.1:18765/mcp\"",
		"legacy orphan recovery failed with code",
		"legacy orphan recovery did not publish a new ownership manifest",
		`LOKI_WSL_REINSTALL`,
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
