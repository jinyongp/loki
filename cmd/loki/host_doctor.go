package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/daemon"
	hostdiagnostics "loki/internal/host/diagnostics"
	"loki/internal/host/lifecycle"
	lifecyclecompose "loki/internal/host/lifecycle/compose"
	toolchain "loki/internal/work/toolchains"
)

const defaultHostToolchainCatalog = "/usr/share/doc/loki/toolchain-catalog.json"

type hostDoctorOptions struct {
	System           bool
	StateRoot        string
	LauncherLayout   string
	ToolchainCatalog string
}

type hostDoctorRuntime interface {
	Readiness(context.Context) (lifecyclecompose.RuntimeReadiness, error)
	lifecycle.RuntimeSnapshotStorage
}

type hostDoctorRuntimeProber interface {
	DoctorProbe(context.Context) ([]byte, error)
}

type hostDoctorDependencies struct {
	OpenRuntime func(*lifecycle.FileStore) (hostDoctorRuntime, error)
	Now         func() time.Time
}

func defaultHostDoctorDependencies() hostDoctorDependencies {
	return hostDoctorDependencies{
		OpenRuntime: openHostDoctorRuntime,
		Now:         func() time.Time { return time.Now().UTC() },
	}
}

func openHostDoctorRuntime(store *lifecycle.FileStore) (hostDoctorRuntime, error) {
	if store == nil {
		return nil, errors.New("host lifecycle store is not configured")
	}
	if docker := strings.TrimSpace(os.Getenv("LOKI_DOCKER")); docker != "" {
		return lifecyclecompose.Open(lifecyclecompose.Config{
			StateRoot: store.Root,
			Runner:    lifecyclecompose.ExecRunner{Executable: docker},
		})
	}
	access := hostDockerAccessDirect
	if snapshot, err := store.Snapshot(context.Background()); err == nil && snapshot.Installation != nil &&
		snapshot.Installation.DockerAccess != "" {
		access = snapshot.Installation.DockerAccess
	}
	var runner lifecyclecompose.Runner
	switch access {
	case hostDockerAccessDirect:
		runner = lifecyclecompose.ExecRunner{Executable: "docker"}
	case hostDockerAccessSudo:
		runner = lifecyclecompose.ExecRunner{Executable: "sudo", Prefix: []string{"-n", "docker"}}
	default:
		return nil, errors.New("host lifecycle Docker access mode is invalid")
	}
	return lifecyclecompose.Open(lifecyclecompose.Config{StateRoot: store.Root, Runner: runner})
}

func parseHostDoctorOptions(args []string, stderr io.Writer) (hostDoctorOptions, error) {
	flags := flag.NewFlagSet("host doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "inspect the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	launcherLayout := flags.String("launcher-layout", "", "launcher service layout")
	toolchainCatalog := flags.String("toolchain-catalog", defaultHostToolchainCatalog, "administrator-owned managed toolchain catalog")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return hostDoctorOptions{}, errors.New("usage: loki host doctor [--system] [--state-root PATH] [--launcher-layout PATH] [--toolchain-catalog PATH]")
	}
	result := hostDoctorOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot),
		LauncherLayout:   strings.TrimSpace(*launcherLayout),
		ToolchainCatalog: strings.TrimSpace(*toolchainCatalog),
	}
	for name, value := range map[string]string{
		"--state-root": result.StateRoot, "--launcher-layout": result.LauncherLayout,
		"--toolchain-catalog": result.ToolchainCatalog,
	} {
		if value == "" {
			if name == "--toolchain-catalog" {
				return hostDoctorOptions{}, errors.New("--toolchain-catalog is required")
			}
			continue
		}
		if !filepath.IsAbs(value) || filepath.Clean(value) != value ||
			value == string(filepath.Separator) || strings.ContainsRune(value, 0) {
			return hostDoctorOptions{}, fmt.Errorf("%s must be a clean absolute non-root path", name)
		}
	}
	return result, nil
}

