package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"loki/internal/tools"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"
)

type Owner struct {
	Schema   int    `json:"schema"`
	Identity string `json:"identity"`
}

func (s Store) Install(ctx context.Context, artifact tools.Artifact, progress io.Writer) error {
	return s.install(ctx, artifact, nil, progress)
}

// InstallManifest also binds module metadata to the trusted catalog. An archive
// cannot substitute extra prerequisites, capabilities or public bindings.
func (s Store) InstallManifest(ctx context.Context, artifact tools.Artifact, expected tools.Manifest, progress io.Writer) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if expected.ID != artifact.Module || expected.Release != artifact.Release || !slices.Contains(expected.Targets, artifact.Target) {
		return fmt.Errorf("catalog manifest and artifact identities differ")
	}
	return s.install(ctx, artifact, &expected, progress)
}

func (s Store) install(ctx context.Context, artifact tools.Artifact, expected *tools.Manifest, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}
	if err := artifact.Validate(); err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	state, err := s.Load()
	if err != nil {
		return err
	}
	if (state.Config.Contract == "" && artifact.Release != state.Config.Release) || artifact.Target != LocalTarget(state.Config.Mode) {
		return fmt.Errorf("artifact differs from configured release or execution host")
	}
	if err := s.mutable(); err != nil {
		return err
	}
	op := tools.Operation{Schema: 1, ID: fmt.Sprintf("op-%d", time.Now().UnixNano()), Action: "install", Module: artifact.Module, Phase: tools.Prepared, Candidate: artifact.SHA256}
	if previous, exists := state.Installed[artifact.Module]; exists {
		op.Previous = previous.Artifact.SHA256
	}
	journal := filepath.Join(s.Root, "operation.json")
	if data, err := os.ReadFile(journal); err == nil {
		var previous tools.Operation
		if json.Unmarshal(data, &previous) != nil || previous.Validate() != nil {
			return fmt.Errorf("invalid operation journal; inspect doctor")
		}
		if previous.Phase != tools.Committed && previous.Phase != tools.Aborted {
			return fmt.Errorf("interrupted operation %s requires recovery", previous.ID)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := atomicJSON(journal, op); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			op.Phase = tools.Aborted
			_ = atomicJSON(journal, op)
		}
	}()
	installation, err := s.acquire(ctx, artifact, expected, progress)
	if err != nil {
		return err
	}
	manifest := installation.Manifest
	// A replacement must preserve the currently selected composition. New
	// capabilities and dependencies cannot silently invalidate active tools.
	candidate := state
	candidate.Installed = make(map[tools.ID]Installation, len(state.Installed)+1)
	for id, current := range state.Installed {
		candidate.Installed[id] = current
	}
	candidate.Installed[artifact.Module] = Installation{Artifact: artifact, Manifest: manifest}
	if len(candidate.Config.Tools) > 0 {
		manifests := make([]tools.Manifest, 0, len(candidate.Installed))
		for _, current := range candidate.Installed {
			manifests = append(manifests, current.Manifest)
		}
		registry, err := tools.NewRegistry(manifests)
		if err != nil {
			return err
		}
		if _, err := registry.ResolveConfig(LocalTarget(candidate.Config.Mode), candidate.Config); err != nil {
			return err
		}
	}
	op, err = op.Advance(tools.Staged)
	if err != nil {
		return err
	}
	if err := atomicJSON(journal, op); err != nil {
		return err
	}
	state.Installed[artifact.Module] = Installation{Artifact: artifact, Manifest: manifest}
	if err := s.Save(state); err != nil {
		return err
	}
	// Once state switches, recovery treats the candidate as committed even if
	// the last journal write was interrupted.
	committed = true
	op, err = op.Advance(tools.Committed)
	if err != nil {
		return err
	}
	return atomicJSON(journal, op)
}

func verifyOwner(generation string, a tools.Artifact) error {
	info, err := os.Lstat(generation)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("generation is not an owned directory")
	}
	marker := filepath.Join(generation, ".loki-owner.json")
	info, err = os.Lstat(marker)
	if err != nil {
		return fmt.Errorf("generation ownership: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("generation ownership marker is not a regular file")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return fmt.Errorf("generation ownership: %w", err)
	}
	var owner Owner
	if json.Unmarshal(data, &owner) != nil || owner.Schema != 1 || owner.Identity != a.Identity() {
		return fmt.Errorf("generation ownership mismatch")
	}
	return nil
}

