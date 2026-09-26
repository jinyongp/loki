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
	"strings"

	"golang.org/x/sys/unix"

	"loki/internal/host/lifecycle"
)

const defaultMCPLocalOriginURL = "http://127.0.0.1:18765/mcp"

type hostInfoOptions struct {
	System    bool
	StateRoot string
	JSON      bool
}

type hostStatusReport struct {
	State             string   `json:"state"`
	Scope             string   `json:"scope,omitempty"`
	Workspace         string   `json:"workspace,omitempty"`
	Release           string   `json:"release,omitempty"`
	GenerationID      string   `json:"generation_id,omitempty"`
	DockerAccess      string   `json:"docker_access,omitempty"`
	EnabledComponents []string `json:"enabled_components,omitempty"`
	UpdateAvailable   bool     `json:"update_available"`
	UpdatePrepared    bool     `json:"update_prepared"`
}

type hostConnectionAuthenticationReport struct {
	Type      string `json:"type"`
	TokenFile string `json:"token_file"`
}

type hostLocalOriginReport struct {
	URL            string                             `json:"url"`
	Transport      string                             `json:"transport"`
	Reachability   string                             `json:"reachability"`
	Authentication hostConnectionAuthenticationReport `json:"authentication"`
}

type hostConnectionReport struct {
	SchemaVersion int                   `json:"schema_version"`
	LocalOrigin   hostLocalOriginReport `json:"local_origin"`
}

type hostInstallResult struct {
	Installed        bool   `json:"installed"`
	AlreadyInstalled bool   `json:"already_installed"`
	Release          string `json:"release"`
	GenerationID     string `json:"generation_id"`
	Workspace        string `json:"workspace"`
	CLI              string `json:"cli,omitempty"`
	LocalOrigin      string `json:"local_origin"`
	PlanID           string `json:"plan_id,omitempty"`
}

func parseHostInfoOptions(action string, args []string, stderr io.Writer) (hostInfoOptions, error) {
	flags := flag.NewFlagSet("host "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "inspect the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return hostInfoOptions{}, fmt.Errorf("usage: loki host %s [--system] [--state-root PATH] [--json]", action)
	}
	result := hostInfoOptions{System: *system, StateRoot: strings.TrimSpace(*stateRoot), JSON: *jsonOutput}
	if result.StateRoot != "" && (!filepath.IsAbs(result.StateRoot) || filepath.Clean(result.StateRoot) != result.StateRoot ||
		result.StateRoot == string(filepath.Separator) || strings.ContainsRune(result.StateRoot, 0)) {
		return hostInfoOptions{}, errors.New("--state-root must be a clean absolute non-root path")
	}
	return result, nil
}

func resolveHostInfoOptions(options hostInfoOptions) (hostInfoOptions, error) {
	if options.StateRoot != "" {
		return options, nil
	}
	root, err := defaultHostStateRoot(options.System)
	if err != nil {
		return hostInfoOptions{}, err
	}
	options.StateRoot = root
	return options, nil
}

func runHostStatus(args []string, stdout, stderr io.Writer) int {
	options, err := parseHostInfoOptions("status", args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options, err = resolveHostInfoOptions(options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "Loki is not installed for this scope.")
		return 1
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "Loki host state could not be validated.")
		return 1
	}
	report, err := hostStatusFromSnapshot(snapshot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if options.JSON {
		if err = json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintln(stderr, "cannot encode host status")
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, "Loki host")
	fmt.Fprintf(stdout, "  Status: %s\n", report.State)
	if report.Release != "" {
		fmt.Fprintf(stdout, "  Release: %s\n", report.Release)
	}
	if report.Workspace != "" {
		fmt.Fprintf(stdout, "  Workspace: %s\n", report.Workspace)
	}
	if report.DockerAccess != "" {
		fmt.Fprintf(stdout, "  Docker: %s\n", report.DockerAccess)
	}
	if len(report.EnabledComponents) == 0 {
		fmt.Fprintln(stdout, "  Optional components: none")
	} else {
		fmt.Fprintf(stdout, "  Optional components: %s\n", strings.Join(report.EnabledComponents, ", "))
	}
	if report.UpdateAvailable {
		if report.UpdatePrepared {
			fmt.Fprintln(stdout, "  Update: prepared")
		} else {
			fmt.Fprintln(stdout, "  Update: available")
		}
	} else {
		fmt.Fprintln(stdout, "  Update: none prepared")
	}
	return 0
}

