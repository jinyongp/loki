package windows

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"loki/internal/host/connect"
)

type fakeOpenAICredentials struct {
	values  map[string]string
	puts    []string
	gets    []string
	deletes []string
	errAt   string
}

func newFakeOpenAICredentials() *fakeOpenAICredentials {
	return &fakeOpenAICredentials{values: map[string]string{}}
}

func (credentials *fakeOpenAICredentials) Put(_ context.Context, target, secret string) error {
	if credentials.errAt == "put" {
		return errors.New("put failed")
	}
	credentials.puts = append(credentials.puts, target)
	credentials.values[target] = secret
	return nil
}

func (credentials *fakeOpenAICredentials) Get(_ context.Context, target string) (string, error) {
	if credentials.errAt == "get" {
		return "", errors.New("get failed")
	}
	credentials.gets = append(credentials.gets, target)
	value, ok := credentials.values[target]
	if !ok {
		return "", errors.New("credential missing")
	}
	return value, nil
}

func (credentials *fakeOpenAICredentials) Delete(_ context.Context, target string) error {
	if credentials.errAt == "delete" {
		return errors.New("delete failed")
	}
	credentials.deletes = append(credentials.deletes, target)
	delete(credentials.values, target)
	return nil
}

type openAIHelperCall struct {
	executable string
	args       []string
	env        map[string]string
}

type fakeOpenAIHelperRunner struct {
	calls   []openAIHelperCall
	results []NativeProbe
	errs    []error
}

func (runner *fakeOpenAIHelperRunner) Run(_ context.Context, executable string, args []string, env map[string]string) (NativeProbe, error) {
	copyEnv := map[string]string{}
	for key, value := range env {
		copyEnv[key] = value
	}
	runner.calls = append(runner.calls, openAIHelperCall{
		executable: executable, args: append([]string(nil), args...), env: copyEnv,
	})
	index := len(runner.calls) - 1
	var result NativeProbe
	if index < len(runner.results) {
		result = runner.results[index]
	}
	var err error
	if index < len(runner.errs) {
		err = runner.errs[index]
	}
	return result, err
}

type fakeOpenAILocalSource struct {
	material ConnectionMaterial
	err      error
	calls    int
}

func (source *fakeOpenAILocalSource) Read(context.Context, string) (ConnectionMaterial, error) {
	source.calls++
	return source.material, source.err
}

type fakeOpenAIProviderStore struct {
	paths    OpenAIProviderPaths
	metadata OpenAIProviderMetadata
	present  bool
	writes   int
	cleanups int
	errAt    string
}

func (store *fakeOpenAIProviderStore) Ensure(context.Context, string) (OpenAIProviderPaths, error) {
	if store.errAt == "ensure" {
		return OpenAIProviderPaths{}, errors.New("ensure failed")
	}
	return store.paths, nil
}

func (store *fakeOpenAIProviderStore) Read(string) (OpenAIProviderMetadata, bool, error) {
	if store.errAt == "read" {
		return OpenAIProviderMetadata{}, false, errors.New("read failed")
	}
	return store.metadata, store.present, nil
}

func (store *fakeOpenAIProviderStore) Write(_ context.Context, _ string, metadata OpenAIProviderMetadata) error {
	if store.errAt == "write" {
		return errors.New("write failed")
	}
	store.metadata = metadata
	store.present = true
	store.writes++
	return nil
}

func (store *fakeOpenAIProviderStore) Cleanup(context.Context, string) error {
	if store.errAt == "cleanup" {
		return errors.New("cleanup failed")
	}
	store.metadata = OpenAIProviderMetadata{}
	store.present = false
	store.cleanups++
	return nil
}

