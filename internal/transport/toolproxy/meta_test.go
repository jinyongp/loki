package toolproxy

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUpstreamMetaKeepsApplicationMetadataAndSeparatesSessions(t *testing.T) {
	source := mcp.Meta{mcp.MetaKeyProtocolVersion: "2026-07-28", mcp.MetaKeyClientInfo: "downstream", mcp.MetaKeyClientCapabilities: "downstream capabilities", "progressToken": "original", "fixture/application": "retained"}
	meta := upstreamMeta(source)
	if len(meta) != 2 || meta["fixture/application"] != "retained" || meta["progressToken"] != "original" {
		t.Fatalf("unexpected forwarded metadata: %v", meta)
	}
	meta["progressToken"] = "upstream"
	if source["progressToken"] != "original" {
		t.Fatal("upstream metadata mutated downstream parameters")
	}
}
