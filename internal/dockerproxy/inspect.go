package dockerproxy

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"loki/internal/portguard"
	"loki/internal/process"
)

type Inspector struct {
	Workspace, SnapshotRoot, Binary, Socket string
	run                                     func(context.Context, ...string) (string, error)
}

var containerPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

const compose = "com.docker.compose."

func pathWithin(path, root string) bool {
	if root == "" || !filepath.IsAbs(path) {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return false
		}
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
func (i Inspector) trusted(labels map[string]string) bool {
	for _, key := range []string{"project", "service", "project.working_dir", "project.config_files"} {
		if labels[compose+key] == "" {
			return false
		}
	}
	working := labels[compose+"project.working_dir"]
	if !pathWithin(working, i.Workspace) && !pathWithin(working, "/workspace") && !pathWithin(working, i.SnapshotRoot) {
		return false
	}
	for _, file := range strings.Split(labels[compose+"project.config_files"], ",") {
		ext := filepath.Ext(file)
		if (ext != ".yml" && ext != ".yaml") || !pathWithin(file, working) {
			return false
		}
	}
	return true
}
func (i Inspector) command(ctx context.Context, args ...string) (string, error) {
	if i.run != nil {
		return i.run(ctx, args...)
	}
	binary := i.Binary
	if binary == "" {
		binary = "/usr/bin/docker"
	}
	argv := []string{binary, "--host", i.Socket}
	argv = append(argv, args...)
	r, err := process.Run(ctx, process.Spec{Argv: argv, Timeout: 15 * time.Second, MaxOutput: 1 << 20})
	if err != nil {
		return "", errors.New("Docker inspection is unavailable")
	}
	if r.Truncated {
		return "", errors.New("Docker inspection response is too large")
	}
	if r.ExitCode != 0 || r.TimedOut {
		return "", errors.New("Docker inspection failed")
	}
	return r.Output, nil
}
func (i Inspector) Inspect(ctx context.Context, port int) (map[string]any, error) {
	if err := portguard.Validate(port); err != nil {
		return nil, err
	}
	listed, err := i.command(ctx, "container", "ls", "--filter", "publish="+strconv.Itoa(port), "--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, line := range strings.Split(listed, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ids = append(ids, line)
		}
	}
	if len(ids) > 32 {
		return nil, errors.New("Docker returned invalid container metadata")
	}
	for _, id := range ids {
		if !containerPattern.MatchString(id) {
			return nil, errors.New("Docker returned invalid container metadata")
		}
	}
	listeners := []map[string]any{}
	for _, id := range ids {
		raw, err := i.command(ctx, "inspect", "--format", "{{.State.Running}}\t{{json .NetworkSettings.Ports}}\t{{json .Config.Labels}}", id)
		if err != nil {
			return nil, err
		}
		fields := strings.SplitN(strings.TrimSpace(raw), "\t", 3)
		if len(fields) != 3 || fields[0] != "true" {
			continue
		}
		var ports map[string][]struct{ HostPort, HostIP string }
		var labels map[string]string
		if json.Unmarshal([]byte(fields[1]), &ports) != nil || json.Unmarshal([]byte(fields[2]), &labels) != nil || ports == nil || labels == nil {
			return nil, errors.New("Docker returned invalid container metadata")
		}
		if !i.trusted(labels) {
			continue
		}
		matched, unsafe := false, false
		for _, bindings := range ports {
			for _, binding := range bindings {
				if binding.HostPort == strconv.Itoa(port) {
					matched = true
					if binding.HostIP != "127.0.0.1" && binding.HostIP != "::1" {
						unsafe = true
					}
				}
			}
		}
		if !matched || unsafe {
			continue
		}
		working := labels[compose+"project.working_dir"]
		cwd := "/workspace"
		for _, root := range []string{i.Workspace, "/workspace"} {
			if pathWithin(working, root) {
				rel, _ := filepath.Rel(root, working)
				cwd = filepath.Join("/workspace", rel)
				break
			}
		}
		listeners = append(listeners, map[string]any{"container_id": id, "cwd": cwd, "command": "docker-compose:" + labels[compose+"project"] + "/" + labels[compose+"service"], "local_address": "127.0.0.1:" + strconv.Itoa(port)})
	}
	if len(listeners) > 1 {
		return nil, errors.New("multiple trusted Docker containers publish this port")
	}
	return map[string]any{"port": port, "in_use": len(listeners) > 0, "listeners": listeners}, nil
}
