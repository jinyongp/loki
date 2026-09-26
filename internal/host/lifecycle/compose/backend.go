package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"loki/internal/host/assets"
	hostingress "loki/internal/host/ingress"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/safeio"
)

const (
	defaultProject          = "loki"
	defaultCoreRepository   = "ghcr.io/jinyongp/loki"
	defaultBrowserRepo      = "ghcr.io/jinyongp/loki-browser"
	maxCommandOutput        = 32 << 10
	snapshotManifestVersion = 1
)

var persistentVolumes = []string{
	"launcher-state",
	"runtime-state",
	"runner-state",
	"user-skills",
	"browser-downloads",
	"browser-state",
	"signing-state",
}

type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

type ExecRunner struct {
	Executable string
	Prefix     []string
}

func (r ExecRunner) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	executable := strings.TrimSpace(r.Executable)
	if executable == "" {
		executable = "docker"
	}
	commandArgs := append(append([]string(nil), r.Prefix...), args...)
	cmd := exec.CommandContext(ctx, executable, commandArgs...)
	cmd.Env = append(os.Environ(), env...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	raw := output.Bytes()
	if len(raw) > maxCommandOutput {
		raw = raw[:maxCommandOutput]
	}
	if err != nil {
		return append([]byte(nil), raw...), fmt.Errorf("docker host operation failed: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	return append([]byte(nil), raw...), nil
}

type Config struct {
	StateRoot         string
	Project           string
	CoreRepository    string
	BrowserRepository string
	Runner            Runner
}

type Backend struct {
	root              string
	runtimeRoot       string
	snapshotRoot      string
	project           string
	coreRepository    string
	browserRepository string
	runner            Runner
}

type runtimeState struct {
	Version      int      `json:"version"`
	GenerationID string   `json:"generation_id"`
	CoreImage    string   `json:"core_image"`
	BrowserImage string   `json:"browser_image,omitempty"`
	Workspace    string   `json:"workspace"`
	MCPPort      int      `json:"mcp_port,omitempty"`
	Profiles     []string `json:"profiles,omitempty"`
	IngressHosts []string `json:"ingress_hosts,omitempty"`
}

func (s runtimeState) effectiveMCPPort() int {
	if s.MCPPort == 0 {
		return lifecycle.DefaultMCPPort
	}
	return s.MCPPort
}

type snapshotVolume struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
}

type snapshotManifest struct {
	Version int                       `json:"version"`
	Runtime *runtimeState             `json:"runtime,omitempty"`
	Volumes map[string]snapshotVolume `json:"volumes"`
}

func New(config Config) (*Backend, error) {
	return openBackend(config, true)
}

func Open(config Config) (*Backend, error) {
	return openBackend(config, false)
}

func openBackend(config Config, create bool) (*Backend, error) {
	root := filepath.Clean(strings.TrimSpace(config.StateRoot))
	if !filepath.IsAbs(root) || root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return nil, errors.New("compose lifecycle state root must be a clean absolute non-root path")
	}
	if config.Runner == nil {
		return nil, errors.New("compose lifecycle runner is not configured")
	}
	project := strings.TrimSpace(config.Project)
	if project == "" {
		project = defaultProject
	}
	if !validProject(project) {
		return nil, errors.New("compose project name is invalid")
	}
	coreRepository := strings.TrimSpace(config.CoreRepository)
	if coreRepository == "" {
		coreRepository = defaultCoreRepository
	}
	browserRepository := strings.TrimSpace(config.BrowserRepository)
	if browserRepository == "" {
		browserRepository = defaultBrowserRepo
	}
	if !validRepository(coreRepository) || !validRepository(browserRepository) {
		return nil, errors.New("compose image repository is invalid")
	}
	runtimeRoot := filepath.Join(root, "runtime")
	snapshotRoot := filepath.Join(runtimeRoot, "snapshots")
	for _, path := range []string{runtimeRoot, snapshotRoot} {
		if create {
			if err := os.MkdirAll(path, 0700); err != nil {
				return nil, err
			}
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("compose lifecycle directories must be private and real")
		}
	}
	return &Backend{
		root: root, runtimeRoot: runtimeRoot, snapshotRoot: snapshotRoot,
		project: project, coreRepository: coreRepository, browserRepository: browserRepository,
		runner: config.Runner,
	}, nil
}

