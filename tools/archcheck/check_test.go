package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestDirectDependencyPolicy(t *testing.T) {
	policy := fixturePolicy(map[string]PackageRule{
		"loki/internal/a": {Kind: "feature", Owner: "a", Target: "work/a"},
		"loki/internal/b": {Kind: "adapter", Owner: "b", Target: "platform/b"},
	})
	policy.AllowedEdges = []Edge{{From: "loki/internal/a", To: "loki/internal/b"}}
	graph := Graph{Packages: map[string]PackageInfo{
		"loki/internal/a": {ImportPath: "loki/internal/a", Imports: []string{"loki/internal/b"}},
		"loki/internal/b": {ImportPath: "loki/internal/b"},
	}}
	if violations := checkVariant(policy, graph); len(violations) != 0 {
		t.Fatalf("allowed edge rejected: %v", violations)
	}
	policy.AllowedEdges = nil
	if !hasViolation(checkVariant(policy, graph), "direct-dependency") {
		t.Fatal("new direct dependency was accepted")
	}
}

func TestNestedInternalVisibility(t *testing.T) {
	policy := fixturePolicy(map[string]PackageRule{
		"loki/internal/work/other":                 {Kind: "feature", Owner: "other", Target: "work/other"},
		"loki/internal/work/jobs/internal/journal": {Kind: "private", Owner: "jobs", Target: "work/jobs/internal/journal"},
	})
	policy.AllowedEdges = []Edge{{From: "loki/internal/work/other", To: "loki/internal/work/jobs/internal/journal"}}
	graph := Graph{Packages: map[string]PackageInfo{
		"loki/internal/work/other":                 {ImportPath: "loki/internal/work/other", Imports: []string{"loki/internal/work/jobs/internal/journal"}},
		"loki/internal/work/jobs/internal/journal": {ImportPath: "loki/internal/work/jobs/internal/journal"},
	}}
	if !hasViolation(checkVariant(policy, graph), "private-package") {
		t.Fatal("cross-feature nested internal import was accepted")
	}
	if !internalImportAllowed("loki/internal/work/jobs/local", "loki/internal/work/jobs/internal/journal") {
		t.Fatal("owning subtree should be allowed to use nested internal package")
	}
}

func TestThirdPartyOwnershipAndExportLeakage(t *testing.T) {
	policy := fixturePolicy(map[string]PackageRule{
		"loki/internal/adapter": {Kind: "adapter", Owner: "feature", Target: "adapter", Tags: []string{"mcp-adapter"}},
	})
	policy.ThirdPartyRules = []ImportRule{{ImportPrefix: "example.test/sdk", AllowedTags: []string{"mcp-adapter"}, Description: "SDK import is adapter-owned"}}
	policy.ExportRules = []ImportRule{{ImportPrefix: "example.test/sdk", AllowedTags: []string{"mcp-wire"}, Description: "SDK type is transport-owned"}}
	graph := Graph{Packages: map[string]PackageInfo{
		"loki/internal/adapter": {ImportPath: "loki/internal/adapter", Imports: []string{"example.test/sdk"}},
	}}
	violations := checkVariant(policy, graph)
	if hasViolation(violations, "third-party-owner") {
		t.Fatalf("permitted adapter import rejected: %v", violations)
	}

	source := `package adapter
import sdk "example.test/sdk"
func Expose() sdk.Value { panic("fixture") }
`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	leaks := exportedImportViolations(policy, "loki/internal/adapter", file, policy.ExportRules)
	if !hasViolation(leaks, "export-type") {
		t.Fatal("restricted SDK type leaked through exported signature")
	}
}

func TestTransitiveRoleClosure(t *testing.T) {
	policy := fixturePolicy(map[string]PackageRule{
		"loki/cmd/loki-gateway": {Kind: "entrypoint", Owner: "gateway", Target: "cmd/loki-gateway"},
		"loki/internal/adapter": {Kind: "adapter", Owner: "gateway", Target: "app/gateway"},
		"loki/internal/store":   {Kind: "store", Owner: "credentials", Target: "control/credentials", Tags: []string{"platform-credential-store"}},
	})
	policy.AllowedEdges = []Edge{
		{From: "loki/cmd/loki-gateway", To: "loki/internal/adapter"},
		{From: "loki/internal/adapter", To: "loki/internal/store"},
	}
	policy.Roles = []RoleRule{{Name: "gateway", Root: "loki/cmd/loki-gateway", ForbiddenTags: []string{"platform-credential-store"}}}
	graph := Graph{Packages: map[string]PackageInfo{
		"loki/cmd/loki-gateway": {ImportPath: "loki/cmd/loki-gateway", Imports: []string{"loki/internal/adapter"}},
		"loki/internal/adapter": {ImportPath: "loki/internal/adapter", Imports: []string{"loki/internal/store"}},
		"loki/internal/store":   {ImportPath: "loki/internal/store"},
	}}
	if !hasViolation(checkVariant(policy, graph), "role-closure") {
		t.Fatal("gateway transitively reached forbidden credential store")
	}
}

func TestUnclassifiedPackageAndExactExceptionMetadata(t *testing.T) {
	policy := fixturePolicy(map[string]PackageRule{
		"loki/internal/a": {Kind: "legacy", Owner: "a", Target: "work/a"},
		"loki/internal/b": {Kind: "legacy", Owner: "b", Target: "work/b"},
	})
	policy.EdgeExceptions = []EdgeException{{From: "loki/internal/a", To: "loki/internal/b", Reason: "legacy edge", RemoveUnit: "S02"}}
	if err := validatePolicy(policy); err != nil {
		t.Fatalf("valid exact exception rejected: %v", err)
	}
	graph := Graph{Packages: map[string]PackageInfo{
		"loki/internal/a": {ImportPath: "loki/internal/a", Imports: []string{"loki/internal/b"}},
		"loki/internal/b": {ImportPath: "loki/internal/b"},
		"loki/internal/c": {ImportPath: "loki/internal/c"},
	}}
	if !hasViolation(checkVariant(policy, graph), "package-classification") {
		t.Fatal("unclassified package was accepted")
	}
	policy.EdgeExceptions[0].RemoveUnit = ""
	if err := validatePolicy(policy); err == nil || !strings.Contains(err.Error(), "remove_unit") {
		t.Fatalf("exception without removal unit accepted: %v", err)
	}
}

func fixturePolicy(packages map[string]PackageRule) Policy {
	return Policy{
		Version: 1, Module: "loki",
		Variants: []Variant{{Name: "fixture", GOOS: "linux", GOARCH: "amd64"}},
		Packages: packages,
	}
}

func hasViolation(items []Violation, rule string) bool {
	for _, item := range items {
		if item.Rule == rule {
			return true
		}
	}
	return false
}
