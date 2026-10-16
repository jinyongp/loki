package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"loki/internal/management"
)

const managerDownloadLimit = 256 << 20

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type upgradeRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type upgradeDependencies struct {
	client     *http.Client
	executable func() (string, error)
	location   func(management.Store, string) (string, error)
	run        func(context.Context, string, ...string) ([]byte, error)
	install    func(context.Context, management.Store, string, string, string) error
	plan       func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error)
	apply      func(context.Context, string, management.Store, upgradeApplyRequest, io.Reader, io.Writer) (upgradeApplyResult, error)
}

type releaseAPITransport struct {
	base  http.RoundTripper
	token string
}

func (t releaseAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" && req.URL.Scheme == "https" && req.URL.Host == "api.github.com" && strings.HasPrefix(req.URL.Path, "/repos/jinyongp/loki/releases/") {
		// Clone per request so redirect requests cannot inherit this credential.
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}

func defaultUpgradeDependencies() upgradeDependencies {
	client := &http.Client{Transport: releaseAPITransport{base: http.DefaultTransport, token: os.Getenv("GH_TOKEN")}}
	return upgradeDependencies{
		plan: func(ctx context.Context, store management.Store, target string, diagnostics io.Writer) (*upgradeToolsPlan, error) {
			return planUpgradeTools(ctx, store, nil, true, target, diagnostics, client)
		},
		apply:  executeUpgradeTools,
		client: client, executable: os.Executable,
		location: func(store management.Store, executable string) (string, error) {
			return store.ManagerExecutable(executable)
		},
		run: func(ctx context.Context, binary string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, binary, args...).CombinedOutput()
		},
		install: func(ctx context.Context, store management.Store, binary, directory, version string) error {
			_, err := store.InstallManagerVersion(ctx, binary, directory, version)
			return err
		},
	}
}

func compareReleaseVersions(a, b string) (int, error) {
	if !stableVersionPattern.MatchString(a) || !stableVersionPattern.MatchString(b) {
		return 0, fmt.Errorf("version must be a stable MAJOR.MINOR.PATCH release")
	}
	aa, bb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range aa {
		x, err := strconv.ParseUint(aa[i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid release version: %w", err)
		}
		y, err := strconv.ParseUint(bb[i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid release version: %w", err)
		}
		if x < y {
			return -1, nil
		}
		if x > y {
			return 1, nil
		}
	}
	return 0, nil
}

func upgradeDownload(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "loki/"+management.ManagerRelease)
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("release download exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release download exceeds size limit")
	}
	return data, nil
}

func verifiedUpgradeBinary(archive, checksums []byte, asset string) ([]byte, error) {
	name := "loki"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return verifiedManagerBinary(archive, checksums, asset, name)
}

func verifiedManagerBinary(archive, checksums []byte, asset, name string) ([]byte, error) {
	var expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && strings.TrimPrefix(parts[1], "*") == asset {
			if expected != "" {
				return nil, fmt.Errorf("duplicate manager checksum")
			}
			expected = parts[0]
		}
	}
	digest, err := hex.DecodeString(expected)
	if err != nil || len(digest) != sha256.Size {
		return nil, fmt.Errorf("release is missing a valid manager checksum")
	}
	actual := sha256.Sum256(archive)
	if !bytes.Equal(digest, actual[:]) {
		return nil, fmt.Errorf("manager archive SHA-256 verification failed")
	}
	packed, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	var binary []byte
	for _, entry := range packed.File {
		if entry.Name != name {
			continue
		}
		if binary != nil || !entry.Mode().IsRegular() || entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > managerDownloadLimit {
			return nil, fmt.Errorf("invalid manager binary in release archive")
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, err
		}
		binary, err = io.ReadAll(io.LimitReader(reader, managerDownloadLimit+1))
		reader.Close()
		if err != nil {
			return nil, err
		}
		if len(binary) > managerDownloadLimit {
			return nil, fmt.Errorf("manager binary exceeds size limit")
		}
	}
	if len(binary) == 0 {
		return nil, fmt.Errorf("release archive is missing native manager binary")
	}
	return binary, nil
}

