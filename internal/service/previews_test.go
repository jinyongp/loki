package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"loki/internal/previews"
)

type previewRuntime struct {
	started, stopped bool
	environment      map[string]string
	fail             bool
}

func (r *previewRuntime) Call(ctx context.Context, request any) (json.RawMessage, error) {
	encoded, _ := json.Marshal(request)
	var req struct {
		Operation string
		Public    map[string]string `json:"public_environment"`
		Token     string            `json:"launch_token"`
	}
	if err := json.Unmarshal(encoded, &req); err != nil {
		return nil, err
	}
	switch req.Operation {
	case "prepare_action":
		return json.RawMessage(`{"port":43000,"launch_token":"prepared","backend_routes":{"/api":43001},"environment_routes":{"PUBLIC_API":"/api"},"environment_suffixes":{"PUBLIC_API":"/v1"},"required_environment":["PUBLIC_API"]}`), nil
	case "run_action":
		if req.Token != "prepared" {
			return nil, errors.New("missing token")
		}
		if r.fail {
			return nil, errors.New("fixture failed")
		}
		r.started = true
		r.environment = req.Public
		return json.RawMessage(`{"session_id":"session","status":"running","port":43000}`), nil
	case "stop_process":
		r.stopped = true
		return json.RawMessage(`{"stopped":true}`), nil
	case "inspect_docker_port":
		return json.RawMessage(`{"in_use":false,"listeners":[]}`), nil
	}
	return nil, errors.New("unexpected operation")
}
func TestPreviewActionLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "timeout", "run-failure", "invalid-mapping"} {
		t.Run(mode, func(t *testing.T) {
			runtime := &previewRuntime{fail: mode == "run-failure"}
			store := previews.New("preview.test", 0, nil)
			c := &PreviewController{Store: store, Runtime: runtime, ReadyTimeout: time.Millisecond, Inspect: func(ctx context.Context, port int) (map[string]any, error) {
				return map[string]any{"in_use": port == 43001 || runtime.started && mode != "timeout", "listeners": []map[string]any{{"cwd": "/workspace", "command": "fixture"}}}, nil
			}}
			profile, action := "fixture", "web"
			req := previewRequest{Action: "action", Profile: &profile, ActionName: &action, TTL: 900}
			if mode == "invalid-mapping" {
				req.Environment = map[string]string{"PUBLIC_API": "/missing"}
			}
			result, err := c.Publish(t.Context(), req)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if !runtime.started || runtime.stopped || len(store.List()) != 1 || runtime.environment["PUBLIC_API"] != result["url"].(string)+"/api/v1" {
					t.Fatal(result, runtime)
				}
			} else {
				if err == nil || len(store.List()) != 0 {
					t.Fatal(result, err)
				}
				if mode == "timeout" && !runtime.stopped {
					t.Fatal("orphan session")
				}
				if mode == "invalid-mapping" && runtime.started {
					t.Fatal("invalid mapping launched")
				}
			}
		})
	}
}

func TestPreviewServerAndSharedHandlers(t *testing.T) {
	store := previews.New("preview.test", 0, nil)
	c := &PreviewController{Store: store, Inspect: func(context.Context, int) (map[string]any, error) {
		return map[string]any{"in_use": true, "listeners": []map[string]any{{"cwd": "/workspace/repo", "command": "node"}}}, nil
	}}
	handlers := PreviewHandlers(c, nil)
	result, err := handlers["preview_publish"](t.Context(), map[string]any{"action": "server", "port": 43000, "ttl_seconds": 900})
	if err != nil {
		t.Fatal(err)
	}
	value := result.StructuredContent.(map[string]any)
	if value["cwd"] != "/workspace/repo" || !strings.HasPrefix(value["url"].(string), "https://loki-") {
		t.Fatal(value)
	}
	listed, err := handlers["shared_resources"](t.Context(), map[string]any{"kind": "all"})
	if err != nil {
		t.Fatal(err)
	}
	all := listed.StructuredContent.(map[string]any)
	if all["artifacts"].(map[string]any)["configured"] != false {
		t.Fatal(all)
	}
	_, err = handlers["revoke_share"](t.Context(), map[string]any{"kind": "preview", "share_id": value["share_id"]})
	if err != nil || len(store.List()) != 0 {
		t.Fatal(err)
	}
	if _, err = handlers["revoke_share"](t.Context(), map[string]any{"kind": "preview", "share_id": value["share_id"]}); err == nil {
		t.Fatal("double revoke")
	}
}
