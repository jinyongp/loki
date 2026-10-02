package windows

import "testing"

func envLookup(values map[string]string) EnvironmentLookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestResolveInstallOptionsCompatibility(t *testing.T) {
	defaults, err := ResolveInstallOptions(envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Distribution != "loki-mcp" || defaults.MCPPort != 18765 || !defaults.AutoStart ||
		defaults.MCPPortExplicit || defaults.InstallLocationExplicit || defaults.AutoStartExplicit {
		t.Fatalf("unexpected defaults %#v", defaults)
	}
	options, err := ResolveInstallOptions(envLookup(map[string]string{
		"LOKI_WSL_NAME":           "custom-loki",
		"LOKI_WSL_LOCATION":       `D:\Loki\custom`,
		"LOKI_WSL_AUTOSTART":      "0",
		"LOKI_WSL_APPLIANCE_FILE": `C:\tmp\loki.wsl`,
		"LOKI_MCP_PORT":           "19000",
		"LOKI_WSL_REINSTALL":      "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if options.Distribution != "custom-loki" || options.MCPPort != 19000 || options.AutoStart ||
		!options.MCPPortExplicit || !options.InstallLocationExplicit || !options.AutoStartExplicit ||
		!options.ReinstallRequested {
		t.Fatalf("unexpected explicit options %#v", options)
	}
}

func TestResolveInstallOptionsRejectsInvalidValues(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"name":     {"LOKI_WSL_NAME": "../bad"},
		"port":     {"LOKI_MCP_PORT": "80"},
		"location": {"LOKI_WSL_LOCATION": "relative"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveInstallOptions(envLookup(values)); err == nil {
				t.Fatal("invalid environment was accepted")
			}
		})
	}
}

func TestExpectedFromOptionsUsesBackgroundTaskAndPreservesLegacyOwnership(t *testing.T) {
	expected, err := ExpectedFromOptions(InstallOptions{Distribution: "loki-mcp"}, `C:\Users\dev\AppData\Local`, `C:\Windows`)
	if err != nil {
		t.Fatal(err)
	}
	if expected.TaskName != "Loki WSL (loki-mcp)" ||
		expected.TaskExecutable != `C:\Users\dev\AppData\Local\Loki\loki-mcp\loki-keepalive.exe` ||
		expected.TaskArguments != "--distribution loki-mcp" ||
		expected.LegacyHiddenTaskExecutable != `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` ||
		expected.LegacyHiddenTaskArguments != hiddenKeepaliveArguments(`C:\Windows\System32\wsl.exe`, "-d loki-mcp --exec /usr/bin/sleep infinity") ||
		expected.LegacyTaskExecutable != `C:\Windows\System32\wsl.exe` ||
		expected.LegacyTaskArguments != "-d loki-mcp --exec /usr/bin/sleep infinity" {
		t.Fatalf("unexpected expected installation %#v", expected)
	}
}

func TestExpectedFromOptionsRejectsUntrustedWindowsRoots(t *testing.T) {
	for name, localAndSystem := range map[string][2]string{
		"relative-local":  {"relative", `C:\Windows`},
		"relative-system": {`C:\Users\dev\AppData\Local`, "relative"},
		"invalid-name":    {`C:\Users\dev\AppData\Local`, `C:\Windows`},
	} {
		t.Run(name, func(t *testing.T) {
			options := InstallOptions{Distribution: "loki-mcp"}
			if name == "invalid-name" {
				options.Distribution = "../bad"
			}
			if _, err := ExpectedFromOptions(options, localAndSystem[0], localAndSystem[1]); err == nil {
				t.Fatal("invalid Windows root/name was accepted")
			}
		})
	}
}
