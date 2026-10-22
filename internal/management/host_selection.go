package management

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	managedcommand "loki/internal/platform/command"
	"loki/internal/tools"
)

// ExecutionSelection is frontend transport state, separate from the selected
// host's module configuration and its credentials.
type ExecutionSelection struct {
	Schema  int        `json:"schema"`
	Host    tools.Host `json:"host"`
	Root    string     `json:"root,omitempty"`
	Command string     `json:"command"`
	User    string     `json:"user,omitempty"`
	Owned   bool       `json:"owned,omitempty"`
	System  bool       `json:"system,omitempty"`
	Socket  string     `json:"socket,omitempty"`
}

func (s ExecutionSelection) Validate() error {
	if s.Schema != 1 {
		return fmt.Errorf("unsupported execution host selection")
	}
	if err := s.Host.Validate(); err != nil {
		return err
	}
	if s.Command == "" || strings.HasPrefix(s.Command, "-") || strings.ContainsAny(s.Command, "\x00\r\n") {
		return fmt.Errorf("invalid execution host command")
	}
	if s.Root != "" && (!strings.HasPrefix(s.Root, "/") || strings.ContainsAny(s.Root, "\x00\r\n")) {
		return fmt.Errorf("remote management root must be an absolute execution-host path")
	}
	if s.User != "" && (s.Host.Kind != "wsl" || strings.HasPrefix(s.User, "-") || strings.ContainsAny(s.User, " \t\r\n\x00")) {
		return fmt.Errorf("execution user is supported only for WSL")
	}
	if s.System {
		if s.Host.Kind != "local" || s.User != "" || !s.Owned || !regexp.MustCompile(`^/run/loki-tools-manager-[0-9]+\.sock$`).MatchString(s.Socket) || s.Root == "" {
			return fmt.Errorf("invalid managed system execution host")
		}
	} else if s.Socket != "" {
		return fmt.Errorf("socket requires a managed system host")
	}
	if s.Host.Kind == "local" && !s.System && (s.Root != "" || s.Owned || s.User != "") {
		return fmt.Errorf("local selection cannot contain managed remote fields")
	}
	return nil
}

func (s ExecutionSelection) Remote() bool { return s.System || s.Host.Kind != "local" }

func (s Store) ExecutionSelection() (*ExecutionSelection, error) {
	r, err := s.HostRegistry()
	if err != nil {
		return nil, err
	}
	target := r.Selected().Target
	if target.Host.Kind == "local" && !target.System {
		// Preserve an explicit native choice for guided setup.
		_, registryErr := os.Stat(filepath.Join(s.ControlDirectory(), "hosts.json"))
		_, legacyErr := os.Stat(filepath.Join(s.ControlDirectory(), "execution-host.json"))
		if os.IsNotExist(registryErr) && os.IsNotExist(legacyErr) {
			return nil, nil
		}
	}
	return &target, nil
}

func (s Store) SelectExecutionHost(selected ExecutionSelection) error {
	return s.selectRegisteredTarget(selected)
}

func (s Store) ForgetExecutionHost(preparation bool) error {
	r, err := s.HostRegistry()
	if err != nil {
		return err
	}
	if r.Selected().Target.Host.Kind != "local" || r.Selected().Target.System {
		if err := s.RemoveHost(r.Selected().ID); err != nil {
			return err
		}
	}
	if preparation {
		return s.ForgetHostPreparation(r.Selected().Target.Host.Distribution)
	}
	return nil
}

func RelaySelection(ctx context.Context, selected ExecutionSelection, args []string) (*exec.Cmd, error) {
	if JobInterruptionRequested(ctx) {
		present := false
		for _, arg := range args {
			present = present || arg == "--interrupt-active-jobs" || strings.HasPrefix(arg, "--interrupt-active-jobs=")
		}
		if !present {
			args = append([]string{"--interrupt-active-jobs"}, args...)
		}
	}
	if err := selected.Validate(); err != nil {
		return nil, err
	}
	if selected.System {
		if runtime.GOOS != "linux" {
			return nil, fmt.Errorf("system execution host requires a Linux frontend")
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return managedcommand.New(ctx, binary, append([]string{"_system-relay", "--socket", selected.Socket, "--"}, args...)...), nil
	}
	if selected.Host.Kind == "wsl" && selected.User != "" {
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("WSL execution selection requires the Windows frontend")
		}
		argv := []string{"--distribution", selected.Host.Distribution, "--user", selected.User, "--exec", selected.Command}
		return managedcommand.New(ctx, "wsl.exe", append(argv, args...)...), nil
	}
	return Relay(ctx, selected.Host, selected.Command, args)
}
