package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/host/lifecycle"
	lifecyclecompose "loki/internal/host/lifecycle/compose"
	"loki/internal/host/releases"
)

func newHostRuntimeBackend(store *lifecycle.FileStore) (lifecycle.TransactionBackend, error) {
	if store == nil || store.Root == "" {
		return nil, errors.New("host lifecycle store is not configured")
	}
	if docker := strings.TrimSpace(os.Getenv("LOKI_DOCKER")); docker != "" {
		return newHostRuntimeBackendWithRunner(store, lifecyclecompose.ExecRunner{Executable: docker})
	}
	access := hostDockerAccessDirect
	if snapshot, err := store.Snapshot(context.Background()); err == nil && snapshot.Installation != nil &&
		snapshot.Installation.DockerAccess != "" {
		access = snapshot.Installation.DockerAccess
	}
	runner, err := dockerLifecycleRunner(access)
	if err != nil {
		return nil, err
	}
	return newHostRuntimeBackendWithRunner(store, runner)
}

func newHostRuntimeBackendWithRunner(store *lifecycle.FileStore, runner lifecyclecompose.Runner) (lifecycle.TransactionBackend, error) {
	if store == nil || store.Root == "" {
		return nil, errors.New("host lifecycle store is not configured")
	}
	return lifecyclecompose.New(lifecyclecompose.Config{StateRoot: store.Root, Runner: runner})
}

type hostInstallOptions struct {
	System               bool
	StateRoot            string
	Workspace            string
	ReleaseManifest      string
	InstallPrerequisites bool
	AllowSudoDocker      bool
	CreateWorkspace      bool
	PrepareWorkspace     bool
	AllowSudoWorkspace   bool
	JSON                 bool
	PersistCLI           bool
	CLIPaths             hostCLIInstallPaths
	DockerAccess         string
}

func parseHostInstallOptions(args []string, stderr io.Writer) (hostInstallOptions, error) {
	flags := flag.NewFlagSet("host install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "install the host manager system-wide")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	workspace := flags.String("workspace", "", "operator-approved workspace directory")
	releaseManifest := flags.String("bootstrap-release-manifest", "", "authenticated bootstrap release manifest")
	installPrerequisites := flags.Bool("install-prerequisites", false, "explicitly approve supported host prerequisite installation")
	allowSudoDocker := flags.Bool("allow-sudo-docker", false, "explicitly allow operator-invoked sudo Docker lifecycle commands")
	createWorkspace := flags.Bool("create-workspace", false, "explicitly approve creating a missing workspace")
	prepareWorkspace := flags.Bool("prepare-workspace", false, "explicitly approve the minimal Loki workspace POSIX ACL")
	allowSudoWorkspace := flags.Bool("allow-sudo-workspace", false, "explicitly allow sudo for approved workspace creation or ACL changes")
	jsonOutput := flags.Bool("json", false, "emit machine-readable installation result")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return hostInstallOptions{}, errors.New("usage: loki host install [--system] [--workspace PATH] [--create-workspace] [--prepare-workspace] [--allow-sudo-workspace] [--install-prerequisites] [--allow-sudo-docker] [--state-root PATH] [--json]")
	}
	result := hostInstallOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot), Workspace: strings.TrimSpace(*workspace),
		ReleaseManifest: strings.TrimSpace(*releaseManifest), InstallPrerequisites: *installPrerequisites,
		AllowSudoDocker: *allowSudoDocker, CreateWorkspace: *createWorkspace,
		PrepareWorkspace: *prepareWorkspace, AllowSudoWorkspace: *allowSudoWorkspace,
		JSON: *jsonOutput,
	}
	if result.Workspace != "" {
		if _, err := cleanInstallWorkspacePath(result.Workspace); err != nil {
			return hostInstallOptions{}, err
		}
	}
	if result.StateRoot != "" && (!filepath.IsAbs(result.StateRoot) || filepath.Clean(result.StateRoot) != result.StateRoot ||
		result.StateRoot == string(filepath.Separator) || strings.ContainsRune(result.StateRoot, 0)) {
		return hostInstallOptions{}, errors.New("--state-root must be a clean absolute non-root path")
	}
	return result, nil
}

