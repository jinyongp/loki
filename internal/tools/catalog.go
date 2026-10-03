package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

type Catalog struct {
	Schema    int        `json:"schema"`
	Release   string     `json:"release"`
	Modules   []Manifest `json:"modules"`
	Artifacts []Artifact `json:"artifacts"`
}

func ParseCatalog(data []byte) (Catalog, error) {
	var c Catalog
	if len(data) > MaxManifestBytes {
		return c, fmt.Errorf("release catalog exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("release catalog must be one document")
	}
	return c, c.Validate()
}

func (c Catalog) Validate() error {
	if c.Schema != 1 || !releasePattern.MatchString(c.Release) {
		return fmt.Errorf("invalid release catalog schema or release")
	}
	if _, err := NewRegistry(c.Modules); err != nil {
		return err
	}
	manifests := map[ID]Manifest{}
	for _, m := range c.Modules {
		if m.Release != c.Release {
			return fmt.Errorf("catalog mixes releases")
		}
		manifests[m.ID] = m
	}
	seen := map[string]bool{}
	for _, a := range c.Artifacts {
		if err := a.Validate(); err != nil {
			return err
		}
		m, exists := manifests[a.Module]
		if !exists || a.Release != c.Release || !slices.Contains(m.Targets, a.Target) {
			return fmt.Errorf("catalog artifact has no matching module support")
		}
		key := fmt.Sprintf("%s/%s/%s/%s", a.Module, a.Target.OS, a.Target.Arch, a.Target.Mode)
		if seen[key] {
			return fmt.Errorf("duplicate catalog artifact %s", key)
		}
		seen[key] = true
	}
	return nil
}
