package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"loki/internal/host/lifecycle"
	"loki/internal/host/releases"
)

const (
	publicHostUpdateInstallerURL = "https://jinyongp.dev/loki/install.sh"
	maxHostUpdateInstallerBytes  = 64 << 10
	maxHostUpdateBootstrapBytes  = 128 << 20
	maxHostUpdateMetadataBytes   = 4 << 20
	maxHostUpdateInfoBytes       = 16 << 10
)

type hostUpdateFetcher interface {
	Fetch(context.Context, string, int64) ([]byte, error)
}

type hostUpdateReleaseBinding struct {
	ReleaseTag            string `json:"release_tag"`
	ReleaseManifestSHA256 string `json:"release_manifest_sha256"`
	HostBinarySHA256      string `json:"host_binary_sha256"`
}

type hostUpdateBootstrapInspector func(context.Context, string) (hostUpdateReleaseBinding, error)

type hostUpdateHTTPFetcher struct {
	Client *http.Client
}

func (f hostUpdateHTTPFetcher) Fetch(ctx context.Context, assetURL string, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		return nil, errors.New("release asset size limit is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download release asset: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, errors.New("release asset exceeds size policy")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("release asset exceeds size policy")
	}
	return raw, nil
}

func prepareHostUpdateCandidate(ctx context.Context, store *lifecycle.FileStore) (lifecycle.Generation, error) {
	host, err := releases.DetectHost()
	if err != nil {
		return lifecycle.Generation{}, err
	}
	backend, err := newHostComposeBackend(store)
	if err != nil {
		return lifecycle.Generation{}, err
	}
	return prepareHostUpdateCandidateWithPrefetch(
		ctx, store, hostUpdateHTTPFetcher{}, inspectHostUpdateBootstrap, host, backend.Prefetch,
	)
}

func prepareHostUpdateCandidateWith(
	ctx context.Context,
	store *lifecycle.FileStore,
	fetcher hostUpdateFetcher,
	inspect hostUpdateBootstrapInspector,
	host releases.SupportedHost,
) (lifecycle.Generation, error) {
	return prepareHostUpdateCandidateWithPrefetch(ctx, store, fetcher, inspect, host, nil)
}

