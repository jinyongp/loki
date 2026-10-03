package toolproxy

import "loki/internal/tools"

// bindingOwners covers discovered upstream names and locally registered names
// on the same server. Replacing one engine's discovery cannot overwrite another
// engine or expose two implementations under one public name.
type bindingOwners struct {
	registry tools.BindingSet
}

func (b *bindingOwners) replace(owner string, names []string, apply func()) error {
	return b.registry.Replace("tools", owner, names, apply)
}
