package config

import (
	"bytes"
	"fmt"

	"github.com/pelletier/go-toml/v2"
)

// MergeClientConnection replaces only an explicitly owned MCP client block.
func MergeClientConnection(old, block []byte, server, beginMarker, endMarker string) ([]byte, error) {
	var document map[string]any
	if err := toml.Unmarshal(old, &document); err != nil {
		return nil, fmt.Errorf("existing Codex configuration is invalid: %w", err)
	}
	begin, end := bytes.Index(old, []byte(beginMarker)), bytes.Index(old, []byte(endMarker))
	if begin >= 0 || end >= 0 {
		if begin < 0 || end < begin || bytes.Count(old, []byte(beginMarker)) != 1 || bytes.Count(old, []byte(endMarker)) != 1 {
			return nil, fmt.Errorf("Loki Codex configuration markers are incomplete or duplicated")
		}
		end += len(endMarker)
		next := append(append(append([]byte{}, old[:begin]...), block...), old[end:]...)
		if err := toml.Unmarshal(next, &document); err != nil {
			return nil, fmt.Errorf("updated Codex configuration is invalid: %w", err)
		}
		return next, nil
	}
	if servers, ok := document["mcp_servers"].(map[string]any); ok && servers[server] != nil {
		return nil, fmt.Errorf("existing %s configuration belongs to the user; keep it or rename it before setup", server)
	}
	next := append([]byte{}, old...)
	if len(next) != 0 && next[len(next)-1] != '\n' {
		next = append(next, '\n')
	}
	next = append(next, '\n')
	next = append(next, block...)
	if err := toml.Unmarshal(next, &document); err != nil {
		return nil, fmt.Errorf("new Codex configuration is invalid: %w", err)
	}
	return next, nil
}
