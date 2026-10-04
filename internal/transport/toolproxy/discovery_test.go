package toolproxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRetainedToolsRemainCallableDuringDiscoveryRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	engine := mcp.NewServer(&mcp.Implementation{Name: "engine", Version: "1"}, nil)
	handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "available"}}}, nil
	}
	for i := range 200 {
		engine.AddTool(&mcp.Tool{Name: fmt.Sprintf("stable_%d", i), InputSchema: map[string]any{"type": "object"}}, handler)
	}
	upClient, upServer := mcp.NewInMemoryTransports()
	upSession, err := engine.Connect(ctx, upServer, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer upSession.Close()
	server := mcp.NewServer(&mcp.Implementation{Name: "proxy", Version: "1"}, nil)
	group, err := AttachMany(ctx, server, []Options{{Name: "engine", Transport: upClient, RootURI: "file:///fixture", Authorize: func(string) error { return nil }}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer group.Close()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "caller", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 100 {
			engine.AddTool(&mcp.Tool{Name: fmt.Sprintf("new_%d", i), InputSchema: map[string]any{"type": "object"}}, handler)
			time.Sleep(time.Millisecond)
		}
	}()
	for i := range 10000 {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "stable_199", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatalf("retained tool became unavailable during discovery: %v %+v", err, result)
		}
		if i%10 == 0 {
			select {
			case <-done:
				listed, err := session.ListTools(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, tool := range listed.Tools {
					if tool.Name == "new_99" {
						return
					}
				}
			default:
			}
		}
	}
	t.Fatal("discovery update was not observed")
}
