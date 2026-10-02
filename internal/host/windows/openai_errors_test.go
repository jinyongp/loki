package windows

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOpenAIDoctorErrorsPrioritizeLocalCauseAndRetainDetails(t *testing.T) {
	for _, test := range []struct {
		name, cause, want string
	}{
		{"windows-refused", "dial tcp 127.0.0.1:18765: connectex: the target machine actively refused it", "connection refused"},
		{"refused", "dial tcp: connection refused", "connection refused"},
		{"timeout", "context deadline exceeded", "timed out"},
		{"http-rejected", "server returned HTTP 401", "endpoint check failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(openAIDoctorReport{
				FailedChecks: []string{"oauth_metadata", "mcp_server_reachable", "mcp_server_reachable"},
				Checks: []openAIDoctorCheck{
					{ID: "oauth_metadata", Summary: "oauth discovery GET failed: " + test.cause},
					{ID: "mcp_server_reachable", Summary: test.cause},
					{ID: "config_source", Summary: "passing check must stay hidden"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = openAIDoctorFailure(NativeProbe{ExitCode: 2, Stdout: string(raw)}, "http://127.0.0.1:18765/mcp")
			var failure *OpenAIDoctorError
			if !errors.As(err, &failure) || !strings.Contains(err.Error(), test.want) || !failure.LocalEndpointFailed {
				t.Fatalf("unexpected cause: %v", err)
			}
			if len(failure.Details) != 2 || strings.Contains(err.Error(), "oauth discovery") ||
				strings.Contains(strings.Join(failure.Details, " "), "passing check") {
				t.Fatalf("repeated or unrelated details: %+v", failure)
			}
		})
	}
}

func TestOpenAIDoctorErrorsPreserveUnknownChecks(t *testing.T) {
	err := openAIDoctorFailure(NativeProbe{ExitCode: 2, Stderr: `{
		"failed_checks":["control_plane_access"],
		"checks":[{"id":"control_plane_access","summary":"runtime key not authorized"}]
	}`}, "http://127.0.0.1:18765/mcp")
	var failure *OpenAIDoctorError
	if !errors.As(err, &failure) || failure.LocalEndpointFailed ||
		!strings.Contains(err.Error(), "control_plane_access") || len(failure.Details) != 1 {
		t.Fatalf("unknown check was dropped or blamed on the local endpoint: %v", err)
	}
}

func TestOpenAIDoctorDetailedErrorsUseRedactedProbe(t *testing.T) {
	adapter, runtime, _, runner, _, store := openAIAdapterFixture()
	runner.results = []NativeProbe{{ExitCode: 2, Stdout: `{
		"failed_checks":["mcp_server_reachable"],
		"checks":[{"id":"mcp_server_reachable","summary":"connection refused; runtime-key-fixture Bearer loki-token-fixture"}]
	}`}}
	err := adapter.doctor(t.Context(), runtime, store.paths, OpenAIProviderMetadata{ProfileName: "loki"},
		ConnectionMaterial{LocalOrigin: "http://127.0.0.1:18765/mcp", Token: "loki-token-fixture"}, "runtime-key-fixture")
	var failure *OpenAIDoctorError
	if !errors.As(err, &failure) || failure.Endpoint != "http://127.0.0.1:18765/mcp" {
		t.Fatalf("missing endpoint or typed diagnostics: %v", err)
	}
	details := strings.Join(failure.Details, " ")
	if strings.Contains(details, "runtime-key-fixture") || strings.Contains(details, "loki-token-fixture") ||
		!strings.Contains(details, "[REDACTED]") {
		t.Fatalf("unsafe detailed diagnostics: %s", details)
	}
}
