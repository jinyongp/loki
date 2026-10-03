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
	"slices"
	"strings"

	"loki/internal/integrations/github/setup"
	"loki/internal/host/lifecycle"
	lifecyclecompose "loki/internal/host/lifecycle/compose"
	"loki/internal/progress"
)

var hostIntegrationStdin io.Reader = os.Stdin

type hostIntegrationReport struct {
	SchemaVersion        int                     `json:"schema_version"`
	Name                 string                  `json:"name"`
	Supported            bool                    `json:"supported"`
	Configured           bool                    `json:"configured"`
	Enabled              bool                    `json:"enabled"`
	Ready                bool                    `json:"ready"`
	State                string                  `json:"state"`
	ProjectionConsistent bool                    `json:"projection_consistent"`
	RequiredServices     []string                `json:"required_services,omitempty"`
	RunningServices      []string                `json:"running_services,omitempty"`
	PublicKey            string                  `json:"public_key,omitempty"`
	Fingerprint          string                  `json:"fingerprint,omitempty"`
	IdentityName         string                  `json:"identity_name,omitempty"`
	IdentityEmail        string                  `json:"identity_email,omitempty"`
	GitHubAppID          int64                   `json:"github_app_id,omitempty"`
	TargetCount          int                     `json:"target_count,omitempty"`
	Authentication       string                  `json:"authentication,omitempty"`
	Detail               string                  `json:"detail,omitempty"`
	AppReady             bool                    `json:"app_ready,omitempty"`
	PersonalProjects     *githubsetup.UserStatus `json:"personal_projects,omitempty"`
}

type hostIntegrationOptions struct {
	System           bool
	StateRoot        string
	LauncherLayout   string
	JSON             bool
	InterruptJobs    bool
	PersonalProjects bool
}

func parseHostIntegrationOptions(action string, args []string, stderr io.Writer) (hostIntegrationOptions, string, error) {
	flags := flag.NewFlagSet("host integration "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	launcherLayout := flags.String("launcher-layout", "", "launcher service layout")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	personalProjects := flags.Bool("personal-projects", false, "inspect optional personal Projects authorization")
	if err := flags.Parse(args); err != nil {
		return hostIntegrationOptions{}, "", err
	}
	if action != "enable" && action != "disable" && action != "remove" && *interrupt {
		return hostIntegrationOptions{}, "", errors.New("--interrupt-active-jobs is valid only for enable, disable, or remove")
	}
	result := hostIntegrationOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot),
		LauncherLayout: strings.TrimSpace(*launcherLayout), JSON: *jsonOutput, InterruptJobs: *interrupt,
		PersonalProjects: *personalProjects,
	}
	if result.PersonalProjects && ((action != "status" && action != "doctor") || flags.NArg() != 1 || flags.Arg(0) != "github") {
		return hostIntegrationOptions{}, "", errors.New("--personal-projects is valid only for status or doctor github")
	}
	for name, value := range map[string]string{"--state-root": result.StateRoot, "--launcher-layout": result.LauncherLayout} {
		if value != "" && (!filepath.IsAbs(value) || filepath.Clean(value) != value ||
			value == string(filepath.Separator) || strings.ContainsRune(value, 0)) {
			return hostIntegrationOptions{}, "", fmt.Errorf("%s must be a clean absolute non-root path", name)
		}
	}
	if action == "list" {
		if flags.NArg() != 0 {
			return hostIntegrationOptions{}, "", errors.New("usage: loki host integration list [OPTIONS]")
		}
		return result, "", nil
	}
	if flags.NArg() != 1 {
		return hostIntegrationOptions{}, "", fmt.Errorf("usage: loki host integration %s [OPTIONS] NAME", action)
	}
	name := strings.TrimSpace(flags.Arg(0))
	switch name {
	case "browser", "signing", "github":
	default:
		return hostIntegrationOptions{}, "", errors.New("integration name must be browser, signing, or github")
	}
	return result, name, nil
}

