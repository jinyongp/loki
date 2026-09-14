package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repositoryContract(t *testing.T) Contract {
	t.Helper()
	path := filepath.Join("..", "..", "packaging", "go", "execution-contract.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func TestRepositoryContractIsValid(t *testing.T) {
	contract := repositoryContract(t)
	if got := contract.Environment["PLAYWRIGHT_BROWSERS_PATH"]; got != "/var/cache/loki-go/runner/playwright" {
		t.Fatalf("Playwright cache = %q", got)
	}
	if contract.NetworkProfiles["dependency-install"].AllowSecrets {
		t.Fatal("dependency installation accepts secrets")
	}
	if got := contract.Directories["runner-state"].Owner; got != "runner" {
		t.Fatalf("runner state owner = %q", got)
	}
}

func TestContractRejectsRootOwnedRunnerState(t *testing.T) {
	contract := repositoryContract(t)
	directory := contract.Directories["runner-state"]
	directory.Owner = "root"
	contract.Directories["runner-state"] = directory
	if err := contract.Validate(); err == nil || !strings.Contains(err.Error(), "runner-state") {
		t.Fatalf("root-owned runner state error = %v", err)
	}
}

func TestContractRejectsSecretsDuringDependencyInstallation(t *testing.T) {
	contract := repositoryContract(t)
	profile := contract.NetworkProfiles["dependency-install"]
	profile.AllowSecrets = true
	contract.NetworkProfiles["dependency-install"] = profile
	if err := contract.Validate(); err == nil || !strings.Contains(err.Error(), "dependency-install") {
		t.Fatalf("dependency secret policy error = %v", err)
	}
}

func TestContractRejectsEnvironmentDrift(t *testing.T) {
	contract := repositoryContract(t)
	contract.Environment["XDG_STATE_HOME"] = "/home/runner/.local/state"
	if err := contract.Validate(); err == nil || !strings.Contains(err.Error(), "XDG_STATE_HOME") {
		t.Fatalf("environment drift error = %v", err)
	}
}

func TestLoadRejectsUnknownAndTrailingData(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"directories":{},"environment":{},"network_profiles":{},"unknown":true}`,
		`{"version":1,"directories":{},"environment":{},"network_profiles":{}} {}`,
	} {
		if _, err := Load([]byte(raw)); err == nil {
			t.Fatalf("invalid contract accepted: %s", raw)
		}
	}
}
