package gitops

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/policy"
)

var checkpointID = regexp.MustCompile(`^[0-9a-f]{64}$`)

type CheckpointMetadata struct {
	Schema      int      `json:"schema,omitempty"`
	PatchSHA256 string   `json:"patch_sha256,omitempty"`
	CreatedAt   string   `json:"created_at"`
	Repository  string   `json:"repository"`
	Untracked   []string `json:"untracked"`
}

func (c *Controller) checkpointFile(id, suffix string) ([]byte, error) {
	if !checkpointID.MatchString(id) {
		return nil, fault.Error("invalid checkpoint id")
	}
	path := filepath.Join(filepath.Dir(c.Config.AuditLog), "checkpoints", id+suffix)
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "checkpoint")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	owner := info.Sys().(*syscall.Stat_t).Uid
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || owner != 0 && owner != uint32(os.Geteuid()) {
		return nil, fault.Error("checkpoint must be an owned protected regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024*1024+1))
	if err == nil && len(data) > 64*1024*1024 {
		err = fault.Error("checkpoint exceeds limit")
	}
	return data, err
}

func (c *Controller) ReadCheckpoint(id string) (CheckpointMetadata, error) {
	raw, err := c.checkpointFile(id, ".json")
	if err != nil {
		return CheckpointMetadata{}, err
	}
	var metadata CheckpointMetadata
	if json.Unmarshal(raw, &metadata) != nil || metadata.CreatedAt == "" || metadata.Repository == "" {
		return metadata, fault.Error("invalid checkpoint metadata")
	}
	if metadata.Schema != 0 && metadata.Schema != 2 {
		return metadata, fault.Error("unsupported checkpoint schema")
	}
	if metadata.Schema == 2 && !checkpointID.MatchString(metadata.PatchSHA256) {
		return metadata, fault.Error("invalid checkpoint patch digest")
	}
	if _, err = policy.Relative(metadata.Repository); err != nil {
		return metadata, err
	}
	return metadata, nil
}

func (c *Controller) ListCheckpoints() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(c.Config.AuditLog), "checkpoints"))
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.Name() != id && checkpointID.MatchString(id) {
			if _, err = c.ReadCheckpoint(id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

func (c *Controller) RestoreCheckpoint(ctx context.Context, id string) (CheckpointMetadata, error) {
	metadata, err := c.ReadCheckpoint(id)
	if err != nil {
		return metadata, err
	}
	patch, err := c.checkpointFile(id, ".patch")
	if err != nil {
		return metadata, err
	}
	if metadata.Schema == 2 && hash(patch) != metadata.PatchSHA256 {
		return metadata, fault.Error("checkpoint patch digest mismatch")
	}
	root, err := c.Paths.ResolveCWD(metadata.Repository)
	if err != nil {
		return metadata, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err = c.index(ctx, root); err != nil {
		return metadata, err
	}
	status, err := c.git(ctx, root, nil, 64*1024*1024, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z")
	if err != nil {
		return metadata, err
	}
	if status.ExitCode != 0 || status.Truncated || len(status.Raw) != 0 {
		return metadata, fault.Error("repository must be clean before restore")
	}
	if len(patch) == 0 {
		return metadata, nil
	}
	for _, args := range [][]string{{"apply", "--check", "--binary", "-"}, {"apply", "--binary", "-"}} {
		result, err := c.git(ctx, root, patch, 65536, args...)
		if err != nil {
			return metadata, err
		}
		if result.ExitCode != 0 {
			return metadata, fault.Error("checkpoint patch could not be applied to this repository")
		}
	}
	return metadata, nil
}
