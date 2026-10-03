package toolproxy

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Protocol and client identity describe each proxy leg's own negotiated
// session. Let the upstream SDK populate them rather than forwarding the
// downstream client's version into a different transport.
func upstreamMeta(source mcp.Meta) mcp.Meta {
	meta := make(mcp.Meta, len(source))
	for key, value := range source {
		if key != mcp.MetaKeyProtocolVersion && key != mcp.MetaKeyClientInfo && key != mcp.MetaKeyClientCapabilities {
			meta[key] = value
		}
	}
	return meta
}