func runUpgrade(ctx context.Context, store management.Store, args []string, input io.Reader, out, diagnostics io.Writer, deps upgradeDependencies) error {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	version := flags.String("version", "", "stable target release")
	yes := flags.Bool("yes", false, "accept without prompting")
	flags.BoolVar(yes, "y", false, "accept without prompting")
	check := flags.Bool("check", false, "show the complete update plan without changes")
	cliOnly := flags.Bool("cli-only", false, "update only this CLI")
	toolsOnly := flags.Bool("tools-only", false, "update only the selected host and installed tools")
	force := flags.Bool("force", false, "allow CLI reinstall or downgrade")
	timeout := flags.Duration("timeout", 30*time.Minute, "overall upgrade timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 || *cliOnly && *toolsOnly {
		return fmt.Errorf("invalid upgrade arguments; run 'loki upgrade --help'")
	}
	requested := strings.TrimPrefix(*version, "v")
	if requested != "" {
		if _, err := compareReleaseVersions(requested, management.ManagerRelease); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	fmt.Fprintln(diagnostics, "Checking Loki releases...")
	endpoint := "https://api.github.com/repos/jinyongp/loki/releases/latest"
	if requested != "" {
		endpoint = "https://api.github.com/repos/jinyongp/loki/releases/tags/v" + requested
	}
	metadata, err := upgradeDownload(ctx, deps.client, endpoint, 2<<20)
	if err != nil {
		return fmt.Errorf("could not check Loki releases: %w", err)
	}
	var release upgradeRelease
	if err := json.Unmarshal(metadata, &release); err != nil {
		return fmt.Errorf("invalid release metadata: %w", err)
	}
	target := strings.TrimPrefix(release.Tag, "v")
	if release.Draft || release.Prerelease || release.Tag != "v"+target || (requested != "" && target != requested) {
		return fmt.Errorf("requested release is not a published stable release")
	}
	comparison, err := compareReleaseVersions(target, management.ManagerRelease)
	if err != nil {
		return err
	}
	minimum, err := compareReleaseVersions(target, "0.2.0")
	if err != nil || minimum < 0 {
		return fmt.Errorf("upgrade requires a native 0.2 or newer release")
	}
	var plan *upgradeToolsPlan
	if !*cliOnly && deps.plan != nil {
		plan, err = deps.plan(ctx, store, target, diagnostics)
		if err != nil {
			return err
		}
	}
	cliWork := !*toolsOnly && (comparison != 0 || *force)
	if cliWork && plan != nil && plan.Clients {
		plan.Reconnect = true
		if !strings.Contains(plan.RuntimeAction, "reconnect") {
			plan.RuntimeAction += "; reconnect client MCP after completion"
		}
	}
	toolWork := plan != nil && plan.Update
	// A normal upgrade also checks already-current installations. Planning and
	// cancellation stay probe-free; --cli-only never touches the execution host.
	healthWork := plan != nil && plan.Installed > 0
	if plan != nil && plan.All {
		for _, child := range plan.Hosts {
			healthWork = healthWork || child.Installed > 0
		}
	}
	applyWork := toolWork || healthWork
	cliChanged := false
	applied := upgradeApplyResult{}
	updateAvailable := !*toolsOnly && comparison > 0 || toolWork
	finish := func(state, message string) error {
		if jsonOutput(out) {
			return result(out, "Loki upgrade", map[string]any{"current": management.ManagerRelease, "target": target, "update_available": updateAvailable, "state": state, "changed": cliChanged || applied.Changed, "cli_changed": cliChanged, "tools": plan, "applied": applied, "reconnect_required": applied.Reconnect || cliChanged && plan != nil && plan.Reconnect})
		}
		if len(applied.Hosts) > 0 {
			fmt.Fprintln(out, "Host update results:")
			for _, host := range applied.Hosts {
				name := host.ID
				if record, err := store.RegisteredHost(host.ID); err == nil {
					name = record.Name
				}
				state := "completed"
				if host.Error != "" {
					state = "failed: " + shortDiagnostic(host.Error)
				}
				fmt.Fprintf(out, "  %s: %s\n", name, state)
				printUpgradeHealth(out, host.Result.Health)
			}
		}
		printUpgradeHealth(out, applied.Health)
		if message != "" {
			_, err := fmt.Fprintln(out, message)
			return err
		}
		return nil
	}
	printPlan := func(writer io.Writer) {
		fmt.Fprintln(writer, "Current:", management.ManagerRelease)
		fmt.Fprintln(writer, "Target: ", target)
		if *toolsOnly {
			fmt.Fprintln(writer, "Scope: selected host and tools; this CLI is retained")
		}
		printUpgradeToolsPlan(writer, plan, target)
	}
	if !jsonOutput(out) {
		printPlan(out)
	}
	if *check {
		return finish("checked", "")
	}
	if !cliWork && !applyWork {
		return finish("up_to_date", "Loki is already up to date.")
	}
	if cliWork && comparison < 0 && !*force {
		return fmt.Errorf("target is older than the current CLI; use --force to allow a downgrade")
	}
	if toolWork && !*force {
		plans := []*upgradeToolsPlan{plan}
		if plan.All {
			plans = plan.Hosts
		}
		for _, child := range plans {
			for _, current := range []string{child.ManagerCurrent, child.ReleaseCurrent} {
				if current != "" {
					if c, err := compareReleaseVersions(target, current); err != nil {
						return err
					} else if c < 0 {
						return fmt.Errorf("target is older than the selected host or tool release; use --force to allow a downgrade")
					}
				}
			}
		}
	}
	if !*yes && (cliWork || toolWork) {
		prompt := out
		if jsonOutput(out) {
			prompt = diagnostics
			printPlan(prompt)
		}
		fmt.Fprint(prompt, "Apply this upgrade plan? [y/N]: ")
		line, err := readSetupLine(input, 4096)
		if err != nil && err != io.EOF {
			return err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if err == io.EOF || (answer != "y" && answer != "yes") {
			return finish("cancelled", "Upgrade cancelled.")
		}
	}
	runner, err := deps.executable()
	if err != nil {
		return err
	}
	runner, err = filepath.EvalSymlinks(runner)
	if err != nil {
		return err
	}
	executable := runner
	if cliWork {
		executable, err = deps.location(store, runner)
		if err != nil {
			return err
		}
	}
	if cliWork || applyWork && target != management.ManagerRelease {
		asset := "loki-manager-" + target + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip"
		base := "https://github.com/jinyongp/loki/releases/download/v" + target + "/"
		fmt.Fprintln(diagnostics, "Downloading Loki "+target+"...")
		sums, err := upgradeDownload(ctx, deps.client, base+"SHA256SUMS", 2<<20)
		if err != nil {
			return err
		}
		archive, err := upgradeDownload(ctx, deps.client, base+asset, managerDownloadLimit)
		if err != nil {
			return err
		}
		binary, err := verifiedUpgradeBinary(archive, sums, asset)
		if err != nil {
			return err
		}
		directory, err := os.MkdirTemp("", "loki-upgrade-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(directory)
		name := "loki"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		runner = filepath.Join(directory, name)
		if err := os.WriteFile(runner, binary, 0700); err != nil {
			return err
		}
		actual, err := deps.run(ctx, runner, "version")
		if err != nil {
			if detail := shortDiagnostic(string(actual)); detail != "" {
				return fmt.Errorf("downloaded CLI could not start: %w\n%s", err, detail)
			}
			return fmt.Errorf("downloaded CLI could not start: %w", err)
		}
		if strings.TrimSpace(string(actual)) != "loki "+target {
			return fmt.Errorf("downloaded CLI version does not match target release %s: %s", target, shortDiagnostic(string(actual)))
		}
	}
	if applyWork {
		protocol, err := deps.run(ctx, runner, "_upgrade-apply", "--help")
		if err != nil {
			return fmt.Errorf("target release cannot apply an integrated upgrade; use --cli-only for this release: %w", err)
		}
		if plan.All && !strings.Contains(string(protocol), "request-file") {
			return fmt.Errorf("target release does not support multiple-host upgrades; no changes applied")
		}
	}
	if cliWork {
		fmt.Fprintln(diagnostics, "Installing Loki "+target+"...")
		if err := deps.install(ctx, store, runner, filepath.Dir(executable), target); err != nil {
			return fmt.Errorf("CLI upgrade failed: %w", err)
		}
		cliChanged = true
	}
	if applyWork {
		request := upgradeApplyRequest{Version: target, Basis: plan.Basis, HostID: plan.HostID, Selection: plan.Selection, Remember: plan.Remember, Force: *force}
		if plan.All {
			request.All = true
			request.Failures = plan.Failures
			for _, child := range plan.Hosts {
				if child.Update || child.Installed > 0 {
					request.Targets = append(request.Targets, upgradeApplyRequest{Version: target, Basis: child.Basis, HostID: child.HostID, Selection: child.Selection, Force: *force})
				}
			}
		}
		applied, err = deps.apply(ctx, runner, store, request, input, diagnostics)
		if err != nil {
			message := "Upgrade incomplete. Retry 'loki upgrade --tools-only --version " + target + " --yes' to finish the selected host and tools."
			if plan.All {
				message = strings.Replace(message, "upgrade --tools-only", "upgrade --all-hosts --tools-only", 1)
			}
			if plan.HostID != "" {
				message = strings.Replace(message, "loki upgrade", "loki --host "+plan.HostID+" upgrade", 1)
				message += " Retain the same frontend --root option if one was supplied."
			}
			if *force {
				message += " Retain --force for the approved downgrade."
			}
			if applied.Health != nil && applied.Health.State == "failed" {
				message = "Upgrade needs attention. Follow the automatic health report above; completed changes are retained."
			}
			if cliChanged {
				message = "CLI updated to " + target + ". " + message
			}
			if finishErr := finish("partial", message); finishErr != nil {
				return finishErr
			}
			return fmt.Errorf("execution-host/tool upgrade failed: %w", err)
		}
	}
	if !cliWork && !toolWork {
		return finish("up_to_date", "Loki is already up to date.")
	}
	message := "Loki " + target + " installed."
	if !cliChanged {
		message = "Selected host and installed tools updated to " + target + "."
	} else if applied.Changed {
		message += "\nSelected host and installed tools updated."
	}
	if applied.ServicesRestarted {
		message += "\nRunning services now use the updated tools."
	}
	if applied.Reconnect || cliChanged && plan != nil && plan.Reconnect {
		message += "\nReconnect the client's Loki MCP server. The Codex app can stay open."
	}
	if *cliOnly {
		message += "\nSelected host and tools were not updated. Reconnect existing MCP sessions to load the new CLI."
	}
	return finish("installed", message)
}
