//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/management"
)

func readSecretMigrationDocument(path string, target any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("migration metadata file must have a canonical absolute path")
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return errors.New("migration metadata is unavailable")
	}
	file := os.NewFile(uintptr(fd), "host-migration-metadata")
	defer file.Close()
	var info unix.Stat_t
	if unix.Fstat(fd, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&0022 != 0 || info.Uid != 0 && info.Uid != uint32(os.Geteuid()) || info.Nlink != 1 || info.Size > lifecycle.MaxSourceManifestBytes {
		return errors.New("migration metadata must be bounded, protected and owned")
	}
	body, err := io.ReadAll(io.LimitReader(file, lifecycle.MaxSourceManifestBytes+1))
	if err != nil || len(body) > lifecycle.MaxSourceManifestBytes {
		return errors.New("migration metadata exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("migration metadata has invalid or unsupported fields")
	}
	return nil
}

func runHostSecretMigration(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "native" {
		return runHostNativeSecretMigration(args[1:], stdout, stderr)
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: loki host secret-migration prepare|apply|status|recover|rollback --manager-root ROOT [OPTIONS]")
		return 2
	}
	action := args[0]
	if action != "prepare" && action != "apply" && action != "status" && action != "recover" && action != "rollback" {
		return 2
	}
	flags := flag.NewFlagSet("host secret-migration "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("manager-root", "", "installed managed-full owner root")
	socket := flags.String("docker-socket", "/var/run/docker.sock", "local administrator-owned Docker socket")
	mapping := flags.String("mapping", "", "protected public mappings/resources JSON; prepare only")
	planPath := flags.String("plan", "", "exact prepared public intent; apply only")
	recipient := flags.String("recipient", "", "explicit operator X25519 age public recipient; prepare only")
	backup := flags.String("backup", "", "unused private operator archive path; prepare only")
	interrupt := flags.Bool("interrupt-active-jobs", false, "freeze the explicit interruption choice in the prepared plan")
	approve := flags.Bool("approve", false, "approve applying this exact prepared plan")
	protection := flags.String("protection", "", "explicit candidate init or unlock; apply/recover only")
	privateInput := flags.Bool("stdin", false, "read protection bytes privately from stdin")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*root) || filepath.Clean(*root) != *root || *root == "/" {
		return 2
	}
	if action == "prepare" {
		if *mapping == "" || *recipient == "" || *backup == "" || *planPath != "" || *approve || *protection != "" || *privateInput {
			return 2
		}
	} else if *mapping != "" || *recipient != "" || *backup != "" || *interrupt || action != "apply" && *planPath != "" || action != "apply" && action != "rollback" && *approve {
		return 2
	}
	if action == "apply" && (*planPath == "" || !*approve) {
		fmt.Fprintln(stderr, "apply requires the exact --plan and --approve")
		return 2
	}
	if (action == "status" || action == "rollback") && (*protection != "" || *privateInput) || action == "rollback" && !*approve || *protection != "" && *protection != "init" && *protection != "unlock" || (*protection != "") != *privateInput {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	store := management.Store{Root: *root}
	if action == "status" {
		status, err := store.SecretMigrationStatus(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if json.NewEncoder(stdout).Encode(status) != nil {
			return 1
		}
		return 0
	}
	backend, err := management.NewDockerFullBackend(store, *socket, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if action == "rollback" {
		status, err := backend.RollbackSecretMigration(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if json.NewEncoder(stdout).Encode(status) != nil {
			return 1
		}
		return 0
	}
	if action == "prepare" {
		var input struct {
			Mappings  []appvalues.MigrationProfileMapping `json:"mappings"`
			Resources []controlpolicy.ApplicationResource `json:"resources"`
		}
		if err := readSecretMigrationDocument(*mapping, &input); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		intent, err := backend.PrepareSecretMigration(ctx, input.Mappings, input.Resources, *recipient, *backup, *interrupt)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if json.NewEncoder(stdout).Encode(intent) != nil {
			return 1
		}
		return 0
	}
	var intent *lifecycle.SecretMigrationIntent
	if action == "apply" {
		intent = new(lifecycle.SecretMigrationIntent)
		if err := readSecretMigrationDocument(*planPath, intent); err != nil || !intent.Valid() || intent.Mode != "managed-full" {
			fmt.Fprintln(stderr, "exact prepared managed-full migration plan is unavailable or invalid")
			return 1
		}
	}
	var passphrase []byte
	if *privateInput {
		passphrase, err = io.ReadAll(io.LimitReader(os.Stdin, 65537))
		defer clear(passphrase)
		if err != nil || len(passphrase) == 0 || len(passphrase) > 65536 {
			fmt.Fprintln(stderr, "provide bounded explicit protection bytes on stdin")
			return 1
		}
	}
	status, err := backend.ApplySecretMigration(ctx, intent, *protection, passphrase)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if json.NewEncoder(stdout).Encode(status) != nil {
		return 1
	}
	return 0
}
