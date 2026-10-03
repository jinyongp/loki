package tools

import (
	"fmt"
	"strings"
	"sync"
)

// BindingSet combines static module registrations and actual engine discovery.
// A failed update preserves every owner, including the updater's old bindings.
// Tools and resources occupy separate protocol namespaces.
type BindingSet struct {
	mu    sync.Mutex
	names map[string]map[string]string
}

func (b *BindingSet) Replace(namespace, owner string, names []string, publish func()) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if (namespace != "tools" && namespace != "resources") || owner == "" || len(owner) > 128 || strings.ContainsAny(owner, "\x00\r\n") {
		return fmt.Errorf("invalid binding namespace or owner")
	}
	if b.names == nil {
		b.names = map[string]map[string]string{}
	}
	current := b.names[namespace]
	if current == nil {
		current = map[string]string{}
		b.names[namespace] = current
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || len(name) > 4096 || seen[name] || namespace == "tools" && !toolPattern.MatchString(name) {
			return fmt.Errorf("owner %s returned an invalid or duplicate %s binding %q", owner, namespace, name)
		}
		seen[name] = true
		if previous, exists := current[name]; exists && previous != owner {
			return fmt.Errorf("%s binding %q belongs to both %s and %s", namespace, name, previous, owner)
		}
	}
	for name, previous := range current {
		if previous == owner {
			delete(current, name)
		}
	}
	for _, name := range names {
		current[name] = owner
	}
	if publish != nil {
		publish()
	}
	return nil
}

func (b *BindingSet) Owner(namespace, name string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner, exists := b.names[namespace][name]
	return owner, exists
}
