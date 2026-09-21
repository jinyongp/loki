package service

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/integrations/sharing/previews"
)

func TestPreviewServerAndSharedHandlers(t *testing.T) {
	store := previews.New("preview.test", 0, nil)
	inspectCalls := 0
	c := &PreviewController{Store: store, Inspect: func(context.Context, int) (map[string]any, error) {
		inspectCalls++
		return map[string]any{"in_use": true, "listeners": []map[string]any{{"cwd": "/workspace/repo", "command": "node"}}}, nil
	}}
	handlers := PreviewHandlers(c, nil)
	requestID := "70000000-0000-4000-8000-000000000001"
	result, err := handlers["preview_publish"](t.Context(), map[string]any{"action": "server", "port": 43000, "request_id": requestID})
	if err != nil {
		t.Fatal(err)
	}
	value := result.StructuredContent.(map[string]any)
	if value["cwd"] != "/workspace/repo" || value["request_id"] != requestID ||
		!strings.HasPrefix(value["url"].(string), "https://loki-") || inspectCalls != 1 {
		t.Fatal(value)
	}
	listed, err := handlers["shared_resources"](t.Context(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	all := listed.StructuredContent.(map[string]any)
	if all["artifacts"].(map[string]any)["configured"] != false ||
		all["artifacts"].(map[string]any)["complete"] != true ||
		all["previews"].(map[string]any)["complete"] != true {
		t.Fatal(all)
	}
	first, err := handlers["revoke_share"](t.Context(), map[string]any{"kind": "preview", "share_id": value["share_id"]})
	if err != nil || len(store.List()) != 0 {
		t.Fatal(err)
	}
	second, err := handlers["revoke_share"](t.Context(), map[string]any{"kind": "preview", "share_id": value["share_id"]})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []*mcp.CallToolResult{first, second} {
		terminal := result.StructuredContent.(map[string]any)
		if terminal["kind"] != "preview" || terminal["share_id"] != value["share_id"] || terminal["revoked"] != true {
			t.Fatal(terminal)
		}
	}
	replayed, err := handlers["preview_publish"](t.Context(), map[string]any{"action": "server", "port": 43000, "request_id": requestID})
	if err != nil {
		t.Fatal(err)
	}
	replayValue := replayed.StructuredContent.(map[string]any)
	if replayValue["share_id"] != value["share_id"] || replayValue["url"] != value["url"] || inspectCalls != 1 || len(store.List()) != 0 {
		t.Fatalf("replay = %#v calls=%d list=%#v", replayValue, inspectCalls, store.List())
	}
	if _, err = handlers["preview_publish"](t.Context(), map[string]any{"action": "server", "port": 43001, "request_id": requestID}); err == nil {
		t.Fatal("changed preview request reused request_id")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("changed preview request error = %#v", detail)
	}
	if inspectCalls != 1 {
		t.Fatalf("conflicting replay re-inspected listener: %d", inspectCalls)
	}
}

func TestPreviewJobPublicationBindsExactEndpointLease(t *testing.T) {
	store := previews.New("preview.test", 0, nil)
	controller := jobControllerFixture()
	jobID := controller.startResult.JobID
	controller.status.JobID = jobID
	c := &PreviewController{Store: store, Jobs: controller}
	requestID := "70000000-0000-4000-8000-000000000002"

	published, err := c.Publish(t.Context(), previewRequest{
		Action: "job", RequestID: requestID, JobID: jobID, Endpoint: "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(published["url"].(string), "https://")
	preview, ok := store.ResolveHost(host)
	if !ok {
		t.Fatal("published Job preview was not retained")
	}
	route, _, ok := previews.ResolveRoute(preview, "/")
	if !ok || route.JobID != jobID || route.LeaseID != strings.Repeat("a", 32) || route.Port != 43001 {
		t.Fatalf("Job preview route = %#v", route)
	}
	if !c.RouteAllowed(t.Context(), route) {
		t.Fatal("active endpoint lease was rejected")
	}

	controller.mu.Lock()
	controller.status.Endpoints[0].ID = strings.Repeat("b", 32)
	controller.mu.Unlock()
	if c.RouteAllowed(t.Context(), route) {
		t.Fatal("stale endpoint lease survived exact-lease replacement")
	}
	if _, err = c.Publish(t.Context(), previewRequest{
		Action: "job", RequestID: requestID, JobID: jobID, Endpoint: "web",
	}); err == nil {
		t.Fatal("reused host port with a different endpoint lease replayed the old preview")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("reused endpoint lease error = %#v", detail)
	}
}
