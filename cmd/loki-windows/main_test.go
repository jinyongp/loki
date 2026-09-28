package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"loki/internal/buildinfo"
	windowshost "loki/internal/host/windows"
)

func TestWindowsConnectOverviewDoesNotConstructManagedHelperState(t *testing.T) {
	raw, err := os.ReadFile("runtime_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "func runConnectOverview(")
	end := strings.Index(text, "func runConnectStatus(")
	if start < 0 || end <= start {
		t.Fatal("Windows connect overview function not found")
	}
	body := text[start:end]
	if !strings.Contains(body, "runConnection(") {
		t.Fatal("direct/local connect overview no longer reports the local connection")
	}
	for _, forbidden := range []string{
		"newWindowsConnectionManager(",
		"NewWindowsHelperManager(",
		".Ensure(",
		"connect setup",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("direct/local connect overview unexpectedly contains managed-helper path %q", forbidden)
		}
	}
}

func TestVersionJSONIncludesReleaseBinding(t *testing.T) {
	oldVersion, oldCommit, oldDate := buildinfo.Version, buildinfo.Commit, buildinfo.Date
	oldWSLSHA, oldWSLLen := windowshost.WSLApplianceSHA256, windowshost.WSLApplianceLength
	oldCatalogSHA, oldCatalogLen := windowshost.HelperCatalogSHA256, windowshost.HelperCatalogLength
	t.Cleanup(func() {
		buildinfo.Version, buildinfo.Commit, buildinfo.Date = oldVersion, oldCommit, oldDate
		windowshost.WSLApplianceSHA256, windowshost.WSLApplianceLength = oldWSLSHA, oldWSLLen
		windowshost.HelperCatalogSHA256, windowshost.HelperCatalogLength = oldCatalogSHA, oldCatalogLen
	})
	buildinfo.Version = "1.2.3"
	buildinfo.Commit = strings.Repeat("a", 40)
	buildinfo.Date = "2026-09-27T00:00:00Z"
	windowshost.WSLApplianceSHA256 = strings.Repeat("b", 64)
	windowshost.WSLApplianceLength = "10"
	windowshost.HelperCatalogSHA256 = strings.Repeat("c", 64)
	windowshost.HelperCatalogLength = "20"

	var stdout, stderr bytes.Buffer
	if code := run([]string{"version", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"release_tag":"v1.2.3"`) ||
		!strings.Contains(stdout.String(), strings.Repeat("b", 64)) {
		t.Fatalf("unexpected version output %s", stdout.String())
	}
}
