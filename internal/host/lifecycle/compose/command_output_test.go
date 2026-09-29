package compose

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCommandOutputRetainsBoundedPrefixAndTail(t *testing.T) {
	for _, size := range []int{1, 317, maxCommandOutput, maxCommandOutput + 21} {
		var output commandOutput
		full := "start\n" + strings.Repeat("progress\n", maxCommandOutput) + "fatal: final cause\n"
		for offset := 0; offset < len(full); offset += size {
			chunk := full[offset:min(offset+size, len(full))]
			n, err := output.Write([]byte(chunk))
			if err != nil || n != len(chunk) {
				t.Fatalf("write n=%d err=%v", n, err)
			}
			if len(output.head) > maxCommandOutput || len(output.tail) > maxCommandOutput {
				t.Fatal("output is unbounded")
			}
		}
		if string(output.head) != full[:maxCommandOutput] || string(output.tail) != full[len(full)-maxCommandOutput:] {
			t.Fatalf("head or tail mismatch for chunk size %d", size)
		}
	}
}

func TestExecRunnerPreservesFinalFailureAfterProgress(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := (ExecRunner{Executable: binary}).Run(t.Context(), []string{"LOKI_TEST_COMMAND_OUTPUT=1"}, "-test.run=^TestExecRunnerOutputHelper$")
	if err == nil || !strings.Contains(err.Error(), "fatal: final cause") || !strings.HasSuffix(string(raw), "fatal: final cause\n") {
		t.Fatalf("missing error tail len=%d err=%v", len(raw), err)
	}
	if len(raw) != maxCommandOutput {
		t.Fatalf("output length=%d", len(raw))
	}
}

func TestExecRunnerOutputHelper(t *testing.T) {
	if os.Getenv("LOKI_TEST_COMMAND_OUTPUT") != "1" {
		return
	}
	fmt.Fprint(os.Stdout, strings.Repeat("pulling layers\n", 10000))
	fmt.Fprintln(os.Stderr, "fatal: final cause")
	os.Exit(7)
}
