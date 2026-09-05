package project

import (
	"context"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"loki/internal/fault"
	"loki/internal/state"
)

type RawTaskRequest struct {
	CWD       string   `json:"cwd"`
	Arguments []string `json:"arguments"`
	Timeout   *int     `json:"timeout_seconds"`
}

// Taskwarrior accepts abbreviated commands and consumes rc overrides even after
// '--'. Validate every token before passing it to the fixed central datastore.
func ValidateTaskArguments(args []string) error {
	if args == nil || len(args) > 128 {
		return fault.Error("task arguments must be an array of at most 128 strings")
	}
	for _, arg := range args {
		if len(arg) > 8192 || strings.ContainsRune(arg, 0) || !utf8.ValidString(arg) {
			return fault.Error("task argument is invalid or too large")
		}
		value := strings.ToLower(strings.TrimSpace(arg))
		if strings.HasPrefix(value, "rc.") || strings.HasPrefix(value, "rc:") {
			return fault.Error("task configuration overrides are not allowed")
		}
		for _, command := range []string{"config", "context", "edit", "execute", "import", "purge", "sync", "synchronize", "undo"} {
			if value != "" && strings.HasPrefix(command, value) {
				return fault.Error("task command is not allowed through the shared datastore wrapper")
			}
		}
	}
	return nil
}

func (t Tasks) Raw(ctx context.Context, r RawTaskRequest) (map[string]any, error) {
	if err := ValidateTaskArguments(r.Arguments); err != nil {
		return nil, err
	}
	seconds := 900
	if r.Timeout != nil {
		seconds = *r.Timeout
	}
	if seconds < 1 || seconds > 1800 {
		return nil, fault.Error("task timeout must be between 1 and 1800 seconds")
	}
	id, err := t.Store.Resolve(ctx, r.CWD)
	if err != nil {
		return nil, err
	}
	if err = t.checkData(id); err != nil {
		return nil, err
	}
	release, err := state.LockFile(ctx, filepath.Join(id.StateDirectory, "task.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	args := append([]string{"rc.hooks=off", "rc.confirmation=off"}, r.Arguments...)
	result, err := t.run(ctx, id, args, time.Duration(seconds)*time.Second)
	if err != nil {
		return nil, err
	}
	return map[string]any{"exit_code": result.ExitCode, "output": result.Output}, nil
}
