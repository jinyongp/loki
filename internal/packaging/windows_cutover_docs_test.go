package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRepositoryText(t *testing.T, relative string) string {
	t.Helper()
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWindowsCLIFirstDocumentationContract(t *testing.T) {
	tests := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: "README.md",
			required: []string{
				"release-bound Windows",
				"loki.exe",
				"loki connection",
				"loki connect setup openai",
				"Windows Credential Manager",
				"loki status",
				"loki doctor",
			},
		},
		{
			path: "docs/first-install.md",
			required: []string{
				"loki bootstrap install",
				"%LOCALAPPDATA%\\Programs\\Loki\\bin\\loki.exe",
				"The PowerShell bootstrap does not classify, register, unregister, or repair WSL",
				"loki connection --json",
				"Loki Connections (<distribution>)",
				"Credential Manager rather than JSON state",
				"loki uninstall",
				"shared Windows frontend and shared helper",
				"v0.1.19-style ownership",
			},
			forbidden: []string{
				"The installer downloads one immutable",
				"The installer registers a per-user Scheduled Task by default",
				"PowerShell native-command stack",
			},
		},
		{
			path: "docs/installation-distribution-plan.md",
			required: []string{
				"loki-windows-amd64.exe",
				"bootstrap install",
				"%LOCALAPPDATA%\\Programs\\Loki",
				"WSL ownership, recovery, Scheduled Task or persistent-PATH implementation",
			},
			forbidden: []string{
				"bound to the same exact release tag and to the accepted",
			},
		},
		{
			path: "docs/connect-mcp-client.md",
			required: []string{
				"loki connection --json",
				"loki connect setup openai",
				"--runtime-key-env",
				"same-release helper mirror",
				"Windows Credential Manager",
				"loki connect remove openai",
				"does not delete the remote OpenAI tunnel",
			},
			forbidden: []string{
				"github.com/openai/tunnel-client/releases/latest",
				"Install the current OpenAI",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			text := readRepositoryText(t, test.path)
			for _, required := range test.required {
				if !strings.Contains(text, required) {
					t.Errorf("%s lacks CLI-first contract %q", test.path, required)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s retains retired guidance %q", test.path, forbidden)
				}
			}
		})
	}
}

func TestWindowsLegacyMigrationFixturesRemainExplicit(t *testing.T) {
	text := readRepositoryText(t, "internal/host/windows/install_state_test.go")
	for _, required := range []string{
		"pre-compat-schema-v1",
		"schema-v1-with-aliases",
		"flat-original",
		"http://127.0.0.1:18765/mcp",
		"TestParseLegacyConnectionRejectsAmbiguousState",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Windows legacy migration fixture is missing %q", required)
		}
	}
}

func TestWindowsProductionPowerShellLifecycleIsRetired(t *testing.T) {
	body := readRepositoryText(t, "tools/release/install.ps1.tmpl")
	for _, forbidden := range []string{
		"wsl.exe",
		"Get-ScheduledTask",
		"Register-ScheduledTask",
		"Unregister-ScheduledTask",
		"ownership.json",
		"configure-install",
		"Get-NetTCPConnection",
		"icacls.exe",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("public PowerShell bootstrap still owns Windows lifecycle via %q", forbidden)
		}
	}
	root := filepath.Join("..", "..")
	retired := filepath.Join(root, "scripts", "verify", "accept-windows-frontend-precutover.ps1")
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("retired pre-cutover PowerShell lifecycle script remains present: %v", err)
	}
}
