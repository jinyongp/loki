package main

import (
	"archive/zip"
	"bufio"
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
}

func defaultUpgradeDependencies() upgradeDependencies {
	return upgradeDependencies{
		client: &http.Client{}, executable: os.Executable,
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
	name := "loki"
	if runtime.GOOS == "windows" {
		name += ".exe"
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
	check := flags.Bool("check", false, "only check versions")
	force := flags.Bool("force", false, "allow reinstall or downgrade")
	timeout := flags.Duration("timeout", 5*time.Minute, "overall timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 {
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
	fmt.Fprintln(out, "Current:", management.ManagerRelease)
	fmt.Fprintln(out, "Target: ", target)
	if *check {
		return nil
	}
	if comparison == 0 && !*force {
		fmt.Fprintln(out, "Loki is already up to date.")
		return nil
	}
	if comparison < 0 && !*force {
		return fmt.Errorf("target is older than the current CLI; use --force to allow a downgrade")
	}
	if !*yes {
		fmt.Fprint(out, "Upgrade CLI to "+target+"? [y/N]: ")
		reader := bufio.NewReader(input)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if err == io.EOF || (answer != "y" && answer != "yes") {
			fmt.Fprintln(out, "Upgrade cancelled.")
			return nil
		}
	}
	executable, err := deps.executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	executable, err = deps.location(store, executable)
	if err != nil {
		return err
	}
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
	candidate := filepath.Join(directory, name)
	if err := os.WriteFile(candidate, binary, 0700); err != nil {
		return err
	}
	result, err := deps.run(ctx, candidate, "version")
	if err != nil || strings.TrimSpace(string(result)) != "loki "+target {
		return fmt.Errorf("downloaded CLI version does not match target release")
	}
	fmt.Fprintln(diagnostics, "Installing Loki "+target+"...")
	if err := deps.install(ctx, store, candidate, filepath.Dir(executable), target); err != nil {
		return fmt.Errorf("CLI upgrade failed: %w", err)
	}
	fmt.Fprintln(out, "Loki "+target+" installed.")
	return nil
}
