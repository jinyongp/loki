//go:build windows

package windows

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestNativeChildDoesNotCreateConsoleWindow(t *testing.T) {
	if os.Getenv("LOKI_TEST_HIDDEN_PROCESS_CHILD") == "1" {
		window, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		if window != 0 {
			os.Exit(3)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run", "^TestNativeChildDoesNotCreateConsoleWindow$")
	configureNativeProcess(command)
	command.Env = append(withoutEnvironment(os.Environ(), "LOKI_TEST_HIDDEN_PROCESS_CHILD"), "LOKI_TEST_HIDDEN_PROCESS_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native child acquired a console window: %v: %s", err, output)
	}
}
