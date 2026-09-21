// Package gitops implements bounded Git inspection and optimistic index edits.
package gitops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"loki/internal/config"
	"loki/internal/fault"
	"loki/internal/policy"
)

type Controller struct {
	Paths  *policy.Workspace
	Config config.Config
	Runner Runner
	// Env remains diagnostic metadata until S04 removes the legacy service wiring.
	Env           []string
	TemplateRoots []*policy.Workspace
	mu            sync.Mutex
}

var errNotRepository = errors.New("cwd is not inside a Git repository")

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func (c *Controller) runnerCWD(cwd string) (string, error) {
	if c == nil || c.Paths == nil || c.Runner == nil {
		return "", errors.New("Git runner is not configured")
	}
	relative, err := filepath.Rel(c.Paths.Root(), cwd)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fault.Error("Git working directory escapes workspace")
	}
	if relative == "." {
		return ".", nil
	}
	return filepath.ToSlash(relative), nil
}

func (c *Controller) git(ctx context.Context, cwd string, input []byte, maximum int, args ...string) (CommandResult, error) {
	runnerCWD, err := c.runnerCWD(cwd)
	if err != nil {
		return CommandResult{}, err
	}
	prefix := []string{
		"/usr/bin/git", "--no-pager", "--literal-pathspecs",
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
	}
	return c.Runner.Run(ctx, CommandRequest{
		Argv: append(prefix, args...), CWD: runnerCWD, Input: input,
		Timeout: 30 * time.Second, MaxOutput: maximum,
	})
}