func validProject(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func validRepository(value string) bool {
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, " \t\r\n\x00@")
}

func (b *Backend) Snapshot(ctx context.Context, _ lifecycle.OperationKind, snapshot lifecycle.Snapshot) (result lifecycle.RuntimeSnapshot, err error) {
	if err = ctx.Err(); err != nil {
		return lifecycle.RuntimeSnapshot{}, err
	}
	current, found, err := b.loadRuntime()
	if err != nil {
		return lifecycle.RuntimeSnapshot{}, err
	}
	stopped := false
	if found {
		if _, err = b.compose(ctx, current, "stop", "--timeout", "30"); err != nil {
			return lifecycle.RuntimeSnapshot{}, err
		}
		stopped = true
	}
	defer func() {
		if !stopped || err == nil {
			return
		}
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		restartErr := b.restartAndHealth(recoveryCtx)
		if restartErr == nil {
			return
		}
		result = lifecycle.RuntimeSnapshot{}
		err = errors.Join(err, fmt.Errorf("resume compose runtime after snapshot failure: %w", restartErr))
	}()

	dir, err := os.MkdirTemp(b.snapshotRoot, ".snapshot-")
	if err != nil {
		return lifecycle.RuntimeSnapshot{}, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return lifecycle.RuntimeSnapshot{}, err
	}
	manifest := snapshotManifest{Version: snapshotManifestVersion, Volumes: map[string]snapshotVolume{}}
	if found {
		copy := current
		copy.Profiles = append([]string(nil), current.Profiles...)
		copy.IngressHosts = append([]string(nil), current.IngressHosts...)
		manifest.Runtime = &copy
		for _, name := range persistentVolumes {
			volume := b.volumeName(name)
			present, existsErr := b.volumeExists(ctx, volume)
			if existsErr != nil {
				_ = os.RemoveAll(dir)
				return lifecycle.RuntimeSnapshot{}, existsErr
			}
			if !present {
				manifest.Volumes[name] = snapshotVolume{}
				continue
			}
			archive := name + ".tar"
			if err = b.archiveVolume(ctx, current, volume, dir, archive); err != nil {
				_ = os.RemoveAll(dir)
				return lifecycle.RuntimeSnapshot{}, err
			}
			sum, hashErr := fileSHA256(filepath.Join(dir, archive))
			if hashErr != nil {
				_ = os.RemoveAll(dir)
				return lifecycle.RuntimeSnapshot{}, hashErr
			}
			manifest.Volumes[name] = snapshotVolume{Present: true, SHA256: sum}
		}
	}
	if err = writePrivateJSON(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		_ = os.RemoveAll(dir)
		return lifecycle.RuntimeSnapshot{}, err
	}
	result = lifecycle.RuntimeSnapshot{
		Ref: dir,
		Coverage: lifecycle.BackupCoverage{
			RuntimeState: true, ConfigState: true, HostState: true, WorkspacePreserved: true,
			OptionalComponentState: append([]string(nil), snapshot.Host.EnabledComponents...),
			ExternalReferences:     []string{"github-app-private-key:external", "signing-key:external"},
		},
	}
	return result, nil
}

func (b *Backend) restartAndHealth(ctx context.Context) error {
	if err := b.Restart(ctx); err != nil {
		return err
	}
	return b.Health(ctx)
}