func openAIAdapterFixture() (*OpenAIAdapter, ConnectionRuntimeContext, *fakeOpenAICredentials, *fakeOpenAIHelperRunner, *fakeOpenAILocalSource, *fakeOpenAIProviderStore) {
	credentials := newFakeOpenAICredentials()
	runner := &fakeOpenAIHelperRunner{results: []NativeProbe{
		{ExitCode: 0, Stdout: `{"healthy":true}`},
		{ExitCode: 0, Stdout: `{"status":"pass"}`},
		{ExitCode: 0, Stdout: `{"runtime_state":"ready","process_running":true,"healthy":true,"ready":true}`},
	}}
	local := &fakeOpenAILocalSource{material: ConnectionMaterial{
		LocalOrigin: "http://127.0.0.1:18765/mcp",
		Token:       "loki-token-secret",
	}}
	store := &fakeOpenAIProviderStore{paths: OpenAIProviderPaths{
		Root:       `C:\Programs\Loki\connections\loki-mcp\openai`,
		Metadata:   `C:\Programs\Loki\connections\loki-mcp\openai\openai.json`,
		StateDir:   `C:\Programs\Loki\connections\loki-mcp\openai\tunnel-client-state`,
		ProfileDir: `C:\Programs\Loki\connections\loki-mcp\openai\tunnel-client-profiles`,
	}}
	adapter := &OpenAIAdapter{
		Credentials: credentials,
		Runner:      runner,
		Local:       local,
		Store:       store,
		SetupConfig: OpenAISetupConfig{
			TunnelID:   "tunnel_0123456789abcdef0123456789abcdef",
			RuntimeKey: "runtime-key-secret",
		},
	}
	runtime := ConnectionRuntimeContext{
		Distribution: "loki-mcp",
		Provider:     OpenAIProviderID,
		Root:         store.paths.Root,
		Helper: ManagedHelper{
			Helper: connect.Helper{
				ID: OpenAIHelperID, Provider: OpenAIProviderID, Version: "0.0.15",
				Platform: "windows-amd64", Executable: "tunnel-client.exe",
			},
			ExecutablePath: `C:\Programs\Loki\helpers\openai-tunnel-client\0.0.15\windows-amd64\tunnel-client.exe`,
		},
	}
	return adapter, runtime, credentials, runner, local, store
}

func TestOpenAISetupUsesSecretReferencesAndProcessScopedAuthorization(t *testing.T) {
	adapter, runtime, credentials, runner, _, store := openAIAdapterFixture()
	if err := adapter.Setup(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	target := defaultOpenAICredentialTarget("loki-mcp")
	if credentials.values[target] != "runtime-key-secret" || !reflect.DeepEqual(credentials.puts, []string{target}) {
		t.Fatalf("credentials=%+v", credentials)
	}
	if !store.present || store.metadata.CredentialTarget != target ||
		store.metadata.TunnelID != adapter.SetupConfig.TunnelID ||
		store.metadata.LocalOrigin != "http://127.0.0.1:18765/mcp" {
		t.Fatalf("metadata=%+v", store.metadata)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls=%+v", runner.calls)
	}
	connectCall := runner.calls[0]
	wantPrefix := []string{
		"runtimes", "connect", "--alias", "loki",
		"--tunnel-id", adapter.SetupConfig.TunnelID,
		"--profile", "loki",
		"--profile-dir", store.paths.ProfileDir,
		"--runtime-api-key", "env:" + openAIRuntimeKeyEnv,
		"--mcp-server-url", "http://127.0.0.1:18765/mcp",
		"--json",
	}
	if !reflect.DeepEqual(connectCall.args, wantPrefix) {
		t.Fatalf("connect args=%q want=%q", connectCall.args, wantPrefix)
	}
	for _, arg := range connectCall.args {
		if strings.Contains(arg, "runtime-key-secret") || strings.Contains(arg, "loki-token-secret") {
			t.Fatalf("literal secret leaked into argv %q", arg)
		}
	}
	if connectCall.env[openAIRuntimeKeyEnv] != "runtime-key-secret" ||
		connectCall.env[openAIMCPAuthorizationEnv] != "Bearer loki-token-secret" ||
		connectCall.env["MCP_EXTRA_HEADERS"] != "Authorization: env:"+openAIMCPAuthorizationEnv ||
		connectCall.env["TUNNEL_CLIENT_STATE_DIR"] != store.paths.StateDir ||
		connectCall.env["TUNNEL_CLIENT_PROFILE_DIR"] != store.paths.ProfileDir {
		t.Fatalf("connect env=%v", connectCall.env)
	}
	if got := runner.calls[1].args; !reflect.DeepEqual(got, []string{"doctor", "--profile", "loki", "--json"}) {
		t.Fatalf("doctor args=%q", got)
	}
	raw, err := encodeOpenAIMetadata(store.metadata)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "runtime-key-secret") || strings.Contains(string(raw), "loki-token-secret") {
		t.Fatalf("secret leaked into metadata: %s", raw)
	}
}

