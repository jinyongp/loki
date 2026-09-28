package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func windowsInstallerTemplate(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "tools", "release", "install.ps1.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWindowsInstallerTemplateIsThinVerifiedFrontendBootstrap(t *testing.T) {
	body := windowsInstallerTemplate(t)
	for _, placeholder := range []string{
		"@@LOKI_RELEASE_TAG@@",
		"@@LOKI_WINDOWS_FRONTEND_SHA256@@",
		"@@LOKI_WINDOWS_FRONTEND_LENGTH@@",
	} {
		if strings.Count(body, placeholder) != 1 {
			t.Fatalf("Windows installer placeholder %q count = %d, want 1", placeholder, strings.Count(body, placeholder))
		}
	}

	for _, required := range []string{
		`$frontendAsset = "loki-windows-amd64.exe"`,
		`https://github.com/jinyongp/loki/releases/download/$releaseTag/$frontendAsset`,
		`$env:LOKI_WINDOWS_FRONTEND_FILE`,
		`Invoke-WebRequest -UseBasicParsing -Uri $releaseUrl -OutFile $frontend`,
		`Copy-Item -LiteralPath $localFrontend -Destination $frontend`,
		`$item.Length -ne $frontendLength`,
		`Get-FileHash -LiteralPath $frontend -Algorithm SHA256`,
		`& $frontend "bootstrap" "install"`,
		`$bootstrapExitCode = $LASTEXITCODE`,
		`Add-CurrentProcessPath $canonicalBin`,
		`Programs\Loki\bin`,
		`$global:LASTEXITCODE = $bootstrapExitCode`,
		`Remove-Item -LiteralPath $tempRoot -Recurse -Force`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("thin Windows bootstrap lacks %q", required)
		}
	}

	for _, forbidden := range []string{
		"@@LOKI_WSL_SHA256@@",
		"@@LOKI_WSL_LENGTH@@",
		"wsl.exe",
		"Get-ScheduledTask",
		"Register-ScheduledTask",
		"Unregister-ScheduledTask",
		"ownership.json",
		"release-manifest.json",
		"configure-install",
		"Get-NetTCPConnection",
		"icacls.exe",
		"SetEnvironmentVariable",
		"setx",
		`host", "doctor`,
		`host", "connection`,
		`--unregister`,
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("thin Windows bootstrap contains retired lifecycle implementation %q", forbidden)
		}
	}
}

func TestWindowsInstallerVerifiesBeforeHandoffAndReflectsPathOnlyAfterSuccess(t *testing.T) {
	body := windowsInstallerTemplate(t)
	lengthCheck := strings.Index(body, `$item.Length -ne $frontendLength`)
	hashCheck := strings.Index(body, `Get-FileHash -LiteralPath $frontend -Algorithm SHA256`)
	handoff := strings.Index(body, `& $frontend "bootstrap" "install"`)
	exitCapture := strings.Index(body, `$bootstrapExitCode = $LASTEXITCODE`)
	successGuard := strings.Index(body, `if ($bootstrapExitCode -eq 0)`)
	pathReflect := strings.Index(body, `Add-CurrentProcessPath $canonicalBin`)
	cleanup := strings.Index(body, `Remove-Item -LiteralPath $tempRoot -Recurse -Force`)
	propagate := strings.Index(body, `$global:LASTEXITCODE = $bootstrapExitCode`)
	for name, position := range map[string]int{
		"length check":     lengthCheck,
		"hash check":       hashCheck,
		"handoff":          handoff,
		"exit capture":     exitCapture,
		"success guard":    successGuard,
		"PATH reflection":  pathReflect,
		"cleanup":          cleanup,
		"exit propagation": propagate,
	} {
		if position < 0 {
			t.Fatalf("Windows bootstrap missing %s", name)
		}
	}
	if !(lengthCheck < hashCheck && hashCheck < handoff && handoff < exitCapture &&
		exitCapture < successGuard && successGuard < pathReflect && pathReflect < cleanup && cleanup < propagate) {
		t.Fatal("Windows bootstrap verification/handoff/PATH/cleanup ordering is unsafe")
	}
	if strings.Count(body, `Add-CurrentProcessPath $canonicalBin`) != 1 {
		t.Fatal("current-process PATH must be reflected exactly once")
	}
}

func TestWindowsWSLAcceptanceUsesVerifiedCandidateFrontend(t *testing.T) {
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
		"evidence.windows_frontend.path",
		"evidence.windows_frontend.length",
		"evidence.windows_frontend.sha256",
		"LOKI_WINDOWS_FRONTEND_FILE",
		"LOKI_WSL_APPLIANCE_FILE",
		"loki-accept-$suffix",
		"healthy non-interactive installer rerun failed with code",
		"non-interactive stale reinstall succeeded without explicit approval",
		"explicit non-interactive stale reinstall failed with code",
		"legacy orphan recovery failed with code",
		"legacy orphan recovery did not publish a new ownership manifest",
		"Windows MCP token copy does not match",
		"Windows ownership manifest is missing",
		"Windows cannot reach the WSL MCP endpoint through localhost forwarding",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Windows WSL acceptance lacks %q", required)
		}
	}
	for _, retired := range []string{
		"rollback injection marker",
		"synthetic acceptance failure after WSL registration",
	} {
		if strings.Contains(body, retired) {
			t.Errorf("Windows WSL acceptance still depends on retired PowerShell lifecycle marker %q", retired)
		}
	}
}
