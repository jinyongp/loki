package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"loki/internal/fault"
	"loki/internal/project"
	"loki/internal/rpc"
)

// Installed wrappers provide the trusted socket, peer UID, and workspace root.
// Remaining arguments retain Taskwarrior's native order and option syntax.
func runTask(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 || !filepath.IsAbs(args[0]) || !filepath.IsAbs(args[2]) {
		fmt.Fprintln(stderr, "task requires SOCKET EXPECTED_UID WORKSPACE [ARGUMENTS...]")
		return 2
	}
	uid, err := strconv.ParseUint(args[1], 10, 32)
	if err != nil {
		fmt.Fprintln(stderr, "invalid runtime UID")
		return 2
	}
	arguments := append([]string{}, args[3:]...)
	if err = project.ValidateTaskArguments(arguments); err != nil {
		fmt.Fprintln(stderr, fault.Public(err))
		return 2
	}
	root, err := filepath.EvalSymlinks(args[2])
	if err != nil {
		fmt.Fprintln(stderr, "cannot resolve workspace")
		return 1
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = filepath.EvalSymlinks(cwd)
	}
	if err != nil {
		fmt.Fprintln(stderr, "cannot resolve current directory")
		return 1
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		fmt.Fprintln(stderr, "task must run inside the workspace")
		return 1
	}
	expected := uint32(uid)
	client := rpc.Client{Socket: args[0], ExpectedUID: &expected, Limits: rpc.Limits{Timeout: 910 * time.Second}}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	encoded, err := client.Call(ctx, map[string]any{"operation": "project_task", "cwd": filepath.ToSlash(rel), "arguments": arguments, "timeout_seconds": 900})
	if err != nil {
		fmt.Fprintln(stderr, fault.Public(err))
		return 1
	}
	var result struct {
		ExitCode *int    `json:"exit_code"`
		Output   *string `json:"output"`
	}
	if json.Unmarshal(encoded, &result) != nil || result.ExitCode == nil || result.Output == nil || *result.ExitCode < 0 || *result.ExitCode > 255 {
		fmt.Fprintln(stderr, "invalid task response")
		return 1
	}
	if _, err = io.WriteString(stdout, *result.Output); err != nil {
		return 1
	}
	return *result.ExitCode
}