func defaultHostStateRoot(system bool) (string, error) {
	if system {
		return defaultHostLifecycleRoot, nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) || filepath.Clean(base) != base || base == string(filepath.Separator) ||
		strings.ContainsRune(base, 0) {
		return "", errors.New("host lifecycle state base must be a clean absolute non-root path")
	}
	return filepath.Join(base, "loki", "lifecycle"), nil
}

func runHostInstall(args []string, stdout, stderr io.Writer) int {
	options, err := parseHostInstallOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system host installation requires root")
		return 1
	}
	if options.StateRoot == "" {
		options.StateRoot, err = defaultHostStateRoot(options.System)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	release, err := loadBootstrapHostRelease(options.ReleaseManifest)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = verifyRunningHostBinary(release.Generation); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options.CLIPaths, err = resolveHostCLIInstallPaths(options.System, release.Generation.ID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = preflightHostCLIInstall(options.CLIPaths); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options.PersistCLI = true

	ctx := context.Background()
	executor := execHostCommandExecutor{}
	prompter := defaultHostInstallPrompter(stdout)
	probe, err := interactiveDockerRuntime(ctx, &options, release.Runtime, prompter, executor)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options.DockerAccess = probe.Access
	if err = resolveInstallWorkspace(ctx, &options, prompter, executor); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	store, err := lifecycle.EnsureFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var runner lifecyclecompose.Runner
	if docker := strings.TrimSpace(os.Getenv("LOKI_DOCKER")); docker != "" {
		runner = lifecyclecompose.ExecRunner{Executable: docker}
	} else {
		runner, err = dockerLifecycleRunner(probe.Access)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	backend, err := newHostRuntimeBackendWithRunner(store, runner)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return runHostInstallWith(context.Background(), options, release.Generation, backend, stdout, stderr)
}

func runHostInstallWith(
	ctx context.Context,
	options hostInstallOptions,
	candidate lifecycle.Generation,
	backend lifecycle.TransactionBackend,
	stdout, stderr io.Writer,
) int {
	if backend == nil {
		fmt.Fprintln(stderr, "host runtime adapter is not configured")
		return 1
	}
	store, err := lifecycle.EnsureFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	scope := "user"
	if options.System {
		scope = "system"
	}
	installation := lifecycle.InstallationState{
		Scope: scope, Workspace: options.Workspace, DockerAccess: options.DockerAccess,
	}
	if _, statErr := os.Stat(filepath.Join(options.StateRoot, "host.json")); statErr == nil {
		snapshot, snapshotErr := store.Snapshot(ctx)
		if snapshotErr != nil {
			fmt.Fprintln(stderr, snapshotErr)
			return 1
		}
		if snapshot.Installed != nil && snapshot.Installed.ID == candidate.ID &&
			snapshot.Installation != nil && *snapshot.Installation == installation {
			if err = backend.Health(ctx); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if options.PersistCLI {
				if err = publishHostCLI(options.CLIPaths, candidate); err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
			}
			if err = writeHostInstallResult(stdout, options, candidate, "", true); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintln(stderr, statErr)
		return 1
	}
	if err = store.InitializeInstall(ctx, candidate, installation, lifecycleTimeNow()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	manager := lifecycle.Manager{Store: store, Now: lifecycleTimeNow}
	plan, err := manager.Prepare(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend, Now: lifecycleTimeNow}
	result, err := engine.Apply(ctx, lifecycle.ApplyRequest{Plan: plan})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if options.PersistCLI {
		if err = publishHostCLI(options.CLIPaths, candidate); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err = writeHostInstallResult(stdout, options, candidate, result.PlanID, false); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

var lifecycleTimeNow = func() time.Time {
	return time.Now().UTC()
}

type hostMaintenanceOptions struct {
	System          bool
	StateRoot       string
	LauncherLayout  string
	InterruptJobs   bool
	RestoreBackupID string
	Component       string
}

func parseHostMaintenanceOptions(action string, args []string, stderr io.Writer) (hostMaintenanceOptions, error) {
	flags := flag.NewFlagSet("host "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	launcherLayout := flags.String("launcher-layout", "", "launcher service layout")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	if err := flags.Parse(args); err != nil {
		return hostMaintenanceOptions{}, err
	}
	result := hostMaintenanceOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot),
		LauncherLayout: strings.TrimSpace(*launcherLayout), InterruptJobs: *interrupt,
	}
	switch action {
	case "restore":
		if flags.NArg() != 1 {
			return hostMaintenanceOptions{}, errors.New("usage: loki host restore [OPTIONS] BACKUP_ID")
		}
		result.RestoreBackupID = strings.TrimSpace(flags.Arg(0))
		if result.RestoreBackupID == "" {
			return hostMaintenanceOptions{}, errors.New("backup id is required")
		}
	case "enable", "disable":
		if flags.NArg() != 1 {
			return hostMaintenanceOptions{}, fmt.Errorf("usage: loki host %s [OPTIONS] COMPONENT", action)
		}
		result.Component = strings.TrimSpace(flags.Arg(0))
		if result.Component == "" || len(result.Component) > 128 || strings.ContainsAny(result.Component, "\r\n\x00") {
			return hostMaintenanceOptions{}, errors.New("component name is invalid")
		}
	default:
		if flags.NArg() != 0 {
			return hostMaintenanceOptions{}, fmt.Errorf("usage: loki host %s [OPTIONS]", action)
		}
	}
	for name, value := range map[string]string{
		"--state-root":      result.StateRoot,
		"--launcher-layout": result.LauncherLayout,
	} {
		if value != "" && (!filepath.IsAbs(value) || filepath.Clean(value) != value ||
			value == string(filepath.Separator) || strings.ContainsRune(value, 0)) {
			return hostMaintenanceOptions{}, fmt.Errorf("%s must be a clean absolute non-root path", name)
		}
	}
	return result, nil
}

func runHostMaintenance(action string, args []string, stdout, stderr io.Writer) int {
	options, err := parseHostMaintenanceOptions(action, args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system host lifecycle operations require root")
		return 1
	}
	if options.StateRoot == "" {
		options.StateRoot, err = defaultHostStateRoot(options.System)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if options.LauncherLayout == "" {
		if options.System {
			options.LauncherLayout = defaultLauncherLayout
		} else {
			options.LauncherLayout = filepath.Join(filepath.Dir(options.StateRoot), "launcher.json")
		}
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	backend, err := newHostRuntimeBackend(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend, Now: lifecycleTimeNow}
	manager := lifecycle.Manager{
		Store:      store,
		Jobs:       launcherJournalInventory{LayoutPath: options.LauncherLayout},
		Maintainer: engine,
		Now:        lifecycleTimeNow,
	}
	return runHostMaintenanceWith(context.Background(), manager, action, options, stdout, stderr)
}

func runHostMaintenanceWith(
	ctx context.Context,
	manager lifecycle.Manager,
	action string,
	options hostMaintenanceOptions,
	stdout, stderr io.Writer,
) int {
	mutationOptions := lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs}
	encoder := json.NewEncoder(stdout)
	switch action {
	case "backup":
		backup, err := manager.Backup(ctx, mutationOptions)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err = encoder.Encode(backup); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "restore":
		if err := manager.Restore(ctx, options.RestoreBackupID, mutationOptions); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := encoder.Encode(map[string]bool{"restored": true}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "rollback":
		if err := manager.Rollback(ctx, mutationOptions); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := encoder.Encode(map[string]bool{"rolled_back": true}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "enable":
		if err := manager.SetComponent(ctx, options.Component, true, mutationOptions); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := encoder.Encode(map[string]string{"enabled": options.Component}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "disable":
		if err := manager.SetComponent(ctx, options.Component, false, mutationOptions); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := encoder.Encode(map[string]string{"disabled": options.Component}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	case "uninstall":
		if err := manager.Uninstall(ctx, mutationOptions); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := encoder.Encode(map[string]bool{"uninstalled": true}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "unsupported host lifecycle action %q\n", action)
		return 2
	}
	return 0
}

type hostInstallRelease struct {
	Generation lifecycle.Generation
	Runtime    releases.RuntimeRequirements
}

func loadBootstrapHostGeneration(path string) (lifecycle.Generation, error) {
	release, err := loadBootstrapHostRelease(path)
	return release.Generation, err
}

func loadBootstrapHostRelease(path string) (hostInstallRelease, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return hostInstallRelease{}, errors.New("authenticated bootstrap release manifest is required")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) || strings.ContainsRune(path, 0) {
		return hostInstallRelease{}, errors.New("authenticated bootstrap release manifest path is invalid")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return hostInstallRelease{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return hostInstallRelease{}, errors.New("authenticated bootstrap release manifest must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return hostInstallRelease{}, err
	}
	if len(raw) == 0 || len(raw) > 1<<20 {
		return hostInstallRelease{}, errors.New("authenticated bootstrap release manifest exceeds size policy")
	}
	manifest, err := releases.LoadReleaseManifest(raw)
	if err != nil {
		return hostInstallRelease{}, err
	}
	generation, err := lifecycleGenerationFromRelease(manifest.Generation)
	if err != nil {
		return hostInstallRelease{}, err
	}
	return hostInstallRelease{Generation: generation, Runtime: manifest.Runtime}, nil
}

func lifecycleGenerationFromRelease(source releases.Generation) (lifecycle.Generation, error) {
	components := make([]lifecycle.Component, 0, len(source.Spec.Components))
	for _, component := range source.Spec.Components {
		components = append(components, lifecycle.Component{
			Name: component.Name, Digest: component.Digest, Optional: component.Optional,
		})
	}
	migrations := make([]lifecycle.StateTransition, 0, len(source.Spec.Migrations))
	for _, transition := range source.Spec.Migrations {
		migrations = append(migrations, lifecycle.StateTransition{
			From: transition.From, To: transition.To, Reversible: transition.Reversible,
		})
	}
	candidate, err := lifecycle.NewGeneration(lifecycle.GenerationSpec{
		Version: source.Spec.Version, ReleasedAt: source.Spec.ReleasedAt,
		HostBinaryDigest: source.Spec.HostBinaryDigest, CoreImageDigest: source.Spec.CoreImageDigest,
		Components:   components,
		ConfigSchema: source.Spec.ConfigSchema, PolicySchema: source.Spec.PolicySchema,
		ToolchainSchema: source.Spec.ToolchainSchema, StateSchema: source.Spec.StateSchema,
		Reads: lifecycle.Compatibility{
			Config:    lifecycle.SchemaRange{Min: source.Spec.Reads.Config.Min, Max: source.Spec.Reads.Config.Max},
			Policy:    lifecycle.SchemaRange{Min: source.Spec.Reads.Policy.Min, Max: source.Spec.Reads.Policy.Max},
			Toolchain: lifecycle.SchemaRange{Min: source.Spec.Reads.Toolchain.Min, Max: source.Spec.Reads.Toolchain.Max},
			State:     lifecycle.SchemaRange{Min: source.Spec.Reads.State.Min, Max: source.Spec.Reads.State.Max},
		},
		Migrations: migrations,
		Rollback: lifecycle.RollbackCoverage{
			StateSnapshot:          source.Spec.Rollback.StateSnapshot,
			ConfigSnapshot:         source.Spec.Rollback.ConfigSnapshot,
			OptionalComponentState: append([]string(nil), source.Spec.Rollback.OptionalComponentState...),
		},
	})
	if err != nil {
		return lifecycle.Generation{}, err
	}
	if candidate.ID != source.ID {
		return lifecycle.Generation{}, errors.New("authenticated release generation identity does not match lifecycle contract")
	}
	return candidate, nil
}

func verifyRunningHostBinary(candidate lifecycle.Generation) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	file, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	want := strings.TrimPrefix(candidate.Spec.HostBinaryDigest, "sha256:")
	if got != want {
		return errors.New("running host binary does not match authenticated release generation")
	}
	return nil
}
