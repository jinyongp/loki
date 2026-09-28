//go:build windows

package windows

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loki/internal/host/connect"
)

type acceptanceMirrorDownloader struct {
	files map[string][]byte
	calls []string
}

func (downloader *acceptanceMirrorDownloader) Fetch(_ context.Context, url string, maxBytes int64) ([]byte, error) {
	downloader.calls = append(downloader.calls, url)
	raw, ok := downloader.files[url]
	if !ok {
		return nil, errors.New("unexpected acceptance mirror URL")
	}
	if int64(len(raw)) > maxBytes {
		return nil, errors.New("acceptance mirror fixture exceeds requested bound")
	}
	return append([]byte(nil), raw...), nil
}

type acceptanceTaskPlatform struct {
	delegate  PowerShellConnectionTaskPlatform
	paths     FrontendPaths
	ownership ConnectionTaskOwnership
	owned     bool
}

func (platform *acceptanceTaskPlatform) Probe(ctx context.Context, taskName string) (ConnectionTaskProbe, error) {
	return platform.delegate.Probe(ctx, taskName)
}

func (platform *acceptanceTaskPlatform) Create(ctx context.Context, ownership ConnectionTaskOwnership) error {
	return platform.delegate.Create(ctx, ownership)
}

func (platform *acceptanceTaskPlatform) Remove(ctx context.Context, ownership ConnectionTaskOwnership) error {
	return platform.delegate.Remove(ctx, ownership)
}

func (platform *acceptanceTaskPlatform) ReadOwnership(string) (ConnectionTaskOwnership, bool, error) {
	return platform.ownership, platform.owned, nil
}

func (platform *acceptanceTaskPlatform) WriteOwnership(_ context.Context, ownership ConnectionTaskOwnership) error {
	platform.ownership = ownership
	platform.owned = true
	return nil
}

func (platform *acceptanceTaskPlatform) DeleteOwnership(context.Context, string) error {
	platform.ownership = ConnectionTaskOwnership{}
	platform.owned = false
	return nil
}

func (platform *acceptanceTaskPlatform) VerifyCanonicalFrontend(context.Context) (FrontendPaths, error) {
	return platform.paths, nil
}

func (platform *acceptanceTaskPlatform) CurrentUser() (string, error) {
	return platform.delegate.CurrentUser()
}

type acceptanceOpenAIRunner struct {
	calls []openAIHelperCall
}

func (runner *acceptanceOpenAIRunner) Run(_ context.Context, executable string, args []string, env map[string]string) (NativeProbe, error) {
	copyEnv := make(map[string]string, len(env))
	for name, value := range env {
		copyEnv[name] = value
	}
	runner.calls = append(runner.calls, openAIHelperCall{
		executable: executable,
		args:       append([]string(nil), args...),
		env:        copyEnv,
	})
	switch {
	case reflect.DeepEqual(args, []string{"doctor", "--profile", openAIProfileName, "--json"}):
		return NativeProbe{ExitCode: 0, Stdout: `{"status":"pass"}`}, nil
	case len(args) >= 2 && args[0] == "runtimes" && args[1] == "connect":
		return NativeProbe{ExitCode: 0, Stdout: `{"runtime_state":"ready","process_running":true,"healthy":true,"ready":true}`}, nil
	case reflect.DeepEqual(args, []string{"runtimes", "status", openAIRuntimeAlias, "--json"}):
		return NativeProbe{ExitCode: 0, Stdout: `{"runtime_state":"ready","process_running":true,"healthy":true,"ready":true}`}, nil
	case reflect.DeepEqual(args, []string{"runtimes", "stop", openAIRuntimeAlias, "--json"}):
		return NativeProbe{ExitCode: 0, Stdout: `{"runtime_state":"stopped"}`}, nil
	case reflect.DeepEqual(args, []string{"runtimes", "rm", openAIRuntimeAlias, "--json"}):
		return NativeProbe{ExitCode: 0, Stdout: `{"removed":true}`}, nil
	default:
		return NativeProbe{}, errors.New("unexpected fake-control-plane helper invocation")
	}
}