func prepareHostUpdateCandidateWithPrefetch(
	ctx context.Context,
	store *lifecycle.FileStore,
	fetcher hostUpdateFetcher,
	inspect hostUpdateBootstrapInspector,
	host releases.SupportedHost,
	prefetch func(context.Context, lifecycle.Generation, lifecycle.InstallationState, []string) error,
) (lifecycle.Generation, error) {
	if store == nil || fetcher == nil || inspect == nil {
		return lifecycle.Generation{}, errors.New("host update discovery is not configured")
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return lifecycle.Generation{}, err
	}
	if snapshot.Installed == nil || snapshot.Installation == nil {
		return lifecycle.Generation{}, errors.New("host update discovery requires an installed release")
	}
	installerRaw, err := fetcher.Fetch(ctx, publicHostUpdateInstallerURL, maxHostUpdateInstallerBytes)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("fetch public Loki installer: %w", err)
	}
	tag, bootstrapSHA256, err := parseHostUpdateInstaller(installerRaw)
	if err != nil {
		return lifecycle.Generation{}, err
	}

	bootstrapURL, err := hostUpdateReleaseAssetURL(tag, "loki-bootstrap-linux-amd64")
	if err != nil {
		return lifecycle.Generation{}, err
	}
	bootstrapRaw, err := fetcher.Fetch(ctx, bootstrapURL, maxHostUpdateBootstrapBytes)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("fetch release bootstrap: %w", err)
	}
	if digestBytes(bootstrapRaw) != bootstrapSHA256 {
		return lifecycle.Generation{}, errors.New("public installer bootstrap digest does not match the immutable release asset")
	}
	stagingRoot, err := os.MkdirTemp(store.Root, ".update-discovery-")
	if err != nil {
		return lifecycle.Generation{}, err
	}
	defer os.RemoveAll(stagingRoot)
	if err = os.Chmod(stagingRoot, 0700); err != nil {
		return lifecycle.Generation{}, err
	}
	bootstrapPath := filepath.Join(stagingRoot, "loki-bootstrap")
	if err = writeHostUpdateExecutable(bootstrapPath, bootstrapRaw); err != nil {
		return lifecycle.Generation{}, err
	}
	info, err := inspect(ctx, bootstrapPath)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("inspect verified release bootstrap: %w", err)
	}
	if info.ReleaseTag != tag {
		return lifecycle.Generation{}, errors.New("release bootstrap tag does not match the public installer")
	}

	manifestURL, err := hostUpdateReleaseAssetURL(tag, "loki-release-manifest.json")
	if err != nil {
		return lifecycle.Generation{}, err
	}
	manifestRaw, err := fetcher.Fetch(ctx, manifestURL, maxHostUpdateMetadataBytes)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("fetch release manifest: %w", err)
	}
	if digestBytes(manifestRaw) != info.ReleaseManifestSHA256 {
		return lifecycle.Generation{}, errors.New("release manifest digest does not match the verified bootstrap binding")
	}
	manifest, err := releases.LoadReleaseManifest(manifestRaw)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("verify release manifest: %w", err)
	}
	if tag != "v"+manifest.Generation.Spec.Version || info.HostBinarySHA256 != manifest.HostBinary.SHA256 {
		return lifecycle.Generation{}, errors.New("verified bootstrap binding does not match the release manifest")
	}
	if !hostUpdateSupportsHost(manifest.SupportedHosts, host) {
		return lifecycle.Generation{}, fmt.Errorf(
			"release %s does not support %s/%s %s %s",
			manifest.Generation.Spec.Version, host.Environment, host.Distribution, host.Version, host.Arch,
		)
	}

	indexURL, err := hostUpdateReleaseAssetURL(tag, "loki-release-index.json")
	if err != nil {
		return lifecycle.Generation{}, err
	}
	indexRaw, err := fetcher.Fetch(ctx, indexURL, maxHostUpdateMetadataBytes)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("fetch release index: %w", err)
	}
	index, err := releases.LoadReleaseIndex(indexRaw)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("verify release index: %w", err)
	}
	if err = verifyHostUpdateIndex(index, manifest, manifestRaw); err != nil {
		return lifecycle.Generation{}, err
	}

	releaseNotesURL, err := hostUpdateReleaseAssetURL(tag, "loki-release-notes.md")
	if err != nil {
		return lifecycle.Generation{}, err
	}
	releaseNotesRaw, err := fetcher.Fetch(ctx, releaseNotesURL, minHostUpdateAssetLimit(manifest.ReleaseNotes.Length, 1<<20))
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("fetch release notes: %w", err)
	}
	if err = manifest.ReleaseNotes.VerifyBytes(releaseNotesRaw); err != nil {
		return lifecycle.Generation{}, fmt.Errorf("verify release notes: %w", err)
	}
	if len(bytes.TrimSpace(releaseNotesRaw)) == 0 || bytes.IndexByte(releaseNotesRaw, 0) >= 0 {
		return lifecycle.Generation{}, errors.New("verified release notes are empty or invalid")
	}

	binaryURL, err := hostUpdateReleaseAssetURL(tag, "loki-linux-amd64")
	if err != nil {
		return lifecycle.Generation{}, err
	}
	binaryRaw, err := fetcher.Fetch(ctx, binaryURL, manifest.HostBinary.Length)
	if err != nil {
		return lifecycle.Generation{}, fmt.Errorf("fetch release host binary: %w", err)
	}
	if err = manifest.HostBinary.VerifyBytes(binaryRaw); err != nil {
		return lifecycle.Generation{}, fmt.Errorf("verify release host binary: %w", err)
	}

	candidate, err := lifecycleGenerationFromRelease(manifest.Generation)
	if err != nil {
		return lifecycle.Generation{}, err
	}
	if err = validateHostUpdateAdvance(*snapshot.Installed, candidate); err != nil {
		return lifecycle.Generation{}, err
	}
	if _, err = lifecycle.Prepare(snapshot.Installed, candidate, snapshot.Host, lifecycleTimeNow()); err != nil {
		return lifecycle.Generation{}, fmt.Errorf("preflight candidate lifecycle compatibility: %w", err)
	}
	paths, err := resolveHostCLIInstallPaths(snapshot.Installation.Scope == "system", candidate.ID)
	if err != nil {
		return lifecycle.Generation{}, err
	}
	if err = stageHostCLI(paths, candidate, binaryRaw); err != nil {
		return lifecycle.Generation{}, fmt.Errorf("stage verified host CLI: %w", err)
	}
	if prefetch != nil {
		if err = prefetch(ctx, candidate, *snapshot.Installation, snapshot.Host.EnabledComponents); err != nil {
			return lifecycle.Generation{}, err
		}
	}
	metadata := lifecycle.AvailableReleaseMetadata{
		GenerationID: candidate.ID,
		DockerMin:    manifest.Runtime.DockerMin, ComposeMin: manifest.Runtime.ComposeMin,
		ReleaseNotesPath: manifest.ReleaseNotes.Path, ReleaseNotesLength: manifest.ReleaseNotes.Length,
		ReleaseNotesSHA256: manifest.ReleaseNotes.SHA256, ReleaseNotes: string(releaseNotesRaw),
	}
	if err = publishHostUpdateCandidate(ctx, store, snapshot, candidate, metadata); err != nil {
		return lifecycle.Generation{}, err
	}
	return candidate, ctx.Err()
}

