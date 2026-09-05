package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type callerFunc func(context.Context, any) (json.RawMessage, error)

func (f callerFunc) Call(ctx context.Context, request any) (json.RawMessage, error) {
	return f(ctx, request)
}
func raw(s string) (json.RawMessage, error) { return json.RawMessage(s), nil }

func TestRunSequentialAndDrain(t *testing.T) {
	var operations []string
	reads := 0
	client := callerFunc(func(ctx context.Context, request any) (json.RawMessage, error) {
		r := request.(map[string]any)
		op := r["operation"].(string)
		operations = append(operations, op)
		switch op {
		case "project_workflow":
			return raw(`{"steps":[["web","setup"],["web","build"]]}`)
		case "run_action":
			if r["cwd"] != "feature" {
				t.Fatal(r)
			}
			return raw(`{"session_id":"session-1"}`)
		case "read_process":
			reads++
			if reads == 1 {
				return raw(`{"status":"exited","exit_code":0,"output":"first","next_offset":5,"has_more":true}`)
			}
			if reads == 2 {
				if r["offset"] != int64(5) {
					t.Fatal(r)
				}
				return raw(`{"status":"exited","exit_code":0,"output":"last","next_offset":9}`)
			}
			if r["offset"] != int64(0) {
				t.Fatal(r)
			}
			return raw(`{"status":"exited","exit_code":0,"output":"done","next_offset":4}`)
		default:
			t.Fatalf("unexpected operation: %s", op)
			return nil, nil
		}
	})
	var out bytes.Buffer
	if err := Run(t.Context(), client, "feature", "development", &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"project_workflow", "run_action", "read_process", "read_process", "run_action", "read_process"}
	if !reflect.DeepEqual(operations, want) {
		t.Fatal(operations)
	}
	if !strings.Contains(out.String(), "firstlastbootstrap: completed web/setup") || !strings.Contains(out.String(), "donebootstrap: completed web/build") {
		t.Fatal(out.String())
	}
}

func TestRunFailureStopsSequence(t *testing.T) {
	starts := 0
	client := callerFunc(func(ctx context.Context, request any) (json.RawMessage, error) {
		switch request.(map[string]any)["operation"] {
		case "project_workflow":
			return raw(`{"steps":[["web","setup"],["web","build"]]}`)
		case "run_action":
			starts++
			return raw(`{"session_id":"session-1"}`)
		case "read_process":
			return raw(`{"status":"exited","exit_code":17,"next_offset":0}`)
		default:
			t.Fatal(request)
			return nil, nil
		}
	})
	var out bytes.Buffer
	err := Run(t.Context(), client, ".", "development", &out)
	var exit ExitError
	if !errors.As(err, &exit) || exit.Code != 17 || starts != 1 {
		t.Fatalf("%v starts=%d", err, starts)
	}
	if strings.Contains(out.String(), "completed") {
		t.Fatal(out.String())
	}
}

func TestRunStopsActiveSession(t *testing.T) {
	for _, mode := range []string{"cancel", "read-error", "invalid-offset", "missing-exit", "writer-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stops := 0
			client := callerFunc(func(callCtx context.Context, request any) (json.RawMessage, error) {
				switch request.(map[string]any)["operation"] {
				case "project_workflow":
					return raw(`{"steps":[["web","setup"]]}`)
				case "run_action":
					return raw(`{"session_id":"session-1"}`)
				case "read_process":
					switch mode {
					case "cancel":
						cancel()
						return raw(`{"status":"running","next_offset":0}`)
					case "read-error":
						return nil, errors.New("synthetic-private")
					case "invalid-offset":
						return raw(`{"status":"running","next_offset":0,"has_more":true}`)
					case "missing-exit":
						return raw(`{"status":"exited","next_offset":0}`)
					default:
						return raw(`{"status":"running","output":"payload","next_offset":7}`)
					}
				case "stop_process":
					stops++
					if callCtx.Err() != nil {
						t.Fatal("cleanup inherited canceled context")
					}
					return raw(`{}`)
				default:
					t.Fatal(request)
					return nil, nil
				}
			})
			out := &stepWriter{fail: mode == "writer-error"}
			err := Run(ctx, client, ".", "development", out)
			if err == nil || strings.Contains(err.Error(), "synthetic-private") || stops != 1 {
				t.Fatalf("%v stops=%d", err, stops)
			}
		})
	}
}

type stepWriter struct {
	writes int
	fail   bool
}

func (w *stepWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.fail && w.writes > 1 {
		return 0, errors.New("closed output")
	}
	return len(p), nil
}
