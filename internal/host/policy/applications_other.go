//go:build !linux

package policy

import "errors"

// Resource planning remains portable. Reading privileged service catalogs
// belongs to the Linux host, where ownership and no-symlink checks are enforced.
func readApplicationCatalog(string, any) error {
	return errors.New("application catalog loading requires a Linux host")
}
