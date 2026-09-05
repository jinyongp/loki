package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
	"loki/internal/fault"
	"loki/internal/policy"
	"loki/internal/state"
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Revision struct {
	Revision  string `json:"revision"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	CreatedAt string `json:"created_at"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
	Mode      uint32 `json:"mode"`
}

func fmtReplacement(want, actual int) string {
	return fmt.Sprintf("invalid request: expected %d replacements, found %d", want, actual)
}
func (f *Files) revisionDir() (string, error) {
	path := filepath.Join(filepath.Dir(f.Config.AuditLog), "file-revisions")
	return path, os.MkdirAll(path, 0700)
}

func (f *Files) capture(path, operation string, data []byte, mode os.FileMode) (string, error) {
	if len(data) > 64<<20 {
		return "", fault.Error("file exceeds revision backup limit; mutation was not applied")
	}
	relative, err := policy.Relative(path)
	if err != nil {
		return "", err
	}
	digest := Digest(data)
	revision := Digest([]byte(fmt.Sprintf("%s\x00%s\x00%d", relative, digest, time.Now().UnixNano())))
	dir, err := f.revisionDir()
	if err != nil {
		return "", err
	}
	if err = state.AtomicWrite(filepath.Join(dir, revision+".bin"), data, false); err != nil {
		return "", err
	}
	metadata := Revision{revision, relative, operation, time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00"), len(data), digest, uint32(mode.Perm())}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	if err = state.AtomicWrite(filepath.Join(dir, revision+".json"), encoded, false); err != nil {
		return "", err
	}
	if err = f.prune(dir); err != nil {
		return "", err
	}
	return revision, nil
}

func (f *Files) loadRevision(revision, path string) (Revision, []byte, error) {
	var metadata Revision
	if !revisionPattern.MatchString(revision) {
		return metadata, nil, fault.Error("invalid file revision")
	}
	dir, err := f.revisionDir()
	if err != nil {
		return metadata, nil, err
	}
	encoded, err := os.ReadFile(filepath.Join(dir, revision+".json"))
	if err != nil {
		return metadata, nil, err
	}
	if err = json.Unmarshal(encoded, &metadata); err != nil {
		return metadata, nil, err
	}
	if metadata.Path != path {
		return metadata, nil, fault.Error("file revision belongs to a different path")
	}
	info, err := os.Stat(filepath.Join(dir, revision+".bin"))
	if err != nil {
		return metadata, nil, err
	}
	if info.Size() > 64<<20 {
		return metadata, nil, fault.Error("file revision exceeds read limit")
	}
	data, err := os.ReadFile(filepath.Join(dir, revision+".bin"))
	if err != nil {
		return metadata, nil, err
	}
	if Digest(data) != metadata.SHA256 {
		return metadata, nil, fault.Error("file revision integrity check failed")
	}
	return metadata, data, nil
}

func (f *Files) Revisions(path string, limit int) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.Policy.Resolve(path, false); err != nil {
		return nil, err
	}
	relative, _ := policy.Relative(path)
	dir, err := f.revisionDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	records := []Revision{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var r Revision
		if json.Unmarshal(data, &r) == nil && r.Path == relative {
			records = append(records, r)
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].CreatedAt > records[j].CreatedAt })
	records = records[:min(clamp(limit, 1, 100), len(records))]
	return map[string]any{"path": relative, "revisions": records}, nil
}

func (f *Files) RevisionDiff(path, revision string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, _, err := f.read(path, f.Config.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	relative, _ := policy.Relative(path)
	metadata, previous, err := f.loadRevision(revision, relative)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(previous) || !utf8.Valid(current) {
		return map[string]any{"path": path, "revision": revision, "binary": true, "diff": nil}, nil
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: Lines(string(previous)), B: Lines(string(current)), FromFile: path + "@" + revision[:12], ToFile: path, Context: 3})
	if err != nil {
		return nil, err
	}
	truncated := len(diff) > f.Config.MaxOutputBytes
	if truncated {
		diff = strings.ToValidUTF8(diff[:f.Config.MaxOutputBytes], "\uFFFD")
	}
	return map[string]any{"path": path, "revision": revision, "binary": false, "diff": diff, "truncated": truncated, "previous_sha256": metadata.SHA256, "current_sha256": Digest(current)}, nil
}

func (f *Files) Restore(path, revision, expected string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.Policy.Resolve(path, false); err != nil {
		return nil, err
	}
	relative, _ := policy.Relative(path)
	metadata, previous, err := f.loadRevision(revision, relative)
	if err != nil {
		return nil, err
	}
	var undo any
	current, info, err := f.read(path, 64<<20)
	if err == nil {
		if Digest(current) != expected {
			return nil, fault.Error("file changed since it was read; inspect it again before restoring")
		}
		undo, err = f.capture(path, "restore_file_revision", current, info.Mode())
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else if expected != "missing" {
		return nil, fault.Error("restore target is missing; use expected_sha256='missing' after confirming")
	}
	if err = f.Policy.AtomicWrite(path, previous, os.FileMode(metadata.Mode), undo != nil); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "restored_revision": revision, "undo_revision": undo, "sha256": metadata.SHA256, "bytes": metadata.Bytes}, nil
}

func (f *Files) prune(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type item struct {
		name     string
		modified time.Time
		size     int64
	}
	records := []item{}
	var total int64
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if e.Name() == name || !revisionPattern.MatchString(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		var size int64
		if b, err := os.Stat(filepath.Join(dir, name+".bin")); err == nil {
			size = b.Size()
		}
		total += size
		records = append(records, item{name, info.ModTime(), size})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].modified.Before(records[j].modified) })
	for len(records) > 0 && (len(records) > 1000 || total > 512<<20) {
		r := records[0]
		records = records[1:]
		for _, suffix := range []string{".bin", ".json"} {
			if err = os.Remove(filepath.Join(dir, r.name+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		total -= r.size
	}
	return nil
}
