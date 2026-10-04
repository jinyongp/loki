// Package management owns portable 0.2 host state and selected tool artifacts.
package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"runtime"
	"slices"
)

const Release = "0.2.1"

type Installation struct {
	Artifact tools.Artifact `json:"artifact"`
	Manifest tools.Manifest `json:"manifest"`
}

type Snapshot struct {
	Schema    int                       `json:"schema"`
	Config    tools.Config              `json:"config"`
	Installed map[tools.ID]Installation `json:"installed"`
}

type Store struct {
	Root string
	// ArchiveDirectory is a transient acquisition source for receipt-bound
	// offline installation. It is never persisted in desired host state.
	ArchiveDirectory string
}

func DefaultRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "loki"), nil
}

func LocalTarget(mode tools.Mode) tools.Target {
	return tools.Target{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: mode}
}

func (s Store) Load() (Snapshot, error) {
	if err := (tools.Layout{Root: s.Root}).Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := s.realRoot(); err != nil {
		return Snapshot{}, err
	}
	if err := s.realControl(); err != nil {
		return Snapshot{}, err
	}
	info, err := os.Lstat(s.ActivationPath())
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Schema: 1, Config: tools.Config{Schema: 1, Release: Release, Host: tools.Host{Kind: "local"}, Mode: tools.ProjectHost, Tools: []tools.Selection{}}, Installed: map[tools.ID]Installation{}}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > tools.MaxManifestBytes {
		return Snapshot{}, fmt.Errorf("management activation must be a bounded regular file")
	}
	file, err := os.Open(s.ActivationPath())
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, tools.MaxManifestBytes+1))
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > tools.MaxManifestBytes {
		return Snapshot{}, fmt.Errorf("management state exceeds size limit")
	}
	var state Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return state, fmt.Errorf("management state must contain exactly one JSON document")
	}
	if err := state.Validate(); err != nil {
		return state, err
	}
	return state, nil
}

func (s Snapshot) Validate() error {
	if s.Schema != 1 || s.Installed == nil {
		return fmt.Errorf("invalid management state schema")
	}
	if err := s.Config.Validate(); err != nil {
		return err
	}
	for id, installation := range s.Installed {
		if err := installation.Artifact.Validate(); err != nil {
			return err
		}
		if err := installation.Manifest.Validate(); err != nil {
			return err
		}
		if id != installation.Artifact.Module || id != installation.Manifest.ID || installation.Artifact.Release != installation.Manifest.Release || installation.Artifact.Release != s.Config.Release || !slices.Contains(installation.Manifest.Targets, installation.Artifact.Target) {
			return fmt.Errorf("installation identity mismatch for %s", id)
		}
	}
	for _, selection := range s.Config.Tools {
		if _, exists := s.Installed[selection.ID]; !exists {
			return fmt.Errorf("selected tool %s is not installed", selection.ID)
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > tools.MaxManifestBytes {
		return fmt.Errorf("management state exceeds size limit")
	}
	return nil
}

func (s Store) Save(state Snapshot) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if err := s.ensure(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.ControlDirectory(), 0700); err != nil {
		return err
	}
	if err := s.realControl(); err != nil {
		return err
	}
	return atomicJSONMode(s.ActivationPath(), state, 0640)
}

// The credential-free atomic activation snapshot has its own directory. A
// full deployment mounts this directory read-only so atomic replacements are
// visible, without exposing tool data, credentials or the management journal.
func (s Store) ControlDirectory() string { return filepath.Join(s.Root, "control") }
func (s Store) ActivationPath() string   { return filepath.Join(s.ControlDirectory(), "state.json") }
func (s Store) realControl() error {
	info, err := os.Lstat(s.ControlDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("activation directory must be real and owned by this host")
	}
	return nil
}

func (s Store) ensure() error {
	if err := (tools.Layout{Root: s.Root}).Validate(); err != nil {
		return err
	}
	if err := s.realRoot(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	return s.realRoot()
}

func (s Store) realRoot() error {
	info, err := os.Lstat(s.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("management root must be a real directory")
	}
	return nil
}

// Lock uses a kernel lock released on process exit, including an interrupted
// writer. The persistent file is never unlinked while another process owns it.
func (s Store) Lock() (func(), error) {
	if err := s.ensure(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Root, "mutation.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	unlock, err := lockFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("management operation is busy: %w", err)
	}
	return func() { unlock(); _ = f.Close() }, nil
}

func atomicJSON(path string, value any) error {
	return atomicJSONMode(path, value, 0600)
}

func atomicJSONMode(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s Store) Generation(a tools.Artifact) (string, error) {
	if err := a.Validate(); err != nil {
		return "", err
	}
	module, err := (tools.Layout{Root: s.Root}).Module(a.Module)
	if err != nil {
		return "", err
	}
	generation := filepath.Join(module, "generations", a.SHA256)
	// Each managed component must remain a real directory. A parent symlink
	// must not redirect ownership checks or removal to external resources.
	for _, directory := range []string{s.Root, filepath.Join(s.Root, "tools"), module, filepath.Dir(generation), generation} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("managed generation component is not a real directory: %s", directory)
		}
	}
	return generation, nil
}

func (s Store) SetEnabled(id tools.ID, enabled bool, capabilities []string) error {
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
	_, exists := state.Installed[id]
	if !exists {
		return fmt.Errorf("tool %s is not installed", id)
	}
	manifests := make([]tools.Manifest, 0, len(state.Installed))
	for _, current := range state.Installed {
		manifests = append(manifests, current.Manifest)
	}
	registry, err := tools.NewRegistry(manifests)
	if err != nil {
		return err
	}
	selection := tools.Selection{ID: id, Enabled: enabled, Capabilities: capabilities}
	if capabilities == nil {
		for _, current := range state.Config.Tools {
			if current.ID == id {
				selection.Capabilities = current.Capabilities
			}
		}
	}
	found := false
	for i, current := range state.Config.Tools {
		if current.ID == id {
			state.Config.Tools[i] = selection
			found = true
		}
	}
	if !found {
		state.Config.Tools = append(state.Config.Tools, selection)
	}
	if _, err := registry.ResolveConfig(LocalTarget(state.Config.Mode), state.Config); err != nil {
		return err
	}
	return s.Save(state)
}