func download(ctx context.Context, a tools.Artifact, out io.Writer, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}
	meter := &downloadProgress{}
	stop := make(chan struct{})
	done := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintf(progress, "Downloading %s: %d / %d bytes (%ds elapsed)...\n", a.Module, meter.received.Load(), a.Bytes, int(time.Since(started).Seconds()))
			}
		}
	}()
	defer func() { close(stop); <-done }()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" {
			return fmt.Errorf("unsafe artifact redirect")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("artifact download returned HTTP %d", response.StatusCode)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash, meter), io.LimitReader(response.Body, a.Bytes+1))
	if err != nil {
		return err
	}
	if n != a.Bytes || hex.EncodeToString(hash.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("artifact length or SHA-256 mismatch")
	}
	return nil
}

type downloadProgress struct {
	received atomic.Int64
}

func (p *downloadProgress) Write(data []byte) (int, error) {
	p.received.Add(int64(len(data)))
	return len(data), nil
}

func (s Store) Recover() error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.recoverManagerPublication(); err != nil {
		return err
	}
	if err := s.recoverRetirement(); err != nil {
		return err
	}
	if err := s.recoverTransaction(); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(s.Root, "operation.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var op tools.Operation
	if err := json.Unmarshal(data, &op); err != nil {
		return err
	}
	if err := op.Validate(); err != nil {
		return err
	}
	if op.Phase == tools.Committed || op.Phase == tools.Aborted {
		return nil
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	if installed, exists := state.Installed[op.Module]; exists && installed.Artifact.SHA256 == op.Candidate {
		op.Phase = tools.Committed
	} else if op.Action == "remove" {
		installed, exists := state.Installed[op.Module]
		if exists && installed.Artifact.SHA256 == op.Previous {
			op.Phase = tools.Aborted
		} else {
			if op.Artifact == nil {
				return fmt.Errorf("removal journal lacks owned artifact identity")
			}
			release, err := s.generationLock(*op.Artifact, false)
			if err != nil {
				return err
			}
			defer release()
			generation, err := s.Generation(*op.Artifact)
			if err != nil {
				return err
			}
			if err := verifyOwner(generation, *op.Artifact); err == nil {
				if err := os.RemoveAll(generation); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			op.Phase = tools.Committed
		}
	} else {
		op.Phase = tools.Aborted
	}
	return atomicJSON(filepath.Join(s.Root, "operation.json"), op)
}

func (s Store) Remove(id tools.ID) error {
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
	installed, exists := state.Installed[id]
	if !exists {
		return nil
	}
	dependents := []tools.ID{}
	for other, current := range state.Installed {
		if slices.Contains(current.Manifest.Requires, id) {
			dependents = append(dependents, other)
		}
	}
	if len(dependents) > 0 {
		slices.Sort(dependents)
		return fmt.Errorf("tool %s is required by installed tools %v; remove dependents first", id, dependents)
	}
	generation, err := s.Generation(installed.Artifact)
	if err != nil {
		return err
	}
	if err := verifyOwner(generation, installed.Artifact); err != nil {
		return err
	}
	release, err := s.generationLock(installed.Artifact, false)
	if err != nil {
		return err
	}
	defer release()
	journal := filepath.Join(s.Root, "operation.json")
	if data, err := os.ReadFile(journal); err == nil {
		var previous tools.Operation
		if json.Unmarshal(data, &previous) != nil || previous.Validate() != nil {
			return fmt.Errorf("invalid operation journal; inspect doctor")
		}
		if previous.Phase != tools.Committed && previous.Phase != tools.Aborted {
			return fmt.Errorf("interrupted operation %s requires recovery", previous.ID)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	op := tools.Operation{Schema: 1, ID: fmt.Sprintf("op-%d", time.Now().UnixNano()), Action: "remove", Module: id, Phase: tools.Prepared, Previous: installed.Artifact.SHA256, Artifact: &installed.Artifact}
	if err := atomicJSON(journal, op); err != nil {
		return err
	}
	op, err = op.Advance(tools.Staged)
	if err != nil {
		return err
	}
	if err := atomicJSON(journal, op); err != nil {
		return err
	}
	// Stop exposure atomically before deleting managed program resources. Tool
	// profiles/results and provider credentials are kept outside generations.
	delete(state.Installed, id)
	state.Config.Tools = slices.DeleteFunc(state.Config.Tools, func(v tools.Selection) bool { return v.ID == id })
	if err := s.Save(state); err != nil {
		return err
	}
	if err := os.RemoveAll(generation); err != nil {
		return err
	}
	op, err = op.Advance(tools.Committed)
	if err != nil {
		return err
	}
	return atomicJSON(journal, op)
}
