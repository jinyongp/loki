package windows

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestHiddenKeepaliveArguments(t *testing.T) {
	arguments := hiddenKeepaliveArguments(`C:\Example's Windows\System32\wsl.exe`, "-d loki-test --exec /usr/bin/sleep infinity")
	prefix := "-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand "
	if !strings.HasPrefix(arguments, prefix) {
		t.Fatal("keepalive parent is not hidden")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(arguments, prefix))
	if err != nil || len(raw)%2 != 0 {
		t.Fatal("invalid encoded command", err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	script := string(utf16.Decode(units))
	for _, required := range []string{`FileName='C:\Example''s Windows\System32\wsl.exe'`, `Arguments='-d loki-test --exec /usr/bin/sleep infinity'`, "UseShellExecute=$false", "CreateNoWindow=$true", "$p.WaitForExit()", "exit $p.ExitCode"} {
		if !strings.Contains(script, required) {
			t.Fatalf("keepalive wrapper lacks %q", required)
		}
	}
}

func TestBackgroundKeepalivePreservesExactLegacyOwnership(t *testing.T) {
	expected, err := ExpectedFromOptions(InstallOptions{Distribution: "loki-test"}, `C:\Users\alice\AppData\Local`, `C:\Windows`)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		action StartupTaskAction
		owned  bool
	}{
		{"background", StartupTaskAction{expected.TaskExecutable, expected.TaskArguments}, true},
		{"legacy", StartupTaskAction{expected.LegacyTaskExecutable, expected.LegacyTaskArguments}, true},
		{"modified background", StartupTaskAction{expected.TaskExecutable, expected.TaskArguments + " extra"}, false},
		{"modified legacy", StartupTaskAction{expected.LegacyTaskExecutable, expected.LegacyTaskArguments + " extra"}, false},
		{"mixed", StartupTaskAction{expected.TaskExecutable, expected.LegacyTaskArguments}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := StartupTaskProbe{Present: true, Description: "Keep the Loki WSL2 appliance running.", Actions: []StartupTaskAction{test.action}}
			if ClassifyStartupTask(probe, expected).Owned != test.owned {
				t.Fatal("task ownership classification changed")
			}
			raw, err := json.Marshal(ownershipManifestDisk{SchemaVersion: 1, Distribution: expected.Distribution, ReleaseTag: "v1.2.3", StateDir: expected.StateDir, MCPPort: 18765, TaskName: expected.TaskName, TaskExecutable: test.action.Executable, TaskArguments: test.action.Arguments})
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseOwnershipManifest(raw, expected)
			if (err == nil) != test.owned {
				t.Fatalf("manifest ownership differs from task ownership: %v", err)
			}
		})
	}
}
