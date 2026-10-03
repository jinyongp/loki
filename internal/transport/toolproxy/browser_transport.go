package toolproxy

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/rpc"
)

// ProtectedBrowserTransport verifies the protected browser service's Unix identity
// before sending MCP initialization or roots. It never launches an engine in
// the calling MCP process or grants that process the browser's private files.
type ProtectedBrowserTransport struct {
	Socket      string
	ExpectedUID uint32
}

func (t ProtectedBrowserTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	if !filepath.IsAbs(t.Socket) || filepath.Clean(t.Socket) != t.Socket || t.ExpectedUID == 0 {
		return nil, errors.New("official browser transport requires its exact socket and non-root peer identity")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	connection, err := dialer.DialContext(ctx, "unix", t.Socket)
	if err != nil {
		return nil, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("official browser transport requires a Unix peer")
	}
	peer, err := rpc.PeerCredentials(unixConnection)
	if err != nil || peer.UID != t.ExpectedUID {
		_ = connection.Close()
		return nil, errors.New("official browser service identity differs from its configured role")
	}
	result, err := (&mcp.IOTransport{Reader: connection, Writer: connection, MaxLineLength: 16 << 20}).Connect(ctx)
	if err != nil {
		_ = connection.Close()
	}
	return result, err
}
