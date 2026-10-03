package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/daemon"
	"loki/internal/fault"
	"loki/internal/platform/safeio"
)

// Checkpoint preserves tracked patches and the untracked path inventory without
// changing the index or worktree. Untracked file contents remain in place.
func (c *Controller) Checkpoint(ctx context.Context, cwd string) (*string, error) {
	full, err := c.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	top, err := c.git(ctx, full, nil, 4096, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	if top.ExitCode != 0 {
		return nil, nil
	}
	if top.Truncated {
		return nil, fault.Error("repository path exceeds limit")
	}
	root, err := c.hostPathFromRunner(top.Output)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(c.Paths.Root(), root)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, fault.Error("repository escapes workspace")
	}
	if _, err = c.index(ctx, root); err != nil {
		return nil, err
	}
	read := func(maximum int, args ...string) ([]byte, error) {
		r, err := c.git(ctx, root, nil, maximum, args...)
		if err != nil {
			return nil, err
		}
		if r.ExitCode != 0 || r.Truncated {
			return nil, fault.Error("unable to checkpoint complete repository changes")
		}
		return r.Raw, nil
	}
	status, err := read(64*1024*1024, "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	if len(status) == 0 {
		return nil, nil
	}
	head, err := c.git(ctx, root, nil, 4096, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, err
	}
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--binary"}
	if head.ExitCode == 0 {
		args = append(args, "HEAD")
	} else {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	patch, err := read(64*1024*1024, args...)
	if err != nil {
		return nil, err
	}
	if head.ExitCode != 0 {
		extra, err := read(64*1024*1024-len(patch), "diff", "--no-ext-diff", "--no-textconv", "--binary", "--")
		if err != nil {
			return nil, err
		}
		patch = append(patch, extra...)
	}
	untracked, err := read(64*1024*1024, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	joined := make([]byte, 0, len(patch)+1+len(untracked))
	joined = append(joined, patch...)
	joined = append(joined, 0)
	joined = append(joined, untracked...)
	// Include repository identity so identical patches in different repositories
	// cannot reuse metadata pointing at the wrong restore destination.
	digest := hash(append([]byte(filepath.ToSlash(rel)+"\x00"), joined...))
	directory := filepath.Join(filepath.Dir(c.Config.AuditLog), "checkpoints")
	if err = daemon.PrivateDirectory(directory); err != nil {
		return nil, err
	}
	names := []string{}
	for _, name := range strings.Split(string(untracked), "\x00") {
		if name != "" {
			names = append(names, strings.ToValidUTF8(name, "�"))
		}
	}
	metadata, err := json.Marshal(map[string]any{"schema": 2, "patch_sha256": hash(patch), "created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00"), "repository": filepath.ToSlash(rel), "untracked": names})
	if err != nil {
		return nil, err
	}
	if len(metadata) > 64*1024*1024 {
		return nil, fault.Error("checkpoint metadata exceeds limit")
	}
	// Metadata is published last: a listed checkpoint always has its patch.
	for _, suffix := range []string{".patch", ".json"} {
		data := patch
		if suffix == ".json" {
			data = metadata
		}
		if err = safeio.PublishPrivate(filepath.Join(directory, digest+suffix), data, false); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return &digest, nil
}
