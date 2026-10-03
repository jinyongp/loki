package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"time"
)

// transaction journals complete before/after snapshots. Only state.json changes
// public pointers; acquired immutable generations remain private until commit.
type transaction struct {
	Schema    int         `json:"schema"`
	ID        string      `json:"id"`
	Action    string      `json:"action"`
	Phase     tools.Phase `json:"phase"`
	Previous  Snapshot    `json:"previous"`
	Candidate Snapshot    `json:"candidate"`
}

func (t transaction) validate() error {
	if t.Schema != 1 || (t.Action != "install" && t.Action != "update") {
		return fmt.Errorf("invalid tool transaction schema or action")
	}
	check := tools.Operation{Schema: 1, ID: t.ID, Module: "transaction", Action: "install", Phase: t.Phase, Candidate: snapshotDigest(t.Candidate)}
	if err := check.Validate(); err != nil {
		return err
	}
	if err := t.Previous.Validate(); err != nil {
		return err
	}
	if err := t.Candidate.Validate(); err != nil {
		return err
	}
	before, after := t.Previous.Config, t.Candidate.Config
	before.Release = after.Release
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) || (t.Action == "install" && t.Previous.Config.Release != after.Release) {
		return fmt.Errorf("tool transaction cannot change host, mode or activation")
	}
	for id := range t.Previous.Installed {
		if _, exists := t.Candidate.Installed[id]; !exists {
			return fmt.Errorf("tool transaction cannot remove installed resources")
		}
	}
	if err := validateComposition(t.Candidate); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if len(encoded)+1 > 3*tools.MaxManifestBytes {
		return fmt.Errorf("tool transaction exceeds size limit")
	}
	return nil
}

