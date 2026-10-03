package runtime

import "loki/internal/config"

// Runtime service selection is administrator-owned. A full preset includes
// every service; an explicit empty collection initializes only control/audit.
func runtimeSelection(selected []string) (map[string]bool, error) {
	return config.ToolSelection(selected)
}