func resolveHostDoctorOptions(options hostDoctorOptions) (hostDoctorOptions, error) {
	var err error
	if options.StateRoot == "" {
		options.StateRoot, err = defaultHostStateRoot(options.System)
		if err != nil {
			return hostDoctorOptions{}, err
		}
	}
	if options.LauncherLayout == "" {
		if options.System {
			options.LauncherLayout = defaultLauncherLayout
		} else {
			options.LauncherLayout = filepath.Join(filepath.Dir(options.StateRoot), "launcher.json")
		}
	}
	return options, nil
}

func runHostRuntimeProbe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("host runtime-probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	launcherLayout := flags.String("launcher-layout", "/etc/loki/launcher.json", "launcher service layout")
	toolchainCatalog := flags.String("toolchain-catalog", defaultHostToolchainCatalog, "managed toolchain catalog")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: loki host runtime-probe [--launcher-layout PATH] [--toolchain-catalog PATH]")
		return 2
	}
	for name, value := range map[string]string{
		"--launcher-layout":   *launcherLayout,
		"--toolchain-catalog": *toolchainCatalog,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value ||
			value == string(filepath.Separator) || strings.ContainsRune(value, 0) {
			fmt.Fprintf(stderr, "%s must be a clean absolute non-root path\n", name)
			return 2
		}
	}

	var layout hostLauncherLayout
	if err := daemon.ReadJSON(*launcherLayout, &layout); err != nil {
		fmt.Fprintln(stderr, "launcher layout is unavailable or invalid")
		return 1
	}
	activeJobs, err := activeJobsFromLauncherLayout(context.Background(), layout)
	if err != nil {
		fmt.Fprintln(stderr, "job journal could not be validated")
		return 1
	}
	probe := hostdiagnostics.RuntimeProbe{
		ActiveJobs:        append([]string(nil), activeJobs...),
		MaxConcurrentJobs: layout.MaxConcurrentJobs,
		ToolchainChecks:   inspectDoctorToolchains(*toolchainCatalog, &layout),
	}
	if err = probe.Validate(); err != nil {
		fmt.Fprintln(stderr, "runtime diagnostic probe is invalid")
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(probe); err != nil {
		fmt.Fprintln(stderr, "cannot encode runtime diagnostic probe")
		return 1
	}
	return 0
}

func runHostDoctor(args []string, stdout, stderr io.Writer) int {
	options, err := parseHostDoctorOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options, err = resolveHostDoctorOptions(options)
	if err != nil {
		fmt.Fprintln(stderr, "cannot resolve host diagnostic paths")
		return 1
	}
	return runHostDoctorWith(context.Background(), options, defaultHostDoctorDependencies(), stdout, stderr)
}

func runHostDoctorWith(
	ctx context.Context,
	options hostDoctorOptions,
	dependencies hostDoctorDependencies,
	stdout, stderr io.Writer,
) int {
	report, err := inspectHostDoctor(ctx, options, dependencies)
	if err != nil {
		fmt.Fprintln(stderr, "host diagnostics failed")
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintln(stderr, "cannot encode host diagnostics")
		return 1
	}
	if !report.Healthy() {
		return 1
	}
	return 0
}

