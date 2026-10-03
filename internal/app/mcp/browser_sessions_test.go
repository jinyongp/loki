package mcpapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/transport/toolproxy"
)

func TestBrowserHTTPClientsOwnDistinctSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var created atomic.Int32
	stopped := make(chan int32, 2)
	base := mcp.NewServer(&mcp.Implementation{Name: "base", Version: "0.2.0"}, nil)
	pool := newBrowserSessionPool(base, func(context.Context) (*mcp.Server, *toolproxy.Group, func(), error) {
		id := created.Add(1)
		server := mcp.NewServer(&mcp.Implementation{Name: "isolated", Version: "0.2.0"}, nil)
		server.AddTool(&mcp.Tool{Name: "session_identity", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint(id)}}}, nil
		})
		return server, &toolproxy.Group{}, func() { stopped <- id }, nil
	})
	defer pool.close()
	server := httptest.NewServer(pool.handler(mcp.NewStreamableHTTPHandler(pool.server, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: time.Minute})))
	defer server.Close()
	connect := func() *mcp.ClientSession {
		t.Helper()
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	first, second := connect(), connect()
	defer first.Close()
	defer second.Close()
	identity := func(session *mcp.ClientSession) string {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "session_identity", Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return result.Content[0].(*mcp.TextContent).Text
	}
	if identity(first) == identity(second) || created.Load() != 2 {
		t.Fatal("HTTP clients reused a browser session or one request created multiple sessions")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-stopped:
		if id != 1 {
			t.Fatal("closing first session closed another client")
		}
	case <-ctx.Done():
		t.Fatal("disconnected browser session was not cleaned up")
	}
	if identity(second) != "2" {
		t.Fatal("remaining session changed")
	}
	pool.close()
	select {
	case id := <-stopped:
		if id != 2 {
			t.Fatal("wrong shutdown owner")
		}
	case <-ctx.Done():
		t.Fatal("service shutdown did not clean up owned session")
	}
}

func TestRejectedBrowserInitializationReleasesEngines(t *testing.T) {
	stopped := make(chan struct{}, 1)
	base := mcp.NewServer(&mcp.Implementation{Name: "base", Version: "0.2.0"}, nil)
	pool := newBrowserSessionPool(base, func(context.Context) (*mcp.Server, *toolproxy.Group, func(), error) {
		server := mcp.NewServer(&mcp.Implementation{Name: "isolated", Version: "0.2.0"}, nil)
		return server, &toolproxy.Group{}, func() { stopped <- struct{}{} }, nil
	})
	defer pool.close()
	handler := pool.handler(mcp.NewStreamableHTTPHandler(pool.server, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("invalid initialization"))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("rejected initialization status = %d", response.Code)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("rejected initialization retained browser engines")
	}
}
