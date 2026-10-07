package management

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"loki/internal/tools"
)

// RollbackTools restores the previous complete acquisition snapshot. Persistent
// tool data and provider credentials are independent and retained. Every program
// must still have its verified ownership record; pruning removes rollback ability.
func (s Store) RollbackTools(ctx context.Context) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.mutable(); err != nil {
		return err
	}
	active, err := s.Deployments()
	if err != nil {
		return err
	}
	if len(active) != 0 {
		return fmt.Errorf("stop selected services before rollback with loki tools stop")
	}
	previous, err := s.readTransaction()
	if err != nil {
		return err
	}
	if previous == nil || previous.Phase != tools.Committed {
		return fmt.Errorf("no completed installation or update is available to roll back")
	}
	current, err := s.Load()
	if err != nil {
		return err
	}
	if snapshotDigest(current) == snapshotDigest(previous.Previous) {
		return fmt.Errorf("tools already match the previous installation")
	}
	for _, installation := range previous.Previous.Installed {
		if err := ctx.Err(); err != nil {
			return err
		}
		generation, err := s.Generation(installation.Artifact)
		if err != nil {
			return err
		}
		record, err := generationMetadata(generation)
		if err != nil || record.Installation.Artifact.Identity() != installation.Artifact.Identity() || !sameManifest(record.Installation.Manifest, installation.Manifest) {
			return fmt.Errorf("previous program resources are missing or changed; restore a backup instead")
		}
	}
	if err := validateComposition(previous.Previous); err != nil {
		return err
	}
	// Active stdio browser connections must detach before changing runtime modes.
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for _, installation := range current.Installed {
		release, err := s.generationLock(installation.Artifact, false)
		if err != nil {
			return err
		}
		releases = append(releases, release)
	}
	t := transaction{Schema: 1, ID: fmt.Sprintf("op-%d", time.Now().UnixNano()), Action: "rollback", Phase: tools.Prepared, Previous: current, Candidate: previous.Previous}
	if err := t.validate(); err != nil {
		return err
	}
	path := filepath.Join(s.Root, "transaction.json")
	if err := atomicJSON(path, t); err != nil {
		return err
	}
	if err := s.Save(t.Candidate); err != nil {
		return err
	}
	t.Phase = tools.Committed
	return atomicJSON(path, t)
}

// UninstallTools removes program selections in dependency order while retaining
// user/provider data. Ownership and active-generation checks remain in Remove.
func (s Store) UninstallTools(ctx context.Context) error {
	for {
		state, err := s.Load()
		if err != nil {
			return err
		}
		if len(state.Installed) == 0 {
			// Updates and mode promotion retain verified inactive generations.
			// Uninstall removes those programs too, while preserving foreign paths.
			entries, err := os.ReadDir(filepath.Join(s.Root, "tools"))
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				report, err := s.prune(ctx, tools.ID(entry.Name()), 0, io.Discard, true)
				if err != nil {
					return err
				}
				if len(report.Busy) != 0 {
					return fmt.Errorf("inactive programs are still in use; disconnect tool clients and retry uninstall")
				}
			}
			return nil
		}
		var removable tools.ID
		for id := range state.Installed {
			used := false
			for other, installation := range state.Installed {
				if other == id {
					continue
				}
				for _, dependency := range installation.Manifest.Requires {
					used = used || dependency == id
				}
			}
			if !used {
				removable = id
				break
			}
		}
		if removable == "" {
			return fmt.Errorf("installed dependency graph cannot be removed safely")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Remove(removable); err != nil {
			return err
		}
	}
}