func inspectHostDoctor(
	ctx context.Context,
	options hostDoctorOptions,
	dependencies hostDoctorDependencies,
) (hostdiagnostics.Report, error) {
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.OpenRuntime == nil {
		dependencies.OpenRuntime = openHostDoctorRuntime
	}

	checks := make([]hostdiagnostics.Check, 0, 8)
	var (
		store            *lifecycle.FileStore
		snapshot         lifecycle.Snapshot
		snapshotReady    bool
		runtime          hostDoctorRuntime
		runtimeReadiness lifecyclecompose.RuntimeReadiness
		runtimeObserved  bool
		launcherLayout   *hostLauncherLayout
	)

	var err error
	store, err = lifecycle.OpenFileStore(options.StateRoot)
	switch {
	case err != nil:
		checks = append(checks, hostdiagnostics.Blocked(
			"host.lifecycle", "lifecycle_state_unavailable",
			"host lifecycle state is unavailable or inaccessible",
		))
	default:
		snapshot, err = store.Snapshot(ctx)
		switch {
		case err != nil:
			checks = append(checks, hostdiagnostics.Blocked(
				"host.lifecycle", "lifecycle_state_invalid",
				"host lifecycle state could not be validated",
			))
		case snapshot.Installed == nil:
			checks = append(checks, hostdiagnostics.Blocked(
				"host.lifecycle", "host_not_installed",
				"no installed host release generation is recorded",
			))
		default:
			snapshotReady = true
			checks = append(checks, hostdiagnostics.Healthy(
				"host.lifecycle", "lifecycle_ready",
				"host lifecycle state is valid",
				hostdiagnostics.Evidence{Name: "active_generation", Value: snapshot.Installed.ID},
				hostdiagnostics.Evidence{Name: "state_schema", Value: strconv.FormatUint(uint64(snapshot.Host.StateSchema), 10)},
				hostdiagnostics.Evidence{Name: "enabled_components", Value: joinedOrNone(snapshot.Host.EnabledComponents)},
			))
		}
	}

	if store == nil {
		checks = append(checks, hostdiagnostics.Blocked(
			"host.operations", "operation_state_unavailable",
			"host operation state cannot be inspected without lifecycle state",
		))
	} else {
		operations, operationErr := lifecycle.ReadOperationSnapshot(store.Root)
		if operationErr != nil {
			checks = append(checks, hostdiagnostics.Blocked(
				"host.operations", "operation_journal_invalid",
				"host operation journal could not be validated",
			))
		} else {
			active, recoveryFailed := 0, 0
			for _, operation := range operations {
				if !operation.State.Terminal() {
					active++
				}
				if operation.State == lifecycle.OperationRecoveryFailed {
					recoveryFailed++
				}
			}
			evidence := []hostdiagnostics.Evidence{
				{Name: "active_operations", Value: strconv.Itoa(active)},
				{Name: "recovery_failed", Value: strconv.Itoa(recoveryFailed)},
				{Name: "retained_operations", Value: strconv.Itoa(len(operations))},
			}
			switch {
			case recoveryFailed > 0:
				checks = append(checks, hostdiagnostics.Blocked(
					"host.operations", "recovery_failed",
					"at least one host operation has unresolved recovery failure", evidence...,
				))
			case active > 0:
				checks = append(checks, hostdiagnostics.Degraded(
					"host.operations", "operation_in_progress",
					"a host lifecycle operation is still in progress", evidence...,
				))
			default:
				checks = append(checks, hostdiagnostics.Healthy(
					"host.operations", "operations_ready",
					"host operation journal has no unresolved operation", evidence...,
				))
			}
		}
	}

	if store == nil || !snapshotReady {
		checks = append(checks, hostdiagnostics.Blocked(
			"runtime", "runtime_dependency_unavailable",
			"runtime readiness cannot be inspected without valid installed lifecycle state",
		))
	} else {
		runtime, err = dependencies.OpenRuntime(store)
		if err != nil {
			checks = append(checks, hostdiagnostics.Blocked(
				"runtime", "runtime_adapter_unavailable",
				"runtime readiness adapter is unavailable",
			))
		} else {
			runtimeReadiness, err = runtime.Readiness(ctx)
			if err != nil {
				code := "runtime_inspection_failed"
				summary := "runtime readiness could not be inspected"
				if errors.Is(err, os.ErrNotExist) {
					code = "runtime_prerequisite_missing"
					summary = "runtime state or deployment assets are missing"
				}
				checks = append(checks, hostdiagnostics.Blocked("runtime", code, summary))
			} else {
				runtimeObserved = true
				evidence := []hostdiagnostics.Evidence{
					{Name: "runtime_generation", Value: valueOrNone(runtimeReadiness.GenerationID)},
					{Name: "required_services", Value: joinedOrNone(runtimeReadiness.RequiredServices)},
					{Name: "running_services", Value: joinedOrNone(runtimeReadiness.RunningServices)},
				}
				switch {
				case !runtimeReadiness.Activated:
					checks = append(checks, hostdiagnostics.Blocked(
						"runtime", "runtime_not_activated",
						"host runtime is not activated", evidence...,
					))
				case runtimeReadiness.GenerationID != snapshot.Installed.ID:
					checks = append(checks, hostdiagnostics.Blocked(
						"runtime", "runtime_generation_mismatch",
						"runtime generation does not match the installed lifecycle generation", evidence...,
					))
				case !runtimeReadiness.Ready():
					checks = append(checks, hostdiagnostics.Blocked(
						"runtime", "runtime_services_incomplete",
						"required runtime services are not all running", evidence...,
					))
				default:
					checks = append(checks, hostdiagnostics.Healthy(
						"runtime", "runtime_ready",
						"required runtime services are running", evidence...,
					))
				}
			}
		}
	}

	probeSupported := false
	if prober, ok := runtime.(hostDoctorRuntimeProber); ok {
		probeSupported = true
		raw, probeErr := prober.DoctorProbe(ctx)
		var probe hostdiagnostics.RuntimeProbe
		if probeErr == nil {
			probeErr = json.Unmarshal(raw, &probe)
		}
		if probeErr == nil {
			probeErr = probe.Validate()
		}
		if probeErr != nil {
			checks = append(checks,
				hostdiagnostics.Blocked(
					"jobs", "runtime_probe_unavailable",
					"runtime job diagnostics could not be validated",
				),
				hostdiagnostics.Blocked(
					"toolchains", "runtime_probe_unavailable",
					"runtime toolchain diagnostics could not be validated",
				),
			)
		} else {
			checks = append(checks, jobDoctorCheck(probe.ActiveJobs, probe.MaxConcurrentJobs, nil))
			checks = append(checks, probe.ToolchainChecks...)
		}
	}

	if !probeSupported {
		var layout hostLauncherLayout
		if err = daemon.ReadJSON(options.LauncherLayout, &layout); err != nil {
			checks = append(checks, hostdiagnostics.Blocked(
				"jobs", "launcher_layout_unavailable",
				"launcher layout is unavailable or invalid",
			))
		} else {
			launcherLayout = &layout
			activeJobs, jobsErr := activeJobsFromLauncherLayout(ctx, layout)
			checks = append(checks, jobDoctorCheck(activeJobs, layout.MaxConcurrentJobs, jobsErr))
		}
		checks = append(checks, inspectDoctorToolchains(options.ToolchainCatalog, launcherLayout)...)
	}

	if store == nil || runtime == nil {
		checks = append(checks, hostdiagnostics.Blocked(
			"storage", "storage_dependency_unavailable",
			"retained storage cannot be inspected without lifecycle and runtime storage",
		))
	} else {
		usage, usageErr := store.StorageUsage(ctx, runtime)
		policy, policyErr := store.StoragePolicy()
		switch {
		case usageErr != nil || policyErr != nil:
			checks = append(checks, hostdiagnostics.Blocked(
				"storage", "storage_state_invalid",
				"retained lifecycle storage could not be validated",
			))
		default:
			evidence := []hostdiagnostics.Evidence{
				{Name: "retained_bytes", Value: strconv.FormatInt(usage.Bytes, 10)},
				{Name: "max_bytes", Value: strconv.FormatInt(policy.MaxBytes, 10)},
				{Name: "backups", Value: strconv.Itoa(usage.Backups)},
				{Name: "max_backups", Value: strconv.Itoa(policy.MaxBackups)},
				{Name: "runtime_snapshots", Value: strconv.Itoa(usage.RuntimeSnapshots)},
				{Name: "orphan_snapshots", Value: strconv.Itoa(usage.OrphanSnapshots)},
			}
			switch {
			case usage.Bytes > policy.MaxBytes || usage.Backups > policy.MaxBackups:
				checks = append(checks, hostdiagnostics.Blocked(
					"storage", "storage_over_quota",
					"retained lifecycle storage exceeds its configured budget", evidence...,
				))
			case usage.OrphanSnapshots > 0:
				checks = append(checks, hostdiagnostics.Degraded(
					"storage", "orphan_snapshots_present",
					"orphan runtime snapshots are retained and eligible for cleanup", evidence...,
				))
			default:
				checks = append(checks, hostdiagnostics.Healthy(
					"storage", "storage_ready",
					"retained lifecycle storage is within configured bounds", evidence...,
				))
			}
		}
	}

	checks = append(checks,
		integrationDoctorCheck("browser", []string{"browser", "browser-proxy"}, snapshot, snapshotReady, runtimeReadiness, runtimeObserved),
		integrationDoctorCheck("signing", []string{"signing"}, snapshot, snapshotReady, runtimeReadiness, runtimeObserved),
	)

	return hostdiagnostics.NewReport(dependencies.Now(), checks...)
}

