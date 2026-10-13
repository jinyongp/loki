package main

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
	"loki/modules/browser"
)

func TestBrowserFeatureSelectionPreservesActivationAndReplacesOnlyOnSave(t *testing.T) {
	for _, test := range []struct {
		name             string
		enabled          bool
		before, selected []string
		want             []string
	}{
		{"enable recording", true, []string{"pdf"}, []string{"pdf", "devtools"}, []string{"tools", "enable", "browser", "--capabilities", "pdf,devtools"}},
		{"keep disabled", false, []string{"pdf"}, []string{"pdf", "devtools"}, []string{"tools", "disable", "browser", "--capabilities", "pdf,devtools"}},
		{"clear features", true, []string{"devtools"}, nil, []string{"tools", "enable", "browser", "--capabilities", ""}},
		{"unchanged", true, []string{"pdf", "devtools"}, []string{"devtools", "pdf"}, nil},
		{"keep existing unknown", false, []string{"future-feature"}, []string{"future-feature", "pdf"}, []string{"tools", "disable", "browser", "--capabilities", "future-feature,pdf"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var command []string
			original := slices.Clone(test.before)
			changed, err := applyBrowserFeatures(tools.State{Installed: true, Enabled: test.enabled, Capabilities: test.before}, test.selected, func(args []string) error { command = args; return nil })
			if err != nil || changed != (test.want != nil) || !reflect.DeepEqual(command, test.want) || !slices.Equal(test.before, original) {
				t.Fatalf("changed=%v command=%v error=%v", changed, command, err)
			}
		})
	}
	want := errors.New("host unavailable")
	changed, err := applyBrowserFeatures(tools.State{Installed: true}, []string{"pdf"}, func([]string) error { return want })
	if changed || !errors.Is(err, want) {
		t.Fatal("failed save claimed completion", changed, err)
	}
}

func TestBrowserFeaturesExplainAllSupportedCapabilities(t *testing.T) {
	var ids []string
	for _, feature := range browserFeatures {
		ids = append(ids, feature.id)
		if feature.label == "" || feature.description == "" {
			t.Fatal("missing feature description", feature.id)
		}
	}
	want := slices.Clone(browser.Capabilities)
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatal("browser features omit or duplicate capabilities", ids, want)
	}
	var help bytes.Buffer
	if err := run(t.Context(), []string{"tools", "enable", "--help"}, &help, &help); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Video recording", "Replace optional features", "unrelated to the devtools CLI"} {
		if !strings.Contains(help.String(), text) {
			t.Fatal("help omitted feature guidance", text)
		}
	}
}

func TestStatusExplainsBrowserFeatures(t *testing.T) {
	report := management.Report{Tools: map[tools.ID]tools.State{"browser": {Installed: true, Enabled: false, Capabilities: []string{"devtools", "pdf"}}}}
	var out bytes.Buffer
	if err := printStatus(&out, report); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Video recording and diagnostics [devtools]", "PDF export [pdf]", "Browser is disabled", "Optional features"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing feature state", text, out.String())
		}
	}
}
