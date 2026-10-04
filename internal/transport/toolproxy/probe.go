package toolproxy

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
)

// ProbeBrowser discovers both engines through the protected service identity.
func ProbeBrowser(ctx context.Context, socket string, uid uint32) error {
	return probeMCP(ctx, ProtectedBrowserTransport{Socket: socket, ExpectedUID: uid}, true)
}

// ProbeHTTP discovers selected tools using the caller's confined HTTP client.
func ProbeHTTP(ctx context.Context, endpoint string, client *http.Client) error {
	return probeMCP(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client}, false)
}

func probeMCP(ctx context.Context, transport mcp.Transport, browser bool) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "loki-full-readiness", Version: "0.2.1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	count := 0
	names := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		count++
		if tool == nil || count > 4096 || names[tool.Name] {
			return errors.New("invalid, duplicate or excessive full MCP discovery")
		}
		names[tool.Name] = true
	}
	if count == 0 {
		return errors.New("full MCP discovered no ready selected tools")
	}
	if browser && (!names["browser_navigate"] || !names["navigate_page"] || !names["loki_browser_files"]) {
		return errors.New("protected browser did not discover both official engines and owned file transfer")
	}
	return nil
}
