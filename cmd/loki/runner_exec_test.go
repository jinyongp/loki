package main

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestRunnerExecUsesOnlyContractEnvironment(t *testing.T) {
	t.Setenv("PRIVATE_PARENT_VALUE", "must-not-pass")
	contract, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildRunnerExecPlan([]string{"--contract", contract, "--", "/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.executable != "/usr/bin/env" || !slices.Equal(plan.arguments, []string{"/usr/bin/env"}) {
		t.Fatalf("runner command = %#v", plan)
	}
	if slices.Contains(plan.environment, "PRIVATE_PARENT_VALUE=must-not-pass") {
		t.Fatal("runner exec inherited a parent value")
	}
	for _, want := range []string{
		"HOME=/home/runner",
		"GH_CONFIG_DIR=/var/lib/loki-go/runner/config/gh",
		"XDG_CACHE_HOME=/var/cache/loki-go/runner",
		"npm_config_store_dir=/var/cache/loki-go/runner/pnpm",
		"GOMODCACHE=/var/cache/loki-go/runner/go-mod",
		"TMPDIR=/var/tmp/loki-go/runner",
		"GIT_CONFIG_GLOBAL=/home/runner/.gitconfig",
	} {
		if !slices.Contains(plan.environment, want) {
			t.Fatalf("runner environment does not contain %q: %#v", want, plan.environment)
		}
	}
}

func TestRunnerExecRejectsRelativeCommands(t *testing.T) {
	contract, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = buildRunnerExecPlan([]string{"--contract", contract, "--", "devtools"}); err == nil {
		t.Fatal("relative runner command accepted")
	}
}

func TestRunnerExecSelectsDependencyNetwork(t *testing.T) {
	contract, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildRunnerExecPlan([]string{"--contract", contract, "--network-profile", "dependency-install", "--", "/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.environment, "HTTPS_PROXY=http://127.0.0.1:18766") {
		t.Fatalf("dependency proxy missing: %#v", plan.environment)
	}
}
