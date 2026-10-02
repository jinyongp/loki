package windows

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func openAIStatusFixture() (*OpenAIAdapter, ConnectionRuntimeContext, *fakeOpenAICredentials, *fakeOpenAIHelperRunner, *fakeOpenAILocalSource, *fakeOpenAIProviderStore) {
	adapter, runtime, credentials, runner, local, store := openAIAdapterFixture()
	store.present = true
	store.metadata = OpenAIProviderMetadata{
		SchemaVersion:    openAIAdapterSchemaVersion,
		TunnelID:         adapter.SetupConfig.TunnelID,
		CredentialTarget: defaultOpenAICredentialTarget(runtime.Distribution),
		RuntimeAlias:     openAIRuntimeAlias, ProfileName: openAIProfileName,
		LocalOrigin: local.material.LocalOrigin,
	}
	runner.results = []NativeProbe{{Stdout: `{"runtime_state":"ready","process_running":true,"healthy":true,"ready":true}`}}
	return adapter, runtime, credentials, runner, local, store
}

func TestOpenAIStatusDetectsLocalEndpointFailuresWithoutMutations(t *testing.T) {
	for _, kind := range []string{"closed", "changed", "unverified"} {
		t.Run(kind, func(t *testing.T) {
			adapter, runtime, credentials, runner, local, store := openAIStatusFixture()
			probeCalls := 0
			adapter.ProbeLocalEndpoint = func(context.Context, string) error {
				probeCalls++
				return errors.New("raw error with private value: " + local.material.Token)
			}
			switch kind {
			case "changed":
				local.material.LocalOrigin = "http://127.0.0.1:18766/mcp"
			case "unverified":
				local.err = errors.New("local state failure: " + local.material.Token)
			}
			status, err := adapter.Status(t.Context(), runtime)
			if err != nil || status.State != "degraded" || status.Healthy || status.Ready ||
				!strings.Contains(status.Detail, "loki connection start --distribution loki-mcp openai") || strings.Contains(status.Detail, local.material.Token) {
				t.Fatalf("status=%+v err=%v", status, err)
			}
			if (kind == "closed" && probeCalls != 1) || (kind != "closed" && probeCalls != 0) || local.calls != 1 {
				t.Fatalf("probe calls=%d local reads=%d", probeCalls, local.calls)
			}
			if len(runner.calls) != 1 || runner.calls[0].args[1] != "status" || len(credentials.gets) != 0 || len(credentials.puts) != 0 || store.writes != 0 {
				t.Fatal("status inspection mutated runtime state or read runtime credentials")
			}
		})
	}
}

func TestOpenAIStatusProbesListenerOnEveryHealthyQuery(t *testing.T) {
	adapter, runtime, _, runner, local, store := openAIStatusFixture()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	local.material.LocalOrigin = "http://" + listener.Addr().String() + "/mcp"
	store.metadata.LocalOrigin = local.material.LocalOrigin
	adapter.ProbeLocalEndpoint = nil
	runner.results = append(runner.results, runner.results[0])
	status, err := adapter.Status(t.Context(), runtime)
	if err != nil || !status.Healthy || !status.Ready || status.State != "ready" {
		t.Fatalf("open listener: %+v %v", status, err)
	}
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	status, err = adapter.Status(t.Context(), runtime)
	if err != nil || status.Healthy || status.Ready || status.State != "degraded" {
		t.Fatalf("closed listener: %+v %v", status, err)
	}
}

func TestOpenAIStatusSkipsStoppedRuntimeAndHonorsCancellation(t *testing.T) {
	adapter, runtime, _, runner, local, _ := openAIStatusFixture()
	runner.results[0].Stdout = `{"runtime_state":"stopped","process_running":false,"healthy":false,"ready":false}`
	status, err := adapter.Status(t.Context(), runtime)
	if err != nil || status.State != "stopped" || local.calls != 0 {
		t.Fatalf("stopped runtime: %+v %v", status, err)
	}
	runner.calls = nil
	runner.results[0].Stdout = `{"runtime_state":"ready","process_running":true,"healthy":true,"ready":true}`
	ctx, cancel := context.WithCancel(t.Context())
	adapter.ProbeLocalEndpoint = func(context.Context, string) error { cancel(); return context.Canceled }
	if _, err = adapter.Status(ctx, runtime); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}
