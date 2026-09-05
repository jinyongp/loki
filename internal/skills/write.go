package skills

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/policy"
	"loki/internal/process"
)

func document(name, description, instructions string, metadata map[string]any) ([]byte, error) {
	if strings.TrimSpace(description) == "" || utf8.RuneCountInString(description) > 1024 {
		return nil, fault.Error("description must contain 1 to 1024 characters")
	}
	if strings.TrimSpace(instructions) == "" {
		return nil, fault.Error("instructions must not be empty")
	}
	front := yaml.Node{Kind: yaml.MappingNode}
	add := func(key string, value any) error {
		var node yaml.Node
		if err := node.Encode(value); err != nil {
			return err
		}
		front.Content = append(front.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &node)
		return nil
	}
	if err := add("name", name); err != nil {
		return nil, err
	}
	if err := add("description", strings.TrimSpace(description)); err != nil {
		return nil, err
	}
	keys := []string{}
	for key := range metadata {
		if key == "name" || key == "description" {
			return nil, fault.Error("metadata may not override " + key)
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := add(key, metadata[key]); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&front); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	raw := "---\n" + strings.TrimRight(buf.String(), "\n") + "\n---\n\n" + strings.TrimRight(instructions, " \t\r\n") + "\n"
	if len(raw) > MaxSkillBytes {
		return nil, fault.Error("SKILL.md exceeds size limit")
	}
	if _, _, err := parse(raw, name); err != nil {
		return nil, err
	}
	return []byte(raw), nil
}

func (r *Registry) Create(scope, cwd, name, description, instructions string, metadata map[string]any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validName(name); err != nil {
		return nil, err
	}
	if scope != "shared" && scope != "project" {
		return nil, fault.Error("scope must be shared or project")
	}
	_, project, err := r.location(cwd)
	if err != nil {
		return nil, err
	}
	root := ".agents/skills"
	if scope == "project" {
		if project == nil {
			return nil, fault.Error("project scope requires a Git repository")
		}
		root = filepath.Join(*project, root)
	}
	data, err := document(name, description, instructions, metadata)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(root, name)
	parent, base, err := r.Workspace.Parent(directory, true)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := unix.Mkdirat(int(parent.Fd()), base, 0700); err != nil {
		return nil, err
	}
	if err := r.Workspace.AtomicWrite(filepath.Join(directory, "SKILL.md"), data, 0600, false); err != nil {
		return nil, err
	}
	item, err := load(source{scope, directory, r.Workspace})
	if err != nil {
		return nil, err
	}
	return item.summary(), nil
}

func (r *Registry) WriteResource(name, path, content, cwd string, overwrite bool, expected *string, encoding string) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, err := r.selected(name, cwd)
	if err != nil {
		return nil, err
	}
	if item.scope == "builtin" {
		return nil, fault.Error("built-in skill resources are read-only; create an override first")
	}
	target, err := resourcePath(item, path)
	if err != nil {
		return nil, err
	}
	data := []byte(content)
	switch encoding {
	case "utf-8":
		if !utf8.ValidString(content) {
			return nil, fault.Error("resource text must be UTF-8")
		}
	case "base64":
		if strings.ContainsAny(content, "\r\n \t") {
			return nil, fault.Error("invalid base64 resource content")
		}
		data, err = base64.StdEncoding.Strict().DecodeString(content)
		if err != nil {
			return nil, fault.Error("invalid base64 resource content")
		}
	default:
		return nil, fault.Error("encoding must be utf-8 or base64")
	}
	if len(data) > MaxResourceBytes {
		return nil, fault.Error("skill resource exceeds size limit")
	}
	current, err := read(item.paths, target, MaxResourceBytes)
	exists := err == nil
	if exists {
		if !overwrite {
			return nil, os.ErrExist
		}
		if expected != nil && digest(current) != *expected {
			return nil, fault.Error("skill resource changed since it was read")
		}
	} else {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if expected != nil {
			return nil, fault.Error("expected_sha256 cannot be used for a new resource")
		}
	}
	if err := item.paths.AtomicWrite(target, data, 0600, exists); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "path": path, "bytes": len(data), "sha256": digest(data), "encoding": encoding}, nil
}

func (r *Registry) Edit(ctx context.Context, name, cwd, patch, expected string) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, err := r.selected(name, cwd)
	if err != nil {
		return nil, err
	}
	if item.scope == "builtin" {
		return nil, fault.Error("built-in skills are read-only; create a shared or project override first")
	}
	if item.digest != expected {
		return nil, fault.Error("skill changed since activation; activate it again before editing")
	}
	if len(patch) == 0 || len(patch) > MaxSkillBytes {
		return nil, fault.Error("patch is empty or exceeds the skill patch limit")
	}
	for _, denied := range []string{"\x00", "GIT binary patch", "new file mode", "deleted file mode", "old mode", "new mode", "rename from", "copy from"} {
		if strings.Contains(patch, denied) {
			return nil, fault.Error("skill patch may modify only SKILL.md")
		}
	}
	temporary, err := os.MkdirTemp("", "loki-skill-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	paths, err := policy.New(temporary)
	if err != nil {
		return nil, err
	}
	defer paths.Close()
	if err := paths.AtomicWrite("SKILL.md", []byte(item.raw), 0600, false); err != nil {
		return nil, err
	}
	git := func(args ...string) (process.Result, error) {
		return process.Run(ctx, process.Spec{Argv: append([]string{"/usr/bin/git", "apply"}, args...), Input: []byte(patch), CWD: temporary, Env: []string{"PATH=/usr/bin:/bin", "HOME=/home/runner", "LANG=C.UTF-8", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, Timeout: 30 * time.Second, MaxOutput: MaxSkillBytes})
	}
	stat, err := git("--numstat", "-z")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(stat.Output, "\x00"), "\t")
	if stat.ExitCode != 0 || stat.Truncated || len(fields) != 3 || fields[2] != "SKILL.md" {
		return nil, fault.Error("skill patch may modify only SKILL.md")
	}
	checked, err := git("--check")
	if err != nil {
		return nil, err
	}
	if checked.ExitCode != 0 {
		return nil, fault.Error("skill patch check failed")
	}
	applied, err := git()
	if err != nil {
		return nil, err
	}
	if applied.ExitCode != 0 {
		return nil, fault.Error("skill patch failed")
	}
	data, err := read(paths, "SKILL.md", MaxSkillBytes)
	if err != nil {
		return nil, err
	}
	if _, _, err := parse(string(data), name); err != nil {
		return nil, err
	}
	current, err := load(item.source)
	if err != nil {
		return nil, err
	}
	if current.digest != expected {
		return nil, fault.Error("skill changed since activation; activate it again before editing")
	}
	if err := item.paths.AtomicWrite(filepath.Join(item.directory, "SKILL.md"), data, 0600, true); err != nil {
		return nil, err
	}
	updated, err := load(item.source)
	if err != nil {
		return nil, err
	}
	return updated.summary(), nil
}
