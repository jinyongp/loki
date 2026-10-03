package config

import "fmt"

// ToolSelection resolves an administrator-owned collection. Nil selects the
// full preset; an explicit empty collection keeps only the management layer.
func ToolSelection(selected []string) (map[string]bool, error) {
	known := []string{"workspace", "git", "github", "browser", "execution", "secrets", "sharing", "coordination"}
	allowed := make(map[string]bool, len(known))
	for _, name := range known {
		allowed[name] = true
	}
	if selected == nil {
		selected = known
	}
	result := make(map[string]bool, len(selected))
	for _, name := range selected {
		if !allowed[name] || result[name] {
			return nil, fmt.Errorf("unknown or duplicate tool %q", name)
		}
		result[name] = true
	}
	return result, nil
}