func jobDoctorCheck(activeJobs []string, maxConcurrentJobs int, err error) hostdiagnostics.Check {
	if err != nil {
		return hostdiagnostics.Blocked(
			"jobs", "job_journal_unavailable",
			"job journal could not be validated",
		)
	}
	evidence := []hostdiagnostics.Evidence{
		{Name: "active_jobs", Value: strconv.Itoa(len(activeJobs))},
		{Name: "max_concurrent_jobs", Value: strconv.Itoa(maxConcurrentJobs)},
	}
	switch {
	case maxConcurrentJobs < 1:
		return hostdiagnostics.Blocked(
			"jobs", "launcher_concurrency_invalid",
			"launcher concurrency limit is invalid", evidence...,
		)
	case len(activeJobs) > maxConcurrentJobs:
		return hostdiagnostics.Blocked(
			"jobs", "active_jobs_over_limit",
			"active jobs exceed the configured launcher concurrency limit", evidence...,
		)
	case len(activeJobs) > 0:
		return hostdiagnostics.Degraded(
			"jobs", "active_jobs_present",
			"active jobs currently block non-interrupting host maintenance", evidence...,
		)
	default:
		return hostdiagnostics.Healthy(
			"jobs", "jobs_idle",
			"no active jobs block host maintenance", evidence...,
		)
	}
}