func snapshotDigest(state Snapshot) string {
	data, _ := json.Marshal(state)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func validateComposition(state Snapshot) error {
	manifests := make([]tools.Manifest, 0, len(state.Installed))
	for _, installation := range state.Installed {
		manifests = append(manifests, installation.Manifest)
	}
	registry, err := tools.NewRegistry(manifests)
	if err != nil {
		return err
	}
	_, err = registry.ResolveConfig(LocalTarget(state.Config.Mode), state.Config)
	return err
}

func (s Store) readTransaction() (*transaction, error) {
	data, err := os.ReadFile(filepath.Join(s.Root, "transaction.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 3*tools.MaxManifestBytes {
		return nil, fmt.Errorf("tool transaction exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var t transaction
	if err := d.Decode(&t); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("tool transaction must be one JSON document")
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// mutable is called under the kernel lock by every public mutation. Activation
// and removal cannot change the recovery basis of an interrupted acquisition.
func (s Store) mutable() error {
	publishing, err := s.readManagerPublication()
	if err != nil {
		return fmt.Errorf("invalid manager publication; inspect doctor: %w", err)
	}
	if publishing != nil && publishing.Phase != tools.Committed && publishing.Phase != tools.Aborted {
		return fmt.Errorf("interrupted manager installation %s requires bundled loki tools recover", publishing.ID)
	}
	retiring, err := s.readRetirement()
	if err != nil {
		return fmt.Errorf("invalid retirement journal; inspect doctor: %w", err)
	}
	if retiring != nil && retiring.Phase != tools.Committed && retiring.Phase != tools.Aborted {
		return fmt.Errorf("interrupted cleanup %s requires loki tools recover", retiring.ID)
	}
	t, err := s.readTransaction()
	if err != nil {
		return fmt.Errorf("invalid tool transaction; inspect doctor: %w", err)
	}
	if t != nil && t.Phase != tools.Committed && t.Phase != tools.Aborted {
		return fmt.Errorf("interrupted transaction %s requires loki tools recover", t.ID)
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "operation.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var op tools.Operation
	if json.Unmarshal(data, &op) != nil || op.Validate() != nil {
		return fmt.Errorf("invalid operation journal; inspect doctor")
	}
	if op.Phase != tools.Committed && op.Phase != tools.Aborted {
		return fmt.Errorf("interrupted operation %s requires loki tools recover", op.ID)
	}
	return nil
}

// RequireMutable is for management initialization under an already held lock.
func (s Store) RequireMutable() error { return s.mutable() }

// InstallTools prepares a complete private prerequisite closure and commits one
// snapshot. Activation stays unchanged. UpdateTools additionally advances all
// retained installations together to a single release train.
func (s Store) InstallTools(ctx context.Context, catalog tools.Catalog, selected []tools.ID, progress io.Writer) error {
	if len(selected) == 0 {
		return fmt.Errorf("select at least one tool to install")
	}
	return s.applyCatalog(ctx, catalog, selected, false, progress)
}

func (s Store) UpdateTools(ctx context.Context, catalog tools.Catalog, progress io.Writer) error {
	return s.applyCatalog(ctx, catalog, nil, true, progress)
}

func (s Store) applyCatalog(ctx context.Context, catalog tools.Catalog, selected []tools.ID, update bool, progress io.Writer) error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	if progress == nil {
		progress = io.Discard
	}
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
	if !update && catalog.Release != state.Config.Release {
		return fmt.Errorf("installation catalog differs from configured release; use loki tools update to change the whole release")
	}
	if update {
		for id := range state.Installed {
			selected = append(selected, id)
		}
		if len(selected) == 0 {
			return fmt.Errorf("no tools are installed; use loki tools install")
		}
	}
	registry, err := tools.NewRegistry(catalog.Modules)
	if err != nil {
		return err
	}
	resolution, err := registry.ResolveInstallation(LocalTarget(state.Config.Mode), selected)
	if err != nil {
		return err
	}
	artifacts, err := resolution.Artifacts(catalog.Artifacts)
	if err != nil {
		return err
	}
	candidate := state
	candidate.Config.Release = catalog.Release
	candidate.Installed = make(map[tools.ID]Installation, len(state.Installed)+len(artifacts))
	for id, current := range state.Installed {
		candidate.Installed[id] = current
	}
	for i, artifact := range artifacts {
		candidate.Installed[artifact.Module] = Installation{Artifact: artifact, Manifest: resolution.Ordered[i]}
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := validateComposition(candidate); err != nil {
		return err
	}
	t := transaction{Schema: 1, ID: fmt.Sprintf("op-%d", time.Now().UnixNano()), Action: "install", Phase: tools.Prepared, Previous: state, Candidate: candidate}
	if update {
		t.Action = "update"
	}
	if err := t.validate(); err != nil {
		return err
	}
	journal := filepath.Join(s.Root, "transaction.json")
	if err := atomicJSON(journal, t); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			t.Phase = tools.Aborted
			_ = atomicJSON(journal, t)
		}
	}()
	for i, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := s.acquire(ctx, artifact, &resolution.Ordered[i], progress); err != nil {
			return err
		}
	}
	t.Phase = tools.Staged
	if err := atomicJSON(journal, t); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(progress, "Committing %d tool resources for release %s...\n", len(artifacts), catalog.Release)
	if err := s.Save(candidate); err != nil {
		return err
	}
	committed = true
	t.Phase = tools.Committed
	return atomicJSON(journal, t)
}

func (s Store) recoverTransaction() error {
	t, err := s.readTransaction()
	if err != nil {
		return err
	}
	if t == nil || t.Phase == tools.Committed || t.Phase == tools.Aborted {
		return nil
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	current := snapshotDigest(state)
	if current == snapshotDigest(t.Candidate) {
		for _, installation := range state.Installed {
			generation, err := s.Generation(installation.Artifact)
			if err != nil {
				return err
			}
			if err := verifyOwner(generation, installation.Artifact); err != nil {
				if current == snapshotDigest(t.Previous) {
					t.Phase = tools.Aborted
					return atomicJSON(filepath.Join(s.Root, "transaction.json"), t)
				}
				return fmt.Errorf("committed transaction resource requires inspection: %w", err)
			}
			data, err := os.ReadFile(filepath.Join(generation, "module.json"))
			if err != nil {
				return err
			}
			manifest, err := tools.ParseManifest(data)
			if err != nil {
				return err
			}
			if !sameManifest(manifest, installation.Manifest) {
				return fmt.Errorf("committed transaction manifest differs from its candidate")
			}
		}
		t.Phase = tools.Committed
	} else if current == snapshotDigest(t.Previous) {
		t.Phase = tools.Aborted
	} else {
		return fmt.Errorf("transaction recovery basis differs from both snapshots; inspect doctor")
	}
	return atomicJSON(filepath.Join(s.Root, "transaction.json"), t)
}
