package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestSetupMenuPipedInput(t *testing.T) {
	for _, input := range []string{"", "\n", "browser\n"} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = writer.WriteString(input)
		_ = writer.Close()
		var output bytes.Buffer
		err = runSetupMenu(context.Background(), nil, reader, &commandOutput{Writer: &output, json: true}, &output)
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
		{tools.State{}, []string{"install-enable", "install"}, "not installed | —"},
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
		{"install and enable", "workspace", "install-enable", tools.Full, true, "", []string{"tools install workspace", "tools enable workspace", "tools start"}},
		{"failed install never enables", "workspace", "install-enable", tools.Full, false, "tools install workspace", []string{"tools install workspace"}},
		{"disable last stops services", "workspace", "disable", tools.Full, false, "", []string{"tools disable workspace", "tools stop"}},
		{"disable preserves other active services", "workspace", "disable", tools.Full, true, "", []string{"tools disable workspace", "tools start"}},
		{"remove stops before deleting", "workspace", "uninstall", tools.Full, true, "", []string{"tools stop", "tools uninstall workspace", "tools start"}},
		{"failed stop never deletes", "workspace", "uninstall", tools.Full, false, "tools stop", []string{"tools stop"}},
		{"github enable runs wizard", "github", "enable", tools.Full, true, "", []string{"tools enable github", "integrations setup github"}},
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
			err := applySetupAction(test.id, test.action, report, invoke, func() (management.Report, error) { return report, nil })
			if (err != nil) != (test.fail != "") || !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("calls %v, error %v", calls, err)
			}
		})
	}
}
