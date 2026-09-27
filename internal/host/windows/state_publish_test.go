package windows

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPublicConnectionPreservesCompatibilityAliases(t *testing.T) {
	expected := fixtureExpected()
	raw, err := BuildPublicConnection(expected, ConnectionMaterial{
		LocalOrigin:        "http://127.0.0.1:19000/mcp",
		Transport:          "streamable-http",
		Reachability:       "loopback",
		AuthenticationType: "bearer-token-file",
		Token:              "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["endpoint"] != "http://127.0.0.1:19000/mcp" ||
		payload["transport"] != "streamable-http" ||
		payload["authentication"] != "bearer-token-file" ||
		payload["distribution"] != expected.Distribution {
		t.Fatalf("unexpected public connection %#v", payload)
	}
	if !strings.Contains(string(raw), "C:\\\\Users\\\\dev\\\\AppData\\\\Local\\\\Loki\\\\loki-mcp\\\\mcp-token") {
		t.Fatalf("token file alias missing: %s", raw)
	}
}

func TestBuildPublicConnectionRejectsNonLokiMaterial(t *testing.T) {
	expected := fixtureExpected()
	tests := []ConnectionMaterial{
		{LocalOrigin: "http://0.0.0.0:19000/mcp", Transport: "streamable-http", Reachability: "loopback", AuthenticationType: "bearer-token-file", Token: "secret"},
		{LocalOrigin: "http://127.0.0.1:19000/mcp", Transport: "stdio", Reachability: "loopback", AuthenticationType: "bearer-token-file", Token: "secret"},
		{LocalOrigin: "http://127.0.0.1:19000/mcp", Transport: "streamable-http", Reachability: "public", AuthenticationType: "bearer-token-file", Token: "secret"},
		{LocalOrigin: "http://127.0.0.1:19000/mcp", Transport: "streamable-http", Reachability: "loopback", AuthenticationType: "none", Token: "secret"},
	}
	for index, material := range tests {
		if _, err := BuildPublicConnection(expected, material); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func TestBuildPublicConnectionRejectsUnsafeTokenMaterial(t *testing.T) {
	expected := fixtureExpected()
	for _, token := range []string{"", "secret\nvalue", "secret\x00value"} {
		if _, err := BuildPublicConnection(expected, ConnectionMaterial{
			LocalOrigin: "http://127.0.0.1:19000/mcp", Transport: "streamable-http",
			Reachability: "loopback", AuthenticationType: "bearer-token-file", Token: token,
		}); err == nil {
			t.Fatalf("unsafe token %q accepted", token)
		}
	}
}

func TestBuildOwnershipManifestMatchesV019Shape(t *testing.T) {
	expected := fixtureExpected()
	raw, err := BuildOwnershipManifest(ReleaseBinding{ReleaseTag: "v0.1.20"}, expected, InstallOptions{
		Distribution:    expected.Distribution,
		InstallLocation: "D:\\\\Loki\\\\loki-mcp",
		AutoStart:       true,
		MCPPort:         19000,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := ParseOwnershipManifest(raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	if state.Kind != WindowsStateManifest || state.MCPPort != 19000 || !state.AutoStart ||
		!WindowsPathEqual(state.InstallLocation, "D:\\\\Loki\\\\loki-mcp") {
		t.Fatalf("unexpected ownership state %#v", state)
	}
}

func TestBuildOwnershipManifestRejectsInvalidInputs(t *testing.T) {
	expected := fixtureExpected()
	tests := []struct {
		binding ReleaseBinding
		options InstallOptions
	}{
		{ReleaseBinding{ReleaseTag: "latest"}, InstallOptions{MCPPort: 18765}},
		{ReleaseBinding{ReleaseTag: "v0.1.20"}, InstallOptions{MCPPort: 80}},
		{ReleaseBinding{ReleaseTag: "v0.1.20"}, InstallOptions{MCPPort: 18765, InstallLocation: expected.StateDir + `\nested`}},
	}
	for index, test := range tests {
		if _, err := BuildOwnershipManifest(test.binding, expected, test.options); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}
