package management

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"loki/internal/tools"
)

func (s Store) IntegrationMetadata(module tools.ID) ([]byte, error) {
	if module != "git" {
		return nil, fmt.Errorf("unknown public integration metadata owner")
	}
	if err := s.realRoot(); err != nil {
		return nil, err
	}
	parent := filepath.Join(s.Root, "integrations")
	directory := filepath.Join(parent, string(module))
	for _, path := range []string{parent, directory} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err := realDirectories(path); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(directory, "public.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > tools.MaxManifestBytes {
		return nil, fmt.Errorf("integration metadata must be a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if len(data) > tools.MaxManifestBytes {
		return nil, fmt.Errorf("integration metadata exceeds its bound")
	}
	return data, err
}

func (s Store) SaveIntegrationMetadata(module tools.ID, data []byte) error {
	if module != "git" || len(data) == 0 || len(data) > tools.MaxManifestBytes {
		return fmt.Errorf("invalid integration metadata owner or bound")
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.RequireMutable(); err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(s.Root, "integrations"), filepath.Join(s.Root, "integrations", string(module))} {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := realDirectories(path); err != nil {
			return err
		}
	}
	return atomicPublicBytes(filepath.Join(s.Root, "integrations", string(module), "public.json"), data)
}
