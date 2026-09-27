package windows

import "testing"

func TestResolveInstallOptionsWithArgsOverridesEnvironment(t *testing.T) {
	options, err := ResolveInstallOptionsWithArgs([]string{
		"--distribution", "cli-loki",
		"--location", "E:\\CLI\\loki",
		"--autostart=false",
		"--appliance-file", "E:\\fixture.wsl",
		"--mcp-port", "20000",
		"--reinstall",
	}, envLookup(map[string]string{
		"LOKI_WSL_NAME":      "env-loki",
		"LOKI_WSL_LOCATION":  "D:\\Env\\loki",
		"LOKI_WSL_AUTOSTART": "1",
		"LOKI_MCP_PORT":      "19000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if options.Distribution != "cli-loki" || options.InstallLocation != "E:\\CLI\\loki" ||
		options.AutoStart || options.MCPPort != 20000 || !options.ReinstallRequested ||
		!options.InstallLocationExplicit || !options.AutoStartExplicit || !options.MCPPortExplicit {
		t.Fatalf("unexpected CLI options %#v", options)
	}
}

func TestResolveInstallOptionsWithArgsKeepsEnvironmentWhenFlagsAbsent(t *testing.T) {
	options, err := ResolveInstallOptionsWithArgs(nil, envLookup(map[string]string{
		"LOKI_WSL_NAME":      "env-loki",
		"LOKI_WSL_LOCATION":  "D:\\Env\\loki",
		"LOKI_WSL_AUTOSTART": "0",
		"LOKI_MCP_PORT":      "19000",
		"LOKI_WSL_REINSTALL": "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if options.Distribution != "env-loki" || options.InstallLocation != "D:\\Env\\loki" ||
		options.AutoStart || options.MCPPort != 19000 || !options.ReinstallRequested {
		t.Fatalf("environment options changed %#v", options)
	}
}

func TestResolveInstallOptionsWithArgsRejectsInvalidFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"distribution": {"--distribution", "../bad"},
		"location":     {"--location", "relative"},
		"autostart":    {"--autostart=maybe"},
		"port":         {"--mcp-port", "80"},
		"positional":   {"extra"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveInstallOptionsWithArgs(args, envLookup(nil)); err == nil {
				t.Fatal("invalid install flags were accepted")
			}
		})
	}
}
