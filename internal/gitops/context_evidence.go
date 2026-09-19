package gitops

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/policy"
)

type ContextEvidence struct {
	RepositoryID  string
	WorktreeID    string
	CodeBasis     string
	Gaps          []string
	RepositoryCWD string
	TargetCWD     string
}

func (c *Controller) metadataPath(ctx context.Context, cwd, option string) (string, error) {
	result, err := c.git(ctx, cwd, nil, c.Config.MaxOutputBytes, "rev-parse", "--path-format=absolute", option)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 || result.Truncated {
		return "", errors.New("unable to inspect Git metadata")
	}
	path, err := filepath.EvalSymlinks(strings.TrimSpace(result.Output))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(c.Paths.Root(), path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("Git metadata escapes workspace")
	}
	return filepath.ToSlash(relative), nil
}

func (c *Controller) contextTargetDirectory(target string) (string, error) {
	current := target
	for {
		handle, err := c.Paths.Open(current, unix.O_PATH, 0)
		if err == nil {
			info, statErr := handle.Stat()
			handle.Close()
			if statErr != nil {
				return "", statErr
			}
			if info.IsDir() {
				return filepath.ToSlash(filepath.Clean(current)), nil
			}
			if info.Mode().IsRegular() {
				return filepath.ToSlash(filepath.Dir(current)), nil
			}
			return "", errors.New("Git context target must be a regular file or directory")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.ToSlash(filepath.Dir(current))
		if parent == current {
			return "", errors.New("Git context target has no existing parent")
		}
		current = parent
	}
}

func (c *Controller) ContextEvidence(ctx context.Context, cwd, target string) (ContextEvidence, error) {
	if target == "" {
		target = "."
	}
	if filepath.IsAbs(target) {
		return ContextEvidence{}, errors.New("Git context target must be relative")
	}
	target, err := policy.Relative(filepath.ToSlash(target))
	if err != nil {
		return ContextEvidence{}, err
	}
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return ContextEvidence{}, err
	}
	relativeCWD, err := filepath.Rel(c.Paths.Root(), full)
	if err != nil {
		return ContextEvidence{}, err
	}
	workspaceTarget, err := policy.Relative(filepath.ToSlash(filepath.Join(relativeCWD, target)))
	if err != nil {
		return ContextEvidence{}, err
	}
	ownerDirectory, err := c.contextTargetDirectory(workspaceTarget)
	if err != nil {
		return ContextEvidence{}, err
	}
	root, err := c.repositoryRoot(ctx, filepath.Join(c.Paths.Root(), filepath.FromSlash(ownerDirectory)))
	if err != nil {
		return ContextEvidence{}, err
	}
	common, err := c.metadataPath(ctx, root, "--git-common-dir")
	if err != nil {
		return ContextEvidence{}, err
	}
	gitdir, err := c.metadataPath(ctx, root, "--git-dir")
	if err != nil {
		return ContextEvidence{}, err
	}
	rootRelative, err := filepath.Rel(c.Paths.Root(), root)
	if err != nil || rootRelative == ".." || strings.HasPrefix(rootRelative, ".."+string(filepath.Separator)) {
		return ContextEvidence{}, errors.New("Git repository escapes workspace")
	}
	rootRelative = filepath.ToSlash(rootRelative)
	evidence := ContextEvidence{
		RepositoryID:  hash([]byte("loki-git-common-v1\x00" + common)),
		WorktreeID:    hash([]byte("loki-git-worktree-v1\x00" + rootRelative + "\x00" + gitdir)),
		Gaps:          []string{},
		RepositoryCWD: rootRelative,
		TargetCWD:     ownerDirectory,
	}

	index, err := c.index(ctx, root)
	if err != nil {
		evidence.Gaps = append(evidence.Gaps, "code_basis_unavailable")
		return evidence, nil
	}
	head, err := c.git(ctx, root, nil, 4096, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil || head.Truncated || head.ExitCode != 0 && head.ExitCode != 1 {
		evidence.Gaps = append(evidence.Gaps, "code_basis_unavailable")
		return evidence, nil
	}
	headValue := "unborn"
	if head.ExitCode == 0 {
		headValue = strings.TrimSpace(head.Output)
	}
	if err := c.rejectExecutableFilters(ctx, root); err != nil {
		evidence.Gaps = append(evidence.Gaps, "code_basis_unavailable")
		return evidence, nil
	}
	status, err := c.git(ctx, root, nil, c.Config.MaxOutputBytes, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil || status.ExitCode != 0 || status.Truncated {
		evidence.Gaps = append(evidence.Gaps, "code_basis_truncated")
		return evidence, nil
	}
	diff, err := c.git(ctx, root, nil, c.Config.MaxOutputBytes, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--")
	if err != nil || diff.ExitCode != 0 || diff.Truncated {
		evidence.Gaps = append(evidence.Gaps, "code_basis_truncated")
		return evidence, nil
	}
	for _, entry := range bytes.Split(status.Raw, []byte{0}) {
		if bytes.HasPrefix(entry, []byte("?? ")) {
			evidence.Gaps = append(evidence.Gaps, "untracked_content_unobserved")
			break
		}
	}
	payload := make([]byte, 0, len(headValue)+len(index)+len(status.Raw)+len(diff.Raw)+64)
	for _, part := range [][]byte{
		[]byte("loki-code-basis-v1"),
		[]byte(headValue),
		[]byte(index),
		status.Raw,
		diff.Raw,
	} {
		payload = append(payload, part...)
		payload = append(payload, 0)
	}
	evidence.CodeBasis = hash(payload)
	return evidence, nil
}
