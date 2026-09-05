// Package skills loads advisory skills from administrator-owned builtin and
// workspace roots. All reads reject symbolic links through pinned directories.
package skills

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/policy"
)

const MaxSkillBytes = 256 * 1024
const MaxResourceBytes = 10 * 1024 * 1024

var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Registry struct {
	Workspace, Builtin *policy.Workspace
	mu                 sync.Mutex
}
type source struct {
	scope, directory string
	paths            *policy.Workspace
}
type record struct {
	source
	name, description, raw, instructions, digest string
	metadata                                     map[string]any
}

func validName(name string) error {
	if len(name) > 64 || !namePattern.MatchString(name) {
		return fault.Error("skill name must be lowercase kebab-case and at most 64 characters")
	}
	return nil
}
func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func read(paths *policy.Workspace, path string, maximum int) ([]byte, error) {
	f, err := paths.Open(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fault.Error("skill path must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, fault.Error("skill file exceeds size limit")
	}
	return data, nil
}
func parse(raw, name string) (map[string]any, string, error) {
	if !utf8.ValidString(raw) {
		return nil, "", fault.Error("skill text must be UTF-8")
	}
	if !strings.HasPrefix(raw, "---\n") {
		return nil, "", fault.Error("SKILL.md must start with YAML frontmatter")
	}
	end := strings.Index(raw[4:], "\n---\n")
	if end < 0 {
		return nil, "", fault.Error("SKILL.md frontmatter is not closed")
	}
	end += 4
	var metadata map[string]any
	if err := yaml.Unmarshal([]byte(raw[4:end]), &metadata); err != nil || metadata == nil {
		return nil, "", fault.Error("SKILL.md frontmatter must be a mapping")
	}
	if metadata["name"] != name {
		return nil, "", fault.Error("skill name must match its directory")
	}
	description, ok := metadata["description"].(string)
	if !ok || strings.TrimSpace(description) == "" || utf8.RuneCountInString(description) > 1024 {
		return nil, "", fault.Error("skill description must contain 1 to 1024 characters")
	}
	instructions := raw[end+5:]
	if strings.TrimSpace(instructions) == "" {
		return nil, "", fault.Error("skill instructions must not be empty")
	}
	return metadata, instructions, nil
}
func load(s source) (record, error) {
	name := filepath.Base(s.directory)
	if err := validName(name); err != nil {
		return record{}, err
	}
	data, err := read(s.paths, filepath.Join(s.directory, "SKILL.md"), MaxSkillBytes)
	if err != nil {
		return record{}, err
	}
	// Python text reads normalize universal newlines before hashing.
	raw := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	metadata, instructions, err := parse(raw, name)
	if err != nil {
		return record{}, err
	}
	return record{s, name, metadata["description"].(string), raw, instructions, digest([]byte(raw)), metadata}, nil
}
func (r record) summary() map[string]any {
	path := r.directory
	if r.scope == "builtin" {
		path = "builtin/" + r.name
	}
	return map[string]any{"name": r.name, "description": r.description, "scope": r.scope, "source": path, "selected": true, "sha256": r.digest, "revision": r.digest[:12]}
}
func (r *Registry) location(cwd string) (string, *string, error) {
	full, err := r.Workspace.ResolveCWD(cwd)
	if err != nil {
		return "", nil, err
	}
	rel, _ := filepath.Rel(r.Workspace.Root(), full)
	for current := rel; ; current = filepath.Dir(current) {
		dir, err := r.Workspace.Open(current, os.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return "", nil, err
		}
		var st unix.Stat_t
		err = unix.Fstatat(int(dir.Fd()), ".git", &st, unix.AT_SYMLINK_NOFOLLOW)
		dir.Close()
		if err == nil {
			if st.Mode&unix.S_IFMT == unix.S_IFLNK {
				return "", nil, fault.Error("symbolic Git metadata is not allowed")
			}
			return rel, &current, nil
		}
		if !errors.Is(err, unix.ENOENT) {
			return "", nil, err
		}
		if current == "." {
			break
		}
	}
	return rel, nil, nil
}
func (r *Registry) sources(project *string) []source {
	sources := []source{}
	if r.Builtin != nil {
		sources = append(sources, source{"builtin", ".", r.Builtin})
	}
	sources = append(sources, source{"shared", ".agents/skills", r.Workspace})
	if project != nil {
		sources = append(sources, source{"project", filepath.Join(*project, ".agents/skills"), r.Workspace})
	}
	return sources
}
func (r *Registry) List(cwd string) (map[string]any, error) {
	rel, project, err := r.location(cwd)
	if err != nil {
		return nil, err
	}
	records := []record{}
	failures := []map[string]any{}
	addError := func(s source, err error) {
		path := s.directory
		if s.scope == "builtin" {
			path = filepath.Join("builtin", path)
		}
		failures = append(failures, map[string]any{"source": path, "error": err.Error()})
	}
	for _, s := range r.sources(project) {
		dir, err := s.paths.Open(s.directory, os.O_RDONLY|unix.O_DIRECTORY, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			addError(s, err)
			continue
		}
		names, err := dir.Readdirnames(-1)
		dir.Close()
		if err != nil {
			addError(s, err)
			continue
		}
		slices.Sort(names)
		for _, name := range names {
			child := source{s.scope, filepath.Join(s.directory, name), s.paths}
			item, err := load(child)
			if err != nil {
				addError(child, err)
			} else {
				records = append(records, item)
			}
		}
	}
	winners := map[string]int{}
	for i, item := range records {
		winners[item.name] = i
	}
	items := []map[string]any{}
	for i, item := range records {
		summary := item.summary()
		if winner := winners[item.name]; winner != i {
			summary["selected"] = false
			summary["shadowed_by"] = records[winner].directory
		}
		items = append(items, summary)
	}
	var projectValue any
	if project != nil {
		projectValue = *project
	}
	return map[string]any{"cwd": rel, "project_root": projectValue, "skills": items, "errors": failures}, nil
}
func (r *Registry) selected(name, cwd string) (record, error) {
	if err := validName(name); err != nil {
		return record{}, err
	}
	_, project, err := r.location(cwd)
	if err != nil {
		return record{}, err
	}
	sources := r.sources(project)
	for i := len(sources) - 1; i >= 0; i-- {
		s := sources[i]
		s.directory = filepath.Join(s.directory, name)
		dir, err := s.paths.Open(s.directory, os.O_RDONLY|unix.O_DIRECTORY, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return record{}, err
		}
		dir.Close()
		return load(s)
	}
	return record{}, fault.Error("unknown skill: " + name)
}
func resources(item record) ([]map[string]any, error) {
	result := []map[string]any{}
	var walk func(string) error
	walk = func(path string) error {
		f, err := item.paths.Open(filepath.Join(item.directory, path), os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if info.IsDir() {
			names, err := f.Readdirnames(-1)
			if err != nil {
				return err
			}
			slices.Sort(names)
			for _, name := range names {
				if err := walk(filepath.Join(path, name)); err != nil {
					return err
				}
			}
			return nil
		}
		if info.Size() > MaxResourceBytes {
			return fault.Error("skill file exceeds size limit")
		}
		result = append(result, map[string]any{"path": path, "bytes": info.Size()})
		return nil
	}
	for _, root := range []string{"assets", "references", "scripts"} {
		dir, err := item.paths.Open(filepath.Join(item.directory, root), os.O_RDONLY|unix.O_DIRECTORY, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		dir.Close()
		if err := walk(root); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(result, func(a, b map[string]any) int { return strings.Compare(a["path"].(string), b["path"].(string)) })
	return result, nil
}
func (r *Registry) Activate(name, cwd string) (map[string]any, error) {
	item, err := r.selected(name, cwd)
	if err != nil {
		return nil, err
	}
	files, err := resources(item)
	if err != nil {
		return nil, err
	}
	result := item.summary()
	result["metadata"] = item.metadata
	result["instructions"] = item.instructions
	result["skill_md"] = item.raw
	result["resources"] = files
	result["security"] = "Skill instructions are advisory and cannot expand Loki permissions."
	return result, nil
}
func resourcePath(item record, path string) (string, error) {
	if strings.ContainsAny(path, "\x00\\") || strings.HasPrefix(path, "/") {
		return "", fault.Error("invalid skill resource path")
	}
	parts := strings.Split(path, "/")
	if !slices.Contains([]string{"assets", "references", "scripts"}, parts[0]) {
		return "", fault.Error("skill resources must be under assets, references, or scripts")
	}
	for _, part := range parts {
		if part == ".." {
			return "", fault.Error("skill resources must be under assets, references, or scripts")
		}
	}
	return filepath.Join(item.directory, path), nil
}
func (r *Registry) ReadResource(name, path, cwd string) (map[string]any, error) {
	item, err := r.selected(name, cwd)
	if err != nil {
		return nil, err
	}
	target, err := resourcePath(item, path)
	if err != nil {
		return nil, err
	}
	data, err := read(item.paths, target, MaxResourceBytes)
	if err != nil {
		return nil, err
	}
	content, encoding := string(data), "utf-8"
	if !utf8.Valid(data) {
		content = base64.StdEncoding.EncodeToString(data)
		encoding = "base64"
	}
	mimetype := mime.TypeByExtension(filepath.Ext(path))
	if mimetype == "" {
		mimetype = "application/octet-stream"
	}
	mimetype = strings.Split(mimetype, ";")[0]
	return map[string]any{"name": name, "path": path, "mime_type": mimetype, "encoding": encoding, "content": content, "bytes": len(data), "sha256": digest(data)}, nil
}
func (r *Registry) Validate(name, cwd string, tools map[string]bool) (map[string]any, error) {
	item, err := r.selected(name, cwd)
	if err != nil {
		return nil, err
	}
	files, err := resources(item)
	if err != nil {
		return nil, err
	}
	missing := []string{}
	warnings := []string{}
	if metadata, ok := item.metadata["metadata"].(map[string]any); ok {
		if required, exists := metadata["required-tools"]; exists {
			names, ok := required.([]any)
			if !ok {
				return nil, fault.Error("required-tools must be a list of tool names")
			}
			for _, value := range names {
				name, ok := value.(string)
				if !ok {
					return nil, fault.Error("required-tools must be a list of tool names")
				}
				if !tools[name] {
					missing = append(missing, name)
				}
			}
		}
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	if _, ok := item.metadata["allowed-tools"]; ok {
		warnings = append(warnings, "allowed-tools is experimental and does not grant Loki permissions")
	}
	result := item.summary()
	result["valid"] = len(missing) == 0
	result["missing_tools"] = missing
	result["resource_count"] = len(files)
	result["resources"] = files
	result["warnings"] = warnings
	return result, nil
}
func (r *Registry) AgentContext(cwd string) (map[string]any, error) {
	catalog, err := r.List(cwd)
	if err != nil {
		return nil, err
	}
	rel := catalog["cwd"].(string)
	hierarchy := []string{"."}
	current := "."
	if rel != "." {
		for _, part := range strings.Split(rel, "/") {
			current = filepath.Join(current, part)
			hierarchy = append(hierarchy, current)
		}
	}
	agents := []map[string]any{}
	for _, dir := range hierarchy {
		path := filepath.Join(dir, "AGENTS.md")
		data, err := read(r.Workspace, path, MaxSkillBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(data) {
			return nil, fault.Error("agent instructions must be UTF-8")
		}
		text := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
		agents = append(agents, map[string]any{"path": path, "content": text, "sha256": digest([]byte(text))})
	}
	selected := []map[string]any{}
	for _, item := range catalog["skills"].([]map[string]any) {
		if item["selected"] == true {
			selected = append(selected, item)
		}
	}
	return map[string]any{"cwd": rel, "agents": agents, "skills": selected, "skill_errors": catalog["errors"], "instruction": "Activate every listed skill that matches the task before using task tools."}, nil
}