func (b *Backend) Activate(_ context.Context, generation lifecycle.Generation, installation lifecycle.InstallationState) error {
	if !generation.Valid() || !installation.Valid() {
		return errors.New("compose lifecycle activation input is invalid")
	}
	if err := b.ensureAssetsAndToken(); err != nil {
		return err
	}
	current, found, err := b.loadRuntime()
	if err != nil {
		return err
	}
	var profiles, ingressHosts []string
	if found {
		profiles = append(profiles, current.Profiles...)
		ingressHosts = append(ingressHosts, current.IngressHosts...)
	}
	state, err := b.stateFor(generation, installation, profiles)
	if err != nil {
		return err
	}
	state.IngressHosts = ingressHosts
	if err = state.validate(); err != nil {
		return err
	}
	return b.saveRuntime(state)
}

func (b *Backend) SetIngressHosts(ctx context.Context, hosts []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := lifecycle.NormalizeIngressHosts(hosts)
	if err != nil {
		return err
	}
	current, found, err := b.loadRuntime()
	if err != nil {
		return err
	}
	if !found {
		return errors.New("compose lifecycle runtime is not activated")
	}
	current.IngressHosts = normalized
	if err = current.validate(); err != nil {
		return err
	}
	return b.saveRuntime(current)
}

func (b *Backend) SetComponent(_ context.Context, generation lifecycle.Generation, name string, enabled bool) error {
	current, found, err := b.loadRuntime()
	if err != nil {
		return err
	}
	if !found || current.GenerationID != generation.ID {
		return errors.New("compose lifecycle runtime generation does not match installed release")
	}
	name = strings.TrimSpace(name)
	profiles := append([]string(nil), current.Profiles...)
	sort.Strings(profiles)
	index := sort.SearchStrings(profiles, name)
	has := index < len(profiles) && profiles[index] == name
	if has == enabled {
		return nil
	}
	if enabled {
		profiles = append(profiles, name)
		sort.Strings(profiles)
	} else {
		profiles = append(profiles[:index], profiles[index+1:]...)
	}
	current.Profiles = profiles
	return b.saveRuntime(current)
}

func (b *Backend) Migrate(_ context.Context, steps []lifecycle.MigrationStep) error {
	if len(steps) != 0 {
		return errors.New("compose lifecycle state migration adapter is not implemented for this release transition")
	}
	return nil
}

// ImportLegacyVault imports a private offline Python v1 vault copy into the
// active runtime-state volume. The Compose runtime is stopped for the mutation
// and always restarted before returning, even when the caller is cancelled.
// Callers are responsible for taking a lifecycle backup first and restoring it
// if this operation returns an error.
func (b *Backend) ImportLegacyVault(ctx context.Context, source string) (raw []byte, err error) {
	source = strings.TrimSpace(source)
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || source == string(filepath.Separator) ||
		strings.ContainsAny(source, ",\r\n\x00") {
		return nil, errors.New("legacy vault source must be a clean absolute private path")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("legacy vault source must be a private real directory")
	}
	state, found, err := b.loadRuntime()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("compose lifecycle runtime is not activated")
	}
	volume := b.volumeName("runtime-state")
	exists, err := b.volumeExists(ctx, volume)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("compose runtime-state volume is unavailable")
	}
	if err = b.Stop(ctx); err != nil {
		return nil, err
	}
	defer func() {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		restartErr := b.restartAndHealth(recoveryCtx)
		if restartErr == nil {
			return
		}
		raw = nil
		resumeErr := fmt.Errorf("resume compose runtime after legacy vault import: %w", restartErr)
		if err == nil {
			err = resumeErr
		} else {
			err = errors.Join(err, resumeErr)
		}
	}()
	if err = b.VerifyStopped(ctx); err != nil {
		return nil, err
	}
	raw, err = b.runner.Run(ctx, nil,
		"run", "--rm", "--network", "none", "--user", "0:0", "--read-only",
		"--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE",
		"--security-opt", "no-new-privileges",
		"--mount", "type=bind,src="+source+",dst=/source,readonly",
		"--mount", "type=volume,src="+volume+",dst=/target",
		state.CoreImage,
		"migrate-vault", "import",
		"--source-copy", "/source",
		"--destination", "/target",
		"--existing-destination",
	)
	return raw, err
}

