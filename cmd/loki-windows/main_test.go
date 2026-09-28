package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"loki/internal/buildinfo"
	windowshost "loki/internal/host/windows"
)

func TestWindowsConnectionListUsesPassiveProviderDiscovery(t *testing.T) {
	raw, err := os.ReadFile("connection_command_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "func runConnectionList(")
	end := strings.Index(text, "func runConnectionShow(")
	if start < 0 || end <= start {
		t.Fatal("Windows connection list function not found")
	}
	body := text[start:end]
	for _, required := range []string{
		"passiveLocalConnectionPresent(",
		"ProviderDescriptors()",
		"ConfiguredStates(",
		"buildConnectionList(",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("connection list lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		".Status(",
		".Setup(",
		".Start(",
		".Ensure(",
		"NewWindowsHelperManager(",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("connection list unexpectedly performs active provider operation %q", forbidden)
		}
	}
}

func TestWindowsOpenAISetupUsesReferenceOnlySecretInputs(t *testing.T) {
	raw, err := os.ReadFile("runtime_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "func runConnectionSetup(")
	end := strings.Index(text, "func validEnvironmentVariableName(")
	if start < 0 || end <= start {
		t.Fatal("Windows OpenAI setup function not found")
	}
	body := text[start:end]
	for _, required := range []string{
		`flags.String("runtime-key-env"`,
		`flags.String("runtime-key-credential"`,
		"term.ReadPassword",
		"WindowsConnectionAdaptersWithOpenAISetup",
		"OpenAITunnelsURL",
		"OpenAIRuntimeKeysURL",
		"OpenAIConnectorsURL",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("OpenAI setup lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		`flags.String("runtime-key"`,
		"OPENAI_ADMIN_KEY",
		"--admin-key",
		"tunnels create",
		"tunnels delete",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("OpenAI setup contains forbidden literal/admin surface %q", forbidden)
		}
	}

	platformRaw, err := os.ReadFile("../../internal/host/windows/openai_platform_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	platform := string(platformRaw)
	if !strings.Contains(platform, "withoutEnvironment(os.Environ(), names...)") {
		t.Fatal("OpenAI helper process no longer inherits the ambient provider environment")
	}
	if !strings.Contains(platform, "CredWriteW") ||
		!strings.Contains(platform, "CredReadW") ||
		!strings.Contains(platform, "CredDeleteW") {
		t.Fatal("OpenAI runtime credential is no longer behind Windows Credential Manager")
	}
}

func TestTopLevelUsageExposesOnlyCanonicalConnectionSurface(t *testing.T) {
	var stderr bytes.Buffer
	printUsage(&stderr)
	usage := stderr.String()
	if !strings.Contains(usage, "connection [COMMAND]") {
		t.Fatalf("usage lacks canonical connection surface: %s", usage)
	}
	if strings.Contains(usage, "| connect ") {
		t.Fatalf("usage exposes legacy connect surface: %s", usage)
	}
}

func TestCanonicalConnectionSurfaceExcludesInternalStartup(t *testing.T) {
	raw, err := os.ReadFile("connection_command_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `case "startup":`) {
		t.Fatal("canonical connection command exposes internal startup entrypoint")
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
