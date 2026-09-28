package windows

import (
	"strings"
	"testing"
)

func TestConnectionStateStrictRoundTrip(t *testing.T) {
	state := ConnectionState{
		SchemaVersion:  ConnectionStateSchemaVersion,
		Distribution:   "loki-mcp",
		Provider:       "provider-one",
		Enabled:        true,
		HelperID:       "helper-one",
		HelperVersion:  "1.2.3",
		HelperPlatform: "windows-amd64",
	}
	raw, err := encodeConnectionState(state)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := parseConnectionState(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != state {
		t.Fatalf("decoded=%+v want=%+v", decoded, state)
	}
}

func TestConnectionStateRejectsUnknownOrMismatchedShape(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown-field": `{"schema_version":1,"distribution":"loki-mcp","provider":"provider-one","enabled":true,"helper_id":"helper-one","helper_version":"1.2.3","helper_platform":"windows-amd64","extra":true}`,
		"bad-provider":  `{"schema_version":1,"distribution":"loki-mcp","provider":"../provider","enabled":true,"helper_id":"helper-one","helper_version":"1.2.3","helper_platform":"windows-amd64"}`,
		"bad-schema":    `{"schema_version":2,"distribution":"loki-mcp","provider":"provider-one","enabled":true,"helper_id":"helper-one","helper_version":"1.2.3","helper_platform":"windows-amd64"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConnectionState([]byte(raw)); err == nil {
				t.Fatal("invalid managed connection state accepted")
			}
		})
	}
}

func TestConnectionProviderRootIsolatedFromLegacyApplianceState(t *testing.T) {
	paths, err := ResolveFrontendPaths("C:\\Users\\alice\\AppData\\Local")
	if err != nil {
		t.Fatal(err)
	}
	root, err := ConnectionProviderRoot(paths, "loki-mcp", "openai")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := ExpectedFromOptions(
		InstallOptions{Distribution: "loki-mcp"},
		"C:\\Users\\alice\\AppData\\Local",
		"C:\\Windows",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "C:\\Users\\alice\\AppData\\Local\\Programs\\Loki\\connections\\loki-mcp\\openai"
	if !WindowsPathEqual(root, want) {
		t.Fatalf("provider root=%q want=%q", root, want)
	}
	if WindowsPathEqual(root, expected.StateDir) ||
		strings.HasPrefix(strings.ToLower(root), strings.ToLower(expected.StateDir)+"\\") {
		t.Fatalf("provider root %q overlaps legacy appliance state %q", root, expected.StateDir)
	}
}

func TestValidateConnectionStateRejectsMissingHelperIdentity(t *testing.T) {
	state := ConnectionState{
		SchemaVersion:  1,
		Distribution:   "loki-mcp",
		Provider:       "provider-one",
		Enabled:        true,
		HelperVersion:  "1.2.3",
		HelperPlatform: "windows-amd64",
	}
	if err := validateConnectionState(state); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("err=%v", err)
	}
}
