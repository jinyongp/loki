package action

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"loki/internal/process"
	"loki/internal/redact"
)

type BootstrapRequest struct {
	CWD      string `json:"cwd"`
	Workflow string `json:"workflow"`
}

func (r *Runtime) Bootstrap(ctx context.Context, request BootstrapRequest) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("action runtime is closed")
	}
	if request.CWD == "" {
		request.CWD = "."
	}
	if request.Workflow == "" {
		request.Workflow = "development"
	}
	plan, err := r.controller.BootstrapPreflight(ctx, request.CWD, request.Workflow)
	if err != nil {
		return nil, err
	}
	result := plan.Metadata
	result["accepted"] = false
	if result["configuration_ready"] == false {
		result["status"] = "blocked"
		return result, nil
	}
	if !filepath.IsAbs(r.layout.RuntimeSocket) {
		return nil, errors.New("bootstrap runtime socket is not configured")
	}
	binary, err := pinned(r.layout.Binary, false)
	if err != nil {
		return nil, err
	}
	defer binary.Close()
	argv := []string{"/proc/self/fd/3", "internal", "bootstrap", r.layout.RuntimeSocket, strconv.FormatUint(uint64(r.layout.RuntimeUID), 10), result["cwd"].(string), request.Workflow}
	if os.Geteuid() == 0 {
		if r.layout.Runner == "" {
			return nil, errors.New("bootstrap runner is not configured")
		}
		argv = append([]string{"/usr/sbin/runuser", "-u", r.layout.Runner, "--"}, argv...)
	}
	if r.layout.SystemdScope {
		var suffix [8]byte
		if _, err = rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		argv = append([]string{"/usr/bin/systemd-run", "--scope", "--quiet", "--collect", "--unit=loki-project-bootstrap-" + hex.EncodeToString(suffix[:]), "--property=MemoryMax=2G", "--"}, argv...)
	}
	filter, err := redact.New(nil)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err := r.processes.Start(process.StartSpec{
		Spec:     process.Spec{Argv: argv, CWD: r.layout.Workspace, Env: []string{"PATH=/usr/bin:/bin", "HOME=/home/runner", "TMPDIR=/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}, Timeout: time.Duration(plan.TimeoutSeconds) * time.Second, MaxOutput: 8388608},
		Name:     "bootstrap/" + result["project_id"].(string) + "/" + request.Workflow,
		Redactor: filter, ExtraFiles: []*os.File{binary},
	})
	if err != nil {
		return nil, err
	}
	for key, value := range outcome(snapshot) {
		result[key] = value
	}
	result["accepted"] = true
	return result, nil
}
