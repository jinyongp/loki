// Package mcpclient owns the loopback MCP wire client used by host acceptance.
// Shipping host packages do not import it or acquire MCP transport authority.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Session struct{ session *mcp.ClientSession }
type bearerTransport struct{ token, host string }

func (b bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "http" || request.URL.Host != b.host || request.URL.Path != "/mcp" {
		return nil, errors.New("fixture transport refused another origin")
	}
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(copy)
}

func New(ctx context.Context, endpoint string, token []byte) (*Session, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/mcp" || u.RawQuery != "" || u.User != nil || len(strings.TrimSpace(string(token))) < 43 {
		return nil, errors.New("invalid private loopback fixture")
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "owned-application-acceptance", Version: "2"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: bearerTransport{strings.TrimSpace(string(token)), u.Host}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, errors.New("authenticated loopback MCP unavailable")
	}
	return &Session{session}, nil
}

func (s *Session) Close() error { return s.session.Close() }

func (s *Session) Call(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	switch name {
	case "job", "secret_inspect", "secret_write", "secret_delete":
	default:
		return nil, errors.New("unsupported fixture tool")
	}
	result, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError {
		return nil, errors.New("authenticated application call refused: " + name)
	}
	body, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, errors.New("invalid public metadata")
	}
	var object map[string]any
	if json.Unmarshal(body, &object) != nil || object == nil {
		return nil, errors.New("invalid public metadata")
	}
	return object, nil
}
