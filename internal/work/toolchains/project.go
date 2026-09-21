package toolchain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type ProjectSelection struct {
	Family       string
	Version      string
	GenerationID string
}

type ProjectResolver struct {
	Root    string
	Store   GenerationStore
	Catalog Catalog
}

func (r ProjectResolver) Resolve(cwd string) ([]ProjectSelection, error) {
	if !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root || r.Root == string(filepath.Separator) {
		return nil, errors.New("toolchain project root must be a clean absolute non-root path")
	}
	if err := r.Catalog.Validate(); err != nil {
		return nil, err
	}
	cwd, err := normalizeProjectCWD(cwd)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(r.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	nodeSelector, nodeFound, err := findNodeSelector(root, cwd)
	if err != nil {
		return nil, err
	}
	packageManager, packageFound, err := findPackageManager(root, cwd)
	if err != nil {
		return nil, err
	}
	pnpmSelected := packageFound && strings.HasPrefix(strings.TrimSpace(packageManager), "pnpm@")
	if !nodeFound && !pnpmSelected {
		return nil, nil
	}

	selections := make([]ProjectSelection, 0, 2)
	if nodeFound {
		nodeProvider := NodeProvider{Store: r.Store}
		nodePlan, resolveErr := nodeProvider.Resolve(nodeSelector, r.Catalog.Node, false)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if nodePlan.Resolution.Acquire {
			return nil, fmt.Errorf("Node.js %s is permitted but not provisioned", nodePlan.Resolution.Version)
		}
		selections = append(selections, ProjectSelection{
			Family: "node", Version: nodePlan.Release.Version, GenerationID: nodePlan.GenerationID,
		})
	}
	if pnpmSelected {
		pnpmProvider := PnpmProvider{Store: r.Store}
		pnpmPlan, resolveErr := pnpmProvider.Resolve(packageManager, r.Catalog.Pnpm, false)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if pnpmPlan.Resolution.Acquire {
			return nil, fmt.Errorf("pnpm %s is permitted but not provisioned", pnpmPlan.Resolution.Version)
		}
		selections = append(selections, ProjectSelection{
			Family: "pnpm", Version: pnpmPlan.Release.Version, GenerationID: pnpmPlan.GenerationID,
		})
	}
	return selections, nil
}

func normalizeProjectCWD(cwd string) (string, error) {
	if cwd == "" {
		cwd = "."
	}
	if strings.ContainsRune(cwd, 0) || filepath.IsAbs(cwd) {
		return "", errors.New("toolchain project cwd must be workspace-relative")
	}
	clean := filepath.Clean(filepath.FromSlash(cwd))
	if clean != filepath.FromSlash(cwd) || !filepath.IsLocal(clean) {
		return "", errors.New("toolchain project cwd must be a clean workspace-relative path")
	}
	return clean, nil
}

func findNodeSelector(root *os.Root, cwd string) (string, bool, error) {
	scheme := NodeVersionScheme{}
	for directory := cwd; ; directory = filepath.Dir(directory) {
		var selected string
		var normalized Selector
		found := false
		for _, name := range []string{".node-version", ".nvmrc"} {
			path := filepath.Join(directory, name)
			raw, exists, err := readProjectFile(root, path, 4096)
			if err != nil {
				return "", false, err
			}
			if !exists {
				continue
			}
			value := strings.TrimSpace(string(raw))
			selector, err := ParseProjectSelector(value, scheme)
			if err != nil {
				return "", false, fmt.Errorf("%s: %w", filepath.ToSlash(path), err)
			}
			if found && selector != normalized {
				return "", false, fmt.Errorf("conflicting Node.js selectors in %s", filepath.ToSlash(directory))
			}
			selected = value
			normalized = selector
			found = true
		}
		if found {
			return selected, true, nil
		}
		if directory == "." {
			break
		}
		directory = filepath.Clean(directory)
		if directory == string(filepath.Separator) {
			break
		}
	}
	return "", false, nil
}

func findPackageManager(root *os.Root, cwd string) (string, bool, error) {
	for directory := cwd; ; directory = filepath.Dir(directory) {
		path := filepath.Join(directory, "package.json")
		raw, exists, err := readProjectFile(root, path, 1<<20)
		if err != nil {
			return "", false, err
		}
		if exists {
			var document struct {
				PackageManager string `json:"packageManager"`
			}
			decoder := jsonNewDecoder(raw)
			if err = decoder.Decode(&document); err != nil {
				return "", false, fmt.Errorf("%s: invalid package.json: %w", filepath.ToSlash(path), err)
			}
			var trailing any
			if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				return "", false, fmt.Errorf("%s: package.json contains trailing data", filepath.ToSlash(path))
			}
			if strings.TrimSpace(document.PackageManager) != "" {
				return strings.TrimSpace(document.PackageManager), true, nil
			}
		}
		if directory == "." {
			break
		}
		directory = filepath.Clean(directory)
		if directory == string(filepath.Separator) {
			break
		}
	}
	return "", false, nil
}

func readProjectFile(root *os.Root, path string, limit int64) ([]byte, bool, error) {
	file, err := root.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, false, fmt.Errorf("project declaration %s is not a bounded regular file", filepath.ToSlash(path))
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(raw)) > limit {
		return nil, false, fmt.Errorf("project declaration %s exceeds its size limit", filepath.ToSlash(path))
	}
	return raw, true, nil
}

func jsonNewDecoder(raw []byte) *json.Decoder {
	return json.NewDecoder(bytes.NewReader(raw))
}
