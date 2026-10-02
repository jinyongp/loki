package windows

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"loki/internal/progress"
)

func TestNativeRunnerForwardsVerbosePreferenceOnlyToChild(t *testing.T) {
	t.Setenv("LOKI_TEST_VERBOSE_CHILD", "1")
	t.Setenv("LOKI_VERBOSE", "")
	t.Setenv("WSLENV", "SYNTHETIC_PATH/p")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, verbose := range []bool{false, true} {
		var output bytes.Buffer
		writer := &output
		reporter := progress.NewLineReporter(writer)
		if verbose {
			reporter = progress.NewLineReporter(progress.WithVerbose(writer))
		}
		result, err := (ExecNativeRunner{}).RunStreaming(t.Context(), binary, []string{"-test.run=^TestNativeVerboseChild$"}, reporter)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("probe=%+v err=%v", result, err)
		}
		want := "|SYNTHETIC_PATH/p"
		if verbose {
			want = "1|SYNTHETIC_PATH/p:LOKI_VERBOSE/u"
		}
		if !strings.HasPrefix(result.Stdout, want) || strings.Contains(output.String(), "Detailed child progress") != verbose {
			t.Fatalf("verbose=%t stdout=%q progress=%q", verbose, result.Stdout, output.String())
		}
		if os.Getenv("LOKI_VERBOSE") != "" || os.Getenv("WSLENV") != "SYNTHETIC_PATH/p" {
			t.Fatal("verbose mode changed parent environment")
		}
	}
}

func TestNativeVerboseChild(t *testing.T) {
	if os.Getenv("LOKI_TEST_VERBOSE_CHILD") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, os.Getenv("LOKI_VERBOSE")+"|"+os.Getenv("WSLENV"))
	fmt.Fprintln(os.Stderr, "[loki] Detailed child progress")
}

func TestNativeFailureKeepsActualErrorWithoutRepeatingProgress(t *testing.T) {
	err := nativeFailure("integration setup", NativeProbe{ExitCode: 9, Stderr: "[loki] Preparing backup...\n[loki] Calculating checksum...\nactual installation failure\n"})
	if err.Error() != "integration setup failed with exit code 9: actual installation failure" {
		t.Fatalf("error=%v", err)
	}
	err = nativeFailure("integration setup", NativeProbe{ExitCode: 9, Stderr: "[loki] Preparing backup...\n"})
	if err.Error() != "integration setup failed with exit code 9" {
		t.Fatalf("error=%v", err)
	}
}
