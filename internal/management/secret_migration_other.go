//go:build !linux

package management

import "fmt"

// Local host migration requires Linux. Keep manager builds independent of
// Linux journals/locks, and reject their data rather than treating it writable.
type fullSecretMigrationRecord struct {
	ID string `json:"id"`
}

func (t transaction) validateSecretMigration() error {
	return fmt.Errorf("secret migration journals require the Linux host owner")
}

func (s Store) requireNoPendingSecretMigration() error {
	_, err := s.readTransaction()
	return err
}
