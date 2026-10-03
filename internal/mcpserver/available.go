package mcpserver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
)

// WatchAvailable refreshes only the known static implementations. The call
// handlers independently recheck authorization; discovery is an observation.
func WatchAvailable(ctx context.Context, server *mcp.Server, handlers map[string]Handler, allowed func(string) bool) (func(), error) {
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		return nil, err
	}
	type binding struct {
		definition *mcp.Tool
		handler    mcp.ToolHandler
	}
	bindings := make(map[string]binding, len(handlers))
	for _, definition := range definitions {
		if handler := handlers[definition.Name]; handler != nil {
			wrapped, err := wrap(definition, handler)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", definition.Name, err)
			}
			bindings[definition.Name] = binding{definition, wrapped}
		}
	}
	visible := make(map[string]bool, len(bindings))
	for name := range bindings {
		visible[name] = true
	}
	refresh := func() {
		for name, binding := range bindings {
			available := allowed(name)
			if visible[name] == available {
				continue
			}
			if available {
				server.AddTool(binding.definition, binding.handler)
			} else {
				server.RemoveTools(name)
			}
			visible[name] = available
		}
	}
	refresh()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done }) }, nil
}