func publishHostUpdateCandidate(
	ctx context.Context,
	store *lifecycle.FileStore,
	baseline lifecycle.Snapshot,
	candidate lifecycle.Generation,
	metadata lifecycle.AvailableReleaseMetadata,
) error {
	lock, err := lifecycle.AcquireOperationLock(store.Root)
	if err != nil {
		return err
	}
	defer lock.Close()
	journal, err := lifecycle.OpenOperationJournal(store.Root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return err
	}
	if active, found, activeErr := journal.Active(); activeErr != nil {
		return activeErr
	} else if found {
		return fmt.Errorf("host update discovery is blocked by interrupted %s operation %s", active.Kind, active.ID)
	}
	current, err := store.Snapshot(ctx)
	if err != nil {
		return err
	}
	if baseline.Installed == nil || current.Installed == nil ||
		baseline.Installed.ID != current.Installed.ID ||
		baseline.Host.Revision != current.Host.Revision ||
		baseline.Installation == nil || current.Installation == nil ||
		!baseline.Installation.Equivalent(*current.Installation) ||
		generationIdentity(baseline.Available) != generationIdentity(current.Available) {
		return errors.New("host lifecycle state changed during update discovery; run prepare again")
	}
	if metadata.GenerationID != candidate.ID {
		return errors.New("available release metadata does not match the discovered candidate")
	}
	if err = store.SaveAvailableMetadata(ctx, metadata); err != nil {
		return err
	}
	return store.SaveAvailable(ctx, candidate)
}

func generationIdentity(generation *lifecycle.Generation) string {
	if generation == nil {
		return ""
	}
	return generation.ID
}

func parseHostUpdateInstaller(raw []byte) (string, string, error) {
	tag, err := singleShellLiteral(raw, "release_tag")
	if err != nil {
		return "", "", err
	}
	if _, err = parseReleaseVersion(strings.TrimPrefix(tag, "v")); err != nil || !strings.HasPrefix(tag, "v") {
		return "", "", errors.New("public installer release tag is invalid")
	}
	digest, err := singleShellLiteral(raw, "bootstrap_sha256")
	if err != nil {
		return "", "", err
	}
	if len(digest) != sha256.Size*2 {
		return "", "", errors.New("public installer bootstrap digest is invalid")
	}
	if decoded, decodeErr := hex.DecodeString(digest); decodeErr != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
		return "", "", errors.New("public installer bootstrap digest is invalid")
	}
	return tag, digest, nil
}

func singleShellLiteral(raw []byte, key string) (string, error) {
	prefix := key + "='"
	var value string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "'") {
			continue
		}
		if value != "" {
			return "", errors.New("public installer contains duplicate release identity")
		}
		value = strings.TrimSuffix(strings.TrimPrefix(line, prefix), "'")
	}
	if value == "" || strings.ContainsAny(value, "\r\n\x00'") {
		return "", errors.New("public installer release identity is missing or invalid")
	}
	return value, nil
}

func hostUpdateReleaseAssetURL(tag, asset string) (string, error) {
	if tag == "" || asset == "" || strings.ContainsAny(tag+asset, "/\\\x00") {
		return "", errors.New("release asset identity is invalid")
	}
	return (&url.URL{
		Scheme: "https",
		Host:   "github.com",
		Path:   "/jinyongp/loki/releases/download/" + tag + "/" + asset,
	}).String(), nil
}