func inspectDoctorToolchains(catalogPath string, layout *hostLauncherLayout) []hostdiagnostics.Check {
	if layout == nil {
		return []hostdiagnostics.Check{hostdiagnostics.Blocked(
			"toolchains", "toolchain_layout_unavailable",
			"managed toolchains cannot be inspected without a valid launcher layout",
		)}
	}
	if !filepath.IsAbs(layout.ToolchainStore) || filepath.Clean(layout.ToolchainStore) != layout.ToolchainStore ||
		layout.ToolchainStore == string(filepath.Separator) {
		return []hostdiagnostics.Check{hostdiagnostics.Blocked(
			"toolchains", "toolchain_store_invalid",
			"launcher layout contains an invalid managed toolchain store",
		)}
	}
	catalog, err := loadDoctorToolchainCatalog(catalogPath)
	if err != nil {
		return []hostdiagnostics.Check{hostdiagnostics.Blocked(
			"toolchains", "toolchain_catalog_invalid",
			"managed toolchain catalog is unavailable or invalid",
		)}
	}
	ids := uniqueDoctorStrings(catalog.GenerationIDs())
	store := toolchain.GenerationStore{Root: layout.ToolchainStore, Protected: ids}
	policy, policyErr := store.StoragePolicy()
	if policyErr != nil {
		return []hostdiagnostics.Check{hostdiagnostics.Blocked(
			"toolchains", "toolchain_store_unavailable",
			"managed toolchain storage could not be validated",
		)}
	}
	usage, usageErr := store.Usage()
	storeInitialized := true
	if usageErr != nil {
		if !errors.Is(usageErr, os.ErrNotExist) {
			return []hostdiagnostics.Check{hostdiagnostics.Blocked(
				"toolchains", "toolchain_store_unavailable",
				"managed toolchain storage could not be validated",
			)}
		}
		storeInitialized = false
		if info, statErr := os.Lstat(layout.ToolchainStore); statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return []hostdiagnostics.Check{hostdiagnostics.Blocked(
					"toolchains", "toolchain_store_unavailable",
					"managed toolchain storage could not be validated",
				)}
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return []hostdiagnostics.Check{hostdiagnostics.Blocked(
				"toolchains", "toolchain_store_unavailable",
				"managed toolchain storage could not be validated",
			)}
		}
		usage = toolchain.GenerationUsage{}
	}
	provisioned := 0
	unprovisioned := 0
	for _, id := range ids {
		if _, lookupErr := store.Lookup(id); lookupErr == nil {
			provisioned++
		} else if errors.Is(lookupErr, os.ErrNotExist) {
			unprovisioned++
		} else {
			return []hostdiagnostics.Check{hostdiagnostics.Blocked(
				"toolchains", "toolchain_generation_invalid",
				"an installed managed toolchain generation could not be validated",
			)}
		}
	}
	evidence := []hostdiagnostics.Evidence{
		{Name: "catalog_generations", Value: strconv.Itoa(len(ids))},
		{Name: "provisioned_catalog_generations", Value: strconv.Itoa(provisioned)},
		{Name: "unprovisioned_catalog_generations", Value: strconv.Itoa(unprovisioned)},
		{Name: "installed_generations", Value: strconv.Itoa(usage.Generations)},
		{Name: "referenced_generations", Value: strconv.Itoa(usage.Referenced)},
		{Name: "retained_bytes", Value: strconv.FormatInt(usage.Bytes, 10)},
		{Name: "max_bytes", Value: strconv.FormatInt(policy.MaxBytes, 10)},
		{Name: "max_generations", Value: strconv.Itoa(policy.MaxGenerations)},
		{Name: "store_initialized", Value: strconv.FormatBool(storeInitialized)},
	}
	if usage.Bytes > policy.MaxBytes || usage.Generations > policy.MaxGenerations {
		return []hostdiagnostics.Check{hostdiagnostics.Blocked(
			"toolchains", "toolchain_storage_over_quota",
			"managed toolchain storage exceeds its configured budget", evidence...,
		)}
	}
	return []hostdiagnostics.Check{hostdiagnostics.Healthy(
		"toolchains", "toolchains_ready",
		"managed toolchain catalog and storage state are valid", evidence...,
	)}
}

