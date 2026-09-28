package main

import (
	"fmt"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
)

func TestFormatWindowsInstallErrorPortConflictHasCopyableNextSteps(t *testing.T) {
	message := formatWindowsInstallError(
		windowshost.InstallBlockedError{Reason: "mcp-port-in-use"},
		"loki-mcp",
		18765,
	)
	for _, required := range []string{
		"MCP local-origin port 18765 is already in use.",
		"No installation resources were changed.",
		"Get-NetTCPConnection -LocalPort 18765 -State Listen",
		`$env:LOKI_MCP_PORT = "19000"`,
		publicWindowsInstallerCommand,
		"will not stop or reconfigure",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("port conflict message lacks %q: %s", required, message)
		}
	}
	if strings.Contains(message, "Windows Loki install blocked") {
		t.Fatalf("port conflict leaked internal reason code: %s", message)
	}
}

func TestFormatWindowsInstallErrorForeignDistributionHasNonDestructiveNextSteps(t *testing.T) {
	message := formatWindowsInstallError(
		windowshost.InstallBlockedError{Reason: "foreign-distribution"},
		"loki-mcp",
		18765,
	)
	for _, required := range []string{
		`A WSL distribution named "loki-mcp" already exists`,
		"Inspect the registered distributions without starting them:",
		"wsl --list --verbose",
		`$env:LOKI_WSL_NAME = "loki-mcp-2"`,
		publicWindowsInstallerCommand,
		"will not unregister or replace",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("foreign distribution message lacks %q: %s", required, message)
		}
	}
	if strings.Contains(message, "--unregister") || strings.Contains(message, "wsl -d ") {
		t.Fatalf("foreign distribution guidance attempted to start or unregister the unverified distribution: %s", message)
	}
}

func TestFormatWindowsInstallErrorProvisioningPortConflictSuppressesJournal(t *testing.T) {
	message := formatWindowsInstallError(&windowshost.ProvisioningFailureError{
		Kind:        windowshost.ProvisioningFailurePortInUse,
		Port:        18765,
		Summary:     "provisioning failed",
		Diagnostics: "verbose journal should not be shown",
	}, "loki-mcp", 18765)
	if strings.Contains(message, "verbose journal") || strings.Contains(message, "provisioning failed") {
		t.Fatalf("recognized port conflict retained verbose provisioning failure: %s", message)
	}
	if !strings.Contains(message, "The installation did not complete.") ||
		!strings.Contains(message, `LOKI_MCP_PORT = "19000"`) {
		t.Fatalf("recognized port conflict lacks concise next step: %s", message)
	}
}

func TestFormatWindowsInstallErrorDoesNotHideRollbackFailure(t *testing.T) {
	provisioning := &windowshost.ProvisioningFailureError{
		Kind:    windowshost.ProvisioningFailurePortInUse,
		Port:    18765,
		Summary: "provisioning failed",
	}
	err := fmt.Errorf("%w; rollback incomplete installation: synthetic rollback failure", provisioning)
	message := formatWindowsInstallError(err, "loki-mcp", 18765)
	if !strings.Contains(message, "rollback incomplete installation") ||
		!strings.Contains(message, "synthetic rollback failure") {
		t.Fatalf("wrapped rollback failure was hidden: %s", message)
	}
}

func TestFormatWindowsInstallErrorUnknownProvisioningFailureRetainsDiagnostics(t *testing.T) {
	err := &windowshost.ProvisioningFailureError{
		Kind:        windowshost.ProvisioningFailureUnknown,
		Port:        18765,
		Summary:     "provisioning failed",
		Diagnostics: "bounded diagnostic line",
	}
	message := formatWindowsInstallError(err, "loki-mcp", 18765)
	if !strings.Contains(message, "provisioning failed") ||
		!strings.Contains(message, "Recent provisioning diagnostics:") ||
		!strings.Contains(message, "bounded diagnostic line") {
		t.Fatalf("unknown provisioning failure lost diagnostics: %s", message)
	}
}
