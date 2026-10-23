//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/host/lifecycle"
	"loki/internal/host/lifecycle/compose"
)

// Operator keys stay beneath the private host owner, outside workload mounts.
// openat2 prevents aliases; the open descriptor supplies both checks and bytes.
func readApplicationRestoreIdentity(root, path string) ([]byte, error) {
	failure := errors.New("restore identity requires a bounded private owned file beneath the host state root")
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || !filepath.IsAbs(path) || filepath.Clean(path) != path || !strings.HasPrefix(path, root+"/") {
		return nil, failure
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, failure
	}
	f := os.NewFile(uintptr(fd), "host-restore-identity")
	defer f.Close()
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&0777 != 0600 || info.Nlink != 1 || info.Uid != uint32(os.Geteuid()) || info.Size < 1 || info.Size > 65536 {
		return nil, failure
	}
	body, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(body) < 1 || len(body) > 65536 {
		clear(body)
		return nil, failure
	}
	return body, nil
}

func runHostApplicationRestore(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "compose" {
		fmt.Fprintln(stderr, "usage: loki host application-restore compose prepare|prepare-new-deployment|apply|recover|status|rollback --state-root ROOT [OPTIONS]")
		return 2
	}
	action := args[1]
	if action != "prepare" && action != "prepare-new-deployment" && action != "apply" && action != "recover" && action != "status" && action != "rollback" {
		return 2
	}
	flags := flag.NewFlagSet("host application-restore compose "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("state-root", "", "original private host state root")
	project := flags.String("project", "loki", "installed Compose project")
	socket := flags.String("docker-socket", "/var/run/docker.sock", "administrator-owned Docker socket")
	backup := flags.String("backup-id", "", "exact paired binary/data backup; prepare only")
	sourceRoot := flags.String("source-state-root", "", "private archived owner; prepare-new-deployment only")
	plan := flags.String("plan", "", "exact prepared restore intent; apply only")
	interrupt := flags.Bool("interrupt-active-jobs", false, "freeze explicit Job interruption choice; prepare only")
	approve := flags.Bool("approve", false, "approve the exact apply plan or pre-cutover rollback")
	identityPath := flags.String("identity-file", "", "private operator age identity beneath the host state root")
	protection := flags.String("protection", "", "explicit candidate init or unlock")
	stdin := flags.Bool("stdin", false, "read bounded protection bytes privately")
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*root) || filepath.Clean(*root) != *root || *root == "/" {
		return 2
	}
	if action == "prepare" || action == "prepare-new-deployment" {
		if *backup == "" || *plan != "" || *approve || *identityPath != "" || *protection != "" || *stdin {
			return 2
		}
	} else if *backup != "" || *interrupt || action != "apply" && *plan != "" || (action == "apply" || action == "rollback") != *approve {
		return 2
	}
	if (action == "prepare-new-deployment") != (*sourceRoot != "") {
		return 2
	}
	if action == "apply" && *plan == "" || (action == "status" || action == "rollback") && (*identityPath != "" || *protection != "" || *stdin) || *protection != "" && *protection != "init" && *protection != "unlock" || (*protection != "") != *stdin {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	b, err := compose.Open(compose.Config{StateRoot: *root, Project: *project, Runner: compose.ExecRunner{Prefix: []string{"--host", "unix://" + *socket}}})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var result any
	switch action {
	case "prepare":
		result, err = b.PrepareApplicationRestore(ctx, *backup, *interrupt)
	case "prepare-new-deployment":
		result, err = b.PrepareNewDeploymentRestore(ctx, *sourceRoot, *backup, *interrupt)
	case "status":
		result, err = b.ApplicationRestoreStatus(ctx)
	case "rollback":
		result, err = b.RollbackApplicationRestore(ctx)
	default:
		var intent *lifecycle.ApplicationRestoreIntent
		if action == "apply" {
			intent = new(lifecycle.ApplicationRestoreIntent)
			if err := readHostOperationDocument(*plan, intent, 3<<20); err != nil || !intent.Valid() || intent.Mode != "compose" {
				fmt.Fprintln(stderr, "exact prepared restore plan is unavailable or invalid")
				return 1
			}
		}
		var identity, pass []byte
		if *identityPath != "" {
			identity, err = readApplicationRestoreIdentity(*root, *identityPath)
			defer clear(identity)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		if *stdin {
			pass, err = io.ReadAll(io.LimitReader(os.Stdin, 65537))
			defer clear(pass)
			if err != nil || len(pass) < 1 || len(pass) > 65536 {
				fmt.Fprintln(stderr, "provide bounded explicit protection bytes on stdin")
				return 1
			}
		}
		result, err = b.ApplyApplicationRestore(ctx, intent, identity, *protection, pass)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}