func (b *Backend) Restart(ctx context.Context) error {
	state, found, err := b.loadRuntime()
	if err != nil {
		return err
	}
	if !found {
		return errors.New("compose lifecycle runtime is not activated")
	}
	_, err = b.compose(ctx, state, "up", "-d", "--remove-orphans")
	return err
}

func (b *Backend) Health(ctx context.Context) error {
	state, found, err := b.loadRuntime()
	if err != nil {
		return err
	}
	if !found {
		return errors.New("compose lifecycle runtime is not activated")
	}
	_, err = b.compose(ctx, state, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "60")
	return err
}

type RuntimeReadiness struct {
	Activated        bool     `json:"activated"`
	GenerationID     string   `json:"generation_id,omitempty"`
	Profiles         []string `json:"profiles,omitempty"`
	RequiredServices []string `json:"required_services,omitempty"`
	RunningServices  []string `json:"running_services,omitempty"`
}

func (r RuntimeReadiness) Ready() bool {
	if !r.Activated || r.GenerationID == "" {
		return false
	}
	if len(r.RequiredServices) != len(r.RunningServices) {
		return false
	}
	for index := range r.RequiredServices {
		if r.RequiredServices[index] != r.RunningServices[index] {
			return false
		}
	}
	return true
}

func (b *Backend) Readiness(ctx context.Context) (RuntimeReadiness, error) {
	if err := ctx.Err(); err != nil {
		return RuntimeReadiness{}, err
	}
	state, found, err := b.loadRuntime()
	if err != nil {
		return RuntimeReadiness{}, err
	}
	if !found {
		return RuntimeReadiness{}, nil
	}
	composePath := filepath.Join(b.runtimeRoot, "assets", "compose.yaml")
	githubPath := filepath.Join(b.runtimeRoot, "assets", "github.compose.toml")
	ingressPath, err := b.materializeIngressConfig(state.IngressHosts)
	if err != nil {
		return RuntimeReadiness{}, err
	}
	for _, path := range []string{composePath, githubPath, ingressPath, b.tokenPath(), b.containerTokenPath()} {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return RuntimeReadiness{}, statErr
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return RuntimeReadiness{}, errors.New("compose lifecycle readiness input is not a regular file")
		}
	}
	env := []string{
		"LOKI_IMAGE=" + state.CoreImage,
		"LOKI_JOB_IMAGE=" + state.CoreImage,
		"LOKI_WORKSPACE=" + state.Workspace,
		"LOKI_MCP_HOST_PORT=" + strconv.Itoa(state.effectiveMCPPort()),
		"LOKI_MCP_TOKEN_FILE=" + b.containerTokenPath(),
		"LOKI_INGRESS_CONFIG_FILE=" + ingressPath,
		"LOKI_GITHUB_CONFIG_FILE=" + githubPath,
		"LOKI_GITHUB_PRIVATE_KEY_FILE=/dev/null",
		"LOKI_SIGNING_KEY_FILE=/dev/null",
	}
	if state.BrowserImage != "" {
		env = append(env, "LOKI_BROWSER_IMAGE="+state.BrowserImage)
	}
	args := []string{"compose", "--project-name", b.project, "--file", composePath}
	for _, profile := range state.Profiles {
		args = append(args, "--profile", profile)
	}
	args = append(args, "ps", "--status", "running", "--services")
	raw, err := b.runner.Run(ctx, env, args...)
	if err != nil {
		return RuntimeReadiness{}, err
	}
	running := make([]string, 0, 8)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !validProject(line) {
			return RuntimeReadiness{}, errors.New("compose lifecycle readiness returned an invalid service name")
		}
		running = append(running, line)
	}
	sort.Strings(running)
	running = compactRuntimeServices(running)
	required := []string{"egress", "executor", "launcher", "mcp", "runtime"}
	for _, profile := range state.Profiles {
		switch profile {
		case "browser":
			required = append(required, "browser", "browser-proxy")
		case "signing":
			required = append(required, "signing")
		}
	}
	sort.Strings(required)
	required = compactRuntimeServices(required)
	profiles := append([]string(nil), state.Profiles...)
	sort.Strings(profiles)
	return RuntimeReadiness{
		Activated: true, GenerationID: state.GenerationID, Profiles: profiles,
		RequiredServices: required, RunningServices: running,
	}, nil
}

