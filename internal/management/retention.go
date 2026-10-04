package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type generationRecord struct {
	Schema       int          `json:"schema"`
	PreparedAt   time.Time    `json:"prepared_at"`
	Installation Installation `json:"installation"`
}

func (r generationRecord) validate() error {
	if r.Schema != 1 || r.PreparedAt.IsZero() {
		return fmt.Errorf("invalid generation record")
	}
	return (Snapshot{Schema: 1, Config: tools.Config{Schema: 1, Contract: r.Installation.Manifest.Contract, Release: r.Installation.Artifact.Release, Host: tools.Host{Kind: "local"}, Mode: r.Installation.Artifact.Target.Mode}, Installed: map[tools.ID]Installation{r.Installation.Artifact.Module: r.Installation}}).Validate()
}

func readOwnedJSON(path string, limit int, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return fmt.Errorf("owned metadata must be a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > limit {
		return fmt.Errorf("owned metadata exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("owned metadata must be one JSON document")
	}
	return nil
}

func generationMetadata(directory string) (generationRecord, error) {
	var record generationRecord
	if err := readOwnedJSON(filepath.Join(directory, ".loki-generation.json"), tools.MaxManifestBytes, &record); err != nil {
		return record, err
	}
	if err := record.validate(); err != nil {
		return record, err
	}
	if err := verifyOwner(directory, record.Installation.Artifact); err != nil {
		return record, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "module.json"))
	if err != nil {
		return record, err
	}
	manifest, err := tools.ParseManifest(data)
	if err != nil {
		return record, err
	}
	if !sameManifest(manifest, record.Installation.Manifest) {
		return record, fmt.Errorf("generation manifest differs from owned metadata")
	}
	return record, nil
}

// retirement grants deletion of one owned generation after it has atomically
// moved into an opaque journal-owned trash directory. Staged deletion can resume
// even if interruption has already removed the generation's inner markers.
type retirement struct {
	Schema   int            `json:"schema"`
	ID       string         `json:"id"`
	Phase    tools.Phase    `json:"phase"`
	Artifact tools.Artifact `json:"artifact"`
}

func (r retirement) validate() error {
	if r.Schema != 1 {
		return fmt.Errorf("invalid retirement schema")
	}
	return (tools.Operation{Schema: 1, ID: r.ID, Action: "remove", Module: r.Artifact.Module, Phase: r.Phase, Previous: r.Artifact.SHA256, Artifact: &r.Artifact}).Validate()
}

func (s Store) readRetirement() (*retirement, error) {
	var r retirement
	err := readOwnedJSON(filepath.Join(s.Root, "retirement.json"), tools.MaxManifestBytes, &r)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

func realDirectories(paths ...string) error {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed directory has invalid ownership boundary: %s", path)
		}
	}
	return nil
}

