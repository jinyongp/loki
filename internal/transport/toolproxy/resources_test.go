package toolproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProtectedBrowserResourcesPreserveContentsAndRecheckAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := mcp.NewServer(&mcp.Implementation{Name: "protected", Version: "0.2.0"}, nil)
	closeFiles, err := registerFiles(ctx, upstream, &bindingOwners{}, []Options{{Owner: "browser/playwright", Results: t.TempDir(), AuthorizeResource: func() error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles()
	upClient, upServer := mcp.NewInMemoryTransports()
	upSession, err := upstream.Connect(ctx, upServer, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer upSession.Close()
	var enabled atomic.Bool
	enabled.Store(true)
	authorize := func() error {
		if !enabled.Load() {
			return errors.New("browser disabled")
		}
		return nil
	}
	downstream := mcp.NewServer(&mcp.Implementation{Name: "combined", Version: "0.2.0"}, nil)
	group, err := AttachMany(ctx, downstream, []Options{{Name: "protected-browser", Owner: "browser/protected", Version: "0.2.0", Transport: upClient, RootURI: "file:///workspace", ForwardOwnedResources: true, Authorize: func(string) error { return authorize() }, AuthorizeResource: authorize}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := downstream.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "loki_browser_files", Arguments: map[string]any{"engine": "browser/playwright", "action": "stage", "name": "remote.txt", "data": base64.StdEncoding.EncodeToString([]byte("protected contents"))}})
	if err != nil || result.IsError {
		t.Fatalf("stage: %+v %v", result, err)
	}
	var entry fileEntry
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &entry); err != nil {
		t.Fatal(err)
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: entry.URI})
	if err != nil || len(resource.Contents) != 1 || string(resource.Contents[0].Blob) != "protected contents" {
		t.Fatalf("resource: %+v %v", resource, err)
	}
	enabled.Store(false)
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: entry.URI}); err == nil {
		t.Fatal("cached resource bypassed host activation")
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "loki_browser_files", Arguments: map[string]any{"engine": "browser/playwright", "action": "read", "name": "inputs/remote.txt"}})
	if err == nil && !result.IsError {
		t.Fatal("cached helper bypassed host activation")
	}
}
