package service

import (
	"context"
	"strings"
	"testing"

	"loki/internal/previews"
)

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