func (b *Backend) DoctorProbe(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, found, err := b.loadRuntime()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("compose lifecycle runtime is not activated")
	}
	return b.compose(
		ctx,
		state,
		"exec", "-T", "launcher",
		"/opt/loki/bin/loki", "host", "runtime-probe",
		"--launcher-layout", "/etc/loki/launcher.json",
		"--toolchain-catalog", "/usr/share/doc/loki/toolchain-catalog.json",
	)
}

func (b *Backend) ActiveJobs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, found, err := b.loadRuntime()
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("compose lifecycle runtime is not activated")
	}
	volume := b.volumeName("launcher-state")
	exists, err := b.volumeExists(ctx, volume)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("compose launcher state volume is unavailable")
	}
	raw, err := b.runner.Run(
		ctx, nil,
		"run", "--rm", "--pull", "never", "--network", "none", "--user", "0:0", "--read-only",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--entrypoint", "/opt/loki/bin/loki",
		"--mount", "type=volume,src="+volume+",dst=/var/lib/loki/launcher,readonly",
		state.CoreImage,
		"host", "runtime-active-jobs",
		"--launcher-layout", "/etc/loki/launcher.json",
	)
	if err != nil {
		return nil, err
	}
	var report struct {
		Jobs []string
	}
	if err = json.Unmarshal(raw, &report); err != nil {
		return nil, errors.New("compose launcher job inventory returned invalid JSON")
	}
	return append([]string(nil), report.Jobs...), nil
}

