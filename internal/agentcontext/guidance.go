package agentcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/policy"
)

const (
	maxGuidanceSources   = 32
	maxGuidanceDepth     = 128
	maxGuidanceFileBytes = 512 << 10
	maxGuidanceTotal     = 2 << 20
)

type GuidanceSource struct {
	Path     string `json:"path"`
	Scope    string `json:"scope"`
	Revision string `json:"revision"`
	Bytes    int    `json:"bytes"`
	Content  string `json:"content"`
}

type GuidanceDiagnostic struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type GuidanceResult struct {
	Target      string               `json:"target"`
	TargetDir   string               `json:"target_dir"`
	Revision    string               `json:"revision"`
	Complete    bool                 `json:"complete"`
	Sources     []GuidanceSource     `json:"sources"`
	Diagnostics []GuidanceDiagnostic `json:"diagnostics"`
	TotalBytes  int                  `json:"total_bytes"`
}

func safeTarget(cwd, target string) (string, error) {
	if target == "" {
		target = "."
	}
	if filepath.IsAbs(target) {
		return "", errors.New("agent guidance target must be relative")
	}
	target, err := policy.Relative(filepath.ToSlash(target))
	if err != nil {
		return "", err
	}
	if cwd == "" || cwd == "." {
		return filepath.ToSlash(target), nil
	}
	cwd, err = policy.Relative(filepath.ToSlash(cwd))
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(cwd, target)), nil
}

func targetDir(paths *policy.Workspace, target string) (string, error) {
	handle, err := paths.Open(target, unix.O_PATH, 0)
	if err == nil {
		defer handle.Close()
		info, statErr := handle.Stat()
		if statErr != nil {
			return "", statErr
		}
		switch {
		case info.IsDir():
			return filepath.ToSlash(filepath.Clean(target)), nil
		case info.Mode().IsRegular():
			return filepath.ToSlash(filepath.Dir(target)), nil
		default:
			return "", errors.New("agent guidance target must be a regular file or directory")
		}
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return filepath.ToSlash(filepath.Dir(target)), nil
}

func guidanceScopes(targetDir string) ([]string, error) {
	targetDir, err := policy.Relative(targetDir)
	if err != nil {
		return nil, err
	}
	scopes := []string{"."}
	if targetDir == "." {
		return scopes, nil
	}
	current := "."
	for _, part := range strings.Split(filepath.ToSlash(targetDir), "/") {
		current = filepath.ToSlash(filepath.Join(current, part))
		scopes = append(scopes, current)
	}
	return scopes, nil
}

func readGuidanceSource(paths *policy.Workspace, scope string) (*GuidanceSource, *GuidanceDiagnostic, error) {
	path := filepath.ToSlash(filepath.Join(scope, "AGENTS.md"))
	handle, err := paths.Open(path, os.O_RDONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, &GuidanceDiagnostic{Path: path, Code: "unreadable", Message: "Cannot read AGENTS.md."}, nil
		}
		return nil, nil, err
	}
	defer handle.Close()

	info, err := handle.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("AGENTS.md must be a regular file")
	}
	if info.Size() > maxGuidanceFileBytes {
		return nil, &GuidanceDiagnostic{Path: path, Code: "oversized", Message: "AGENTS.md exceeds the source size limit."}, nil
	}
	data, err := io.ReadAll(io.LimitReader(handle, maxGuidanceFileBytes+1))
	if err != nil {
		return nil, &GuidanceDiagnostic{Path: path, Code: "unreadable", Message: "Cannot read AGENTS.md."}, nil
	}
	if len(data) > maxGuidanceFileBytes {
		return nil, &GuidanceDiagnostic{Path: path, Code: "oversized", Message: "AGENTS.md exceeds the source size limit."}, nil
	}
	if !utf8.Valid(data) {
		return nil, &GuidanceDiagnostic{Path: path, Code: "invalid_utf8", Message: "AGENTS.md must be valid UTF-8."}, nil
	}
	sum := sha256.Sum256(data)
	return &GuidanceSource{
		Path: path, Scope: filepath.ToSlash(filepath.Clean(scope)),
		Revision: hex.EncodeToString(sum[:]), Bytes: len(data), Content: string(data),
	}, nil, nil
}

func resolveGuidance(paths *policy.Workspace, cwd, target string) (GuidanceResult, error) {
	target, err := safeTarget(cwd, target)
	if err != nil {
		return GuidanceResult{}, err
	}
	if _, err := paths.Resolve(target, false); err != nil {
		return GuidanceResult{}, err
	}
	dir, err := targetDir(paths, target)
	if err != nil {
		return GuidanceResult{}, err
	}
	scopes, err := guidanceScopes(dir)
	if err != nil {
		return GuidanceResult{}, err
	}
	if len(scopes) > maxGuidanceDepth {
		return GuidanceResult{}, errors.New("agent guidance target depth exceeds the limit")
	}

	result := GuidanceResult{
		Target: target, TargetDir: dir, Complete: true,
		Sources: []GuidanceSource{}, Diagnostics: []GuidanceDiagnostic{},
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(dir))
	_, _ = hasher.Write([]byte{0})

	for _, scope := range scopes {
		source, diagnostic, readErr := readGuidanceSource(paths, scope)
		if readErr != nil {
			return GuidanceResult{}, readErr
		}
		if diagnostic != nil {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, *diagnostic)
			continue
		}
		if source == nil {
			continue
		}
		if len(result.Sources) >= maxGuidanceSources {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, GuidanceDiagnostic{
				Path: source.Path, Code: "too_many_sources", Message: "AGENTS.md chain exceeds the source count limit.",
			})
			continue
		}
		if result.TotalBytes+source.Bytes > maxGuidanceTotal {
			result.Complete = false
			result.Diagnostics = append(result.Diagnostics, GuidanceDiagnostic{
				Path: source.Path, Code: "total_oversized", Message: "AGENTS.md chain exceeds the total content limit.",
			})
			continue
		}
		result.TotalBytes += source.Bytes
		result.Sources = append(result.Sources, *source)
		_, _ = hasher.Write([]byte(source.Scope))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(source.Path))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(source.Revision))
		_, _ = hasher.Write([]byte{0})
	}
	result.Revision = hex.EncodeToString(hasher.Sum(nil))
	return result, nil
}
