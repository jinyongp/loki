package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/execution"
)

func shippedInputs(t *testing.T) (config.Config, execution.Contract) {
	t.Helper()
	c, err := config.Load(filepath.Join("..", "..", "..", "config", "loki-go.toml"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c, contract
}

func TestCompileShippedPolicyAndDiff(t *testing.T) {
	c, contract := shippedInputs(t)
	generation, err := Compile(c, contract)
	if err != nil {
		t.Fatal(err)
	}
	if !generation.Valid() || generation.Metadata().Schema != controlpolicy.GenerationSchema {
		t.Fatalf("generation = %#v", generation.Metadata())
	}

	changed := c
	changed.MaxFileBytes++
	next, err := Compile(changed, contract)
	if err != nil {
		t.Fatal(err)
	}
	if generation.Digest() == next.Digest() {
		t.Fatal("changed limit retained policy digest")
	}
	diff, err := controlpolicy.Diff(generation, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || diff[0].Path != "/policy/limits/max_file_bytes" {
		t.Fatalf("limit diff = %#v", diff)
	}
}

func TestCompileCanonicalizesSetsAndMaps(t *testing.T) {
	c, contract := shippedInputs(t)
	c.PublicHosts = []string{"z.example.test", "a.example.test"}
	c.GitHubAppID = 123
	c.GitHubInstallations = []config.GitHubInstallation{
		{Account: "zeta", AccountType: "organization", InstallationID: 22, Repositories: []string{"two", "one"}},
		{Account: "alpha", AccountType: "user", InstallationID: 11, Repositories: []string{"repo"}},
	}
	c.GitHubTargets = []string{"zeta/two", "alpha/repo", "zeta/one"}
	first, err := Compile(c, contract)
	if err != nil {
		t.Fatal(err)
	}

	reordered := c
	reordered.PublicHosts = slices.Clone(c.PublicHosts)
	slices.Reverse(reordered.PublicHosts)
	reordered.GitHubInstallations = []config.GitHubInstallation{
		{Account: "alpha", AccountType: "user", InstallationID: 11, Repositories: []string{"repo"}},
		{Account: "zeta", AccountType: "organization", InstallationID: 22, Repositories: []string{"one", "two"}},
	}
	reordered.GitHubTargets = slices.Clone(c.GitHubTargets)
	slices.Reverse(reordered.GitHubTargets)

	reorderedContract := contract
	reorderedContract.Directories = reverseMap(contract.Directories)
	reorderedContract.Environment = reverseMap(contract.Environment)
	reorderedContract.NetworkProfiles = reverseMap(contract.NetworkProfiles)

	second, err := Compile(reordered, reorderedContract)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() {
		t.Fatalf("equivalent effective policies differ: %s != %s", first.Digest(), second.Digest())
	}
	var document struct {
		Policy Document `json:"policy"`
	}
	if err = json.Unmarshal(first.CanonicalJSON(), &document); err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(document.Policy.Listener.PublicHosts) {
		t.Fatalf("public hosts are not canonical: %#v", document.Policy.Listener.PublicHosts)
	}
	for i := 1; i < len(document.Policy.GitHub.Targets); i++ {
		if document.Policy.GitHub.Targets[i-1].Target > document.Policy.GitHub.Targets[i].Target {
			t.Fatalf("GitHub targets are not canonical: %#v", document.Policy.GitHub.Targets)
		}
	}
}

func TestCompileRejectsCrossInputDrift(t *testing.T) {
	c, contract := shippedInputs(t)
	c.Root = "/different-workspace"
	if _, err := Compile(c, contract); err == nil {
		t.Fatal("workspace mismatch accepted")
	}

	c, contract = shippedInputs(t)
	c.GitHubAppID = 123
	c.GitHubInstallations = []config.GitHubInstallation{
		{Account: "owner", AccountType: "organization", InstallationID: 1, Repositories: []string{"repo"}},
	}
	c.GitHubTargets = []string{"owner/other"}
	if _, err := Compile(c, contract); err == nil {
		t.Fatal("GitHub target mismatch accepted")
	}
}

func reverseMap[K comparable, V any](input map[K]V) map[K]V {
	keys := make([]K, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	output := make(map[K]V, len(input))
	for index := len(keys) - 1; index >= 0; index-- {
		output[keys[index]] = input[keys[index]]
	}
	return output
}
