package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestFullInstallationNeedsHostSelectionBeforeMutation(t *testing.T) {
	for _, command := range [][]string{{"setup", "workspace"}, {"tools", "install", "workspace"}} {
		root := filepath.Join(t.TempDir(), "untouched")
		var out, diagnostics bytes.Buffer
		err := run(t.Context(), append([]string{"--root", root}, command...), &out, &diagnostics)
		if err == nil || !strings.Contains(err.Error(), "choose an execution host first") || out.Len() != 0 {
			t.Fatalf("installation did not require a host: %v, error %v", command, err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("unselected host was mutated", err)
		}
	}
}

func TestSetupMenuPipedInput(t *testing.T) {
	for _, input := range []string{"", "\n", "browser\n"} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = writer.WriteString(input)
		_ = writer.Close()
		var output bytes.Buffer
		err = runSetupMenu(context.Background(), nil, setupMenuHost{}, reader, &commandOutput{Writer: &output, json: true}, &output)
		_ = reader.Close()
		if input == "browser\n" {
			if err == nil || output.Len() != 0 {
				t.Fatal("noninteractive menu unexpectedly performed an action", err)
			}
		} else if err != nil || !strings.Contains(output.String(), `"state":"cancelled"`) {
			t.Fatal("empty piped setup did not cancel", output.String(), err)
		}
	}
}

func TestSetupMenuAvailableActions(t *testing.T) {
	for _, test := range []struct {
		state tools.State
		want  []string
		label string
	}{
		{tools.State{}, []string{"install-enable", "install"}, "not installed"},
		{tools.State{Installed: true}, []string{"enable", "uninstall"}, "installed | disabled"},
		{tools.State{Installed: true, Enabled: true}, []string{"disable", "enable", "uninstall"}, "installed | enabled"},
	} {
		var names []string
		for _, action := range setupActions(test.state) {
			names = append(names, action.name)
		}
		if !reflect.DeepEqual(names, test.want) || setupStateLabel(test.state) != test.label {
			t.Fatalf("actions %v and state %q", names, setupStateLabel(test.state))
		}
	}
}

func TestSetupMenuActionsRespectInstallationActivationAndServices(t *testing.T) {
	for _, test := range []struct {
		name, id, action string
		mode             tools.Mode
		enabled          bool
		fail             string
		want             []string
	}{
		{"install remains disabled", "browser", "install", tools.ProjectHost, false, "", []string{"tools install browser"}},
		{"install and enable uses guided setup", "workspace", "install-enable", tools.Full, true, "", []string{"setup workspace"}},
		{"failed setup stops action", "workspace", "install-enable", tools.Full, false, "setup workspace", []string{"setup workspace"}},
		{"disable uses shared lifecycle", "workspace", "disable", tools.Full, false, "", []string{"tools disable workspace"}},
		{"remove uses shared lifecycle", "workspace", "uninstall", tools.Full, true, "", []string{"tools uninstall workspace"}},
		{"github enable runs wizard", "github", "enable", tools.Full, true, "", []string{"tools enable github --no-start", "integrations setup github"}},
		{"native browser enable needs no full services", "browser", "enable", tools.ProjectHost, true, "", []string{"tools enable browser"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := management.Report{Target: tools.Target{Mode: test.mode}, Tools: map[tools.ID]tools.State{"git": {Installed: true, Enabled: test.enabled}}}
			var calls []string
			invoke := func(args []string) error {
				call := strings.Join(args, " ")
				calls = append(calls, call)
				if call == test.fail {
					return errors.New("operation failed")
				}
				return nil
			}
			err := applySetupAction(test.id, test.action, report, invoke)
			if (err != nil) != (test.fail != "") || !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls %v, error %v", calls, err)
			}
		})
	}
}

func TestIntegrationMenuRequiresEnabledFullTool(t *testing.T) {
	for _, id := range []string{"git", "github"} {
		for _, mode := range []tools.Mode{tools.Full, tools.ProjectHost} {
			for _, state := range []tools.State{{}, {Installed: true}, {Installed: true, Enabled: true}} {
				configure := false
				for _, action := range setupToolActions(id, state, mode) {
					configure = configure || action.name == "configure"
				}
				if configure != (state.Installed && state.Enabled && mode == tools.Full) {
					t.Fatalf("%s configuration offered without its enabled full-mode prerequisite: %+v %s", id, state, mode)
				}
			}
		}
	}
}