func (c *Controller) rejectExecutableFilters(ctx context.Context, cwd string) error {
	result, err := c.git(ctx, cwd, nil, c.Config.MaxOutputBytes,
		"config", "--includes", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process)$`)
	if err != nil {
		return err
	}
	switch result.ExitCode {
	case 1:
		return nil
	case 0:
		if result.Truncated {
			return fault.Error("unable to inspect Git filter configuration")
		}
		return fault.Error("repository executable Git filters are unavailable in the Git controller")
	default:
		return fault.Error("unable to inspect Git filter configuration")
	}
}
func public(result CommandResult, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	value := map[string]any{"exit_code": result.ExitCode, "output": result.Output, "truncated": result.Truncated}
	if result.TimedOut {
		value["timed_out"] = true
	}
	return value, nil
}
func (c *Controller) hostPathFromRunner(value string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(value))
	const runnerRoot = "/workspace"
	if clean != runnerRoot && !strings.HasPrefix(clean, runnerRoot+"/") {
		return "", fault.Error("Git path escapes workspace")
	}
	relative := strings.TrimPrefix(clean, runnerRoot)
	relative = strings.TrimPrefix(relative, "/")
	target := c.Paths.Root()
	if relative != "" {
		target = filepath.Join(target, filepath.FromSlash(relative))
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(c.Paths.Root(), resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fault.Error("Git path escapes workspace")
	}
	return resolved, nil
}

func (c *Controller) path(cwd, requested string, exists bool) (string, error) {
	if requested == "" {
		return "", fault.Error("invalid Git path")
	}
	rel, err := policy.Relative(requested)
	if err != nil {
		return "", err
	}
	base, err := filepath.Rel(c.Paths.Root(), cwd)
	if err != nil {
		return "", err
	}
	target, err := c.Paths.Resolve(filepath.Join(base, rel), exists)
	if err != nil {
		return "", err
	}
	return filepath.Rel(cwd, target)
}

func (c *Controller) repositoryRoot(ctx context.Context, cwd string) (string, error) {
	repository, err := c.git(ctx, cwd, nil, c.Config.MaxOutputBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if repository.ExitCode != 0 || repository.Truncated {
		return "", errNotRepository
	}
	root, err := c.hostPathFromRunner(repository.Output)
	if err != nil {
		return "", fault.Error("repository escapes workspace")
	}
	relative, err := filepath.Rel(c.Paths.Root(), root)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fault.Error("repository escapes workspace")
	}
	for _, option := range []string{"--git-common-dir", "--git-dir"} {
		metadata, runErr := c.git(ctx, cwd, nil, c.Config.MaxOutputBytes, "rev-parse", "--path-format=absolute", option)
		if runErr != nil {
			return "", runErr
		}
		if metadata.ExitCode != 0 || metadata.Truncated {
			return "", fault.Error("unable to inspect Git metadata")
		}
		target, resolveErr := c.hostPathFromRunner(metadata.Output)
		if resolveErr != nil {
			return "", fault.Error("Git metadata escapes workspace")
		}
		relative, resolveErr = filepath.Rel(c.Paths.Root(), target)
		if resolveErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fault.Error("Git metadata escapes workspace")
		}
	}
	return root, nil
}

func (c *Controller) RepositoryRoot(ctx context.Context, cwd string) (string, error) {
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return "", err
	}
	return c.repositoryRoot(ctx, full)
}

// TrackedFile reports whether an existing confined file is tracked by its
// nearest owning repository. Repository metadata must remain inside the
// workspace; absence of a repository is reported as untracked rather than
// falling back to a parent workspace repository assumption.
func (c *Controller) TrackedFile(ctx context.Context, requested string) (bool, error) {
	relative, err := policy.Relative(requested)
	if err != nil {
		return false, err
	}
	target, err := c.Paths.Resolve(relative, true)
	if err != nil {
		return false, err
	}
	root, err := c.repositoryRoot(ctx, filepath.Dir(target))
	if err != nil {
		if errors.Is(err, errNotRepository) {
			return false, nil
		}
		return false, err
	}
	repositoryRelative, err := filepath.Rel(root, target)
	if err != nil || repositoryRelative == ".." || strings.HasPrefix(repositoryRelative, ".."+string(filepath.Separator)) {
		return false, fault.Error("Git path escapes repository")
	}
	result, err := c.git(ctx, root, nil, c.Config.MaxOutputBytes, "ls-files", "--error-unmatch", "--", filepath.ToSlash(repositoryRelative))
	if err != nil {
		return false, err
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	if result.ExitCode != 0 || result.Truncated {
		return false, fault.Error("unable to inspect Git tracked file")
	}
	return true, nil
}

func (c *Controller) repositoryKnowsPath(ctx context.Context, cwd, relative string) (bool, error) {
	root, err := c.repositoryRoot(ctx, cwd)
	if err != nil {
		if errors.Is(err, errNotRepository) {
			return false, fault.Error(errNotRepository.Error())
		}
		return false, err
	}
	absolute := filepath.Clean(filepath.Join(cwd, relative))
	repositoryRelative, err := filepath.Rel(root, absolute)
	if err != nil || repositoryRelative == ".." || strings.HasPrefix(repositoryRelative, ".."+string(filepath.Separator)) {
		return false, fault.Error("Git path escapes repository")
	}
	pathspec := filepath.ToSlash(repositoryRelative)
	tracked, err := c.git(ctx, root, nil, c.Config.MaxOutputBytes, "ls-files", "-z", "--", pathspec)
	if err != nil {
		return false, err
	}
	if tracked.ExitCode != 0 || tracked.Truncated {
		return false, fault.Error("unable to inspect Git tracked paths")
	}
	if len(tracked.Raw) != 0 {
		return true, nil
	}
	head, err := c.git(ctx, root, nil, c.Config.MaxOutputBytes, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return false, err
	}
	if head.ExitCode == 1 {
		return false, nil
	}
	if head.ExitCode != 0 || head.Truncated {
		return false, fault.Error("unable to inspect Git HEAD")
	}
	committed, err := c.git(ctx, root, nil, c.Config.MaxOutputBytes, "ls-tree", "-r", "-z", "--name-only", "HEAD", "--", pathspec)
	if err != nil {
		return false, err
	}
	if committed.ExitCode != 0 || committed.Truncated {
		return false, fault.Error("unable to inspect Git committed paths")
	}
	return len(committed.Raw) != 0, nil
}

func (c *Controller) knownPath(ctx context.Context, cwd, requested string) (string, error) {
	relative, err := c.path(cwd, requested, false)
	if err != nil {
		return "", err
	}
	if _, err = c.path(cwd, requested, true); err == nil {
		return relative, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	known, err := c.repositoryKnowsPath(ctx, cwd, relative)
	if err != nil {
		return "", err
	}
	if !known {
		return "", fault.Error("Git path does not exist and is not tracked")
	}
	return relative, nil
}

func (c *Controller) knownIndexPath(ctx context.Context, cwd, requested string) (string, error) {
	relative, err := c.path(cwd, requested, false)
	if err != nil {
		return "", err
	}
	known, err := c.repositoryKnowsPath(ctx, cwd, relative)
	if err != nil {
		return "", err
	}
	if !known {
		return "", fault.Error("Git path is not tracked or staged")
	}
	return relative, nil
}

func (c *Controller) Status(ctx context.Context, cwd string) (map[string]any, error) {
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	if err = c.rejectExecutableFilters(ctx, full); err != nil {
		return nil, err
	}
	return public(c.git(ctx, full, nil, c.Config.MaxOutputBytes, "status", "--short", "--branch", "--untracked-files=all"))
}
func (c *Controller) Diff(ctx context.Context, cwd string, staged bool, path *string) (map[string]any, error) {
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	if err = c.rejectExecutableFilters(ctx, full); err != nil {
		return nil, err
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv"}
	if staged {
		args = append(args, "--cached")
	}
	if path != nil {
		relative, err := c.knownPath(ctx, full, *path)
		if err != nil {
			return nil, err
		}
		args = append(args, "--", relative)
	}
	return public(c.git(ctx, full, nil, c.Config.MaxOutputBytes, args...))
}
func (c *Controller) index(ctx context.Context, cwd string) (string, error) {
	if _, err := c.repositoryRoot(ctx, cwd); err != nil {
		if errors.Is(err, errNotRepository) {
			return "", fault.Error(errNotRepository.Error())
		}
		return "", err
	}
	result, err := c.git(ctx, cwd, nil, 64*1024*1024, "ls-files", "--stage", "-z")
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 || result.Truncated {
		return "", fault.Error("unable to inspect complete Git index")
	}
	return hash(result.Raw), nil
}
func (c *Controller) Index(ctx context.Context, cwd string) (map[string]any, error) {
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	digest, err := c.index(ctx, full)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cwd": cwd, "index_sha256": digest}, nil
}
func (c *Controller) MutatePaths(ctx context.Context, operation, cwd string, paths []string, expected *string) (map[string]any, error) {
	if operation != "stage" && operation != "unstage" {
		return nil, fault.Error("invalid Git index operation")
	}
	if len(paths) == 0 || len(paths) > c.Config.MaxPatchFiles {
		return nil, fault.Error("paths must contain between 1 and the configured patch file limit")
	}
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	if operation == "stage" {
		if err = c.rejectExecutableFilters(ctx, full); err != nil {
			return nil, err
		}
	}
	clean := []string{}
	for _, path := range paths {
		var relative string
		if operation == "stage" {
			relative, err = c.knownPath(ctx, full, path)
		} else {
			relative, err = c.knownIndexPath(ctx, full, path)
		}
		if err != nil {
			return nil, err
		}
		clean = append(clean, relative)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	before, err := c.index(ctx, full)
	if err != nil {
		return nil, err
	}
	if expected != nil && *expected != before {
		return nil, fault.New(fault.CodeConflict, "Git index changed; inspect it again before changing staged state", false, "run git_inspect action=index and retry with the current index_sha256")
	}
	args := []string{"add", "--"}
	if operation == "unstage" {
		head, err := c.git(ctx, full, nil, c.Config.MaxOutputBytes, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return nil, err
		}
		if head.ExitCode == 0 {
			args = []string{"restore", "--staged", "--"}
		} else {
			args = []string{"rm", "--cached", "--ignore-unmatch", "--"}
		}
	}
	result, err := c.git(ctx, full, nil, c.Config.MaxOutputBytes, append(args, clean...)...)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fault.Error("git " + operation + " failed: " + result.Output)
	}
	after, err := c.index(ctx, full)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation": operation, "paths": clean, "previous_index_sha256": before, "index_sha256": after, "output": result.Output}, nil
}
func (c *Controller) StagePatch(ctx context.Context, cwd, patch string, reverse bool, expected *string) (map[string]any, error) {
	if patch == "" || len(patch) > c.Config.MaxPatchBytes {
		return nil, fault.Error("patch is empty or exceeds the patch limit")
	}
	for _, marker := range []string{"\x00", "GIT binary patch", "Binary files ", "new file mode ", "deleted file mode ", "old mode ", "new mode ", "rename from ", "rename to ", "copy from ", "copy to "} {
		if strings.Contains(patch, marker) {
			return nil, fault.Error("only modifications to regular text files may be partially staged")
		}
	}
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	before, err := c.index(ctx, full)
	if err != nil {
		return nil, err
	}
	if expected != nil && *expected != before {
		return nil, fault.New(fault.CodeConflict, "Git index changed; inspect it again before changing staged state", false, "run git_inspect action=index and retry with the current index_sha256")
	}
	data := []byte(patch)
	stats, err := c.git(ctx, full, data, c.Config.MaxOutputBytes, "apply", "--numstat", "-z")
	if err != nil {
		return nil, err
	}
	if stats.ExitCode != 0 || stats.Truncated {
		return nil, fault.Error("invalid staging patch")
	}
	files := []map[string]any{}
	for _, entry := range bytes.Split(stats.Raw, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		fields := bytes.SplitN(entry, []byte{'\t'}, 3)
		if len(fields) != 3 || !utf8.Valid(fields[2]) {
			return nil, fault.Error("invalid staging patch")
		}
		added, e1 := strconv.Atoi(string(fields[0]))
		deleted, e2 := strconv.Atoi(string(fields[1]))
		if e1 != nil || e2 != nil {
			return nil, fault.Error("binary patches are not allowed")
		}
		path := string(fields[2])
		if _, err := c.path(full, path, true); err != nil {
			return nil, err
		}
		files = append(files, map[string]any{"path": path, "added": added, "deleted": deleted})
	}
	if len(files) == 0 || len(files) > c.Config.MaxPatchFiles {
		return nil, fault.Error("staging patch has no files or changes too many files")
	}
	args := []string{"apply", "--cached"}
	if reverse {
		args = append(args, "--reverse")
	}
	checked, err := c.git(ctx, full, data, c.Config.MaxOutputBytes, append(append([]string{}, args...), "--check")...)
	if err != nil {
		return nil, err
	}
	if checked.ExitCode != 0 {
		return nil, fault.Error("staging patch check failed: " + checked.Output)
	}
	applied, err := c.git(ctx, full, data, c.Config.MaxOutputBytes, args...)
	if err != nil {
		return nil, err
	}
	if applied.ExitCode != 0 {
		return nil, fault.Error("staging patch failed: " + applied.Output)
	}
	after, err := c.index(ctx, full)
	if err != nil {
		return nil, err
	}
	return map[string]any{"files": files, "reverse": reverse, "patch_sha256": hash(data), "previous_index_sha256": before, "index_sha256": after}, nil
}
func (c *Controller) CommitContext(ctx context.Context, cwd string) (map[string]any, error) {
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	origins, err := c.git(ctx, full, nil, c.Config.MaxOutputBytes, "config", "--show-origin", "--get-all", "commit.template")
	if err != nil {
		return nil, err
	}
	if origins.ExitCode == 1 {
		return map[string]any{"configured": false, "origins": []map[string]any{}, "template": nil}, nil
	}
	if origins.ExitCode != 0 || origins.Truncated {
		return nil, fault.Error("unable to inspect commit template")
	}
	entries := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSuffix(origins.Output, "\n"), "\n") {
		source, value, _ := strings.Cut(line, "\t")
		entries = append(entries, map[string]any{"source": source, "value": value})
	}
	resolved, err := c.git(ctx, full, nil, c.Config.MaxOutputBytes, "config", "--path", "--get", "commit.template")
	if err != nil {
		return nil, err
	}
	if resolved.ExitCode != 0 || resolved.Truncated {
		return nil, fault.Error("unable to resolve commit template")
	}
	configured := strings.TrimSpace(resolved.Output)
	target := configured
	if filepath.IsAbs(target) {
		if target == "/workspace" || strings.HasPrefix(target, "/workspace/") {
			target, err = c.hostPathFromRunner(target)
			if err != nil {
				return nil, err
			}
		}
	} else {
		target = filepath.Join(full, target)
	}
	for _, root := range append([]*policy.Workspace{c.Paths}, c.TemplateRoots...) {
		rel, err := filepath.Rel(root.Root(), target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		file, err := root.Open(rel, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fault.Error("commit template must be a regular non-symbolic file")
		}
		maximum := min(c.Config.MaxFileBytes, 65536)
		data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maximum {
			return nil, fault.Error("commit template exceeds read limit")
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return nil, fault.Error("commit template must be UTF-8 text")
		}
		return map[string]any{"configured": true, "origins": entries, "template": map[string]any{"path": configured, "content": string(data), "sha256": hash(data)}}, nil
	}
	return nil, errors.New("commit template is outside trusted template roots")
}
