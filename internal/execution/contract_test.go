package execution

import (
	"os"
	"path/filepath"
	"slices"
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
	for name, want := range map[string]string{
		"runtime-state": "/var/lib/loki-go/runtime",
		"signing-state": "/var/lib/loki-go/signing",
		"runner-state":  "/var/lib/loki-go/runner",
		"snapshots":     "/var/lib/loki-go/snapshots",
		"runner-cache":  "/var/cache/loki-go/runner",
		"runner-temp":   "/var/tmp/loki-go/runner",
		"workspace":     "/workspace",
	} {
		if got := contract.Directories[name].Path; got != want {
			t.Fatalf("%s path = %q, want %q", name, got, want)
		}
	}
	if got := contract.Environment["PLAYWRIGHT_BROWSERS_PATH"]; got != "/var/cache/loki-go/runner-playwright" {
		t.Fatalf("Playwright cache = %q", got)
	}
	if contract.NetworkProfiles["dependency-install"].AllowSecrets {
		t.Fatal("dependency installation accepts secrets")
	}
	if got := contract.Directories["runner-state"].Owner; got != "runner" {
		t.Fatalf("runner state owner = %q", got)
	}
}

func TestEnvironmentListIsClosedAndStable(t *testing.T) {
	t.Setenv("PRIVATE_PARENT_VALUE", "must-not-pass")
	environment, err := repositoryContract(t).EnvironmentList()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(environment) {
		t.Fatalf("environment is not sorted: %#v", environment)
	}
	for _, want := range []string{
		"HOME=/home/runner",
		"GH_CONFIG_DIR=/var/lib/loki-go/runner-gh-config",
		"XDG_CACHE_HOME=/var/cache/loki-go/runner",
		"NPM_CONFIG_CACHE=/var/cache/loki-go/runner-npm",
		"npm_config_store_dir=/var/cache/loki-go/runner-pnpm",
		"PLAYWRIGHT_BROWSERS_PATH=/var/cache/loki-go/runner-playwright",
		"GOCACHE=/var/cache/loki-go/runner-go-build",
		"GOMODCACHE=/var/cache/loki-go/runner-go-mod",
		"PIP_CACHE_DIR=/var/cache/loki-go/runner-pip",
		"TMPDIR=/var/tmp/loki-go/runner",
		"GIT_CONFIG_GLOBAL=/etc/loki-go/gitconfig",
	} {
		if !slices.Contains(environment, want) {
			t.Fatalf("environment does not contain %q: %#v", want, environment)
		}
	}
	if slices.Contains(environment, "PRIVATE_PARENT_VALUE=must-not-pass") {
		t.Fatal("runner environment inherited a parent value")
	}
}

func TestDependencyEnvironmentUsesOnlyLoopbackProxy(t *testing.T) {
	environment, err := repositoryContract(t).EnvironmentForNetwork("dependency-install")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"HTTP_PROXY=http://127.0.0.1:18766",
		"HTTPS_PROXY=http://127.0.0.1:18766",
		"NO_PROXY=127.0.0.1,localhost",
	} {
		if !slices.Contains(environment, want) {
			t.Fatalf("dependency environment does not contain %q", want)
		}
	}
	if _, err = repositoryContract(t).EnvironmentForNetwork("unknown"); err == nil {
		t.Fatal("unknown network profile accepted")
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
