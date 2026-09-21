package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"loki/internal/config"
	"loki/internal/daemon"
	"loki/internal/fault"
	"loki/internal/gitops"
	"loki/internal/policy"
	jobsremote "loki/internal/work/jobs/remote"
)

func checkpointGitRunner(layout mcpLayout) (gitops.Runner, error) {
	if layout.ExecutorUID == nil {
		return nil, fmt.Errorf("checkpoint executor peer is not configured")
	}
	executor, err := jobsremote.NewExecutor(jobsremote.ExecutorOptions{
		Socket: layout.ExecutorSocket, ExpectedUID: layout.ExecutorUID, Timeout: 60 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	return gitops.JobRunner{Jobs: executor}, nil
}

func runCheckpoint(args []string, stdout, stderr io.Writer) int {
	counts := map[string]int{"list": 1, "show": 2, "restore": 2}
	if len(args) == 0 || counts[args[0]] != len(args) {
		fmt.Fprintln(stderr, "usage: loki checkpoint list | show ID | restore ID")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if os.Geteuid() == 0 {
		// Git configuration belongs to runner; never interpret repository
		// filters or helper settings with administrator privileges.
		binary, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, "cannot resolve Loki executable")
			return 1
		}
		command := exec.CommandContext(ctx, "/usr/sbin/runuser", append([]string{"-u", "runner", "--", binary, "checkpoint"}, args...)...)
		command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
		command.Stdout = stdout
		command.Stderr = stderr
		if err = command.Run(); err != nil {
			return 1
		}
		return 0
	}
	paths, err := policy.New("/srv/workspace/loki")
	if err != nil {
		fmt.Fprintln(stderr, fault.Public(err))
		return 1
	}
	defer paths.Close()
	configuration, _ := config.Parse(nil)
	c := &gitops.Controller{Paths: paths, Config: configuration, Env: []string{"PATH=/usr/bin:/bin", "HOME=/home/runner", "LANG=C.UTF-8", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}}
	if args[0] == "list" {
		ids, err := c.ListCheckpoints()
		if err != nil {
			fmt.Fprintln(stderr, fault.Public(err))
			return 1
		}
		for _, id := range ids {
			metadata, err := c.ReadCheckpoint(id)
			if err != nil {
				fmt.Fprintln(stderr, fault.Public(err))
				return 1
			}
			if _, err = fmt.Fprintf(stdout, "%s\t%s\t%s\n", id, metadata.CreatedAt, metadata.Repository); err != nil {
				return 1
			}
		}
		return 0
	}
	if args[0] == "show" {
		metadata, err := c.ReadCheckpoint(args[1])
		if err != nil {
			fmt.Fprintln(stderr, fault.Public(err))
			return 1
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		if err = encoder.Encode(metadata); err != nil {
			return 1
		}
		return 0
	}
	var layout mcpLayout
	if err = daemon.ReadJSON("/etc/loki-go/mcp.json", &layout); err != nil {
		fmt.Fprintln(stderr, "checkpoint executor layout unavailable")
		return 1
	}
	runner, err := checkpointGitRunner(layout)
	if err != nil {
		fmt.Fprintln(stderr, fault.Public(err))
		return 1
	}
	c.Runner = runner
	metadata, err := c.RestoreCheckpoint(ctx, args[1])
	if err != nil {
		fmt.Fprintln(stderr, fault.Public(err))
		return 1
	}
	fmt.Fprintln(stdout, "restored tracked changes in", filepath.Join(paths.Root(), metadata.Repository))
	if len(metadata.Untracked) > 0 {
		fmt.Fprintln(stdout, "untracked file contents were not archived; names are available with show")
	}
	return 0
}
