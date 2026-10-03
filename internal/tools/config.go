package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

const ConfigSchema = 1

// Host identifies where tools run. Target is detected by that host, never by
// the desktop client. SSH and WSL are explicit management transports.
type Host struct {
	Kind         string `json:"kind"`
	Address      string `json:"address,omitempty"`
	Distribution string `json:"distribution,omitempty"`
}

func (h Host) Validate() error {
	clean := func(s string) bool {
		return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "\x00\r\n")
	}
	switch h.Kind {
	case "local":
		if h.Address != "" || h.Distribution != "" {
			return fmt.Errorf("local host cannot have remote connection fields")
		}
	case "wsl":
		if !clean(h.Distribution) || h.Address != "" {
			return fmt.Errorf("WSL host requires a distribution only")
		}
	case "ssh":
		if !clean(h.Address) || strings.ContainsAny(h.Address, " \t") || h.Distribution != "" {
			return fmt.Errorf("SSH host requires an address only")
		}
	default:
		return fmt.Errorf("unsupported execution host %q", h.Kind)
	}
	return nil
}

type Selection struct {
	ID           ID       `json:"id"`
	Enabled      bool     `json:"enabled"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type Config struct {
	Schema  int         `json:"schema"`
	Release string      `json:"release"`
	Host    Host        `json:"host"`
	Mode    Mode        `json:"mode"`
	Tools   []Selection `json:"tools"`
}

func (c Config) Validate() error {
	if c.Schema != ConfigSchema || !releasePattern.MatchString(c.Release) {
		return fmt.Errorf("configuration requires schema 1 and an exact 0.2.x release")
	}
	if err := c.Host.Validate(); err != nil {
		return err
	}
	if c.Mode != ProjectHost && c.Mode != Full {
		return fmt.Errorf("invalid configuration mode %q", c.Mode)
	}
	seen := map[ID]bool{}
	for _, s := range c.Tools {
		if !validID(s.ID) || seen[s.ID] {
			return fmt.Errorf("invalid or duplicate selected tool %q", s.ID)
		}
		seen[s.ID] = true
		caps := map[string]bool{}
		for _, cap := range s.Capabilities {
			if !validID(ID(cap)) || caps[cap] {
				return fmt.Errorf("invalid or duplicate capability %q", cap)
			}
			caps[cap] = true
		}
	}
	return nil
}

func ParseConfig(data []byte) (Config, error) {
	var c Config
	if len(data) > MaxManifestBytes {
		return c, fmt.Errorf("configuration exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("configuration must contain exactly one JSON document")
	}
	return c, c.Validate()
}

func (r *Registry) ResolveConfig(target Target, c Config) (Resolution, error) {
	if err := c.Validate(); err != nil {
		return Resolution{}, err
	}
	if target.Mode != c.Mode {
		return Resolution{}, fmt.Errorf("execution target mode differs from configuration")
	}
	selected := make([]ID, 0, len(c.Tools))
	enabled := make([]ID, 0, len(c.Tools))
	for _, selection := range c.Tools {
		selected = append(selected, selection.ID)
		if selection.Enabled {
			enabled = append(enabled, selection.ID)
		}
	}
	resolved, err := r.ResolveInstallation(target, selected)
	if err != nil {
		return Resolution{}, err
	}
	for _, manifest := range resolved.Ordered {
		if manifest.Release != c.Release {
			return Resolution{}, fmt.Errorf("configured release differs from catalog")
		}
	}
	for _, selection := range c.Tools {
		for _, capability := range selection.Capabilities {
			if !slices.Contains(r.modules[selection.ID].Capabilities, capability) {
				return Resolution{}, fmt.Errorf("tool %s does not declare capability %q", selection.ID, capability)
			}
		}
	}
	public, err := r.Resolve(target, enabled)
	if err != nil {
		return Resolution{}, err
	}
	resolved.Bindings = public.Bindings
	return resolved, nil
}

// Layout reserves a distinct 0.2 namespace. Each module owns its managed
// resource subtree; user workspaces, profiles and credentials remain separate.
type Layout struct{ Root string }

func (l Layout) Validate() error {
	if !filepath.IsAbs(l.Root) || filepath.Dir(filepath.Clean(l.Root)) == filepath.Clean(l.Root) {
		return fmt.Errorf("management root must be an absolute dedicated directory")
	}
	return nil
}

func (l Layout) Module(id ID) (string, error) {
	if err := l.Validate(); err != nil {
		return "", err
	}
	if !validID(id) {
		return "", fmt.Errorf("invalid module ID %q", id)
	}
	return filepath.Join(l.Root, "tools", string(id)), nil
}
