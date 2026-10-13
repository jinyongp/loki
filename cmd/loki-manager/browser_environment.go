package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
	"loki/internal/management"
	"loki/internal/progress"
	"loki/internal/tools"
	"loki/modules/browser"
)

type browserEnvironmentReport struct {
	Schema                int      `json:"schema"`
	Applicable            bool     `json:"applicable"`
	Supported             bool     `json:"supported"`
	Missing               []string `json:"missing_packages"`
	AdministratorRequired bool     `json:"administrator_required"`
}

type browserEnvironmentDependencies struct {
	os       string
	uid      func() int
	readFile func(string) ([]byte, error)
	run      func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
}

func defaultBrowserEnvironmentDependencies() browserEnvironmentDependencies {
	return browserEnvironmentDependencies{os: runtime.GOOS, uid: os.Geteuid, readFile: os.ReadFile, run: func(ctx context.Context, binary string, args []string, input io.Reader, out, diagnostics io.Writer) error {
		command := exec.CommandContext(ctx, binary, args...)
		command.Stdin, command.Stdout, command.Stderr = input, out, diagnostics
		return command.Run()
	}}
}

// Chromium's native package contract follows Playwright's official dependency
// list: microsoft/playwright/packages/playwright-core/src/server/registry/nativeDeps.ts.
// Bundled Chrome for Testing also requires libatomic.so.1; this contract must
// be checked with the managed Chrome, not only Playwright Chromium.
// Package names are fixed inputs; no package or elevated command comes from MCP.
func browserSystemPackages(release []byte) ([]string, bool) {
	values := map[string]string{}
	for _, line := range strings.Split(string(release), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	majorText, _, _ := strings.Cut(values["VERSION_ID"], ".")
	major, err := strconv.Atoi(majorText)
	if err != nil || values["ID"] != "ubuntu" && values["ID"] != "debian" {
		return nil, false
	}
	if values["ID"] == "ubuntu" && major < 22 || values["ID"] == "debian" && major < 12 {
		return nil, false
	}
	packages := []string{"libatomic1", "libasound2", "libatk-bridge2.0-0", "libatk1.0-0", "libatspi2.0-0", "libcairo2", "libcups2", "libdbus-1-3", "libdrm2", "libgbm1", "libglib2.0-0", "libnspr4", "libnss3", "libpango-1.0-0", "libwayland-client0", "libx11-6", "libxcb1", "libxcomposite1", "libxdamage1", "libxext6", "libxfixes3", "libxkbcommon0", "libxrandr2", "fonts-liberation", "fonts-noto-color-emoji"}
	if values["ID"] == "ubuntu" && major >= 24 || values["ID"] == "debian" && major >= 13 {
		for i, name := range packages {
			if slices.Contains([]string{"libasound2", "libatk-bridge2.0-0", "libatk1.0-0", "libatspi2.0-0", "libcups2", "libglib2.0-0"}, name) {
				packages[i] += "t64"
			}
		}
	}
	return packages, true
}

func inspectBrowserEnvironment(ctx context.Context, deps browserEnvironmentDependencies) (browserEnvironmentReport, error) {
	report := browserEnvironmentReport{Schema: 1, Missing: []string{}}
	if deps.os != "linux" {
		return report, nil
	}
	report.Applicable = true
	release, err := deps.readFile("/etc/os-release")
	if err != nil {
		return report, fmt.Errorf("read browser execution-host distribution: %w", err)
	}
	packages, supported := browserSystemPackages(release)
	report.Supported = supported
	if !supported {
		return report, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var output githubRelayOutput
	err = deps.run(probeCtx, "/usr/bin/dpkg-query", append([]string{"-W", "-f=${Package} ${db:Status-Status}\n"}, packages...), nil, &output, io.Discard)
	if err != nil {
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 1 {
			return report, fmt.Errorf("check browser system packages: %w", err)
		}
	}
	if output.overflow {
		return report, fmt.Errorf("browser package inspection exceeds its bound")
	}
	installed := map[string]bool{}
	for _, line := range strings.Split(output.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "installed" {
			installed[fields[0]] = true
		}
	}
	for _, name := range packages {
		if !installed[name] {
			report.Missing = append(report.Missing, name)
		}
	}
	if len(report.Missing) > 0 && deps.uid() != 0 {
		report.AdministratorRequired = deps.run(probeCtx, "/usr/bin/sudo", []string{"-n", "true"}, nil, io.Discard, io.Discard) != nil
	}
	return report, nil
}

func prepareBrowserEnvironment(ctx context.Context, password []byte, diagnostics io.Writer, deps browserEnvironmentDependencies) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	report, err := inspectBrowserEnvironment(ctx, deps)
	if err != nil {
		return err
	}
	if !report.Applicable {
		return nil
	}
	if !report.Supported {
		fmt.Fprintln(diagnostics, "Automatic browser library preparation supports Ubuntu 22.04+ and Debian 12+. Existing system libraries are retained; use loki doctor to verify this host.")
		return nil
	}
	if len(report.Missing) == 0 {
		return nil
	}
	if report.AdministratorRequired {
		if password == nil {
			return fmt.Errorf("browser system packages need administrator authentication; retry loki setup in a terminal")
		}
		if len(password) > 4096 || bytes.ContainsAny(password, "\x00\r\n") {
			return fmt.Errorf("invalid bounded administrator input")
		}
		material := append(append([]byte{}, password...), '\n')
		defer clear(material)
		if err := deps.run(ctx, "/usr/bin/sudo", []string{"-S", "-p", "", "-v"}, bytes.NewReader(material), diagnostics, diagnostics); err != nil {
			return fmt.Errorf("browser dependency administrator authentication failed: %w", err)
		}
	}
	fmt.Fprintln(diagnostics, "Installing missing browser system packages:", strings.Join(report.Missing, ", "))
	stop := progress.StartHeartbeat(ctx, progress.NewLineReporter(diagnostics), progress.HeartbeatOptions{Operation: "setup", Phase: "browser-libraries", Message: "Still preparing browser system libraries"})
	defer stop()
	for _, args := range [][]string{{"-o", "DPkg::Lock::Timeout=120", "update"}, append([]string{"-o", "DPkg::Lock::Timeout=120", "install", "--yes", "--no-install-recommends", "--"}, report.Missing...)} {
		binary := "/usr/bin/env"
		args = append([]string{"DEBIAN_FRONTEND=noninteractive", "/usr/bin/apt-get"}, args...)
		if deps.uid() != 0 {
			args = append([]string{"-n", "--", binary}, args...)
			binary = "/usr/bin/sudo"
		}
		if err := deps.run(ctx, binary, args, nil, diagnostics, diagnostics); err != nil {
			return fmt.Errorf("browser dependency installation failed; retry loki setup to resume: %w", err)
		}
	}
	verified, err := inspectBrowserEnvironment(ctx, deps)
	if err != nil {
		return err
	}
	if len(verified.Missing) > 0 {
		return fmt.Errorf("browser system packages remain missing: %s", strings.Join(verified.Missing, ", "))
	}
	return nil
}

func browserAdministratorPassword(report browserEnvironmentReport, input io.Reader, diagnostics io.Writer) ([]byte, error) {
	if !report.AdministratorRequired {
		return nil, nil
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return nil, fmt.Errorf("browser system packages need administrator authentication; retry loki setup in an interactive terminal")
	}
	fmt.Fprintln(diagnostics, "Missing browser system packages:", strings.Join(report.Missing, ", "))
	fmt.Fprint(diagnostics, "[INPUT REQUIRED] Administrator password for browser system packages (hidden): ")
	password, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(diagnostics)
	return password, err
}

func prepareLocalBrowserHost(ctx context.Context, input io.Reader, diagnostics io.Writer) error {
	deps := defaultBrowserEnvironmentDependencies()
	report, err := inspectBrowserEnvironment(ctx, deps)
	if err != nil {
		return err
	}
	password, err := browserAdministratorPassword(report, input, diagnostics)
	defer clear(password)
	if err != nil {
		return err
	}
	return prepareBrowserEnvironment(ctx, password, diagnostics, deps)
}

func prepareRemoteBrowserHost(ctx context.Context, selected management.ExecutionSelection, input io.Reader, diagnostics io.Writer, run remoteHostRunner) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	call := func(action string, material []byte, out io.Writer) error {
		args := []string{action}
		if selected.Root != "" {
			args = append([]string{"--root", selected.Root}, args...)
		}
		return run(ctx, selected, args, bytes.NewReader(material), out, diagnostics)
	}
	var raw githubRelayOutput
	if err := call("_browser-environment", nil, &raw); err != nil {
		return fmt.Errorf("check browser host dependencies: %w", err)
	}
	var report browserEnvironmentReport
	if raw.overflow || json.Unmarshal(raw.Bytes(), &report) != nil || report.Schema != 1 || report.Missing == nil || report.AdministratorRequired && len(report.Missing) == 0 {
		return fmt.Errorf("invalid browser environment report")
	}
	if !report.Applicable || !report.Supported || len(report.Missing) == 0 {
		return nil
	}
	password, err := browserAdministratorPassword(report, input, diagnostics)
	defer clear(password)
	if err != nil {
		return err
	}
	return call("_prepare-browser-environment", password, diagnostics)
}

