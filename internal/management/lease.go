package management

import (
	"errors"
	"fmt"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"sync"
)

var ErrGenerationInUse = errors.New("generation is in use")

// Lease pins program resources while an owned tool process uses them. Update
// can switch to a different generation; removal of a leased generation waits
// for the MCP client to disconnect. Kernel locks release on process death.
func (s Store) Lease(artifact tools.Artifact) (func(), error) {
	unlock, err := s.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.mutable(); err != nil {
		return nil, err
	}
	state, err := s.Load()
	if err != nil {
		return nil, err
	}
	current, exists := state.Installed[artifact.Module]
	if !exists || current.Artifact.Identity() != artifact.Identity() {
		return nil, fmt.Errorf("tool installation changed before launch; reconnect")
	}
	generation, err := s.Generation(artifact)
	if err != nil {
		return nil, err
	}
	if err := verifyOwner(generation, artifact); err != nil {
		return nil, err
	}
	return s.generationLock(artifact, true)
}

// Callers already hold the host mutation lock, so a new lease cannot race an
// exclusive resource deletion check. Lease files are persistent and external
// to immutable program generations; removing a generation never unlinks them.
func (s Store) generationLock(artifact tools.Artifact, shared bool) (func(), error) {
	if err := artifact.Validate(); err != nil {
		return nil, err
	}
	if !shared {
		if err := s.requireGenerationNotDeployed(artifact); err != nil {
			return nil, err
		}
	}
	dir := filepath.Join(s.Root, "leases", string(artifact.Module))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, artifact.SHA256+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var unlock func()
	if shared {
		unlock, err = sharedLockFile(f)
	} else {
		unlock, err = lockFile(f)
	}
	if err != nil {
		f.Close()
		if isLockBusy(err) {
			return nil, fmt.Errorf("tool %s has active sessions; disconnect them before removing its program resources: %w", artifact.Module, ErrGenerationInUse)
		}
		return nil, fmt.Errorf("generation lock could not be acquired: %w", err)
	}
	var once sync.Once
	return func() { once.Do(func() { unlock(); _ = f.Close() }) }, nil
}
