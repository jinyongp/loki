package windows

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func fixtureExpected() ExpectedInstallation {
	return ExpectedInstallation{
		Distribution:   "loki-mcp",
		StateDir:       `C:\Users\dev\AppData\Local\Loki\loki-mcp`,
		TaskName:       "Loki WSL (loki-mcp)",
		TaskExecutable: `C:\Windows\System32\wsl.exe`,
		TaskArguments:  "-d loki-mcp --exec /usr/bin/sleep infinity",
	}
}

func TestParseOwnershipManifestCanonicalizesInstallLocation(t *testing.T) {
	expected := fixtureExpected()
	raw := []byte(fmt.Sprintf(`{
		"schema_version":1,
		"distribution":"loki-mcp",
		"release_tag":"v0.1.19",
		"state_dir":"C:/Users/dev/AppData/Local/Loki/loki-mcp",
		"install_location":"D:/Loki/./loki-mcp/",
		"autostart":true,
		"mcp_port":18765,
		"task_name":%q,
		"task_executable":"C:/Windows/System32/wsl.exe",
		"task_arguments":%q
	}`, expected.TaskName, expected.TaskArguments))
	state, err := ParseOwnershipManifest(raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	if state.InstallLocation != `D:\Loki\loki-mcp` {
		t.Fatalf("install location was not canonicalized: %q", state.InstallLocation)
	}
}

func TestParseOwnershipManifest(t *testing.T) {
	expected := fixtureExpected()
	raw := []byte(fmt.Sprintf(`{
		"schema_version":1,
		"distribution":"LOKI-MCP",
		"release_tag":"v0.1.19",
		"state_dir":"C:/Users/dev/AppData/Local/Loki/loki-mcp/",
		"install_location":"C:/Users/dev/AppData/Local/Programs/LokiWSL/loki-mcp",
		"autostart":true,
		"mcp_port":19000,
		"task_name":%q,
		"task_executable":"C:/Windows/System32/wsl.exe",
		"task_arguments":%q
	}`, expected.TaskName, expected.TaskArguments))
	state, err := ParseOwnershipManifest(raw, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Owned || state.Kind != WindowsStateManifest || !state.AutoStartKnown ||
		state.MCPPort != 19000 {
		t.Fatalf("unexpected state %#v", state)
	}
}

func TestParseOwnershipManifestRejectsUnsafeOrForeignState(t *testing.T) {
	expected := fixtureExpected()
	base := map[string]any{
		"schema_version": 1, "distribution": expected.Distribution, "release_tag": "v0.1.19",
		"state_dir": expected.StateDir, "install_location": `C:\LokiWSL`,
		"autostart": true, "mcp_port": 18765,
		"task_name": expected.TaskName, "task_executable": expected.TaskExecutable,
		"task_arguments": expected.TaskArguments,
	}
	tests := map[string]func(map[string]any){
		"foreign-distribution": func(m map[string]any) { m["distribution"] = "ubuntu" },
		"unsafe-state-child":   func(m map[string]any) { m["install_location"] = expected.StateDir + `\nested` },
		"unsafe-volume-root":   func(m map[string]any) { m["install_location"] = `C:\` },
		"bad-port":             func(m map[string]any) { m["mcp_port"] = 80 },
		"foreign-task":         func(m map[string]any) { m["task_arguments"] = "-d ubuntu" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := map[string]any{}
			for key, value := range base {
				fixture[key] = value
			}
			mutate(fixture)
			raw, _ := json.Marshal(fixture)
			if _, err := ParseOwnershipManifest(raw, expected); err == nil {
				t.Fatal("expected ownership rejection")
			}
		})
	}
}

func TestParseLegacyConnectionShapes(t *testing.T) {
	expected := fixtureExpected()
	token := expected.StateDir + `\mcp-token`
	localOrigin := func(withAliases bool) []byte {
		value := map[string]any{
			"schema_version": 1,
			"distribution":   expected.Distribution,
			"local_origin": map[string]any{
				"url":          "http://127.0.0.1:19001/mcp",
				"transport":    "streamable-http",
				"reachability": "loopback",
				"authentication": map[string]any{
					"type":       "bearer-token-file",
					"token_file": token,
				},
			},
		}
		if withAliases {
			value["endpoint"] = "http://127.0.0.1:19001/mcp"
			value["transport"] = "streamable-http"
			value["authentication"] = "bearer-token-file"
			value["token_file"] = token
		}
		raw, _ := json.Marshal(value)
		return raw
	}
	flat, _ := json.Marshal(map[string]any{
		"endpoint":       "http://127.0.0.1:18765/mcp",
		"transport":      "streamable-http",
		"authentication": "bearer-token-file",
		"token_file":     token,
		"distribution":   expected.Distribution,
	})
	for name, raw := range map[string][]byte{
		"pre-compat-schema-v1":   localOrigin(false),
		"schema-v1-with-aliases": localOrigin(true),
		"flat-original":          flat,
	} {
		t.Run(name, func(t *testing.T) {
			state, err := ParseLegacyConnection(raw, expected)
			if err != nil {
				t.Fatal(err)
			}
			if !state.Owned || state.Kind != WindowsStateLegacy {
				t.Fatalf("unexpected legacy state %#v", state)
			}
		})
	}
}

func TestParseLegacyConnectionRejectsAmbiguousState(t *testing.T) {
	expected := fixtureExpected()
	token := expected.StateDir + `\mcp-token`
	tests := []map[string]any{
		{
			"endpoint":  "http://127.0.0.1:19000/mcp",
			"transport": "streamable-http", "authentication": "bearer-token-file",
			"token_file": token, "distribution": expected.Distribution,
		},
		{
			"endpoint":  "http://127.0.0.1:18765/mcp",
			"transport": "streamable-http", "authentication": "bearer-token-file",
			"token_file": token, "distribution": expected.Distribution, "extra": true,
		},
		{
			"schema_version": 1, "distribution": expected.Distribution,
			"local_origin": map[string]any{
				"url": "http://127.0.0.1:19000/mcp", "transport": "streamable-http",
				"reachability":   "loopback",
				"authentication": map[string]any{"type": "bearer-token-file", "token_file": token},
			},
			"endpoint": "http://127.0.0.1:19001/mcp",
		},
	}
	for index, value := range tests {
		raw, _ := json.Marshal(value)
		if _, err := ParseLegacyConnection(raw, expected); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func TestWindowsPathSafety(t *testing.T) {
	if !WindowsPathEqual(`C:\Users\DEV\Loki\`, `c:/users/dev/loki`) {
		t.Fatal("case-insensitive Windows path equality failed")
	}
	state := `C:\Users\dev\AppData\Local\Loki\loki-mcp`
	for _, unsafe := range []string{
		`C:\`, state, state + `\nested`, `C:\Users\dev\AppData\Local\Loki`,
	} {
		if SafeOwnedInstallLocation(unsafe, state) {
			t.Fatalf("unsafe install location accepted: %s", unsafe)
		}
	}
	if !SafeOwnedInstallLocation(`D:\Loki\loki-mcp`, state) {
		t.Fatal("independent install location rejected")
	}
	if !WindowsPathEqual(`\\Server\Share\Loki\`, `//server/share/loki`) {
		t.Fatal("UNC path equality failed")
	}
	if _, ok := normalizeWindowsPath(`\\server\share\..\escape`); ok {
		t.Fatal("UNC path escaped above its share root")
	}
	if normalized, ok := normalizeWindowsPath(`\\server\share\folder\..\loki`); !ok ||
		!strings.EqualFold(normalized, "//server/share/loki") {
		t.Fatalf("UNC normalization failed: %q ok=%v", normalized, ok)
	}
}

func TestClassifyDistribution(t *testing.T) {
	identity := func() DistributionProbe {
		return DistributionProbe{
			Present:     true,
			Manifest:    NativeProbe{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
			Version:     NativeProbe{Stdout: "loki 0.1.19 (test)"},
			Provisioned: NativeProbe{ExitCode: 0},
			Doctor:      NativeProbe{ExitCode: 0},
			Connection:  NativeProbe{ExitCode: 0},
		}
	}
	tests := map[string]struct {
		probe DistributionProbe
		want  DistributionStateKind
	}{
		"absent":              {DistributionProbe{}, DistributionAbsent},
		"foreign-no-manifest": {DistributionProbe{Present: true, Manifest: NativeProbe{ExitCode: 1}}, DistributionForeign},
		"healthy":             {identity(), DistributionHealthy},
		"stale-doctor": {func() DistributionProbe {
			p := identity()
			p.Doctor.ExitCode = 1
			return p
		}(), DistributionStale},
		"indeterminate-provision-probe": {func() DistributionProbe {
			p := identity()
			p.Provisioned.ExitCode = 2
			return p
		}(), DistributionIndeterminate},
		"provisioning": {func() DistributionProbe {
			p := identity()
			p.Provisioned.ExitCode = 1
			p.ServiceState = NativeProbe{Stdout: "ActiveState=activating\nSubState=start\nNRestarts=0\n"}
			return p
		}(), DistributionProvisioning},
		"stale-failed-service": {func() DistributionProbe {
			p := identity()
			p.Provisioned.ExitCode = 1
			p.ServiceState = NativeProbe{Stdout: "ActiveState=failed\nSubState=failed\nNRestarts=3\n"}
			return p
		}(), DistributionStale},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := ClassifyDistribution(test.probe)
			if got.State != test.want {
				t.Fatalf("got %s want %s", got.State, test.want)
			}
		})
	}
}

func TestAssessExistingInstallation(t *testing.T) {
	manifest := WindowsState{Present: true, Owned: true, Kind: WindowsStateManifest, AutoStart: true}
	legacy := WindowsState{Present: true, Owned: true, Kind: WindowsStateLegacy}
	tests := map[string]struct {
		snapshot ExistingSnapshot
		action   ExistingAction
		reason   string
	}{
		"fresh":                {ExistingSnapshot{Distribution: DistributionState{State: DistributionAbsent}}, ExistingFresh, ""},
		"orphan":               {ExistingSnapshot{Distribution: DistributionState{State: DistributionAbsent}, Windows: legacy}, ExistingRemoveOrphan, ""},
		"stale":                {ExistingSnapshot{Distribution: DistributionState{State: DistributionStale}}, ExistingStaleNeedsApproval, ""},
		"foreign":              {ExistingSnapshot{Distribution: DistributionState{State: DistributionForeign}}, ExistingBlocked, "foreign-distribution"},
		"provisioning":         {ExistingSnapshot{Distribution: DistributionState{State: DistributionProvisioning}}, ExistingBlocked, "distribution-provisioning"},
		"healthy-legacy":       {ExistingSnapshot{Distribution: DistributionState{State: DistributionHealthy}, Windows: legacy}, ExistingHealthyNoop, ""},
		"healthy-manifest":     {ExistingSnapshot{Distribution: DistributionState{State: DistributionHealthy}, Windows: manifest, StartupTask: StartupTaskState{Present: true, Owned: true}}, ExistingHealthyNoop, ""},
		"healthy-task-missing": {ExistingSnapshot{Distribution: DistributionState{State: DistributionHealthy}, Windows: manifest}, ExistingBlocked, "healthy-missing-startup-task"},
		"unverified-windows":   {ExistingSnapshot{Distribution: DistributionState{State: DistributionAbsent}, Windows: WindowsState{Present: true}}, ExistingBlocked, "unverified-windows-state"},
		"unverified-task":      {ExistingSnapshot{Distribution: DistributionState{State: DistributionAbsent}, StartupTask: StartupTaskState{Present: true}}, ExistingBlocked, "unverified-startup-task"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := AssessExistingInstallation(test.snapshot)
			if got.Action != test.action || got.Reason != test.reason {
				t.Fatalf("got %#v want action=%s reason=%s", got, test.action, test.reason)
			}
		})
	}
}

func TestPreserveOwnedSettings(t *testing.T) {
	requested := InstallSettings{MCPPort: 18765, AutoStart: true}
	state := WindowsState{
		Owned: true, Kind: WindowsStateManifest, MCPPort: 19000,
		AutoStart: false, InstallLocation: `D:\Loki\loki-mcp`,
	}
	got := PreserveOwnedSettings(requested, state)
	if got.MCPPort != 19000 || got.AutoStart || !strings.EqualFold(got.InstallLocation, state.InstallLocation) {
		t.Fatalf("unexpected preserved settings %#v", got)
	}
	explicit := PreserveOwnedSettings(InstallSettings{
		MCPPort: 20000, MCPPortExplicit: true,
		AutoStart: true, AutoStartExplicit: true,
		InstallLocation: `E:\Explicit`, InstallLocationExplicit: true,
	}, state)
	if explicit.MCPPort != 20000 || !explicit.AutoStart || explicit.InstallLocation != `E:\Explicit` {
		t.Fatalf("explicit settings were overwritten %#v", explicit)
	}
}
