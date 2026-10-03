package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"loki/internal/tools"
)

// ToolGate reads the host-published 0.2 public selection for each call. The
// file contains no credentials. A worker's pinned release/mode cannot drift
// when its administrator updates activation or optional capabilities.
type ToolGate struct {
	Path    string
	Release string
	Mode    tools.Mode
	// Snapshot reads the management state's single atomic commit, avoiding a
	// second publication file that could disagree during enable/disable.
	Snapshot bool
}

func (g ToolGate) load() (tools.Config, []byte, error) {
	if !filepath.IsAbs(g.Path) {
		return tools.Config{}, nil, fmt.Errorf("tool selection path must be absolute")
	}
	info, err := os.Lstat(g.Path)
	if err != nil || !info.Mode().IsRegular() {
		return tools.Config{}, nil, fmt.Errorf("tool selection must be a regular host-published file")
	}
	file, err := os.Open(g.Path)
	if err != nil {
		return tools.Config{}, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, tools.MaxManifestBytes+1))
	if err != nil || len(data) > tools.MaxManifestBytes {
		return tools.Config{}, nil, fmt.Errorf("tool selection cannot be read within its bound")
	}
	configuration, err := tools.ParseConfig(data)
	if g.Snapshot {
		configuration, err = activationSnapshot(data)
	}
	if err != nil {
		return tools.Config{}, nil, err
	}
	if configuration.Release != g.Release || configuration.Mode != g.Mode {
		return tools.Config{}, nil, fmt.Errorf("tool release or runtime mode changed; reconnect the worker")
	}
	return configuration, data, nil
}

func activationSnapshot(data []byte) (tools.Config, error) {
	var document struct {
		Schema    int          `json:"schema"`
		Config    tools.Config `json:"config"`
		Installed map[tools.ID]struct {
			Artifact tools.Artifact `json:"artifact"`
			Manifest tools.Manifest `json:"manifest"`
		} `json:"installed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return tools.Config{}, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || document.Schema != 1 || document.Installed == nil {
		return tools.Config{}, fmt.Errorf("invalid host activation snapshot")
	}
	if err := document.Config.Validate(); err != nil {
		return tools.Config{}, err
	}
	var manifests []tools.Manifest
	for id, installation := range document.Installed {
		if installation.Artifact.Module != id || installation.Manifest.ID != id || installation.Artifact.Release != document.Config.Release || installation.Manifest.Release != document.Config.Release || installation.Artifact.Target.Mode != document.Config.Mode || !slices.Contains(installation.Manifest.Targets, installation.Artifact.Target) {
			return tools.Config{}, fmt.Errorf("installed resource differs from its host snapshot")
		}
		if err := installation.Artifact.Validate(); err != nil {
			return tools.Config{}, err
		}
		if err := installation.Manifest.Validate(); err != nil {
			return tools.Config{}, err
		}
		manifests = append(manifests, installation.Manifest)
	}
	for _, choice := range document.Config.Tools {
		installation, exists := document.Installed[choice.ID]
		if !exists || installation.Artifact.Module != choice.ID || installation.Manifest.ID != choice.ID || installation.Artifact.Release != document.Config.Release || installation.Manifest.Release != document.Config.Release || installation.Artifact.Target.Mode != document.Config.Mode {
			return tools.Config{}, fmt.Errorf("selected tool does not match its installed activation snapshot")
		}
		if err := installation.Artifact.Validate(); err != nil {
			return tools.Config{}, err
		}
		if err := installation.Manifest.Validate(); err != nil {
			return tools.Config{}, err
		}
	}
	registry, err := tools.NewRegistry(manifests)
	if err != nil {
		return tools.Config{}, err
	}
	if _, err := registry.ResolveConfig(tools.Target{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: document.Config.Mode}, document.Config); err != nil {
		return tools.Config{}, err
	}
	return document.Config, nil
}

func (g ToolGate) Selection(module string) (tools.Selection, error) {
	configuration, _, err := g.load()
	if err != nil {
		return tools.Selection{}, err
	}
	for _, selected := range configuration.Tools {
		if string(selected.ID) == module && selected.Enabled {
			return selected, nil
		}
	}
	return tools.Selection{}, fmt.Errorf("tool %s is disabled or absent", module)
}

func (g ToolGate) Revision() string {
	_, data, err := g.load()
	if err != nil {
		return "unavailable"
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Resource also admits a private prerequisite of an enabled public choice.
// This is used by service-to-service adapters, not public MCP registration.
func (g ToolGate) Resource(module string) error {
	configuration, data, err := g.load()
	if err != nil {
		return err
	}
	if !g.Snapshot {
		_, err := g.Selection(module)
		return err
	}
	var snapshot struct {
		Installed map[tools.ID]struct {
			Manifest tools.Manifest `json:"manifest"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	var manifests []tools.Manifest
	for _, installed := range snapshot.Installed {
		manifests = append(manifests, installed.Manifest)
	}
	registry, err := tools.NewRegistry(manifests)
	if err != nil {
		return err
	}
	var selected []tools.ID
	for _, choice := range configuration.Tools {
		if choice.Enabled {
			selected = append(selected, choice.ID)
		}
	}
	resolution, err := registry.ResolveInstallation(tools.Target{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: g.Mode}, selected)
	if err != nil {
		return err
	}
	for _, resource := range resolution.Ordered {
		if string(resource.ID) == module {
			return nil
		}
	}
	return fmt.Errorf("resource %s is absent from the active prerequisite closure", module)
}
