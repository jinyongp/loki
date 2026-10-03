package management

import (
	"context"
	"fmt"
	"loki/internal/platform/command"
	"loki/internal/tools"
	"os/exec"
	"runtime"
	"strings"
)

// Relay returns a transport command to the management CLI on the selected host.
// Arguments are data: SSH's remote shell receives each argument quoted once.
func Relay(ctx context.Context, host tools.Host, binary string, args []string) (*exec.Cmd, error) {
	if err := host.Validate(); err != nil {
		return nil, err
	}
	if binary == "" || strings.ContainsAny(binary, "\x00\r\n") {
		return nil, fmt.Errorf("invalid execution-host command")
	}
	switch host.Kind {
	case "wsl":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("WSL selection is available from the Windows manager; use the Linux manager inside WSL")
		}
		return command.New(ctx, "wsl.exe", append([]string{"--distribution", host.Distribution, "--exec", binary}, args...)...), nil
	case "ssh":
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
		words := []string{quote(binary)}
		for _, arg := range args {
			if strings.ContainsRune(arg, 0) {
				return nil, fmt.Errorf("invalid remote argument")
			}
			words = append(words, quote(arg))
		}
		return command.New(ctx, "ssh", "-T", host.Address, strings.Join(words, " ")), nil
	default:
		return nil, fmt.Errorf("local execution does not need a relay")
	}
}
