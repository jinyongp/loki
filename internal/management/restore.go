package management

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"loki/internal/tools"
)

type RestoreDataBackupBackend interface {
	DataBackupBackend
	EnsureDataPaths(context.Context, []string) (map[string]string, error)
}

type restoreJournal struct {
	Schema int    `json:"schema"`
	ID     string `json:"id"`
	Phase  string `json:"phase"`
}

func (s Store) RestorePending(id string) (bool, error) {
	journal, err := s.readRestoreJournal()
	if err != nil {
		return false, err
	}
	if journal != nil && journal.Phase == "prepared" {
		if journal.ID != id {
			return false, fmt.Errorf("restore %s must finish before another restore starts", journal.ID)
		}
		return true, nil
	}
	return false, nil
}

func (s Store) readRestoreJournal() (*restoreJournal, error) {
	var journal restoreJournal
	err := readOwnedJSON(filepath.Join(s.Root, "restore.json"), tools.MaxManifestBytes, &journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if journal.Schema != 1 || !backupIDPattern.MatchString(journal.ID) || (journal.Phase != "prepared" && journal.Phase != "committed") {
		return nil, fmt.Errorf("invalid backup restore journal")
	}
	return &journal, nil
}

// RestoreBackup stages and verifies all replacement trees before publishing
// them. The private journal authorizes only derived paths; interruption resumes
// the same backup. Program/data backups remain available after publication.
func (s Store) RestoreBackup(ctx context.Context, id string, backend RestoreDataBackupBackend) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.requireSystemMutable(); err != nil {
		return err
	}
	journal, err := s.readRestoreJournal()
	if err != nil {
		return err
	}
	if journal != nil && journal.Phase == "prepared" && journal.ID != id {
		return fmt.Errorf("restore %s is interrupted; run loki restore %s to resume", journal.ID, journal.ID)
	}
	// Other acquisition/publication recovery must finish before restore starts.
	if journal == nil || journal.Phase == "committed" {
		if err := s.mutable(); err != nil {
			return err
		}
	}
	record, err := s.ReadBackup(id)
	if err != nil {
		return err
	}
	for _, installation := range record.Snapshot.Installed {
		if installation.Artifact.Target != LocalTarget(record.Snapshot.Config.Mode) {
			return fmt.Errorf("backup program target differs from this execution host")
		}
	}
	active, err := s.Deployments()
	if err != nil {
		return err
	}
	if len(active) != 0 {
		return fmt.Errorf("stop services before restoring their data")
	}
	current, err := s.Load()
	if err != nil {
		return err
	}
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
	directory, err := s.backupPath(id)
	if err != nil {
		return err
	}
	type tree struct{ source, target, digest string }
	var trees []tree
	for _, component := range backupComponents {
		digest, exists := record.Components[component]
		if exists {
			trees = append(trees, tree{filepath.Join(directory, component), filepath.Join(s.Root, component), digest})
		}
	}
	if len(record.Volumes) != 0 {
		if backend == nil {
			return fmt.Errorf("full backup restoration requires the owned volume backend")
		}
		var names []string
		for name := range record.Volumes {
			names = append(names, name)
		}
		slices.Sort(names)
		paths, err := backend.EnsureDataPaths(ctx, names)
		if err != nil {
			return err
		}
		for _, name := range names {
			trees = append(trees, tree{filepath.Join(directory, "volumes", name), paths[name], record.Volumes[name]})
		}
	}
	// Verify all source trees and the previous program identity before mutation.
	for _, tree := range trees {
		digest, err := copyBackupTree(ctx, tree.source, "")
		if err != nil || digest != tree.digest {
			return fmt.Errorf("backup data is missing or changed; current data were retained")
		}
	}
	for _, installation := range record.Snapshot.Installed {
		generation, err := s.Generation(installation.Artifact)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(s.Root, generation)
		if err != nil {
			return err
		}
		meta, err := generationMetadata(filepath.Join(directory, relative))
		if err != nil || meta.Installation.Artifact.Identity() != installation.Artifact.Identity() || !sameManifest(meta.Installation.Manifest, installation.Manifest) {
			return fmt.Errorf("backup program identity is invalid")
		}
	}
	journal = &restoreJournal{Schema: 1, ID: id, Phase: "prepared"}
	if err := atomicJSON(filepath.Join(s.Root, "restore.json"), journal); err != nil {
		return err
	}
	for _, tree := range trees {
		stage, saved := tree.target+".loki-candidate-"+id, tree.target+".loki-previous-"+id
		if err := realDirectories(filepath.Dir(tree.target), tree.target, stage, saved); err != nil {
			return err
		}
		// A completed per-tree publication can be recognized after interruption.
		if digest, err := copyBackupTree(ctx, tree.target, ""); err == nil && digest == tree.digest {
			continue
		}
		if _, err := os.Lstat(stage); err == nil {
			digest, err := copyBackupTree(ctx, stage, "")
			if err != nil || digest != tree.digest {
				if err := os.RemoveAll(stage); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Lstat(stage); errors.Is(err, os.ErrNotExist) {
			digest, err := copyBackupTree(ctx, tree.source, stage)
			if err != nil || digest != tree.digest {
				if err == nil {
					err = fmt.Errorf("copied tree checksum differs from backup")
				}
				return fmt.Errorf("restoration staging failed; retry loki restore %s: %w", id, err)
			}
		}
		if _, err := os.Lstat(saved); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(tree.target); err == nil {
				if err := os.Rename(tree.target, saved); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := os.Rename(stage, tree.target); err != nil {
			return fmt.Errorf("restore interrupted; retry loki restore %s: %w", id, err)
		}
	}
	if err := s.Save(record.Snapshot); err != nil {
		return err
	}
	for _, tree := range trees {
		if err := os.RemoveAll(tree.target + ".loki-previous-" + id); err != nil {
			return err
		}
		if err := os.RemoveAll(tree.target + ".loki-candidate-" + id); err != nil {
			return err
		}
	}
	// Keep mutations blocked until cleanup completes. Retrying the same backup
	// recognizes already published trees and resumes the remaining cleanup.
	journal.Phase = "committed"
	return atomicJSON(filepath.Join(s.Root, "restore.json"), journal)
}

func (s Store) RemoveBackup(id string) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.requireSystemMutable(); err != nil {
		return err
	}
	journal, err := s.readRestoreJournal()
	if err != nil {
		return err
	}
	if journal != nil && journal.ID == id && journal.Phase == "prepared" {
		return fmt.Errorf("backup is required by an interrupted restore")
	}
	if _, err := s.ReadBackup(id); err != nil {
		return err
	}
	path, err := s.backupPath(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}