func TestOpenAISetupExistingRecordRestartsBeforeApplyingRotatedRuntimeKey(t *testing.T) {
	adapter, runtime, credentials, runner, _, store := openAIAdapterFixture()
	target := defaultOpenAICredentialTarget("loki-mcp")
	credentials.values[target] = "old-runtime-key"
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         adapter.SetupConfig.TunnelID,
		CredentialTarget: target,
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: "http://127.0.0.1:18765/mcp",
	}

	if err := adapter.Setup(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls=%+v", runner.calls)
	}
	if got := runner.calls[0].args; !reflect.DeepEqual(got, []string{"runtimes", "stop", "loki", "--json"}) {
		t.Fatalf("setup reconciliation did not stop existing runtime first: %q", got)
	}
	if runner.calls[0].env[openAIRuntimeKeyEnv] != "" ||
		runner.calls[0].env[openAIMCPAuthorizationEnv] != "" {
		t.Fatalf("stop env unexpectedly contains secrets: %v", runner.calls[0].env)
	}
	connectCall := runner.calls[1]
	if len(connectCall.args) < 2 || connectCall.args[0] != "runtimes" || connectCall.args[1] != "connect" {
		t.Fatalf("setup reconciliation did not reconnect after stop: %+v", connectCall)
	}
	if connectCall.env[openAIRuntimeKeyEnv] != "runtime-key-secret" ||
		connectCall.env[openAIMCPAuthorizationEnv] != "Bearer loki-token-secret" {
		t.Fatalf("reconciled runtime env=%v", connectCall.env)
	}
	if got := runner.calls[2].args; !reflect.DeepEqual(got, []string{"doctor", "--profile", "loki", "--json"}) {
		t.Fatalf("setup reconciliation doctor args=%q", got)
	}
	if credentials.values[target] != "runtime-key-secret" ||
		!reflect.DeepEqual(credentials.puts, []string{target}) {
		t.Fatalf("rotated credential was not committed after successful restart: %+v", credentials)
	}
}

func TestOpenAIStartRestartsBeforeReloadingCredentialAndCurrentLokiToken(t *testing.T) {
	adapter, runtime, credentials, runner, local, store := openAIAdapterFixture()
	target := defaultOpenAICredentialTarget("loki-mcp")
	credentials.values[target] = "stored-runtime-key"
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         adapter.SetupConfig.TunnelID,
		CredentialTarget: target,
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: "http://127.0.0.1:18765/mcp",
	}
	adapter.SetupConfig = OpenAISetupConfig{}
	local.material.LocalOrigin = "http://127.0.0.1:19000/mcp"
	local.material.Token = "rotated-loki-token"
	if err := adapter.Start(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(credentials.gets, []string{target}) {
		t.Fatalf("credential gets=%v", credentials.gets)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls=%+v", runner.calls)
	}
	if got := runner.calls[0].args; !reflect.DeepEqual(got, []string{"runtimes", "stop", "loki", "--json"}) {
		t.Fatalf("restart did not stop existing runtime first: %q", got)
	}
	if runner.calls[0].env[openAIRuntimeKeyEnv] != "" ||
		runner.calls[0].env[openAIMCPAuthorizationEnv] != "" {
		t.Fatalf("stop env unexpectedly contains rotated secrets: %v", runner.calls[0].env)
	}
	connectCall := runner.calls[1]
	if len(connectCall.args) < 2 || connectCall.args[0] != "runtimes" || connectCall.args[1] != "connect" {
		t.Fatalf("restart did not reconnect after stop: %+v", connectCall)
	}
	if connectCall.env[openAIRuntimeKeyEnv] != "stored-runtime-key" ||
		connectCall.env[openAIMCPAuthorizationEnv] != "Bearer rotated-loki-token" {
		t.Fatalf("restarted runtime env=%v", connectCall.env)
	}
	if got := runner.calls[2].args; !reflect.DeepEqual(got, []string{"doctor", "--profile", "loki", "--json"}) {
		t.Fatalf("restart doctor args=%q", got)
	}
	if store.metadata.LocalOrigin != "http://127.0.0.1:19000/mcp" {
		t.Fatalf("metadata local origin=%q", store.metadata.LocalOrigin)
	}
}

func TestOpenAIStartFailsClosedWhenOwnedRuntimeCannotStop(t *testing.T) {
	adapter, runtime, credentials, runner, _, store := openAIAdapterFixture()
	target := defaultOpenAICredentialTarget("loki-mcp")
	credentials.values[target] = "stored-runtime-key"
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         adapter.SetupConfig.TunnelID,
		CredentialTarget: target,
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: "http://127.0.0.1:18765/mcp",
	}
	adapter.SetupConfig = OpenAISetupConfig{}
	runner.results = []NativeProbe{{ExitCode: 2, Stderr: "owned runtime did not stop"}}

	err := adapter.Start(t.Context(), runtime)
	if err == nil || !strings.Contains(err.Error(), "before restart") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 1 ||
		!reflect.DeepEqual(runner.calls[0].args, []string{"runtimes", "stop", "loki", "--json"}) {
		t.Fatalf("failed stop should prevent reconnect: %+v", runner.calls)
	}
}

