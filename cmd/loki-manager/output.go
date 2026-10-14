package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"loki/internal/management"
	"loki/internal/tools"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

type commandOutput struct {
	io.Writer
	json bool
}

func jsonOutput(out io.Writer) bool {
	formatted, ok := out.(*commandOutput)
	return ok && formatted.json
}

// Extract the global format flag at either side of the command while preserving
// option values and the explicit positional separator.
func outputArguments(args []string) ([]string, bool, error) {
	clean := make([]string, 0, len(args))
	structured := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			clean = append(clean, args[i:]...)
			break
		}
		name, value, explicit := strings.Cut(arg, "=")
		if name == "--json" || name == "-json" {
			structured = true
			if explicit {
				var err error
				structured, err = strconv.ParseBool(value)
				if err != nil {
					return nil, false, fmt.Errorf("--json requires true or false")
				}
			}
			continue
		}
		clean = append(clean, arg)
		if !explicit && strings.HasPrefix(arg, "-") && helpValueOption(arg) && i+1 < len(args) {
			i++
			clean = append(clean, args[i])
		}
	}
	return clean, structured, nil
}

func result(out io.Writer, title string, value any) error {
	if jsonOutput(out) {
		return json.NewEncoder(out).Encode(value)
	}
	if report, ok := value.(management.Report); ok {
		return printStatus(out, report)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var fields any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, title); err != nil {
		return err
	}
	return printFields(out, fields, "  ")
}

func success(out io.Writer, message string, fields map[string]any) error {
	if !jsonOutput(out) {
		_, err := fmt.Fprintln(out, message)
		return err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["success"] = true
	return json.NewEncoder(out).Encode(fields)
}

func printFields(out io.Writer, value any, indent string) error {
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			label := strings.ReplaceAll(key, "_", " ")
			if len(label) > 0 {
				label = strings.ToUpper(label[:1]) + label[1:]
			}
			switch item := value[key].(type) {
			case map[string]any:
				if len(item) == 0 {
					if _, err := fmt.Fprintf(out, "%s%s: none\n", indent, label); err != nil {
						return err
					}
					continue
				}
				if _, err := fmt.Fprintf(out, "%s%s:\n", indent, label); err != nil {
					return err
				}
				if err := printFields(out, item, indent+"  "); err != nil {
					return err
				}
			case []any:
				if len(item) == 0 {
					if _, err := fmt.Fprintf(out, "%s%s: none\n", indent, label); err != nil {
						return err
					}
					continue
				}
				if _, err := fmt.Fprintf(out, "%s%s:\n", indent, label); err != nil {
					return err
				}
				if err := printFields(out, item, indent+"  "); err != nil {
					return err
				}
			default:
				if _, err := fmt.Fprintf(out, "%s%s: %s\n", indent, label, displayValue(item)); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, item := range value {
			switch item.(type) {
			case map[string]any, []any:
				if _, err := fmt.Fprintln(out, indent+"-"); err != nil {
					return err
				}
				if err := printFields(out, item, indent+"  "); err != nil {
					return err
				}
			default:
				if _, err := fmt.Fprintf(out, "%s- %s\n", indent, displayValue(item)); err != nil {
					return err
				}
			}
		}
	default:
		_, err := fmt.Fprintln(out, indent+displayValue(value))
		return err
	}
	return nil
}

func displayValue(value any) string {
	if value == nil {
		return "unknown"
	}
	if boolean, ok := value.(bool); ok {
		if boolean {
			return "yes"
		}
		return "no"
	}
	return fmt.Sprint(value)
}

func printStatus(out io.Writer, report management.Report) error {
	return printStatusForHost(out, report, report.Release, nil, "")
}

func printStatusForHost(out io.Writer, report management.Report, cliVersion string, host *management.ExecutionSelection, root string) error {
	title := "Loki"
	if report.Healthy != nil {
		title = "Loki doctor: healthy"
		if !*report.Healthy {
			title = "Loki doctor: unhealthy"
		}
	}
	var text bytes.Buffer
	writer := tabwriter.NewWriter(&text, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, title)
	fmt.Fprintf(writer, "  Version: %s\n  Target: %s\n", cliVersion, setupHostLabel(host))
	if report.Target.OS != "" && report.Target.Arch != "" {
		fmt.Fprintf(writer, "  Platform: %s/%s\n", report.Target.OS, report.Target.Arch)
	}
	if root != "" {
		fmt.Fprintln(writer, "  State directory:", root)
	}
	if report.Ready != nil {
		fmt.Fprintln(writer, "  Ready:", displayValue(*report.Ready))
		_, enabled := setupToolCounts(report)
		if enabled == 0 {
			fmt.Fprintln(writer, "  No tools are enabled for client use.")
		}
	}
	ids := []tools.ID{}
	reported := map[string]bool{}
	detailed := false
	for _, name := range publicToolNames {
		if _, exists := report.Tools[tools.ID(name)]; exists {
			ids = append(ids, tools.ID(name))
		}
	}
	if report.Tools == nil {
		fmt.Fprintln(writer, "\nTools: unavailable — execution host could not be inspected")
	} else if len(ids) == 0 {
		fmt.Fprintln(writer, "\nTools: none installed")
		fmt.Fprintln(writer, "  Run 'loki setup' to choose and configure tools.")
	} else {
		fmt.Fprintln(writer, "\nTools:")
		fmt.Fprintln(writer, "  Tool\tVersion\tInstalled\tEnabled\tRuntime")
		for _, id := range ids {
			state := report.Tools[id]
			fmt.Fprintf(writer, "  %s\t%s\t%s\t%s\t%s\n", id, state.Release, displayValue(state.Installed), displayValue(state.Enabled), toolRuntimeLabel(state, report.Target.Mode))
			if state.Reason != "" {
				summary := shortDiagnostic(state.Reason)
				fmt.Fprintln(writer, "    "+summary)
				detailed = detailed || summary != state.Reason
				reported[string(id)+": "+state.Reason] = true
			}
		}
	}
	if state := report.Tools["browser"]; state.Installed {
		fmt.Fprintln(writer, "\nBrowser features:")
		for _, line := range strings.Split(browserFeatureSummary(state), "\n") {
			fmt.Fprintln(writer, "  "+line)
		}
		fmt.Fprintln(writer, "  Configure: loki setup > Manage tools > browser > Optional features")
		if !state.Enabled {
			fmt.Fprintln(writer, "  Browser is disabled; selected features are not available to clients.")
		}
	}
	if len(report.Issues) > 0 {
		first := true
		for _, issue := range report.Issues {
			if reported[issue] {
				continue
			}
			if first {
				fmt.Fprintln(writer, "\nIssues:")
				first = false
			}
			summary := shortDiagnostic(issue)
			fmt.Fprintln(writer, "  - "+summary)
			detailed = detailed || summary != issue
		}
	}
	if detailed {
		fmt.Fprintln(writer, "\nFull diagnostics: repeat this command with --json, keeping the same host options.")
	}
	if len(report.Deployments) > 0 {
		if err := result(writer, "\nDeployments:", report.Deployments); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	_, err := io.Copy(out, &text)
	return err
}
