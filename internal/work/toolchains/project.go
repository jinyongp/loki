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

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"
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
	pythonProject, err := findPythonProject(root, cwd)
	if err != nil {
		return nil, err
	}
	rustProject, rustFound, err := findRustProject(root, cwd)
	if err != nil {
		return nil, err
	}
	goProject, goFound, err := findGoProject(root, cwd)
	if err != nil {
		return nil, err
	}
	pnpmSelected := packageFound && strings.HasPrefix(strings.TrimSpace(packageManager), "pnpm@")
	pythonSelected := pythonProject.selectorFound || pythonProject.requirementFound
	if !nodeFound && !pnpmSelected && !pythonSelected && !pythonProject.uvProject && !rustFound && !goFound {
		return nil, nil
	}

	selections := make([]ProjectSelection, 0, 6)
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
	if pythonSelected {
		pythonProvider := PythonProvider{Store: r.Store}
		pythonPlan, resolveErr := pythonProvider.ResolveProject(
			pythonProject.selector, pythonProject.requirement, r.Catalog.Python, false,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if pythonPlan.Resolution.Acquire {
			return nil, fmt.Errorf("Python %s is permitted but not provisioned", pythonPlan.Resolution.Version)
		}
		selections = append(selections, ProjectSelection{
			Family: "python", Version: pythonPlan.Release.Version, GenerationID: pythonPlan.GenerationID,
		})
	}
	if pythonProject.uvProject {
		uvProvider := UVProvider{Store: r.Store}
		uvPlan, resolveErr := uvProvider.Resolve("*", r.Catalog.UV, false)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if uvPlan.Resolution.Acquire {
			return nil, fmt.Errorf("uv %s is permitted but not provisioned", uvPlan.Resolution.Version)
		}
		selections = append(selections, ProjectSelection{
			Family: "uv", Version: uvPlan.Release.Version, GenerationID: uvPlan.GenerationID,
		})
	}
	if rustFound {
		rustProvider := RustProvider{Store: r.Store}
		rustPlan, resolveErr := rustProvider.Resolve(rustProject, r.Catalog.Rust, false)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if rustPlan.Resolution.Acquire {
			return nil, fmt.Errorf("Rust %s is permitted but not provisioned", rustPlan.Resolution.Version)
		}
		selections = append(selections, ProjectSelection{
			Family: "rust", Version: rustPlan.Release.Version, GenerationID: rustPlan.GenerationID,
		})
	}
	if goFound {
		goProvider := GoProvider{Store: r.Store}
		goPlan, resolveErr := goProvider.Resolve(goProject, r.Catalog.Go, false)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if goPlan.Resolution.Acquire {
			return nil, fmt.Errorf("Go %s is permitted but not provisioned", goPlan.Resolution.Version)
		}
		selections = append(selections, ProjectSelection{
			Family: "go", Version: goPlan.Release.Version, GenerationID: goPlan.GenerationID,
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

type pythonProjectRequest struct {
	selector         string
	selectorFound    bool
	requirement      string
	requirementFound bool
	uvProject        bool
}

func findPythonProject(root *os.Root, cwd string) (pythonProjectRequest, error) {
	var result pythonProjectRequest
	for directory := cwd; ; directory = filepath.Dir(directory) {
		selectorPath := filepath.Join(directory, ".python-version")
		rawSelector, exists, err := readProjectFile(root, selectorPath, 4096)
		if err != nil {
			return pythonProjectRequest{}, err
		}
		if exists && !result.selectorFound {
			value := strings.TrimSpace(string(rawSelector))
			if _, err = ParseProjectSelector(value, PythonVersionScheme{}); err != nil {
				return pythonProjectRequest{}, fmt.Errorf("%s: %w", filepath.ToSlash(selectorPath), err)
			}
			result.selector = value
			result.selectorFound = true
		}

		boundary := false
		pyprojectPath := filepath.Join(directory, "pyproject.toml")
		rawProject, projectExists, err := readProjectFile(root, pyprojectPath, 1<<20)
		if err != nil {
			return pythonProjectRequest{}, err
		}
		if projectExists {
			boundary = true
			var document struct {
				Project struct {
					RequiresPython string `toml:"requires-python"`
				} `toml:"project"`
				Tool map[string]any `toml:"tool"`
			}
			if err = toml.Unmarshal(rawProject, &document); err != nil {
				return pythonProjectRequest{}, fmt.Errorf("%s: invalid pyproject.toml: %w", filepath.ToSlash(pyprojectPath), err)
			}
			requirement := strings.TrimSpace(document.Project.RequiresPython)
			if requirement != "" {
				if _, err = ParsePythonRequirement(requirement); err != nil {
					return pythonProjectRequest{}, fmt.Errorf("%s project.requires-python: %w", filepath.ToSlash(pyprojectPath), err)
				}
				result.requirement = requirement
				result.requirementFound = true
			}
			if _, ok := document.Tool["uv"]; ok {
				result.uvProject = true
			}
		}

		lockPath := filepath.Join(directory, "uv.lock")
		_, lockExists, err := readProjectFile(root, lockPath, 8<<20)
		if err != nil {
			return pythonProjectRequest{}, err
		}
		if lockExists {
			result.uvProject = true
			boundary = true
		}
		if boundary || directory == "." {
			break
		}
		directory = filepath.Clean(directory)
		if directory == string(filepath.Separator) {
			break
		}
	}
	return result, nil
}

func findRustProject(root *os.Root, cwd string) (RustProjectRequest, bool, error) {
	for directory := cwd; ; directory = filepath.Dir(directory) {
		for _, candidate := range []struct {
			name        string
			allowLegacy bool
		}{
			{name: "rust-toolchain", allowLegacy: true},
			{name: "rust-toolchain.toml"},
		} {
			path := filepath.Join(directory, candidate.name)
			raw, exists, err := readProjectFile(root, path, 64<<10)
			if err != nil {
				return RustProjectRequest{}, false, err
			}
			if !exists {
				continue
			}
			request, err := parseRustProjectRequest(raw, candidate.allowLegacy)
			if err != nil {
				return RustProjectRequest{}, false, fmt.Errorf("%s: %w", filepath.ToSlash(path), err)
			}
			return request, true, nil
		}
		if directory == "." {
			break
		}
		directory = filepath.Clean(directory)
		if directory == string(filepath.Separator) {
			break
		}
	}
	return RustProjectRequest{}, false, nil
}

func parseRustProjectRequest(raw []byte, allowLegacy bool) (RustProjectRequest, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return RustProjectRequest{}, errors.New("Rust toolchain declaration is empty")
	}
	if allowLegacy && !strings.HasPrefix(trimmed, "[") {
		if strings.ContainsAny(trimmed, " \t\r\n\x00") {
			return RustProjectRequest{}, errors.New("legacy Rust toolchain declaration must contain one channel")
		}
		request := RustProjectRequest{Channel: trimmed}
		if _, err := normalizeRustProjectRequest(request); err != nil {
			return RustProjectRequest{}, err
		}
		return request, nil
	}

	var document struct {
		Toolchain struct {
			Channel    string   `toml:"channel"`
			Path       string   `toml:"path"`
			Profile    string   `toml:"profile"`
			Components []string `toml:"components"`
			Targets    []string `toml:"targets"`
		} `toml:"toolchain"`
	}
	decoder := toml.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return RustProjectRequest{}, fmt.Errorf("invalid Rust toolchain TOML: %w", err)
	}
	if strings.TrimSpace(document.Toolchain.Path) != "" {
		return RustProjectRequest{}, errors.New("workspace Rust toolchain path selection is not allowed")
	}
	request := RustProjectRequest{
		Channel:    strings.TrimSpace(document.Toolchain.Channel),
		Profile:    strings.TrimSpace(document.Toolchain.Profile),
		Components: append([]string(nil), document.Toolchain.Components...),
		Targets:    append([]string(nil), document.Toolchain.Targets...),
	}
	if _, err := normalizeRustProjectRequest(request); err != nil {
		return RustProjectRequest{}, err
	}
	return request, nil
}

func findGoProject(root *os.Root, cwd string) (GoProjectRequest, bool, error) {
	for directory := cwd; ; directory = filepath.Dir(directory) {
		path := filepath.Join(directory, "go.work")
		raw, exists, err := readProjectFile(root, path, 1<<20)
		if err != nil {
			return GoProjectRequest{}, false, err
		}
		if exists {
			file, parseErr := modfile.ParseWork(filepath.ToSlash(path), raw, nil)
			if parseErr != nil {
				return GoProjectRequest{}, false, fmt.Errorf("%s: invalid go.work: %w", filepath.ToSlash(path), parseErr)
			}
			if file.Go == nil {
				return GoProjectRequest{}, false, fmt.Errorf("%s: go.work has no go directive", filepath.ToSlash(path))
			}
			request := GoProjectRequest{Minimum: file.Go.Version}
			if file.Toolchain != nil {
				request.Toolchain = file.Toolchain.Name
			}
			normalized, normalizeErr := normalizeGoProjectRequest(request)
			if normalizeErr != nil {
				return GoProjectRequest{}, false, fmt.Errorf("%s: %w", filepath.ToSlash(path), normalizeErr)
			}
			return GoProjectRequest{Minimum: normalized.Minimum, Toolchain: normalized.Toolchain}, true, nil
		}
		if directory == "." {
			break
		}
		directory = filepath.Clean(directory)
		if directory == string(filepath.Separator) {
			break
		}
	}

	for directory := cwd; ; directory = filepath.Dir(directory) {
		path := filepath.Join(directory, "go.mod")
		raw, exists, err := readProjectFile(root, path, 1<<20)
		if err != nil {
			return GoProjectRequest{}, false, err
		}
		if exists {
			file, parseErr := modfile.Parse(filepath.ToSlash(path), raw, nil)
			if parseErr != nil {
				return GoProjectRequest{}, false, fmt.Errorf("%s: invalid go.mod: %w", filepath.ToSlash(path), parseErr)
			}
			if file.Module == nil {
				return GoProjectRequest{}, false, fmt.Errorf("%s: go.mod has no module directive", filepath.ToSlash(path))
			}
			request := GoProjectRequest{Minimum: "1.16"}
			if file.Go != nil {
				request.Minimum = file.Go.Version
			}
			if file.Toolchain != nil {
				request.Toolchain = file.Toolchain.Name
			}
			normalized, normalizeErr := normalizeGoProjectRequest(request)
			if normalizeErr != nil {
				return GoProjectRequest{}, false, fmt.Errorf("%s: %w", filepath.ToSlash(path), normalizeErr)
			}
			return GoProjectRequest{Minimum: normalized.Minimum, Toolchain: normalized.Toolchain}, true, nil
		}
		if directory == "." {
			break
		}
		directory = filepath.Clean(directory)
		if directory == string(filepath.Separator) {
			break
		}
	}
	return GoProjectRequest{}, false, nil
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
