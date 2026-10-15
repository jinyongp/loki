package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"charm.land/huh/v2"
	"loki/internal/tools"
)

type browserFeature struct {
	id, label, description string
}

// Keep public descriptions here: upstream capability names alone do not explain
// what a user can do, and devtools is unrelated to the task-management CLI.
var browserFeatures = []browserFeature{
	{"devtools", "Video recording and diagnostics", "Record WebM video, annotate screenshots and use developer diagnostics"},
	{"pdf", "PDF export", "Save a page as a PDF"},
	{"vision", "Visual interaction", "Interact with page coordinates and visual elements"},
	{"network", "Network interception", "Mock and intercept browser requests"},
	{"storage", "Browser storage", "Inspect and change cookies and local storage"},
	{"testing", "Page assertions", "Check visible text, elements and values"},
	{"tracing", "Action tracing", "Save a diagnostic log of browser actions"},
	{"config", "Browser configuration", "Inspect and change session configuration"},
	{"extensions", "Chrome extensions", "Manage browser extensions"},
	{"pwa", "Web app installation", "Manage progressive web apps"},
	{"webmcp", "WebMCP (experimental)", "Discover and call tools exposed by web pages"},
	{"third-party", "Third-party tools (experimental)", "Discover and call third-party developer tools"},
	{"memory", "Memory diagnostics", "Collect browser memory debugging information"},
	{"unsafe-code", "Server-side code execution (advanced)", "Allow code execution in the MCP server with its host permissions"},
}

func browserFeatureLabel(id string) string {
	for _, feature := range browserFeatures {
		if feature.id == id {
			return feature.label + " [" + id + "]"
		}
	}
	return id
}

func browserFeatureHelp() string {
	lines := []string{"Optional browser features (--capabilities):"}
	for _, feature := range browserFeatures {
		lines = append(lines, "  "+feature.id+": "+feature.description)
	}
	return strings.Join(lines, "\n")
}

func browserFeatureSummary(state tools.State) string {
	lines := []string{"Navigation and screenshots are included."}
	if len(state.Capabilities) == 0 {
		return strings.Join(append(lines, "Optional features: none selected; video recording is off."), "\n")
	}
	lines = append(lines, fmt.Sprintf("Optional features: %d selected.", len(state.Capabilities)))
	for _, id := range state.Capabilities {
		lines = append(lines, "  • "+browserFeatureLabel(id))
	}
	return strings.Join(lines, "\n")
}

func browserFeatureMenuSummary(state tools.State) string {
	recording := "off"
	if slices.Contains(state.Capabilities, "devtools") {
		recording = "selected"
	}
	return fmt.Sprintf("Optional features: %d selected.\nVideo recording: %s.\nOpen Optional features to review or change them.", len(state.Capabilities), recording)
}

func applyBrowserFeatures(state tools.State, selected []string, invoke func([]string) error) (bool, error) {
	before, after := slices.Clone(state.Capabilities), slices.Clone(selected)
	slices.Sort(before)
	slices.Sort(after)
	if slices.Equal(before, after) {
		return false, nil
	}
	action := "disable"
	if state.Enabled {
		action = "enable"
	}
	err := invoke([]string{"tools", action, "browser", "--capabilities", strings.Join(selected, ",")})
	return err == nil, err
}

func setupBrowserFeatures(ctx context.Context, state tools.State, host string, input, terminal *os.File, invoke func([]string, io.Writer) error) (bool, error) {
	selected := slices.Clone(state.Capabilities)
	features := slices.Clone(browserFeatures)
	// Preserve selections written by a newer manager rather than silently
	// dropping them when this menu saves a different feature.
	for _, id := range selected {
		if !slices.ContainsFunc(browserFeatures, func(feature browserFeature) bool { return feature.id == id }) {
			features = append(features, browserFeature{id, id, "Existing selection"})
		}
	}
	form := huh.NewForm(huh.NewGroup(newBrowserFeaturePicker(features, host, &selected)))
	aborted, err := runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted {
		return false, err
	}
	var changed bool
	err = setupWorking(ctx, terminal, "Saving browser features", host, func(log io.Writer) error {
		var err error
		changed, err = applyBrowserFeatures(state, selected, func(args []string) error { return invoke(args, log) })
		return err
	})
	if err != nil {
		return false, err
	}
	state.Capabilities = selected
	summary := fmt.Sprintf("Host: %s\nBrowser activation: %s.", host, setupStateLabel(state))
	title := "Browser features — unchanged"
	if changed {
		title = "Browser features — saved"
		summary += "\nReconnect the client's Loki MCP server to load the selected features."
	}
	summary += "\n" + browserFeatureSummary(state)
	return changed, showSetupResult(ctx, title, summary, "", input, terminal)
}
