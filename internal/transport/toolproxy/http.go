package toolproxy

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerTransport struct {
	base            http.RoundTripper
	endpoint, token string
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme+"://"+request.URL.Host+request.URL.Path != t.endpoint || request.URL.RawQuery != "" || request.URL.User != nil {
		return nil, fmt.Errorf("MCP transport refused an unrelated destination")
	}
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}

// LocalHTTPTransport connects only to the owned full service's literal
// loopback endpoint. Ambient proxies and redirects cannot receive its bearer.
func LocalHTTPTransport(endpoint string, token []byte) (mcp.Transport, func(), error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/mcp" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, nil, fmt.Errorf("MCP requires its owned loopback endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1024 || port > 65535 {
		return nil, nil, fmt.Errorf("invalid MCP loopback port")
	}
	decoded, err := hex.DecodeString(string(token))
	clear(decoded)
	if err != nil || len(token) != 64 {
		return nil, nil, fmt.Errorf("invalid private MCP bearer")
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.Proxy = nil
	client := &http.Client{Transport: bearerTransport{base: base, endpoint: endpoint, token: string(token)}, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("MCP redirects are disabled") }}
	return &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client}, base.CloseIdleConnections, nil
}
