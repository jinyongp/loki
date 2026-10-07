package management

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// PurgeData removes only the known private data namespaces of this root. The
// caller must stop services and remove programs first. Backup and manager
// ownership records remain available to recover or prepare a new selection.
func (s Store) PurgeData(ctx context.Context) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.mutable(); err != nil {
		return err
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	active, err := s.Deployments()
	if err != nil {
		return err
	}
	if len(state.Installed) != 0 || len(active) != 0 {
		return fmt.Errorf("remove tool programs and stop services before purging data")
	}
	for _, name := range []string{"data", "providers", "integrations", "auth"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(s.Root, name)
		if err := realDirectories(s.Root, path); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}