func resolveHostIntegrationOptions(options hostIntegrationOptions) (hostIntegrationOptions, error) {
	var err error
	if options.StateRoot == "" {
		options.StateRoot, err = defaultHostStateRoot(options.System)
		if err != nil {
			return hostIntegrationOptions{}, err
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

func runHostIntegration(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: loki host integration list|status|setup|import|rotate|refresh|logout|enable|disable|remove|doctor ...")
		return 2
	}
	action := args[0]
	if action == "refresh" {
		return runHostGitHubRefresh(args[1:], stdout, stderr)
	}
	if action == "login" || action == "logout" {
		return runHostGitHubUser(action, args[1:], stdout, stderr)
	}
	if action == "setup" || action == "rotate" || action == "import" {
		if len(args) < 2 {
			fmt.Fprintf(stderr, "usage: loki host integration %s [OPTIONS] signing|github\n", action)
			return 2
		}
		switch args[len(args)-1] {
		case "signing":
			if action == "import" {
				fmt.Fprintln(stderr, "integration import supports github only")
				return 2
			}
			return runHostSigningSetup(action, args[1:], stdout, stderr)
		case "github":
			return runHostGitHubSetup(action, args[1:], stdout, stderr)
		default:
			fmt.Fprintf(stderr, "usage: loki host integration %s [OPTIONS] signing|github\n", action)
			return 2
		}
	}
	switch action {
	case "list", "status", "enable", "disable", "remove", "doctor":
	default:
		fmt.Fprintln(stderr, "usage: loki host integration list|status|setup|import|rotate|refresh|logout|enable|disable|remove|doctor ...")
		return 2
	}
	options, name, err := parseHostIntegrationOptions(action, args[1:], stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options, err = resolveHostIntegrationOptions(options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "Loki is not installed for this scope.")
		return 1
	}
	ctx := context.Background()
	var reporter progress.Reporter
	if action != "list" && action != "status" {
		var stopHeartbeat func()
		reporter, stopHeartbeat = startHostIntegrationProgress(ctx, stderr, action, name)
		defer stopHeartbeat()
	}
	backend, err := newHostComposeBackend(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	backend.Progress = reporter
	manager := lifecycle.Manager{
		Store: store, Jobs: backend, Maintainer: &lifecycle.TransactionEngine{
			Store: store, Backend: backend, Now: lifecycleTimeNow, Progress: reporter,
		}, Now: lifecycleTimeNow,
	}

	if action == "enable" || action == "disable" {
		enabled := action == "enable"
		switch name {
		case "browser", "signing":
			err = manager.SetComponent(ctx, name, enabled, lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs})
		case "github":
			err = toggleManagedGitHub(ctx, manager, store, enabled, lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs}, reporter)
		default:
			err = errors.New("integration name is invalid")
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if action == "remove" {
		switch name {
		case "signing":
			err = removeManagedSigning(ctx, manager, store, lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs}, lifecycleTimeNow)
		case "github":
			err = removeManagedGitHub(ctx, manager, store, lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs})
		default:
			err = fmt.Errorf("%s integration removal is not available", name)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if action == "list" {
		reports := make([]hostIntegrationReport, 0, 3)
		for _, integration := range []string{"browser", "signing", "github"} {
			report, reportErr := inspectHostIntegration(ctx, store, backend, integration)
			if reportErr != nil {
				fmt.Fprintln(stderr, reportErr)
				return 1
			}
			reports = append(reports, report)
		}
		if options.JSON {
			if err = json.NewEncoder(stdout).Encode(map[string]any{"schema_version": 1, "integrations": reports}); err != nil {
				fmt.Fprintln(stderr, "cannot encode integration list")
				return 1
			}
			return 0
		}
		fmt.Fprintln(stdout, "Loki integrations")
		for _, report := range reports {
			fmt.Fprintf(stdout, "  %s: %s\n", report.Name, report.State)
		}
		return 0
	}

	report, err := inspectHostIntegration(ctx, store, backend, name, options.PersonalProjects)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if action == "doctor" && name == "github" && report.Configured && report.Enabled {
		progress.Emit(reporter, progress.Event{
			Operation: "integration", Phase: "validate-github", State: progress.StateStarted,
			Level:   progress.LevelSummary,
			Message: "Checking GitHub App authentication and repository access...",
		})
		candidate, loadErr := loadManagedGitHubFromStore(ctx, store)
		if loadErr != nil {
			report.AppReady = false
			report.Ready = false
			report.State = "degraded"
			report.Detail = loadErr.Error()
		} else {
			defer clear(candidate.KeyRaw)
			if validateErr := validateManagedGitHubCandidate(ctx, candidate, nil); validateErr != nil {
				report.AppReady = false
				report.Ready = false
				report.State = "degraded"
				report.Detail = "GitHub App validation failed: " + validateErr.Error()
			}
		}
	}
	if options.JSON {
		if err = json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintln(stderr, "cannot encode integration status")
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "Loki integration: %s\n", report.Name)
		fmt.Fprintf(stdout, "  State: %s\n", report.State)
		fmt.Fprintf(stdout, "  Configured: %t\n", report.Configured)
		fmt.Fprintf(stdout, "  Enabled: %t\n", report.Enabled)
		fmt.Fprintf(stdout, "  Ready: %t\n", report.Ready)
		if report.GitHubAppID != 0 {
			fmt.Fprintf(stdout, "  App ID: %d\n", report.GitHubAppID)
			fmt.Fprintf(stdout, "  Repositories: %d\n", report.TargetCount)
			fmt.Fprintf(stdout, "  App ready: %t\n", report.AppReady)
		}
		if report.PersonalProjects != nil {
			githubsetup.RenderUserStatus(stdout, *report.PersonalProjects)
		}
		if report.Detail != "" {
			fmt.Fprintf(stdout, "  Detail: %s\n", report.Detail)
		}
	}
	if action == "doctor" && report.Enabled && !report.Ready {
		return 1
	}
	return 0
}

type hostIntegrationRuntime interface {
	Readiness(context.Context) (lifecyclecompose.RuntimeReadiness, error)
}

func inspectHostIntegration(
	ctx context.Context,
	store *lifecycle.FileStore,
	backend hostIntegrationRuntime,
	name string,
	personalProjects ...bool,
) (hostIntegrationReport, error) {
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return hostIntegrationReport{}, err
	}
	managed, err := store.ReadManagedIntegrations(ctx)
	if err != nil {
		return hostIntegrationReport{}, err
	}
	report := hostIntegrationReport{SchemaVersion: 1, Name: name, Supported: true, ProjectionConsistent: true}
	switch name {
	case "browser":
		report.Configured = true
		report.Enabled = slices.Contains(snapshot.Host.EnabledComponents, "browser")
		report.ProjectionConsistent = managed.Revision == "" || managed.Browser.Enabled == report.Enabled
		report.RequiredServices = []string{"browser", "browser-proxy"}
		if !report.Enabled {
			report.State = "disabled"
			return report, nil
		}
		readiness, readyErr := backend.Readiness(ctx)
		if readyErr != nil {
			report.State = "degraded"
			return report, nil
		}
		report.RunningServices = append([]string(nil), readiness.RunningServices...)
		report.Ready = report.ProjectionConsistent &&
			slices.Contains(readiness.RunningServices, "browser") &&
			slices.Contains(readiness.RunningServices, "browser-proxy")
		if report.Ready {
			report.State = "ready"
		} else {
			report.State = "degraded"
		}
	case "signing":
		report.Configured = managed.Signing.Configured
		report.Enabled = slices.Contains(snapshot.Host.EnabledComponents, "signing")
		report.ProjectionConsistent = managed.Revision == "" || managed.Signing.Enabled == report.Enabled
		if !report.Configured {
			report.State = "unconfigured"
			break
		}
		info, infoErr := store.ReadManagedSigningPublicInfo(ctx)
		if infoErr != nil {
			report.State = "degraded"
			break
		}
		report.PublicKey = info.PublicKey
		report.Fingerprint = info.Fingerprint
		report.IdentityName = info.IdentityName
		report.IdentityEmail = info.IdentityEmail
		if !report.Enabled {
			report.State = "disabled"
			break
		}
		readiness, readyErr := backend.Readiness(ctx)
		if readyErr != nil {
			report.State = "degraded"
			break
		}
		report.RequiredServices = []string{"signing"}
		report.RunningServices = append([]string(nil), readiness.RunningServices...)
		report.Ready = report.ProjectionConsistent && slices.Contains(readiness.RunningServices, "signing")
		if report.Ready {
			report.State = "ready"
		} else {
			report.State = "degraded"
		}
	case "github":
		report.Configured = managed.GitHub.Configured
		report.Enabled = managed.GitHub.Enabled
		report.Authentication = "GitHub App installation tokens"
		if !report.Configured {
			report.State = "unconfigured"
			break
		}
		_, githubConfig, loadErr := loadManagedGitHubPublicConfig(ctx, store)
		if loadErr != nil {
			report.State = "degraded"
			report.Detail = loadErr.Error()
			break
		}
		report.GitHubAppID = githubConfig.GitHubAppID
		report.TargetCount = len(githubConfig.GitHubTargets)
		personalAccounts := []string{}
		for _, installation := range githubConfig.GitHubInstallations {
			if installation.AccountType == "user" {
				personalAccounts = append(personalAccounts, installation.Account)
			}
		}
		inspectPersonal := len(personalProjects) != 0 && personalProjects[0]
		report.PersonalProjects = &githubsetup.UserStatus{Status: "not_requested"}
		if inspectPersonal && len(personalAccounts) == 0 {
			report.PersonalProjects.Status = "not_required"
		} else if inspectPersonal {
			report.PersonalProjects.Status = "unavailable"
		}
		if !report.Enabled {
			report.State = "disabled"
			break
		}
		readiness, readyErr := backend.Readiness(ctx)
		if readyErr != nil {
			report.State = "degraded"
			report.Detail = "runtime readiness is unavailable"
			break
		}
		report.RunningServices = append([]string(nil), readiness.RunningServices...)
		report.AppReady = readiness.Ready()
		report.Ready = report.AppReady
		if report.Ready {
			report.State = "ready"
		} else {
			report.State = "degraded"
			report.Detail = "core runtime services are not ready"
		}
		if inspectPersonal && report.AppReady && len(personalAccounts) != 0 {
			inspectGitHubUserStatus(ctx, backend, personalAccounts, &report)
		}
	default:
		return hostIntegrationReport{}, errors.New("integration name is invalid")
	}
	return report, nil
}
