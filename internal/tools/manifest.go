// Package tools defines host-independent contracts for the 0.2 tool collection.
// It has no runtime, credential, installer or product-module dependencies.
package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
)

const ManifestSchema = 1
const MaxManifestBytes = 1 << 20

type ID string
type Mode string

const (
	ProjectHost Mode = "project-host"
	Full        Mode = "full"
)

var (
	idPattern      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	releasePattern = regexp.MustCompile(`^0\.2\.(?:0|[1-9][0-9]*)$`)
	toolPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$`)
)

// Target describes the execution host, not the desktop client's OS.
type Target struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Mode Mode   `json:"mode"`
}

func (t Target) Validate() error {
	if t.OS != "linux" && t.OS != "windows" && t.OS != "darwin" {
		return fmt.Errorf("unsupported target OS %q", t.OS)
	}
	if t.Arch != "amd64" && t.Arch != "arm64" {
		return fmt.Errorf("unsupported target architecture %q", t.Arch)
	}
	if t.Mode != ProjectHost && t.Mode != Full {
		return fmt.Errorf("unsupported target mode %q", t.Mode)
	}
	return nil
}

// Manifest describes one tool implementation in the shared release train.
// Requires installs private prerequisites; Tools exposes public bindings only
// when this module is explicitly selected and enabled by host composition.
type Manifest struct {
	Schema       int      `json:"schema"`
	ID           ID       `json:"id"`
	Release      string   `json:"release"`
	Targets      []Target `json:"targets"`
	Requires     []ID     `json:"requires,omitempty"`
	Tools        []string `json:"tools"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func validID(id ID) bool {
	return len(id) <= 64 && idPattern.MatchString(string(id))
}

func (m Manifest) Validate() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("unsupported tool manifest schema %d", m.Schema)
	}
	if !validID(m.ID) || !releasePattern.MatchString(m.Release) {
		return errors.New("tool manifest requires a valid ID and exact 0.2.x release")
	}
	if len(m.Targets) == 0 {
		return fmt.Errorf("tool %s has no execution targets", m.ID)
	}
	targets := map[Target]bool{}
	for _, target := range m.Targets {
		if err := target.Validate(); err != nil {
			return fmt.Errorf("tool %s: %w", m.ID, err)
		}
		if targets[target] {
			return fmt.Errorf("tool %s has a duplicate target", m.ID)
		}
		targets[target] = true
	}
	requires := map[ID]bool{}
	for _, id := range m.Requires {
		if !validID(id) || id == m.ID || requires[id] {
			return fmt.Errorf("tool %s has an invalid or duplicate prerequisite %q", m.ID, id)
		}
		requires[id] = true
	}
	bindings := map[string]bool{}
	for _, name := range m.Tools {
		if len(name) > 128 || !toolPattern.MatchString(name) || bindings[name] {
			return fmt.Errorf("tool %s has an invalid or duplicate MCP binding %q", m.ID, name)
		}
		bindings[name] = true
	}
	capabilities := map[string]bool{}
	for _, name := range m.Capabilities {
		if !validID(ID(name)) || capabilities[name] {
			return fmt.Errorf("tool %s has an invalid or duplicate capability %q", m.ID, name)
		}
		capabilities[name] = true
	}
	return nil
}

func ParseManifest(data []byte) (Manifest, error) {
	if len(data) > MaxManifestBytes {
		return Manifest{}, errors.New("tool manifest exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode tool manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, errors.New("tool manifest must contain exactly one JSON document")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func cloneManifest(m Manifest) Manifest {
	m.Targets = slices.Clone(m.Targets)
	m.Requires = slices.Clone(m.Requires)
	m.Tools = slices.Clone(m.Tools)
	m.Capabilities = slices.Clone(m.Capabilities)
	return m
}
