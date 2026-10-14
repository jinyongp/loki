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
	if p.Schema != 1 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(p.Distribution) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(p.Token) || (p.Phase != "reserved" && p.Phase != "registered" && p.Phase != "ready") {
		return fmt.Errorf("invalid managed host preparation record")
	}
	return nil
}

func (s Store) HostPreparations() (map[string]HostPreparation, error) {
	if err := s.realRoot(); err != nil {
		return nil, err
	}
	if err := s.realControl(); err != nil {
		return nil, err
	}
	records := map[string]HostPreparation{}
	err := readOwnedJSON(filepath.Join(s.ControlDirectory(), "host-preparations.json"), tools.MaxManifestBytes, &records)
	if err == nil {
		if records == nil || len(records) > 128 {
			return nil, fmt.Errorf("invalid host preparation registry")
		}
		for name, p := range records {
			if name != p.Distribution {
				return nil, fmt.Errorf("host preparation key differs")
			}
			if err := p.Validate(); err != nil {
				return nil, err
			}
		}
		return records, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var p HostPreparation
	err = readOwnedJSON(filepath.Join(s.ControlDirectory(), "host-preparation.json"), tools.MaxManifestBytes, &p)
	if errors.Is(err, os.ErrNotExist) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if err = p.Validate(); err != nil {
		return nil, err
	}
	records[p.Distribution] = p
	return records, nil
}
func (s Store) HostPreparation() (*HostPreparation, error) { return s.HostPreparationFor("loki-tools") }
func (s Store) HostPreparationFor(distribution string) (*HostPreparation, error) {
	records, err := s.HostPreparations()
	if err != nil {
		return nil, err
	}
	p, ok := records[distribution]
	if !ok {
		return nil, nil
	}
	return &p, nil
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
	records, err := s.HostPreparations()
	if err != nil {
		return err
	}
	records[p.Distribution] = p
	if len(records) > 128 {
		return fmt.Errorf("host preparation registry is full")
	}
	if err := os.MkdirAll(s.ControlDirectory(), 0700); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(s.ControlDirectory(), "host-preparations.json"), records)
}
func (s Store) ForgetHostPreparation(distribution string) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	records, err := s.HostPreparations()
	if err != nil {
		return err
	}
	delete(records, distribution)
	if err := os.MkdirAll(s.ControlDirectory(), 0700); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(s.ControlDirectory(), "host-preparations.json"), records)
}
