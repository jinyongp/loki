package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWSLGatesManagedIntegrationLifecycle(t *testing.T) {
	root := filepath.Join("..", "..", "scripts", "verify")
	entry, err := os.ReadFile(filepath.Join(root, "accept-wsl.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), `"accept-managed-integrations.ps1"`) {
		t.Fatal("exact-candidate WSL acceptance must execute managed integration acceptance")
	}
	raw, err := os.ReadFile(filepath.Join(root, "accept-managed-integrations.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`function Invoke-AcceptanceSection`,
		`Invoke-AcceptanceSection "managed browser enable, MCP use, and disable"`,
		`Invoke-AcceptanceSection "managed signing through isolated MCP Jobs"`,
		`Invoke-AcceptanceSection "final optional authority and catalog invariants"`,
		`$failures.Count -gt 0`,
		`"integration", "enable", "--distribution", $Distribution, "browser"`,
		`"browser_session" @{ action = "start" }`,
		`"integration", "setup", "signing"`,
		`"integration", "rotate", "signing"`,
		`"integration", "remove", "--distribution", $Distribution, "signing"`,
		`"/usr/bin/git", "verify-commit", "HEAD"`,
		`"/usr/bin/ssh-add", "-l") -ExpectFailure`,
		`"github_read" @{ action = "repository"; target = "example-org/integration-fixture" } -ExpectError`,
		`$catalog -cne $finalCatalog`,
		`$status.cleanup -in @("complete", "not_required")`,
	} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("integration acceptance lost %q", required)
		}
	}
}
