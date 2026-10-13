package toolproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProtectedBrowserResourcesPreserveContentsAndRecheckAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	upstream := mcp.NewServer(&mcp.Implementation{Name: "protected", Version: "0.2.0"}, nil)
	upstream.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "server/discover" {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}
			}
			return next(ctx, method, request)
		}
	})
	closeFiles, err := registerFiles(ctx, upstream, &bindingOwners{}, []Options{{Owner: "browser/playwright", Results: t.TempDir(), AuthorizeResource: func() error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles()
	upServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, nil))
	defer upServer.Close()
	upClient := &mcp.StreamableClientTransport{Endpoint: upServer.URL}
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
	// The modern downstream must not overwrite the independently negotiated
	// legacy HTTP upstream's version when returning owned resources.
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
