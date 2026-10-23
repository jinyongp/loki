//go:build linux

package policy

import "loki/internal/daemon"

func readApplicationCatalog(path string, document any) error {
	return daemon.ReadJSON(path, document)
}
