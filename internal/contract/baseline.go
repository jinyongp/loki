package contract

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed testdata/mcp-v0471.json
var baselineJSON []byte

const BaselineVersion = "0.47.1"
const CatalogRevision = "2026-09-04.4"

// Baseline returns an independent copy; callers cannot mutate the canonical data.
func Baseline() (*Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(baselineJSON, &s); err != nil {
		return nil, err
	}
	if s.Baseline != "python-"+BaselineVersion || len(s.Tools) != 37 {
		return nil, fmt.Errorf("invalid embedded baseline")
	}
	return &s, nil
}

func (s *Snapshot) Definitions() ([]*mcp.Tool, error) {
	items := make([]*mcp.Tool, 0, len(s.Tools))
	seen := map[string]bool{}
	for _, raw := range s.Tools {
		var t mcp.Tool
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		if t.Name == "" || seen[t.Name] {
			return nil, fmt.Errorf("invalid or duplicate tool name")
		}
		seen[t.Name] = true
		items = append(items, &t)
	}
	return items, nil
}
