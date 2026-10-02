package progress

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCLIOutputKeepsSummariesAndOneWaitNotice(t *testing.T) {
	t.Setenv("LOKI_VERBOSE", "")
	var output bytes.Buffer
	reporter := NewLineReporter(&output)
	Emit(reporter, Event{Level: LevelSummary, Message: "Installing Loki..."})
	for _, message := range []string{"Inspecting current configuration...", "Backing up runtime-state...", "Calculating the backup checksum..."} {
		Emit(reporter, Event{Message: message})
	}
	Emit(reporter, Event{Level: LevelHeartbeat, Message: "Still installing (30s elapsed)..."})
	Emit(reporter, Event{Level: LevelHeartbeat, Message: "Still installing (60s elapsed)..."})
	fmt.Fprintln(&output, "actual warning")
	if got, want := output.String(), "[loki] Installing Loki...\n[loki] Still installing (30s elapsed)...\nactual warning\n"; got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
}

func TestGlobalVerboseOptionPreservesAllProgressWithoutChangingEnvironment(t *testing.T) {
	t.Setenv("LOKI_VERBOSE", "")
	var output bytes.Buffer
	args, writer := CLIArguments([]string{"--verbose", "integration", "setup", "github"}, &output)
	if !reflect.DeepEqual(args, []string{"integration", "setup", "github"}) || !Verbose(writer) || Verbose(&output) {
		t.Fatal("verbose option changed command arguments or leaked into another writer")
	}
	reporter := NewLineReporter(writer)
	for _, event := range []Event{{Message: "Inspecting configuration..."}, {Message: "Backing up runtime-state..."}, {Level: LevelHeartbeat, Message: "Still working..."}, {Level: LevelHeartbeat, Message: "Still working..."}} {
		Emit(reporter, event)
	}
	if strings.Count(output.String(), LinePrefix) != 4 || !Verbose(reporter) {
		t.Fatalf("detailed progress=%q", output.String())
	}
	for _, args := range [][]string{{"version"}, {"github", "--", "--verbose"}} {
		got, writer := CLIArguments(args, &output)
		if !reflect.DeepEqual(got, args) || Verbose(writer) {
			t.Fatal("subcommand arguments were consumed as global options")
		}
	}
}

func TestVerboseEnvironmentPreservesOtherSettingsAndWSLFlags(t *testing.T) {
	base := []string{"PATH=synthetic-path", "WSLENV=FIRST/p:LOKI_VERBOSE/l:SECOND/w", "LOKI_VERBOSE=0", "THIRD=synthetic-value"}
	want := []string{"PATH=synthetic-path", "THIRD=synthetic-value", "LOKI_VERBOSE=1", "WSLENV=FIRST/p:SECOND/w:LOKI_VERBOSE/u"}
	if got := VerboseEnvironment(base); !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%v want=%v", got, want)
	}
	if base[1] != "WSLENV=FIRST/p:LOKI_VERBOSE/l:SECOND/w" {
		t.Fatal("parent environment changed")
	}
}

func TestForwardedVerboseEnvironmentEnablesDetails(t *testing.T) {
	t.Setenv("LOKI_VERBOSE", "1")
	var output bytes.Buffer
	Emit(NewLineReporter(&output), Event{Message: "Detailed child progress"})
	if output.String() != "[loki] Detailed child progress\n" {
		t.Fatalf("forwarded detail missing: %q", output.String())
	}
}