func prepareInstalledBrowserHost(ctx context.Context, host setupMenuHost, input io.Reader, diagnostics io.Writer) error {
	report, err := inspectOperatorHost(ctx, host.frontend, host.selected, diagnostics, false)
	if err != nil {
		// The setup home retains unavailable-host recovery.
		return nil
	}
	if report.Target.Mode != tools.ProjectHost || !report.Tools["browser"].Installed {
		return nil
	}
	if host.selected != nil && host.selected.Remote() {
		return prepareRemoteBrowserHost(ctx, *host.selected, input, diagnostics, defaultRemotePreparationDependencies().run)
	}
	return prepareLocalBrowserHost(ctx, input, diagnostics)
}

// A package database is not evidence that the bundled runtime can launch. Keep
// failed installations available for repair, but never report setup success or
// enable a fresh browser before observing its sandboxed startup.
func verifyInstalledBrowser(ctx context.Context, store management.Store, diagnostics io.Writer) error {
	return verifyInstalledBrowserWithProbe(ctx, store, diagnostics, browser.CheckRuntime)
}

func verifyInstalledBrowserWithProbe(ctx context.Context, store management.Store, diagnostics io.Writer, probe management.Probe) error {
	state, err := store.Load()
	if err != nil {
		return err
	}
	if state.Config.Mode != tools.ProjectHost {
		return nil
	}
	if _, installed := state.Installed["browser"]; !installed {
		return nil
	}
	fmt.Fprintln(diagnostics, "Verifying installed browser startup, sandbox, screenshot and video (up to 45 seconds)...")
	report, err := store.Doctor(ctx, map[tools.ID]management.Probe{"browser": probe})
	if err != nil {
		return err
	}
	observed := report.Tools["browser"]
	if observed.Readiness != tools.Ready {
		return fmt.Errorf("browser files were installed, but runtime verification failed; files and data are retained. Retry loki setup to repair dependencies, or run loki doctor for host diagnostics: %s", observed.Reason)
	}
	fmt.Fprintln(diagnostics, "Browser runtime verified.")
	return nil
}