func compactRuntimeServices(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func (b *Backend) Restore(ctx context.Context, ref string) error {
	dir, err := b.snapshotDirectory(ref)
	if err != nil {
		return err
	}
	var manifest snapshotManifest
	if err = readPrivateJSON(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return err
	}
	if manifest.Version != snapshotManifestVersion || manifest.Volumes == nil {
		return errors.New("compose lifecycle snapshot manifest is invalid")
	}
	if current, found, loadErr := b.loadRuntime(); loadErr != nil {
		return loadErr
	} else if found {
		if _, err = b.compose(ctx, current, "down", "--remove-orphans"); err != nil {
			return err
		}
	}
	helper := manifest.Runtime
	for _, name := range persistentVolumes {
		entry, ok := manifest.Volumes[name]
		if !ok {
			entry = snapshotVolume{}
		}
		volume := b.volumeName(name)
		if !entry.Present {
			if _, err = b.runner.Run(ctx, nil, "volume", "rm", "-f", volume); err != nil {
				return err
			}
			continue
		}
		if helper == nil || !validSHA256(entry.SHA256) {
			return errors.New("compose lifecycle snapshot volume metadata is invalid")
		}
		archive := filepath.Join(dir, name+".tar")
		sum, hashErr := fileSHA256(archive)
		if hashErr != nil || sum != entry.SHA256 {
			return errors.New("compose lifecycle snapshot volume integrity check failed")
		}
		if _, err = b.runner.Run(ctx, nil, "volume", "create", volume); err != nil {
			return err
		}
		if err = b.restoreVolume(ctx, *helper, volume, dir, name+".tar"); err != nil {
			return err
		}
	}
	if manifest.Runtime == nil {
		return removeIfPresent(b.runtimeStatePath())
	}
	return b.saveRuntime(*manifest.Runtime)
}

func (b *Backend) Stop(ctx context.Context) error {
	state, found, err := b.loadRuntime()
	if err != nil || !found {
		return err
	}
	_, err = b.compose(ctx, state, "down", "--remove-orphans")
	return err
}

func (b *Backend) VerifyStopped(ctx context.Context) error {
	state, found, err := b.loadRuntime()
	if err != nil || !found {
		return err
	}
	raw, err := b.compose(ctx, state, "ps", "-q")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "" {
		return errors.New("compose lifecycle services are still running")
	}
	return nil
}

func (b *Backend) stateFor(generation lifecycle.Generation, installation lifecycle.InstallationState, profiles []string) (runtimeState, error) {
	profiles = append([]string(nil), profiles...)
	sort.Strings(profiles)
	core := b.coreRepository + "@" + generation.Spec.CoreImageDigest
	browser := ""
	for _, component := range generation.Spec.Components {
		if component.Name == "browser" {
			browser = b.browserRepository + "@" + component.Digest
		}
	}
	state := runtimeState{
		Version: 1, GenerationID: generation.ID, CoreImage: core, BrowserImage: browser,
		Workspace: installation.Workspace, MCPPort: installation.EffectiveMCPPort(), Profiles: profiles,
	}
	if err := state.validate(); err != nil {
		return runtimeState{}, err
	}
	return state, nil
}

func (s runtimeState) validate() error {
	if s.Version != 1 || !validDigestID(s.GenerationID) || !validImageRef(s.CoreImage) ||
		!filepath.IsAbs(s.Workspace) || filepath.Clean(s.Workspace) != s.Workspace || s.Workspace == string(filepath.Separator) {
		return errors.New("compose lifecycle runtime state is invalid")
	}
	if s.BrowserImage != "" && !validImageRef(s.BrowserImage) {
		return errors.New("compose lifecycle browser image is invalid")
	}
	if s.MCPPort != 0 && (s.MCPPort < 1024 || s.MCPPort > 65535) {
		return errors.New("compose lifecycle MCP port is invalid")
	}
	normalizedHosts, err := lifecycle.NormalizeIngressHosts(s.IngressHosts)
	if err != nil || len(normalizedHosts) != len(s.IngressHosts) {
		return errors.New("compose lifecycle ingress hosts are invalid")
	}
	for index := range normalizedHosts {
		if normalizedHosts[index] != s.IngressHosts[index] {
			return errors.New("compose lifecycle ingress hosts are not canonical")
		}
	}
	if !sort.StringsAreSorted(s.Profiles) {
		return errors.New("compose lifecycle profiles are not canonical")
	}
	for index, profile := range s.Profiles {
		if !validProject(profile) || index > 0 && profile == s.Profiles[index-1] {
			return errors.New("compose lifecycle profile is invalid")
		}
	}
	return nil
}

func validDigestID(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validImageRef(value string) bool {
	repository, digest, ok := strings.Cut(value, "@")
	return ok && validRepository(repository) && validDigestID(digest)
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (b *Backend) materializeIngressConfig(hosts []string) (string, error) {
	raw, err := hostingress.Render(hosts)
	if err != nil {
		return "", err
	}
	path := b.ingressConfigPath()
	if err = safeio.PublishPrivate(path, raw, true); err != nil {
		return "", err
	}
	if err = os.Chmod(path, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func (b *Backend) compose(ctx context.Context, state runtimeState, command ...string) ([]byte, error) {
	if err := b.ensureAssetsAndToken(); err != nil {
		return nil, err
	}
	materialized, err := assets.Materialize(filepath.Join(b.runtimeRoot, "assets"))
	if err != nil {
		return nil, err
	}
	ingressPath, err := b.materializeIngressConfig(state.IngressHosts)
	if err != nil {
		return nil, err
	}
	env := []string{
		"LOKI_IMAGE=" + state.CoreImage,
		"LOKI_JOB_IMAGE=" + state.CoreImage,
		"LOKI_WORKSPACE=" + state.Workspace,
		"LOKI_MCP_HOST_PORT=" + strconv.Itoa(state.effectiveMCPPort()),
		"LOKI_MCP_TOKEN_FILE=" + b.containerTokenPath(),
		"LOKI_INGRESS_CONFIG_FILE=" + ingressPath,
		"LOKI_GITHUB_CONFIG_FILE=" + materialized.GitHubConfig,
		"LOKI_GITHUB_PRIVATE_KEY_FILE=/dev/null",
		"LOKI_SIGNING_KEY_FILE=/dev/null",
	}
	if state.BrowserImage != "" {
		env = append(env, "LOKI_BROWSER_IMAGE="+state.BrowserImage)
	}
	args := []string{"compose", "--project-name", b.project, "--file", materialized.ComposePath}
	for _, profile := range state.Profiles {
		args = append(args, "--profile", profile)
	}
	args = append(args, command...)
	return b.runner.Run(ctx, env, args...)
}

func (b *Backend) ensureAssetsAndToken() error {
	if _, err := assets.Materialize(filepath.Join(b.runtimeRoot, "assets")); err != nil {
		return err
	}
	path := b.tokenPath()
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("compose MCP token must be a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		raw := make([]byte, 32)
		if _, err = rand.Read(raw); err != nil {
			return err
		}
		token := []byte(base64.RawURLEncoding.EncodeToString(raw))
		if err = safeio.PublishPrivate(path, token, false); err != nil {
			return err
		}
	}
	token, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	projection := b.containerTokenPath()
	if err = safeio.PublishPrivate(projection, token, true); err != nil {
		return err
	}
	// Local Compose file secrets are bind mounts. The private parent directory
	// protects the host-side projection while this mode lets the unprivileged
	// MCP container UID read the mounted token.
	return os.Chmod(projection, 0444)
}

func (b *Backend) loadRuntime() (runtimeState, bool, error) {
	var state runtimeState
	if err := readOptionalPrivateJSON(b.runtimeStatePath(), &state); err != nil {
		return runtimeState{}, false, err
	}
	if state.Version == 0 {
		return runtimeState{}, false, nil
	}
	if err := state.validate(); err != nil {
		return runtimeState{}, false, err
	}
	return state, true, nil
}

func (b *Backend) saveRuntime(state runtimeState) error {
	if err := state.validate(); err != nil {
		return err
	}
	return writePrivateJSON(b.runtimeStatePath(), state)
}

func (b *Backend) runtimeStatePath() string { return filepath.Join(b.runtimeRoot, "runtime.json") }
func (b *Backend) tokenPath() string        { return filepath.Join(b.root, "mcp-token") }
func (b *Backend) ingressConfigPath() string {
	return filepath.Join(b.runtimeRoot, "assets", hostingress.FileName)
}
func (b *Backend) containerTokenPath() string {
	return filepath.Join(b.runtimeRoot, "assets", "mcp-token")
}
func (b *Backend) volumeName(name string) string { return b.project + "_" + name }

func (b *Backend) volumeExists(ctx context.Context, name string) (bool, error) {
	raw, err := b.runner.Run(ctx, nil, "volume", "ls", "--quiet", "--filter", "name=^"+name+"$")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == name {
			return true, nil
		}
	}
	return false, nil
}

func (b *Backend) archiveVolume(ctx context.Context, state runtimeState, volume, dir, archive string) error {
	uid, gid := fmt.Sprint(os.Getuid()), fmt.Sprint(os.Getgid())
	_, err := b.runner.Run(ctx, nil,
		"run", "--rm", "--network", "none", "--user", "0:0", "--read-only",
		"--cap-drop", "ALL", "--cap-add", "DAC_READ_SEARCH", "--cap-add", "DAC_OVERRIDE", "--cap-add", "CHOWN",
		"--security-opt", "no-new-privileges",
		"--entrypoint", "/bin/sh",
		"--mount", "type=volume,src="+volume+",dst=/source,readonly",
		"--mount", "type=bind,src="+dir+",dst=/backup",
		state.CoreImage, "-ec",
		`cd /source && tar -cf "/backup/$1" . && chown "$2:$3" "/backup/$1"`,
		"sh", archive, uid, gid,
	)
	return err
}

func (b *Backend) restoreVolume(ctx context.Context, state runtimeState, volume, dir, archive string) error {
	_, err := b.runner.Run(ctx, nil,
		"run", "--rm", "--network", "none", "--user", "0:0",
		"--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--cap-add", "CHOWN",
		"--security-opt", "no-new-privileges",
		"--entrypoint", "/bin/sh",
		"--mount", "type=volume,src="+volume+",dst=/target",
		"--mount", "type=bind,src="+dir+",dst=/backup,readonly",
		state.CoreImage, "-ec",
		`find /target -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -xf "/backup/$1" -C /target`,
		"sh", archive,
	)
	return err
}

func (b *Backend) snapshotDirectory(ref string) (string, error) {
	ref = filepath.Clean(strings.TrimSpace(ref))
	if !filepath.IsAbs(ref) || strings.ContainsRune(ref, 0) {
		return "", errors.New("compose lifecycle snapshot reference is invalid")
	}
	relative, err := filepath.Rel(b.snapshotRoot, ref)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("compose lifecycle snapshot reference escapes snapshot root")
	}
	info, err := os.Lstat(ref)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("compose lifecycle snapshot directory must be private and real")
	}
	return ref, nil
}

func (b *Backend) RuntimeSnapshotUsage(ctx context.Context, ref string) (int64, error) {
	dir, err := b.snapshotDirectory(ref)
	if err != nil {
		return 0, err
	}
	var bytes int64
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("compose lifecycle snapshot contains a symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return nil
		case info.Mode().IsRegular():
			if info.Size() > 0 && bytes > (1<<62)-info.Size() {
				return errors.New("compose lifecycle snapshot accounting overflow")
			}
			bytes += info.Size()
			return nil
		default:
			return errors.New("compose lifecycle snapshot contains an unsupported object")
		}
	})
	return bytes, err
}