func TestOpenAIStatusUsesNativeStructuredStatusWithoutCredentialRead(t *testing.T) {
	adapter, runtime, credentials, runner, _, store := openAIAdapterFixture()
	target := defaultOpenAICredentialTarget("loki-mcp")
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         adapter.SetupConfig.TunnelID,
		CredentialTarget: target,
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: "http://127.0.0.1:18765/mcp",
	}
	runner.results = []NativeProbe{{
		ExitCode: 0,
		Stdout:   `{"runtime_state":"healthy","process_running":true,"healthy":true,"ready":false}`,
	}}
	status, err := adapter.Status(t.Context(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Healthy || status.Ready || status.State != "healthy" {
		t.Fatalf("status=%+v", status)
	}
	if len(credentials.gets) != 0 {
		t.Fatalf("status unexpectedly read runtime credential: %v", credentials.gets)
	}
	if got := runner.calls[0].args; !reflect.DeepEqual(got, []string{"runtimes", "status", "loki", "--json"}) {
		t.Fatalf("status args=%q", got)
	}
}

func TestOpenAIRemoveUsesLocalRuntimeRemovalAndDeletesCredential(t *testing.T) {
	adapter, runtime, credentials, runner, _, store := openAIAdapterFixture()
	target := defaultOpenAICredentialTarget("loki-mcp")
	credentials.values[target] = "stored-key"
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         adapter.SetupConfig.TunnelID,
		CredentialTarget: target,
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: "http://127.0.0.1:18765/mcp",
	}
	runner.results = []NativeProbe{{ExitCode: 0}, {ExitCode: 0}}
	if err := adapter.Remove(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 ||
		!reflect.DeepEqual(runner.calls[0].args, []string{"runtimes", "stop", "loki", "--json"}) ||
		!reflect.DeepEqual(runner.calls[1].args, []string{"runtimes", "rm", "loki", "--json"}) {
		t.Fatalf("remove calls=%+v", runner.calls)
	}
	for _, call := range runner.calls {
		for _, arg := range call.args {
			if strings.Contains(strings.ToLower(arg), "admin") ||
				strings.Contains(strings.ToLower(arg), "tunnels delete") {
				t.Fatalf("remove attempted remote/admin operation %q", arg)
			}
		}
	}
	if !reflect.DeepEqual(credentials.deletes, []string{target}) || store.cleanups != 1 {
		t.Fatalf("deletes=%v cleanups=%d", credentials.deletes, store.cleanups)
	}
}

func TestOpenAISetupRejectsTunnelRebinding(t *testing.T) {
	adapter, runtime, _, runner, _, store := openAIAdapterFixture()
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         "tunnel_existing",
		CredentialTarget: defaultOpenAICredentialTarget("loki-mcp"),
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: "http://127.0.0.1:18765/mcp",
	}
	if err := adapter.Setup(t.Context(), runtime); err == nil || !strings.Contains(err.Error(), "different tunnel") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("tunnel rebinding launched helper: %+v", runner.calls)
	}
}

func TestOpenAIStartFailsClosedWhenCredentialMissing(t *testing.T) {
	adapter, runtime, _, runner, _, store := openAIAdapterFixture()
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         adapter.SetupConfig.TunnelID,
		CredentialTarget: defaultOpenAICredentialTarget("loki-mcp"),
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: "http://127.0.0.1:18765/mcp",
	}
	adapter.SetupConfig = OpenAISetupConfig{}
	if err := adapter.Start(t.Context(), runtime); err == nil || !strings.Contains(err.Error(), "credential is unavailable") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("missing credential launched helper: %+v", runner.calls)
	}
}

func TestOpenAIProcessFailureRedactsRuntimeAndLokiSecrets(t *testing.T) {
	probe := redactOpenAIProbe(NativeProbe{
		ExitCode: 1,
		Stdout:   "runtime-key-secret",
		Stderr:   "Authorization: Bearer loki-token-secret",
	}, "runtime-key-secret", "loki-token-secret")
	err := openAIProcessFailure("test", probe)
	if strings.Contains(err.Error(), "runtime-key-secret") || strings.Contains(err.Error(), "loki-token-secret") {
		t.Fatalf("secret leaked in error: %v", err)
	}
}

func TestOpenAIReferenceLinksAreExact(t *testing.T) {
	if OpenAITunnelsURL != "https://platform.openai.com/settings/organization/tunnels" ||
		OpenAIRuntimeKeysURL != "https://platform.openai.com/settings/organization/api-keys" ||
		OpenAIConnectorsURL != "https://chatgpt.com/#settings/Connectors" {
		t.Fatal("OpenAI setup reference links drifted")
	}
}
