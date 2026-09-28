package main

import (
	"errors"
	"fmt"

	windowshost "loki/internal/host/windows"
)

const publicWindowsInstallerCommand = "irm https://jinyongp.dev/loki/install.ps1 | iex"

func formatWindowsInstallError(err error, distribution string, mcpPort int) string {
	var provisioning *windowshost.ProvisioningFailureError
	if errors.As(err, &provisioning) && err == provisioning && provisioning.Kind == windowshost.ProvisioningFailurePortInUse {
		port := provisioning.Port
		if port == 0 {
			port = mcpPort
		}
		return formatWindowsPortConflict(port, false)
	}

	var blocked windowshost.InstallBlockedError
	if errors.As(err, &blocked) {
		switch blocked.Reason {
		case "mcp-port-in-use", "requested-port-in-use":
			return formatWindowsPortConflict(mcpPort, true)
		case "foreign-distribution":
			return formatForeignDistribution(distribution)
		case "distribution-provisioning":
			return formatExistingProvisioning(distribution, false)
		case "indeterminate-distribution":
			return formatExistingProvisioning(distribution, true)
		}
	}
	return err.Error()
}

func formatWindowsPortConflict(port int, preflight bool) string {
	nextPort := 19000
	if port == nextPort {
		nextPort++
	}
	state := "The installation did not complete."
	if preflight {
		state = "No installation resources were changed."
	}
	return fmt.Sprintf(
		"MCP local-origin port %d is already in use.\n%s\n\n"+
			"Inspect the current listener:\n"+
			"  Get-NetTCPConnection -LocalPort %d -State Listen\n\n"+
			"Choose another unused local port and rerun:\n"+
			"  $env:LOKI_MCP_PORT = \"%d\"\n"+
			"  %s\n\n"+
			"Loki will not stop or reconfigure the existing listener automatically.",
		port, state, port, nextPort, publicWindowsInstallerCommand,
	)
}

func formatForeignDistribution(distribution string) string {
	nextName := "loki-mcp-2"
	if distribution == nextName {
		nextName = "loki-mcp-3"
	}
	return fmt.Sprintf(
		"A WSL distribution named %q already exists, but Loki cannot prove that it owns that distribution.\n"+
			"No changes were made.\n\n"+
			"Inspect the registered distributions without starting them:\n"+
			"  wsl --list --verbose\n\n"+
			"Or choose another distribution name and rerun:\n"+
			"  $env:LOKI_WSL_NAME = \"%s\"\n"+
			"  %s\n\n"+
			"Loki will not unregister or replace an unverified distribution automatically.",
		distribution, nextName, publicWindowsInstallerCommand,
	)
}

func formatExistingProvisioning(distribution string, indeterminate bool) string {
	headline := fmt.Sprintf("Loki appliance %q is still provisioning.", distribution)
	action := "Wait for provisioning to finish, then rerun the installer."
	if indeterminate {
		headline = fmt.Sprintf("Loki appliance %q exists, but its provisioning state could not be verified safely.", distribution)
		action = "Inspect the appliance before retrying the installer."
	}
	return fmt.Sprintf(
		"%s\nNo changes were made.\n\n%s\n"+
			"  wsl -d %s --user root -- /usr/bin/systemctl status loki-appliance-provision.service --no-pager\n"+
			"  wsl -d %s --user root -- /usr/bin/journalctl -u loki-appliance-provision.service --no-pager -n 24\n\n"+
			"Rerun when the appliance state is understood:\n"+
			"  %s",
		headline, action, distribution, distribution, publicWindowsInstallerCommand,
	)
}
