package management

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"loki/internal/tools"
)

// The frontend records intent before registration. A same-name distribution
// alone never establishes ownership; the execution host must match this token.
type HostPreparation struct {
	Schema       int    `json:"schema"`
	Distribution string `json:"distribution"`
	Token        string `json:"token"`
	Phase        string `json:"phase"`
}

func (p HostPreparation) Validate() error {
	if p.Schema != 1 || p.Distribution != "loki-tools" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.Token) || (p.Phase != "reserved" && p.Phase != "registered" && p.Phase != "ready") {
		return fmt.Errorf("invalid managed host preparation record")
	}
	return nil
}

func (s Store) HostPreparation() (*HostPreparation, error) {
	if err := s.realRoot(); err != nil {
		return nil, err
	}
	if err := s.realControl(); err != nil {
		return nil, err
	}
	var p HostPreparation
	err := readOwnedJSON(filepath.Join(s.ControlDirectory(), "host-preparation.json"), tools.MaxManifestBytes, &p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, p.Validate()
}

func (s Store) SaveHostPreparation(p HostPreparation) error {
	if err := p.Validate(); err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.realControl(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.ControlDirectory(), 0700); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(s.ControlDirectory(), "host-preparation.json"), p)
}