func hostStatusFromSnapshot(snapshot lifecycle.Snapshot) (hostStatusReport, error) {
	report := hostStatusReport{
		State:             "not-installed",
		EnabledComponents: append([]string(nil), snapshot.Host.EnabledComponents...),
		UpdateAvailable:   snapshot.Available != nil && (snapshot.Installed == nil || snapshot.Available.ID != snapshot.Installed.ID),
		UpdatePrepared:    snapshot.Prepared != nil,
	}
	if snapshot.Installation != nil {
		if !snapshot.Installation.Valid() {
			return hostStatusReport{}, errors.New("Loki installation state is invalid")
		}
		report.Scope = snapshot.Installation.Scope
		report.Workspace = snapshot.Installation.Workspace
		report.DockerAccess = snapshot.Installation.DockerAccess
	}
	if snapshot.Installed != nil {
		if !snapshot.Installed.Valid() {
			return hostStatusReport{}, errors.New("Loki installed release state is invalid")
		}
		report.State = "installed"
		report.Release = snapshot.Installed.Spec.Version
		report.GenerationID = snapshot.Installed.ID
	} else if snapshot.Available != nil {
		report.State = "install-pending"
	}
	return report, nil
}

func runHostConnection(args []string, stdout, stderr io.Writer) int {
	options, err := parseHostInfoOptions("connection", args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options, err = resolveHostInfoOptions(options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "Loki is not installed for this scope.")
		return 1
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || snapshot.Installed == nil || snapshot.Installation == nil {
		fmt.Fprintln(stderr, "Loki does not have an active installed release.")
		return 1
	}
	tokenFile := filepath.Join(options.StateRoot, "mcp-token")
	if err = validateConnectionTokenFile(tokenFile); err != nil {
		fmt.Fprintln(stderr, "Loki MCP authentication state is unavailable.")
		return 1
	}
	report := hostConnectionReport{
		SchemaVersion: 1,
		LocalOrigin: hostLocalOriginReport{
			URL: defaultMCPLocalOriginURL, Transport: "streamable-http", Reachability: "loopback",
			Authentication: hostConnectionAuthenticationReport{Type: "bearer-token-file", TokenFile: tokenFile},
		},
	}
	if options.JSON {
		if err = json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintln(stderr, "cannot encode host connection information")
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, "Loki MCP local origin")
	fmt.Fprintf(stdout, "  URL: %s\n", report.LocalOrigin.URL)
	fmt.Fprintln(stdout, "  Reachability: loopback only")
	fmt.Fprintln(stdout, "  Transport: Streamable HTTP")
	fmt.Fprintln(stdout, "  Authentication: Bearer token")
	fmt.Fprintf(stdout, "  Token file: %s\n", report.LocalOrigin.Authentication.TokenFile)
	fmt.Fprintln(stdout, "This is a local origin, not a public MCP URL.")
	fmt.Fprintln(stdout, "Expose it with a tunnel, reverse proxy, VPN, or gateway of your choice if remote access is required.")
	fmt.Fprintln(stdout, "Loki does not create or manage a public MCP endpoint.")
	return 0
}

func writeHostInstallResult(
	stdout io.Writer,
	options hostInstallOptions,
	candidate lifecycle.Generation,
	planID string,
	alreadyInstalled bool,
) error {
	cli := ""
	if options.PersistCLI {
		cli = options.CLIPaths.Link
	}
	report := hostInstallResult{
		Installed: true, AlreadyInstalled: alreadyInstalled,
		Release: candidate.Spec.Version, GenerationID: candidate.ID,
		Workspace: options.Workspace, CLI: cli,
		LocalOrigin: defaultMCPLocalOriginURL, PlanID: planID,
	}
	if options.JSON {
		return json.NewEncoder(stdout).Encode(report)
	}
	if alreadyInstalled {
		fmt.Fprintln(stdout, "Loki is already installed.")
	} else {
		fmt.Fprintln(stdout, "Loki installed successfully.")
	}
	fmt.Fprintf(stdout, "  Release: %s\n", report.Release)
	fmt.Fprintf(stdout, "  Workspace: %s\n", report.Workspace)
	fmt.Fprintf(stdout, "  MCP local origin: %s\n", report.LocalOrigin)
	fmt.Fprintln(stdout, "  External access: user-managed")

	commandCLI := "loki"
	if report.CLI != "" {
		fmt.Fprintf(stdout, "  CLI: %s\n", report.CLI)
		commandCLI = shellQuote(report.CLI)
	}
	connectionCommand := commandCLI + " host connection"
	doctorCommand := commandCLI + " host doctor"
	if options.System {
		connectionCommand += " --system"
		doctorCommand += " --system"
	}
	fmt.Fprintf(stdout, "Connection details: %s\n", connectionCommand)
	fmt.Fprintf(stdout, "Health check: %s\n", doctorCommand)
	return nil
}

func validateConnectionTokenFile(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("MCP token file must be a private regular file")
	}
	return nil
}
