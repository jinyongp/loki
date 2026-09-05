// Package bootstrap executes registered workflow steps through the runtime RPC.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type Caller interface {
	Call(context.Context, any) (json.RawMessage, error)
}

// ExitError preserves the failed action's exit status.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return "workflow action failed" }

// Run streams runtime-redacted output and stops the active action on interruption.
func Run(ctx context.Context, client Caller, cwd, workflow string, out io.Writer) error {
	var definition struct {
		Steps [][]string `json:"steps"`
	}
	if err := call(ctx, client, map[string]any{"operation": "project_workflow", "cwd": cwd, "workflow": workflow}, &definition); err != nil {
		return err
	}
	for _, step := range definition.Steps {
		if len(step) != 2 || step[0] == "" || step[1] == "" {
			return errors.New("invalid workflow step")
		}
	}
	for _, step := range definition.Steps {
		if err := runStep(ctx, client, cwd, step[0], step[1], out); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func runStep(ctx context.Context, client Caller, cwd, profile, action string, out io.Writer) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "bootstrap: starting %s/%s\n", profile, action); err != nil {
		return err
	}
	var started struct {
		Session string `json:"session_id"`
	}
	if err := call(ctx, client, map[string]any{"operation": "run_action", "profile": profile, "action_name": action, "cwd": cwd}, &started); err != nil {
		return err
	}
	if started.Session == "" {
		return errors.New("runtime omitted action session")
	}
	finished := false
	defer func() {
		if !finished {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, stopErr := client.Call(stopCtx, map[string]any{"operation": "stop_process", "session_id": started.Session})
			if stopErr != nil {
				err = errors.Join(err, errors.New("cannot stop workflow action"))
			}
		}
	}()
	offset := int64(0)
	for {
		var snapshot struct {
			Output string `json:"output"`
			Next   int64  `json:"next_offset"`
			More   bool   `json:"has_more"`
			Status string `json:"status"`
			Code   *int   `json:"exit_code"`
		}
		if err := call(ctx, client, map[string]any{"operation": "read_process", "session_id": started.Session, "offset": offset, "limit": 65536}, &snapshot); err != nil {
			return err
		}
		if snapshot.Next < offset || (snapshot.More && snapshot.Next == offset) {
			return errors.New("invalid process output offset")
		}
		if _, err := io.WriteString(out, snapshot.Output); err != nil {
			return err
		}
		offset = snapshot.Next
		if snapshot.Status != "running" && snapshot.Status != "exited" {
			return errors.New("invalid process status")
		}
		if snapshot.Status == "exited" && !snapshot.More {
			if snapshot.Code == nil {
				return errors.New("runtime omitted action exit status")
			}
			finished = true
			if *snapshot.Code != 0 {
				return ExitError{Code: *snapshot.Code}
			}
			_, err := fmt.Fprintf(out, "bootstrap: completed %s/%s\n", profile, action)
			return err
		}
		if snapshot.More {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func call(ctx context.Context, client Caller, request any, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := client.Call(ctx, request)
	if err != nil {
		return errors.New("workflow runtime request failed")
	}
	if err := json.Unmarshal(data, target); err != nil {
		return errors.New("invalid workflow runtime response")
	}
	return nil
}