func verifyHostUpdateIndex(index releases.ReleaseIndex, manifest releases.ReleaseManifest, raw []byte) error {
	for _, entry := range index.Entries {
		if entry.Release != manifest.Generation.Spec.Version || entry.GenerationID != manifest.Generation.ID {
			continue
		}
		verified, err := entry.VerifyManifest(raw)
		if err != nil {
			return fmt.Errorf("verify release index manifest binding: %w", err)
		}
		if verified.Generation.ID != manifest.Generation.ID {
			return errors.New("release index manifest binding changed generation identity")
		}
		return nil
	}
	return errors.New("release index does not contain the verified release manifest")
}

func validateHostUpdateAdvance(installed, candidate lifecycle.Generation) error {
	if !installed.Valid() || !candidate.Valid() {
		return errors.New("host update release generation is invalid")
	}
	comparison, err := compareReleaseVersions(candidate.Spec.Version, installed.Spec.Version)
	if err != nil {
		return err
	}
	switch {
	case comparison < 0:
		return errors.New("published release is older than the installed release")
	case comparison == 0 && candidate.ID != installed.ID:
		return errors.New("published release changed immutable identity for the installed version")
	case comparison == 0:
		return errors.New("no newer release is available")
	case comparison > 0 && !candidate.Spec.ReleasedAt.After(installed.Spec.ReleasedAt):
		return errors.New("published release timestamp does not advance the installed release")
	default:
		return nil
	}
}

func compareReleaseVersions(left, right string) (int, error) {
	lv, err := parseReleaseVersion(left)
	if err != nil {
		return 0, err
	}
	rv, err := parseReleaseVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range lv {
		switch {
		case lv[index] < rv[index]:
			return -1, nil
		case lv[index] > rv[index]:
			return 1, nil
		}
	}
	return 0, nil
}

func parseReleaseVersion(value string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(value, ".")
	if len(parts) != len(result) {
		return result, errors.New("release version is invalid")
	}
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return result, errors.New("release version is invalid")
		}
		number, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return result, errors.New("release version is invalid")
		}
		result[index] = number
	}
	return result, nil
}

func hostUpdateSupportsHost(supported []releases.SupportedHost, host releases.SupportedHost) bool {
	for _, candidate := range supported {
		if candidate == host {
			return true
		}
	}
	return false
}

func inspectHostUpdateBootstrap(ctx context.Context, path string) (hostUpdateReleaseBinding, error) {
	var stdout, stderr boundedHostUpdateOutput
	stdout.Maximum = maxHostUpdateInfoBytes
	stderr.Maximum = maxHostUpdateInfoBytes
	command := exec.CommandContext(ctx, path, "--bootstrap-info")
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return hostUpdateReleaseBinding{}, fmt.Errorf("bootstrap info command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Exceeded || stderr.Exceeded {
		return hostUpdateReleaseBinding{}, errors.New("bootstrap info output exceeds size policy")
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	var info hostUpdateReleaseBinding
	if err := decoder.Decode(&info); err != nil {
		return hostUpdateReleaseBinding{}, errors.New("bootstrap info output is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return hostUpdateReleaseBinding{}, errors.New("bootstrap info output contains trailing data")
	}
	if info.ReleaseTag == "" || !validHostUpdateDigest(info.ReleaseManifestSHA256) || !validHostUpdateDigest(info.HostBinarySHA256) {
		return hostUpdateReleaseBinding{}, errors.New("bootstrap info release binding is invalid")
	}
	return info, nil
}

type boundedHostUpdateOutput struct {
	Maximum  int
	Exceeded bool
	buffer   bytes.Buffer
}

func (w *boundedHostUpdateOutput) Write(p []byte) (int, error) {
	if w.Maximum <= 0 {
		w.Exceeded = true
		return len(p), nil
	}
	remaining := w.Maximum - w.buffer.Len()
	if remaining <= 0 {
		w.Exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = w.buffer.Write(p[:remaining])
		w.Exceeded = true
		return len(p), nil
	}
	_, _ = w.buffer.Write(p)
	return len(p), nil
}

func (w *boundedHostUpdateOutput) Bytes() []byte {
	return w.buffer.Bytes()
}

func (w *boundedHostUpdateOutput) String() string {
	return w.buffer.String()
}

func writeHostUpdateExecutable(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func validHostUpdateDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func minHostUpdateAssetLimit(length, maximum int64) int64 {
	if length <= 0 || length > maximum {
		return maximum
	}
	return length
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
