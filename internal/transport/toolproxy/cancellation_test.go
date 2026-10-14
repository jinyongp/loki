package toolproxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCanceledEngineReportsRecoveryAndLeavesSiblingUsable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := make(chan struct{})
	var options []Options
	var interrupted *mcp.ServerSession
	for _, name := range []string{"blocked", "healthy"} {
		engine := mcp.NewServer(&mcp.Implementation{Name: name, Version: "1"}, nil)
		engine.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if name == "blocked" {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "available"}}}, nil
		})
		upClient, upServer := mcp.NewInMemoryTransports()
		session, err := engine.Connect(ctx, upServer, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		if name == "blocked" {
			interrupted = session
		}
		options = append(options, Options{Name: name, Transport: upClient, RootURI: "file:///fixture", Authorize: func(string) error { return nil }})
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "proxy", Version: "1"}, nil)
	group, err := AttachMany(ctx, server, options, nil)
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
	callCtx, stopCall := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "blocked"})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("upstream call did not start")
	}
	stopCall()
	if err := <-done; err == nil {
		t.Fatal("canceled call succeeded")
	}
	closed := make(chan struct{})
	go func() { _ = interrupted.Wait(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("canceled engine was not stopped")
	}
	_, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "blocked"})
	if err == nil || !strings.Contains(err.Error(), "reconnect to continue") {
		t.Fatalf("stopped engine lost recovery instruction: %v", err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "healthy"})
	if err != nil || result.IsError {
		t.Fatalf("sibling engine became unavailable: %v %+v", err, result)
	}
}
