package management

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"loki/internal/tools"
)

// Public provider configuration is separate from the provider's encrypted
// service data. Only the finite supported provider name can choose a path.
func (s Store) ProviderConfiguration(provider tools.ID) ([]byte, error) {
	if provider != "github" {
		return nil, fmt.Errorf("unknown public provider configuration")
	}
	if err := s.realRoot(); err != nil {
		return nil, err
	}
	parent := filepath.Join(s.Root, "providers")
	directory := filepath.Join(parent, string(provider))
	for _, path := range []string{parent, directory} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err := realDirectories(path); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(directory, "config.toml")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > tools.MaxManifestBytes {
		return nil, fmt.Errorf("public provider configuration must be a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if len(data) > tools.MaxManifestBytes {
		return nil, fmt.Errorf("public provider configuration exceeds its bound")
	}
	return data, err
}

func (s Store) SaveProviderConfiguration(provider tools.ID, data []byte) error {
	if provider != "github" || len(data) == 0 || len(data) > tools.MaxManifestBytes {
		return fmt.Errorf("invalid public provider configuration")
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.RequireMutable(); err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(s.Root, "providers"), filepath.Join(s.Root, "providers", string(provider))} {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := realDirectories(path); err != nil {
			return err
		}
	}
	return atomicPublicBytes(filepath.Join(s.Root, "providers", string(provider), "config.toml"), data)
}

func atomicPublicBytes(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("public provider configuration is not regular")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