type acceptanceLocalConnectionSource struct {
	material ConnectionMaterial
}

func (source *acceptanceLocalConnectionSource) Read(context.Context, string) (ConnectionMaterial, error) {
	return source.material, nil
}

func TestWindowsProviderAcceptance(t *testing.T) {
	if os.Getenv("LOKI_WINDOWS_PROVIDER_ACCEPTANCE") != "1" {
		t.Skip("Windows provider acceptance is opt-in")
	}
	tag := strings.TrimSpace(os.Getenv("LOKI_ACCEPTANCE_RELEASE_TAG"))
	catalogPath := strings.TrimSpace(os.Getenv("LOKI_ACCEPTANCE_HELPER_CATALOG"))
	archivePath := strings.TrimSpace(os.Getenv("LOKI_ACCEPTANCE_HELPER_ARCHIVE"))
	frontendPath := strings.TrimSpace(os.Getenv("LOKI_ACCEPTANCE_FRONTEND"))
	for name, value := range map[string]string{
		"LOKI_ACCEPTANCE_RELEASE_TAG":    tag,
		"LOKI_ACCEPTANCE_HELPER_CATALOG": catalogPath,
		"LOKI_ACCEPTANCE_HELPER_ARCHIVE": archivePath,
		"LOKI_ACCEPTANCE_FRONTEND":       frontendPath,
	} {
		if value == "" {
			t.Fatalf("%s is required", name)
		}
	}
	if !filepath.IsAbs(catalogPath) || !filepath.IsAbs(archivePath) || !filepath.IsAbs(frontendPath) {
		t.Fatal("provider acceptance candidate paths must be absolute")
	}
	catalogRaw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	archiveRaw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := connect.LoadCatalog(catalogRaw)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := selectHelper(catalog, OpenAIHelperID, FrontendArchitecture)
	if err != nil {
		t.Fatal(err)
	}

	binding := verifyProviderCandidate(t, tag, catalogPath, archivePath, frontendPath)
	t.Log("candidate source, executed frontend binding, helper archive and compliance sidecars verified")
	catalogURL, err := helperMirrorURL(tag, helperCatalogAssetName)
	if err != nil {
		t.Fatal(err)
	}
	archiveURL, err := helperMirrorURL(tag, helper.Archive.MirrorAsset)
	if err != nil {
		t.Fatal(err)
	}
	downloader := &acceptanceMirrorDownloader{files: map[string][]byte{
		catalogURL: catalogRaw,
		archiveURL: archiveRaw,
	}}

	localRoot := t.TempDir()
	frontendPaths, err := ResolveFrontendPaths(localRoot)
	if err != nil {
		t.Fatal(err)
	}
	frontendPlatform := NewWindowsFrontendPlatform()
	if err = frontendPlatform.EnsurePrivateDirectory(t.Context(), frontendPaths.Root); err != nil {
		t.Fatal(err)
	}
	helperManager := HelperManager{
		Platform:    WindowsHelperInstallPlatform{},
		Downloader:  downloader,
		Binding:     binding,
		HelpersRoot: frontendPaths.HelpersRoot,
	}
	installed, err := helperManager.Ensure(t.Context(), OpenAIHelperID, FrontendArchitecture)
	if err != nil {
		t.Fatal(err)
	}
	for _, url := range downloader.calls {
		if !strings.HasPrefix(url, "https://github.com/jinyongp/loki/releases/download/"+tag+"/") ||
			strings.Contains(url, "openai/tunnel-client/releases") {
			t.Fatalf("helper acceptance contacted a non-Loki mirror URL %q", url)
		}
	}

	distribution := providerAcceptanceDistribution()
	connectionStore := NewWindowsConnectionStateStore(localRoot)
	providerRoot, err := connectionStore.ProviderRoot(distribution, OpenAIProviderID)
	if err != nil {
		t.Fatal(err)
	}
	openAIStore := NewWindowsOpenAIProviderStore()

	// Native helper smoke needs isolated tunnel-client state, but it must not
	// pre-create the canonical provider root. A pre-existing provider root
	// without connection.json is intentionally rejected by ConnectionManager
	// as unowned state.
	smokeRoot := filepath.Join(t.TempDir(), "openai-smoke")
	providerPaths, err := openAIStore.Ensure(t.Context(), smokeRoot)
	if err != nil {
		t.Fatal(err)
	}
	defaultState := filepath.Join(t.TempDir(), "seeded-default-tunnel-state")
	if err = os.MkdirAll(defaultState, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(defaultState, "sentinel.txt")
	if err = os.WriteFile(sentinel, []byte("unrelated-user-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	smokeEnv := openAIBaseEnvironment(providerPaths)
	smokeEnv["CODEX_HOME"] = defaultState
	native := ExecOpenAIHelperRunner{}
	for _, args := range [][]string{
		{"--version"},
		{"help", "plugin"},
		{"runtimes", "--help"},
		{"runtimes", "list", "--json"},
	} {
		probe, runErr := native.Run(t.Context(), installed.ExecutablePath, args, smokeEnv)
		if runErr != nil {
			t.Fatalf("candidate helper %q failed to start: %v", args, runErr)
		}
		if probe.ExitCode != 0 {
			t.Fatalf("candidate helper %q exited %d: %s", args, probe.ExitCode, probe.Stderr)
		}
	}
	rawSentinel, err := os.ReadFile(sentinel)
	if err != nil || string(rawSentinel) != "unrelated-user-state" {
		t.Fatal("Loki-isolated helper smoke mutated unrelated tunnel-client state")
	}

	credentialTarget := "Loki/Acceptance/VAL-013/" + distribution
	credentialSecret := acceptanceSecret(t)
	credentials := WindowsCredentialManager{}
	if err = credentials.Put(t.Context(), credentialTarget, credentialSecret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = credentials.Delete(context.Background(), credentialTarget) })
	gotSecret, err := credentials.Get(t.Context(), credentialTarget)
	if err != nil {
		t.Fatal(err)
	}
	if gotSecret != credentialSecret {
		t.Fatal("Windows Credential Manager round-trip changed generated secret material")
	}
	if err = credentials.Delete(t.Context(), credentialTarget); err != nil {
		t.Fatal(err)
	}
	if _, err = credentials.Get(t.Context(), credentialTarget); err == nil {
		t.Fatal("Windows Credential Manager credential remained readable after delete")
	}

	taskPaths := FrontendPaths{Binary: frontendPath}
	taskPlatform := &acceptanceTaskPlatform{
		delegate: PowerShellConnectionTaskPlatform{},
		paths:    taskPaths,
	}
	taskManager := ConnectionTaskManager{Platform: taskPlatform}
	expectedTask := expectedConnectionTask(taskPaths, distribution)
	t.Cleanup(func() {
		_ = taskPlatform.delegate.Remove(context.Background(), expectedTask)
	})
	if probe, probeErr := taskPlatform.Probe(t.Context(), expectedTask.TaskName); probeErr != nil || probe.Present {
		t.Fatal("connection startup task existed before any managed adapter was enabled")
	}

	runtimeSecret := acceptanceSecret(t)
	lokiToken := acceptanceSecret(t)
	adapterRunner := &acceptanceOpenAIRunner{}
	localSource := &acceptanceLocalConnectionSource{material: ConnectionMaterial{
		LocalOrigin:        "http://127.0.0.1:18765/mcp",
		Transport:          "streamable-http",
		Reachability:       "loopback",
		AuthenticationType: "bearer-token-file",
		Token:              lokiToken,
	}}
	adapter := &OpenAIAdapter{
		Credentials: credentials,
		Runner:      adapterRunner,
		Local:       localSource,
		Store:       openAIStore,
		SetupConfig: OpenAISetupConfig{
			TunnelID:   "tunnel_val013_" + strings.ReplaceAll(distribution, "-", "_"),
			RuntimeKey: runtimeSecret,
		},
	}
	manager := ConnectionManager{
		Helpers:  helperManager,
		Store:    connectionStore,
		Tasks:    taskManager,
		Adapters: []RemoteConnectionAdapter{adapter},
		Platform: FrontendArchitecture,
	}
	t.Cleanup(func() { _ = credentials.Delete(context.Background(), defaultOpenAICredentialTarget(distribution)) })

	if err = manager.Setup(t.Context(), distribution, OpenAIProviderID); err != nil {
		t.Fatal(err)
	}
	taskProbe, err := taskPlatform.Probe(t.Context(), expectedTask.TaskName)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := taskPlatform.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	if err = validateConnectionTaskProbe(taskProbe, expectedTask, userID); err != nil {
		t.Fatal(err)
	}
	if taskProbe.ExecutionTimeTicks != connectionTaskExecutionTicks {
		t.Fatal("connection startup task is not finite")
	}

	// Inspect persisted bytes while they exist; checking only after Remove
	// would silently skip the leak assertion on a successful cleanup.
	metadataBeforeRemove, err := os.ReadFile(filepath.Join(providerRoot, openAIAdapterMetadataFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadataBeforeRemove), runtimeSecret) || strings.Contains(string(metadataBeforeRemove), lokiToken) {
		t.Fatal("provider secret or Loki bearer material appeared in persisted provider metadata")
	}

	adapter.SetupConfig = OpenAISetupConfig{}
	if err = manager.Stop(t.Context(), distribution, OpenAIProviderID); err != nil {
		t.Fatal(err)
	}
	if probe, probeErr := taskPlatform.Probe(t.Context(), expectedTask.TaskName); probeErr != nil || probe.Present {
		t.Fatal("connection startup task remained after the last enabled adapter was stopped")
	}

	if err = manager.Start(t.Context(), distribution, OpenAIProviderID); err != nil {
		t.Fatal(err)
	}
	taskProbe, err = taskPlatform.Probe(t.Context(), expectedTask.TaskName)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateConnectionTaskProbe(taskProbe, expectedTask, userID); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(t.Context(), distribution, OpenAIProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Runtime.Healthy || !status.Runtime.Ready {
		t.Fatalf("fake-control-plane adapter status is not healthy/ready: %+v", status.Runtime)
	}

	if err = manager.Remove(t.Context(), distribution, OpenAIProviderID); err != nil {
		t.Fatal(err)
	}
	if probe, probeErr := taskPlatform.Probe(t.Context(), expectedTask.TaskName); probeErr != nil || probe.Present {
		t.Fatal("connection startup task remained after the managed adapter was removed")
	}
	for _, call := range adapterRunner.calls {
		for _, arg := range call.args {
			if strings.Contains(arg, runtimeSecret) || strings.Contains(arg, lokiToken) {
				t.Fatal("provider secret or Loki bearer material appeared in process arguments")
			}
		}
	}
	metadataRaw, readErr := os.ReadFile(filepath.Join(providerRoot, openAIAdapterMetadataFileName))
	if readErr == nil &&
		(strings.Contains(string(metadataRaw), runtimeSecret) || strings.Contains(string(metadataRaw), lokiToken)) {
		t.Fatal("provider secret or Loki bearer material appeared in provider metadata")
	}
	for _, action := range taskProbe.Actions {
		if strings.Contains(action.Arguments, runtimeSecret) || strings.Contains(action.Arguments, lokiToken) {
			t.Fatal("provider secret or Loki bearer material appeared in Scheduled Task metadata")
		}
	}
}

func providerAcceptanceDistribution() string {
	runID := strings.TrimSpace(os.Getenv("GITHUB_RUN_ID"))
	attempt := strings.TrimSpace(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if runID == "" {
		runID = "local"
	}
	if attempt == "" {
		attempt = "1"
	}
	value := "loki-val013-" + runID + "-" + attempt
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func acceptanceSecret(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}
