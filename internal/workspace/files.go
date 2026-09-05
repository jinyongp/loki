// Package workspace implements confined file reads, edits, and recoverable history.
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/fault"
	"loki/internal/policy"
)

type Files struct {
	Config          config.Config
	Policy          *policy.Workspace
	mu              sync.Mutex
	GitPath, RGPath string
}

func New(c config.Config) (*Files, error) {
	p, err := policy.New(c.Root)
	if err != nil {
		return nil, err
	}
	return &Files{Config: c, Policy: p, GitPath: "/usr/bin/git", RGPath: "/usr/bin/rg"}, nil
}
func (f *Files) Close() error                 { return f.Policy.Close() }
func Digest(data []byte) string               { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func clamp(value, minValue, maxValue int) int { return min(max(value, minValue), maxValue) }

func (f *Files) read(path string, maximum int) ([]byte, os.FileInfo, error) {
	file, err := f.Policy.Open(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fault.Error("path must be a regular file")
	}
	if info.Size() > int64(maximum) {
		return nil, nil, fault.Error("file exceeds read limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maximum {
		return nil, nil, fault.Error("file exceeds read limit")
	}
	return data, info, nil
}

// Lines follows Python str.splitlines(keepends=True), including Unicode breaks.
func Lines(s string) []string {
	result := []string{}
	start := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r == '\r' && i < len(s) && s[i] == '\n' {
			i++
		}
		if r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == '\x1c' || r == '\x1d' || r == '\x1e' || r == '\u0085' || r == '\u2028' || r == '\u2029' {
			result = append(result, s[start:i])
			start = i
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}

func (f *Files) Read(path string, offset, limit int) (map[string]any, error) {
	data, _, err := f.read(path, f.Config.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fault.Error("binary files are not supported")
	}
	if !utf8.Valid(data) {
		return nil, fault.Error("file is not valid UTF-8 text")
	}
	start := max(0, offset)
	limit = clamp(limit, 1, f.Config.MaxReadLines)
	all := Lines(string(data))
	lines := []string{}
	size := 0
	eof := true
	for _, line := range all[min(start, len(all)):] {
		if len(lines) >= limit {
			eof = false
			break
		}
		if size+len(line) > f.Config.MaxOutputBytes {
			if len(lines) == 0 {
				return nil, fault.Error("a single line exceeds the output limit")
			}
			eof = false
			break
		}
		lines = append(lines, line)
		size += len(line)
	}
	return map[string]any{"path": path, "offset": start, "next_offset": start + len(lines), "start_line": start + 1, "end_line": start + len(lines), "content": strings.Join(lines, ""), "eof": eof, "size": len(data), "sha256": Digest(data)}, nil
}

func (f *Files) List(ctx context.Context, path string, maxDepth, offset, limit int) (map[string]any, error) {
	relative, err := policy.Relative(path)
	if err != nil {
		return nil, err
	}
	root, err := f.Policy.Open(relative, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat()
	root.Close()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fault.Error("path must be a directory")
	}
	depthLimit := clamp(maxDepth, 1, 10)
	start := max(offset, 0)
	pageSize := clamp(limit, 1, f.Config.MaxListEntries)
	entries := []map[string]any{}
	seen := 0
	more := false
	var walk func(string, int) error
	walk = func(current string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := f.Policy.Open(current, os.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		children, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return err
		}
		type entry struct {
			path string
			info os.FileInfo
		}
		directories := []entry{}
		files := []entry{}
		for _, child := range children {
			path := filepath.Join(current, child.Name())
			item, err := f.Policy.Open(path, unix.O_PATH, 0)
			if err != nil {
				continue
			}
			info, err := item.Stat()
			item.Close()
			if err != nil {
				continue
			}
			e := entry{path, info}
			if info.IsDir() {
				directories = append(directories, e)
			} else {
				files = append(files, e)
			}
		}
		sort.Slice(directories, func(i, j int) bool { return directories[i].path < directories[j].path })
		sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
		for _, e := range append(append([]entry{}, directories...), files...) {
			if seen < start {
				seen++
				continue
			}
			if len(entries) >= pageSize {
				more = true
				return nil
			}
			kind := "file"
			if e.info.IsDir() {
				kind = "directory"
			}
			item := map[string]any{"path": e.path, "type": kind}
			if kind == "file" {
				item["size"] = e.info.Size()
			}
			entries = append(entries, item)
			seen++
		}
		if depth+1 < depthLimit {
			for _, d := range directories {
				if err := walk(d.path, depth+1); err != nil && !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if more {
					return nil
				}
			}
		}
		return nil
	}
	if err = walk(relative, 0); err != nil {
		return nil, err
	}
	return map[string]any{"entries": entries, "offset": start, "next_offset": start + len(entries), "has_more": more}, nil
}

func (f *Files) Create(path, content string) (map[string]any, error) {
	if len(content) > f.Config.MaxWriteBytes {
		return nil, fault.Error("content exceeds write limit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.Policy.AtomicWrite(path, []byte(content), 0600, false); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "bytes": len(content)}, nil
}

func (f *Files) Replace(path, old, new, expected string, count int) (map[string]any, error) {
	if old == "" {
		return nil, fault.Error("invalid request: old text must not be empty")
	}
	if count < 1 {
		return nil, fault.Error("invalid request: expected_replacements must be positive")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, info, err := f.read(path, f.Config.MaxWriteBytes)
	if err != nil {
		return nil, err
	}
	if Digest(data) != expected {
		return nil, fault.Error("file changed since it was read; read it again before editing")
	}
	if !utf8.Valid(data) {
		return nil, fault.Error("content encoding is invalid; use valid UTF-8 text or a supported binary tool")
	}
	actual := strings.Count(string(data), old)
	if actual != count {
		return nil, fault.Error(fmtReplacement(count, actual))
	}
	updated := []byte(strings.ReplaceAll(string(data), old, new))
	if len(updated) > f.Config.MaxWriteBytes {
		return nil, fault.Error("result exceeds write limit")
	}
	revision, err := f.capture(path, "replace_text", data, info.Mode())
	if err != nil {
		return nil, err
	}
	if err = f.Policy.AtomicWrite(path, updated, info.Mode(), true); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "replacements": actual, "previous_revision": revision, "sha256": Digest(updated)}, nil
}

func (f *Files) Move(source, destination string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, info, err := f.read(source, 64<<20)
	if err != nil {
		return nil, err
	}
	if _, err = f.Policy.Resolve(destination, false); err != nil {
		return nil, err
	}
	if dest, err := f.Policy.Open(destination, unix.O_PATH, 0); err == nil {
		dest.Close()
		return nil, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	revision, err := f.capture(source, "move_path", data, info.Mode())
	if err != nil {
		return nil, err
	}
	if err = f.Policy.Move(source, destination); err != nil {
		return nil, err
	}
	return map[string]any{"source": source, "destination": destination, "previous_revision": revision}, nil
}