func (b *Backend) ListRuntimeSnapshots(ctx context.Context) ([]lifecycle.RuntimeSnapshotInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(b.snapshotRoot)
	if err != nil {
		return nil, err
	}
	result := make([]lifecycle.RuntimeSnapshotInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".snapshot-") {
			return nil, errors.New("compose lifecycle snapshot root contains an unexpected entry")
		}
		ref := filepath.Join(b.snapshotRoot, entry.Name())
		dir, dirErr := b.snapshotDirectory(ref)
		if dirErr != nil {
			return nil, dirErr
		}
		var manifest snapshotManifest
		manifestPath := filepath.Join(dir, "manifest.json")
		if err = readPrivateJSON(manifestPath, &manifest); err != nil {
			return nil, err
		}
		if manifest.Version != snapshotManifestVersion || manifest.Volumes == nil {
			return nil, errors.New("compose lifecycle snapshot manifest is invalid")
		}
		bytes, usageErr := b.RuntimeSnapshotUsage(ctx, dir)
		if usageErr != nil {
			return nil, usageErr
		}
		info, statErr := os.Stat(manifestPath)
		if statErr != nil {
			return nil, statErr
		}
		result = append(result, lifecycle.RuntimeSnapshotInfo{
			Ref: dir, Bytes: bytes, CreatedAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Ref < result[j].Ref
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (b *Backend) DeleteRuntimeSnapshot(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := b.snapshotDirectory(ref)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	root, err := os.Open(b.snapshotRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Sync()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writePrivateJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return safeio.PublishPrivate(path, raw, true)
}

func readOptionalPrivateJSON(path string, target any) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return readPrivateJSON(path, target)
}

func readPrivateJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("compose lifecycle state file must be a private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("compose lifecycle state contains trailing data")
	}
	return nil
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