func loadDoctorToolchainCatalog(path string) (toolchain.Catalog, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return toolchain.Catalog{}, errors.New("toolchain catalog path is invalid")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return toolchain.Catalog{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4<<20 {
		return toolchain.Catalog{}, errors.New("toolchain catalog file is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return toolchain.Catalog{}, errors.New("toolchain catalog exceeds diagnostic read limit")
	}
	return toolchain.LoadCatalog(raw)
}

func integrationDoctorCheck(
	name string,
	requiredServices []string,
	snapshot lifecycle.Snapshot,
	snapshotReady bool,
	readiness lifecyclecompose.RuntimeReadiness,
	runtimeObserved bool,
) hostdiagnostics.Check {
	checkName := "integration." + name
	if !snapshotReady {
		return hostdiagnostics.Blocked(
			checkName, name+"_state_unavailable",
			name+" integration cannot be inspected without valid lifecycle state",
		)
	}
	enabled := false
	for _, component := range snapshot.Host.EnabledComponents {
		if component == name {
			enabled = true
			break
		}
	}
	if !enabled {
		return hostdiagnostics.Healthy(
			checkName, name+"_disabled",
			name+" integration is not enabled",
			hostdiagnostics.Evidence{Name: "enabled", Value: "false"},
		)
	}
	if !runtimeObserved {
		return hostdiagnostics.Blocked(
			checkName, name+"_runtime_unavailable",
			name+" integration is enabled but runtime readiness is unavailable",
			hostdiagnostics.Evidence{Name: "enabled", Value: "true"},
		)
	}
	running := map[string]bool{}
	for _, service := range readiness.RunningServices {
		running[service] = true
	}
	missing := make([]string, 0, len(requiredServices))
	for _, service := range requiredServices {
		if !running[service] {
			missing = append(missing, service)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return hostdiagnostics.Blocked(
			checkName, name+"_services_incomplete",
			name+" integration is enabled but required services are not all running",
			hostdiagnostics.Evidence{Name: "enabled", Value: "true"},
			hostdiagnostics.Evidence{Name: "missing_services", Value: strings.Join(missing, ",")},
		)
	}
	return hostdiagnostics.Healthy(
		checkName, name+"_ready",
		name+" integration is enabled and its runtime services are running",
		hostdiagnostics.Evidence{Name: "enabled", Value: "true"},
	)
}

func valueOrNone(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "none"
	}
	return value
}

func joinedOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	return strings.Join(copy, ",")
}

func uniqueDoctorStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	result := copy[:1]
	for _, value := range copy[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