func (s Store) recoverRetirement() error {
	r, err := s.readRetirement()
	if err != nil {
		return err
	}
	if r == nil || r.Phase == tools.Committed || r.Phase == tools.Aborted {
		return nil
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	if current, exists := state.Installed[r.Artifact.Module]; exists && current.Artifact.SHA256 == r.Artifact.SHA256 {
		return fmt.Errorf("retirement names an active generation; inspect doctor")
	}
	release, err := s.generationLock(r.Artifact, false)
	if err != nil {
		return err
	}
	defer release()
	return s.finishRetirement(r)
}

// Called with host and exclusive generation locks held.
func (s Store) finishRetirement(r *retirement) error {
	generation, err := s.Generation(r.Artifact)
	if err != nil {
		return err
	}
	trashRoot := filepath.Join(s.Root, "trash")
	trash := filepath.Join(trashRoot, r.ID)
	if err := realDirectories(s.Root, trashRoot, trash); err != nil {
		return err
	}
	journal := filepath.Join(s.Root, "retirement.json")
	if r.Phase == tools.Prepared {
		if _, err := os.Lstat(trash); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(generation); errors.Is(err, os.ErrNotExist) {
				r.Phase = tools.Committed
				return atomicJSON(journal, r)
			} else if err != nil {
				return err
			}
			record, err := generationMetadata(generation)
			if err != nil {
				return err
			}
			if record.Installation.Artifact.Identity() != r.Artifact.Identity() {
				return fmt.Errorf("retirement generation identity mismatch")
			}
			if err := os.MkdirAll(trashRoot, 0700); err != nil {
				return err
			}
			if err := os.Rename(generation, trash); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err := verifyOwner(trash, r.Artifact); err != nil {
			return err
		}
		r.Phase = tools.Staged
		if err := atomicJSON(journal, r); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(generation); err == nil {
		return fmt.Errorf("retired generation path was recreated; inspect doctor")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(trash); err != nil {
		return err
	}
	r.Phase = tools.Committed
	return atomicJSON(journal, r)
}

type PruneReport struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
	Busy    []string `json:"busy"`
	Unowned []string `json:"unowned"`
}

// Prune preserves the current generation and active sessions. keep counts
// additional inactive generations. User profiles, results and credentials live
// outside program generations and are not examined or deleted here.
func (s Store) Prune(ctx context.Context, id tools.ID, keep int, progress io.Writer) (PruneReport, error) {
	report := PruneReport{Removed: []string{}, Kept: []string{}, Busy: []string{}, Unowned: []string{}}
	if keep < 0 {
		return report, fmt.Errorf("retention count must be nonnegative")
	}
	module, err := (tools.Layout{Root: s.Root}).Module(id)
	if err != nil {
		return report, err
	}
	unlock, err := s.Lock()
	if err != nil {
		return report, err
	}
	defer unlock()
	if err := s.mutable(); err != nil {
		return report, err
	}
	state, err := s.Load()
	if err != nil {
		return report, err
	}
	directory := filepath.Join(module, "generations")
	if err := realDirectories(s.Root, filepath.Join(s.Root, "tools"), module, directory); err != nil {
		return report, err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	var records []generationRecord
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			report.Unowned = append(report.Unowned, entry.Name())
			continue
		}
		record, err := generationMetadata(filepath.Join(directory, entry.Name()))
		if err != nil || record.Installation.Artifact.Module != id || record.Installation.Artifact.SHA256 != entry.Name() {
			report.Unowned = append(report.Unowned, entry.Name())
			continue
		}
		if record.Installation.Artifact.Target != LocalTarget(state.Config.Mode) {
			report.Unowned = append(report.Unowned, entry.Name())
			continue
		}
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b generationRecord) int {
		if order := b.PreparedAt.Compare(a.PreparedAt); order != 0 {
			return order
		}
		return bytes.Compare([]byte(a.Installation.Artifact.SHA256), []byte(b.Installation.Artifact.SHA256))
	})
	if progress == nil {
		progress = io.Discard
	}
	retained := 0
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		artifact := record.Installation.Artifact
		if current, exists := state.Installed[id]; exists && current.Artifact.SHA256 == artifact.SHA256 {
			report.Kept = append(report.Kept, artifact.Identity())
			continue
		}
		release, err := s.generationLock(artifact, false)
		if errors.Is(err, ErrGenerationInUse) {
			report.Busy = append(report.Busy, artifact.Identity())
			continue
		}
		if err != nil {
			return report, err
		}
		if retained < keep {
			retained++
			report.Kept = append(report.Kept, artifact.Identity())
			release()
			continue
		}
		r := &retirement{Schema: 1, ID: fmt.Sprintf("op-%d", time.Now().UnixNano()), Phase: tools.Prepared, Artifact: artifact}
		err = atomicJSON(filepath.Join(s.Root, "retirement.json"), r)
		if err == nil {
			fmt.Fprintf(progress, "Removing inactive generation %s...\n", artifact.Identity())
			err = s.finishRetirement(r)
		}
		release()
		if err != nil {
			return report, err
		}
		report.Removed = append(report.Removed, artifact.Identity())
	}
	return report, nil
}
