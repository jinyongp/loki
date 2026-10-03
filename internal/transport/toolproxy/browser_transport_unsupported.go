//go:build !linux

package toolproxy

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProtectedBrowserTransport is the Linux full-mode service transport. Other
// hosts select a Linux execution host or use the standalone project browser.
type ProtectedBrowserTransport struct {
	Socket      string
	ExpectedUID uint32
}

func (ProtectedBrowserTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, errors.New("protected browser service requires a Linux full execution host")
}
