package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func singleJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("invalid result: %s: %v", data, err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatalf("more than one result: %s", data)
	}
	return value
}

func TestGlobalJSONFlagKeepsValuesAndSeparator(t *testing.T) {
	for _, test := range []struct {
		args, clean []string
		json        bool
	}{
		{[]string{"status", "--json"}, []string{"status"}, true},
		{[]string{"--json", "status"}, []string{"status"}, true},
		{[]string{"tools", "--json", "status"}, []string{"tools", "status"}, true},
		{[]string{"status", "--json=false"}, []string{"status"}, false},
		{[]string{"status", "--json", "--json=false"}, []string{"status"}, false},
		{[]string{"--root", "--json", "status"}, []string{"--root", "--json", "status"}, false},
		{[]string{"tools", "configure", "--mode", "--json"}, []string{"tools", "configure", "--mode", "--json"}, false},
		{[]string{"tools", "install", "--", "--json"}, []string{"tools", "install", "--", "--json"}, false},
		{[]string{"integrations", "setup", "git", "--identity-name=--json", "--json"}, []string{"integrations", "setup", "git", "--identity-name=--json"}, true},
	} {
		actual, structured, err := outputArguments(test.args)
		if err != nil || structured != test.json || !slices.Equal(actual, test.clean) {
			t.Fatalf("%q: %q %v %v", test.args, actual, structured, err)
		}
	}
	if _, _, err := outputArguments([]string{"status", "--json=bad"}); err == nil {
		t.Fatal("invalid boolean accepted")
	}
}

func TestPublicStatusAndMutationOutputFormats(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		args       []string
		structured bool
		field      string
	}{
		{[]string{"install"}, false, "Loki management installed"},
		{[]string{"status"}, false, "Tools: none installed"},
		{[]string{"tools", "list"}, false, "Add tools with"},
		{[]string{"doctor"}, false, "Loki doctor: healthy"},
		{[]string{"status", "--json"}, true, "installed"},
		{[]string{"--json", "tools", "status"}, true, "tools"},
		{[]string{"doctor", "--json"}, true, "healthy"},
		{[]string{"version", "--json"}, true, "version"},
		{[]string{"tools", "configure", "--mode", "full", "--json"}, true, "mode"},
		{[]string{"tools", "plan", "--json"}, true, "services"},
		{[]string{"tools", "plan"}, false, "Full tool plan"},
		{[]string{"tools", "resources", "--json"}, true, "plan"},
		{[]string{"tools", "resources"}, false, "Full tool resources"},
		{[]string{"tools", "layouts", "--json"}, true, "mcp"},
		{[]string{"tools", "layouts"}, false, "Full tool layouts"},
		{[]string{"tools", "topology", "--json"}, true, "schema"},
		{[]string{"tools", "topology"}, false, "Full tool topology"},
		{[]string{"tools", "recover", "--json"}, true, "success"},
	} {
		var out, diagnostics bytes.Buffer
		args := append([]string{"--root", root}, test.args...)
		if err := run(t.Context(), args, &out, &diagnostics); err != nil {
			t.Fatalf("%q: %v\n%s", test.args, err, out.String())
		}
		if test.structured {
			if _, ok := singleJSON(t, out.Bytes())[test.field]; !ok {
				t.Fatalf("%q: missing %s: %s", test.args, test.field, out.String())
			}
		} else if json.Valid(out.Bytes()) || !strings.Contains(out.String(), test.field) {
			t.Fatalf("unreadable %q: %s", test.args, out.String())
		}
	}
}

func TestDoctorFailureStillReportsStateInRequestedFormat(t *testing.T) {
	for _, structured := range []bool{false, true} {
		var out, diagnostics bytes.Buffer
		args := []string{"--root", filepath.Join(t.TempDir(), "absent"), "doctor"}
		if structured {
			args = append(args, "--json")
		}
		if err := run(t.Context(), args, &out, &diagnostics); err == nil {
			t.Fatal("uninstalled doctor succeeded")
		}
		if structured {
			value := singleJSON(t, out.Bytes())
			if value["healthy"] != false {
				t.Fatal(value)
			}
		} else if !strings.Contains(out.String(), "Loki doctor: unhealthy") || !strings.Contains(out.String(), "management is not installed") {
			t.Fatal(out.String())
		}
	}
}

func TestJSONUpgradeDoesNotMixPromptsWithResult(t *testing.T) {
	for _, test := range []struct {
		args         []string
		input, state string
	}{
		{[]string{"--check"}, "", "checked"},
		{nil, "n\n", "cancelled"},
		{[]string{"--yes"}, "", "installed"},
	} {
		deps, _, _ := upgradeFixture(t, "9.0.0", false)
		var out, diagnostics bytes.Buffer
		writer := &commandOutput{Writer: &out, json: true}
		if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, test.args, strings.NewReader(test.input), writer, &diagnostics, deps); err != nil {
			t.Fatal(err)
		}
		value := singleJSON(t, out.Bytes())
		if value["state"] != test.state || value["target"] != "9.0.0" {
			t.Fatal(value)
		}
		if test.state == "cancelled" && !strings.Contains(diagnostics.String(), "[y/N]") {
			t.Fatal("JSON prompt was not sent to stderr")
		}
	}
}

func TestStatusReportsUnknownReadinessAndIssues(t *testing.T) {
	var out bytes.Buffer
	report := management.Report{Installed: true, Release: "0.2.4", Tools: map[tools.ID]tools.State{
		"browser": {Installed: true, Release: "0.2.4", Enabled: true, Readiness: tools.Unknown, Reason: "run doctor to verify"},
	}, Issues: []string{"test issue"}}
	if err := result(&out, "Loki", report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"browser", "unknown", "run doctor to verify", "test issue"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestProtocolAndHelpOutputKeepTheirOwnFormat(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if err := run(t.Context(), []string{"tools", "serve", "--json"}, &out, &diagnostics); err == nil || out.Len() != 0 {
		t.Fatal("format option reached MCP stream")
	}
	if err := run(t.Context(), []string{"--json", "status", "--help"}, &out, &diagnostics); err != nil || json.Valid(out.Bytes()) || !strings.Contains(out.String(), "Usage:") {
		t.Fatal("help lost readable output")
	}
}

func TestSSHRelayForwardsJSONWithoutLocalResultOrState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SSH fixture; Windows local output is covered above")
	}
	directory := t.TempDir()
	recorded := filepath.Join(directory, "arguments")
	root := filepath.Join(directory, "absent root")
	ssh := filepath.Join(directory, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$LOKI_TEST_RELAY_ARGS\"\nprintf '{\"installed\":true}\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOKI_TEST_RELAY_ARGS", recorded)
	var out, diagnostics bytes.Buffer
	if err := run(t.Context(), []string{"--host", "ssh", "--address", "test@example", "--root", root, "status", "--json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if singleJSON(t, out.Bytes())["installed"] != true {
		t.Fatal(out.String())
	}
	arguments, err := os.ReadFile(recorded)
	if err != nil || !strings.Contains(string(arguments), "--json") || !strings.Contains(string(arguments), root) {
		t.Fatalf("format/root not relayed: %s: %v", arguments, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("remote query created local state")
	}
}
