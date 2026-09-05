// Package project owns central repository state shared across Git worktrees.
package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"loki/internal/fault"
	"loki/internal/process"
	"loki/internal/state"
)

const MaxArtifactBytes = 2_097_152

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+){2,}$`)

type Identity struct {
	ProjectID, RepositoryRoot, WorktreeRoot, LogicalCommonDirectory, StateDirectory, WorktreeID string
}

type Store struct {
	WorkspaceRoot, StateRoot string
	// Runner and GroupID are selected by the privileged service configuration,
	// never by an MCP request. Empty Runner operates as the current test user.
	Runner  string
	GroupID int
	GitPath string
	GitRead func(context.Context, string, ...string) (string, error)
}

func New(workspace, root string) (*Store, error) {
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if contains(workspace, root) {
		return nil, fault.Error("central project state must be outside the workspace")
	}
	return &Store{WorkspaceRoot: workspace, StateRoot: root, GitPath: "/usr/bin/git", GroupID: -1}, nil
}

func contains(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// ResolveGitPath normalizes the bind-mount alias before following filesystem
// links. The caller then checks the resulting worktree and common-dir bounds.
func ResolveGitPath(value, root string) (string, error) {
	if value == "/workspace" {
		value = root
	} else if strings.HasPrefix(value, "/workspace/") {
		value = filepath.Join(root, strings.TrimPrefix(value, "/workspace/"))
	}
	if !filepath.IsAbs(value) {
		return "", fault.Error("project Git path is not absolute")
	}
	return filepath.EvalSymlinks(value)
}

func (s *Store) gitRead(ctx context.Context, cwd string, args ...string) (string, error) {
	if s.GitRead != nil {
		return s.GitRead(ctx, cwd, args...)
	}
	argv := append([]string{s.GitPath, "-c", "safe.directory=*", "-C", cwd, "rev-parse"}, args...)
	argv = s.asRunner(argv)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := process.Command(ctx, process.Spec{Argv: argv, Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8"}})
	output := process.NewBuffer(16384)
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fault.Error("project state requires a Git worktree")
	}
	data, truncated := output.Snapshot()
	if truncated {
		return "", fault.Error("project Git path exceeds limit")
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *Store) asRunner(argv []string) []string {
	if s.Runner == "" {
		return argv
	}
	return append([]string{"/usr/sbin/runuser", "-u", s.Runner, "--"}, argv...)
}

func (s *Store) Resolve(ctx context.Context, cwd string) (Identity, error) {
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(s.WorkspaceRoot, cwd)
	}
	candidate, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return Identity{}, err
	}
	if !contains(s.WorkspaceRoot, candidate) {
		return Identity{}, fault.Error("project cwd escapes workspace")
	}
	paths := make([]string, 2)
	for i, args := range [][]string{{"--show-toplevel"}, {"--path-format=absolute", "--git-common-dir"}} {
		value, err := s.gitRead(ctx, candidate, args...)
		if err != nil {
			return Identity{}, err
		}
		paths[i], err = ResolveGitPath(value, s.WorkspaceRoot)
		if err != nil {
			return Identity{}, err
		}
	}
	worktree, common := paths[0], paths[1]
	if !contains(s.WorkspaceRoot, worktree) {
		return Identity{}, fault.Error("project worktree escapes workspace")
	}
	if !contains(s.WorkspaceRoot, common) {
		return Identity{}, fault.Error("project Git metadata escapes workspace")
	}
	relCommon, _ := filepath.Rel(s.WorkspaceRoot, common)
	logical := filepath.ToSlash(filepath.Join("/workspace", relCommon))
	relWorktree, _ := filepath.Rel(s.WorkspaceRoot, worktree)
	id := hash([]byte(logical))[:32]
	repository := worktree
	if filepath.Base(common) == ".git" {
		repository = filepath.Dir(common)
	}
	return Identity{id, repository, worktree, logical, filepath.Join(s.StateRoot, "projects", id), hash([]byte(filepath.ToSlash(relWorktree)))[:24]}, nil
}

func (s *Store) Metadata(id Identity) map[string]any {
	repository, _ := filepath.Rel(s.WorkspaceRoot, id.RepositoryRoot)
	worktree, _ := filepath.Rel(s.WorkspaceRoot, id.WorktreeRoot)
	return map[string]any{"project_id": id.ProjectID, "repository": filepath.ToSlash(repository), "worktree": filepath.ToSlash(worktree), "worktree_id": id.WorktreeID}
}

// Central metadata lives in administrator-owned directories. Reject symlinks
// and nonregular files before use, including symlinks in parent directories.
func checkParents(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fault.Error("central project state path is unsafe")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
func readFile(path string, limit int64) ([]byte, error) {
	if err := checkParents(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fault.Error("central project state path is unsafe")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, fault.Error("central project state file exceeds limit")
	}
	return data, err
}
func writeJSON(path string, v any, overwrite bool) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return state.AtomicWrite(path, append(data, '\n'), overwrite)
}
func (s *Store) directory(path string, mode os.FileMode) error {
	if err := checkParents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if s.GroupID >= 0 {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		if int(stat.Gid) != s.GroupID {
			if err = os.Chown(path, -1, s.GroupID); err != nil {
				return err
			}
		}
	}
	return os.Chmod(path, mode)
}
func (s *Store) mutationLock(ctx context.Context) (func(), error) {
	mode := os.FileMode(0700)
	if s.Runner != "" {
		mode = 0710 | os.ModeSetgid
	}
	for _, path := range []string{s.StateRoot, filepath.Join(s.StateRoot, "projects")} {
		if err := s.directory(path, mode); err != nil {
			return nil, err
		}
	}
	return state.LockFile(ctx, filepath.Join(s.StateRoot, "project-state.lock"))
}
func (s *Store) ensureProject(ctx context.Context, id Identity) error {
	mode := os.FileMode(0700)
	if s.Runner != "" {
		mode = 0710 | os.ModeSetgid
	}
	root := id.StateDirectory
	if err := s.directory(root, mode); err != nil {
		return err
	}
	for _, name := range []string{"workstreams", "bindings"} {
		if err := s.directory(filepath.Join(root, name), 0700); err != nil {
			return err
		}
	}
	taskroot := filepath.Join(root, "taskwarrior")
	if err := s.directory(taskroot, mode); err != nil {
		return err
	}
	data := filepath.Join(taskroot, "data")
	info, err := os.Lstat(data)
	if errors.Is(err, os.ErrNotExist) {
		if s.Runner == "" {
			err = os.Mkdir(data, 0700)
		} else {
			// Managed runtime has SETUID/SETGID but no CHOWN or DAC override.
			// Let runner create its private data, then close the parent write bit.
			if err = os.Chmod(taskroot, 0730|os.ModeSetgid); err != nil {
				return err
			}
			result, runErr := process.Run(ctx, process.Spec{Argv: s.asRunner([]string{"/usr/bin/mkdir", "-m", "0700", data}), MaxOutput: 4096})
			restoreErr := os.Chmod(taskroot, mode)
			if runErr != nil {
				return runErr
			}
			if restoreErr != nil {
				return restoreErr
			}
			if result.ExitCode != 0 {
				return fault.Error("could not initialize the Taskwarrior data store")
			}
		}
	} else if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return fault.Error("central project state path is unsafe")
	}
	if err != nil {
		return err
	}
	taskrc := filepath.Join(taskroot, "taskrc")
	if _, err = readFile(taskrc, 65536); errors.Is(err, os.ErrNotExist) {
		err = state.AtomicWrite(taskrc, []byte("data.location="+data+"\nconfirmation=1\nhooks=0\n"), false)
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(taskrc, 0640); err != nil {
		return err
	}
	project := filepath.Join(root, "project.json")
	if _, err = readFile(project, 65536); errors.Is(err, os.ErrNotExist) {
		err = writeJSON(project, s.Metadata(id), false)
	}
	return err
}

func (s *Store) Active(id Identity) (*string, error) {
	data, err := readFile(filepath.Join(id.StateDirectory, "bindings", id.WorktreeID+".json"), 65536)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var binding struct {
		Slug *string `json:"slug"`
	}
	if json.Unmarshal(data, &binding) != nil {
		return nil, nil
	}
	return binding.Slug, nil
}
func (s *Store) StatusIdentity(id Identity) (map[string]any, error) {
	active, err := s.Active(id)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(id.StateDirectory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fault.Error("central project state path is unsafe")
	}
	out := s.Metadata(id)
	out["initialized"] = err == nil
	out["active_workstream"] = nil
	if active != nil {
		out["active_workstream"] = *active
	}
	out["task_store"] = "central"
	return out, nil
}
func (s *Store) Status(ctx context.Context, cwd string) (map[string]any, error) {
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	return s.StatusIdentity(id)
}
func (s *Store) List(ctx context.Context, cwd string) (map[string]any, error) {
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	active, err := s.Active(id)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(id.StateDirectory, "workstreams")
	if err = checkParents(root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	items := []map[string]any{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		data, err := readFile(filepath.Join(root, entry.Name(), "manifest.json"), 65536)
		if err != nil {
			continue
		}
		var manifest map[string]any
		if json.Unmarshal(data, &manifest) != nil || manifest == nil {
			continue
		}
		items = append(items, map[string]any{"slug": entry.Name(), "goal": manifest["goal"], "depth": manifest["depth"], "active": active != nil && *active == entry.Name()})
	}
	out := s.Metadata(id)
	out["workstreams"] = items
	return out, nil
}

func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) || len(slug) > 63 {
		return fault.Error("invalid workstream slug")
	}
	return nil
}

type InitRequest struct {
	Goal         string  `json:"goal"`
	SlugBase     *string `json:"slug_base"`
	Slug         *string `json:"slug"`
	Depth        string  `json:"depth"`
	IntentKind   string  `json:"intent_source_kind"`
	IntentSource *string `json:"intent_source"`
}

func (s *Store) Initialize(ctx context.Context, cwd string, r InitRequest) (map[string]any, error) {
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	goal := strings.Join(strings.FieldsFunc(norm.NFKC.String(r.Goal), func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }), " ")
	if goal == "" || len(goal) > 4096 {
		return nil, fault.Error("workstream goal is empty or too large")
	}
	if r.Depth != "light" && r.Depth != "standard" && r.Depth != "high-risk" {
		return nil, fault.Error("workstream depth must be light, standard, or high-risk")
	}
	if r.IntentKind != "plan-local" && r.IntentKind != "authoritative" {
		return nil, fault.Error("intent source kind must be plan-local or authoritative")
	}
	if r.IntentKind == "authoritative" && (r.IntentSource == nil || *r.IntentSource == "") {
		return nil, fault.Error("authoritative intent requires an exact source")
	}
	if r.IntentKind == "plan-local" && r.IntentSource != nil {
		return nil, fault.Error("plan-local intent does not accept an external source")
	}
	goalHash := hash([]byte(cases.Fold().String(goal)))
	var slug string
	if r.Slug != nil {
		slug = *r.Slug
		if ValidateSlug(slug) != nil {
			return nil, fault.Error("explicit workstream slug is invalid")
		}
	} else {
		if r.SlugBase == nil || !slugPattern.MatchString(*r.SlugBase) {
			return nil, fault.Error("slug_base must contain at least three lowercase words")
		}
		slug = *r.SlugBase + "-" + goalHash[:10]
		if len(slug) > 63 {
			return nil, fault.Error("workstream slug exceeds 63 characters")
		}
	}
	manifest := map[string]any{"schema": 1, "slug": slug, "goal": goal, "goal_hash": goalHash, "depth": r.Depth, "intent_source_kind": r.IntentKind, "intent_source": r.IntentSource, "task_project": slug}
	release, err := s.mutationLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err = s.ensureProject(ctx, id); err != nil {
		return nil, err
	}
	dir := filepath.Join(id.StateDirectory, "workstreams", slug)
	_, err = os.Lstat(dir)
	created := errors.Is(err, os.ErrNotExist)
	if created {
		if err = s.directory(dir, 0700); err != nil {
			return nil, err
		}
		if err = writeJSON(filepath.Join(dir, "manifest.json"), manifest, false); err != nil {
			return nil, err
		}
	} else {
		data, err := readFile(filepath.Join(dir, "manifest.json"), 65536)
		if err != nil {
			return nil, fault.Error("workstream directory is unmanaged")
		}
		var existing, wanted map[string]any
		if json.Unmarshal(data, &existing) != nil {
			return nil, fault.Error("workstream manifest is invalid")
		}
		encoded, _ := json.Marshal(manifest)
		_ = json.Unmarshal(encoded, &wanted)
		if !reflect.DeepEqual(existing, wanted) {
			return nil, fault.Error("workstream identity does not match the existing manifest")
		}
	}
	if err = s.writeBinding(id, slug); err != nil {
		return nil, err
	}
	out := s.Metadata(id)
	out["slug"] = slug
	out["created"] = created
	out["active"] = true
	out["artifacts"] = []string{"plan.md", "spec.md", "validation.md"}
	return out, nil
}
func (s *Store) Workstream(id Identity, slug string) (string, error) {
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	dir := filepath.Join(id.StateDirectory, "workstreams", slug)
	if _, err := readFile(filepath.Join(dir, "manifest.json"), 65536); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fault.Error("unknown workstream")
		}
		return "", err
	}
	return dir, nil
}
func (s *Store) writeBinding(id Identity, slug string) error {
	return writeJSON(filepath.Join(id.StateDirectory, "bindings", id.WorktreeID+".json"), map[string]string{"slug": slug}, true)
}
func (s *Store) Bind(ctx context.Context, cwd, slug string) (map[string]any, error) {
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	release, err := s.mutationLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err = s.Workstream(id, slug); err != nil {
		return nil, err
	}
	if err = s.ensureProject(ctx, id); err != nil {
		return nil, err
	}
	if err = s.writeBinding(id, slug); err != nil {
		return nil, err
	}
	out := s.Metadata(id)
	out["slug"] = slug
	out["active"] = true
	return out, nil
}
func (s *Store) artifact(id Identity, slug *string, filename string) (string, string, error) {
	if filename != "plan.md" && filename != "spec.md" && filename != "validation.md" {
		return "", "", fault.Error("workstream artifact must be spec.md, plan.md, or validation.md")
	}
	if slug == nil || *slug == "" {
		var err error
		slug, err = s.Active(id)
		if err != nil {
			return "", "", err
		}
	}
	if slug == nil {
		return "", "", fault.Error("worktree has no active workstream")
	}
	dir, err := s.Workstream(id, *slug)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, filename), *slug, nil
}
func (s *Store) ReadArtifact(ctx context.Context, cwd string, slug *string, filename string) (map[string]any, error) {
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	path, resolved, err := s.artifact(id, slug, filename)
	if err != nil {
		return nil, err
	}
	data, err := readFile(path, MaxArtifactBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fault.Error("workstream artifact does not exist")
	}
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, fault.Error("workstream artifact is not valid UTF-8")
	}
	// Python text-mode reads normalize CRLF and CR before computing this hash.
	data = bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
	out := s.Metadata(id)
	out["slug"] = resolved
	out["filename"] = filename
	out["content"] = string(data)
	out["sha256"] = hash(data)
	return out, nil
}
func (s *Store) WriteArtifact(ctx context.Context, cwd string, slug *string, filename, content string, expected *string) (map[string]any, error) {
	if len(content) > MaxArtifactBytes {
		return nil, fault.Error("workstream artifact is too large")
	}
	id, err := s.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	release, err := s.mutationLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	path, resolved, err := s.artifact(id, slug, filename)
	if err != nil {
		return nil, err
	}
	data, err := readFile(path, MaxArtifactBytes)
	if err == nil {
		if expected == nil {
			return nil, fault.Error("expected_sha256 is required when updating an artifact")
		}
		if hash(data) != *expected {
			return nil, fault.Error("workstream artifact changed since it was read")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if expected != nil {
			return nil, fault.Error("workstream artifact does not exist for the expected revision")
		}
	} else {
		return nil, err
	}
	if err = state.AtomicWrite(path, []byte(content), expected != nil); err != nil {
		return nil, err
	}
	out := s.Metadata(id)
	out["slug"] = resolved
	out["filename"] = filename
	out["sha256"] = hash([]byte(content))
	out["created"] = expected == nil
	return out, nil
}
