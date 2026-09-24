package sandbox

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEngineInspectDetectsOrphanedSubordinateDomain(t *testing.T) {
	const version = "1.44"
	resource := validPlan(t).Resource()
	gatewayID := strings.Repeat("e", 64)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
		case "/v" + version + "/containers/" + resource.Name() + "/json":
			http.NotFound(w, r)
		case "/v" + version + "/containers/" + resource.GatewayName() + "/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     gatewayID,
				"Config": map[string]any{"Labels": resource.labelsFor(resourceComponentGateway)},
				"State":  map[string]any{"Status": "running", "Running": true, "OOMKilled": false, "ExitCode": 0},
			})
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
	state, err := engineForSocket(t, fakeDockerSocket(t, handler), nil).Inspect(t.Context(), resource)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || state.Running || state.Terminal {
		t.Fatalf("orphan domain state = %#v", state)
	}
}

func TestEngineInspectOwnsOnlyLokiResources(t *testing.T) {
	const version = "1.44"
	resource := validPlan(t).Resource()
	containerID := strings.Repeat("d", 64)

	type inspectState struct {
		Status    string
		Running   bool
		OOMKilled bool
		ExitCode  int64
	}
	tests := []struct {
		name      string
		status    int
		labels    map[string]string
		state     inspectState
		want      ResourceState
		wantError string
	}{
		{
			name:   "absent",
			status: http.StatusNotFound,
		},
		{
			name:   "running",
			status: http.StatusOK,
			labels: resource.labels(),
			state:  inspectState{Status: "running", Running: true},
			want:   ResourceState{Exists: true, Running: true},
		},
		{
			name:   "oom-terminal",
			status: http.StatusOK,
			labels: resource.labels(),
			state:  inspectState{Status: "exited", OOMKilled: true, ExitCode: 137},
			want:   ResourceState{Exists: true, Terminal: true, OOMKilled: true, ExitCode: 137},
		},
		{
			name:   "foreign-resource",
			status: http.StatusOK,
			labels: map[string]string{
				resourceOwnerLabel:  resourceOwnerValue,
				resourceJobLabel:    strings.Repeat("f", 32),
				resourcePolicyLabel: resource.PolicySHA256(),
			},
			state:     inspectState{Status: "running", Running: true},
			wantError: "ownership does not match",
		},
		{
			name:      "contradictory-running",
			status:    http.StatusOK,
			labels:    resource.labels(),
			state:     inspectState{Status: "running"},
			wantError: "contradictory resource state",
		},
		{
			name:      "invalid-exit-code",
			status:    http.StatusOK,
			labels:    resource.labels(),
			state:     inspectState{Status: "exited", ExitCode: 999},
			wantError: "contradictory resource state",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/version":
					_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
				case "/v" + version + "/containers/" + resource.Name() + "/json":
					w.WriteHeader(test.status)
					if test.status == http.StatusOK {
						_ = json.NewEncoder(w).Encode(map[string]any{
							"Id":     containerID,
							"Config": map[string]any{"Labels": test.labels},
							"State": map[string]any{
								"Status":    test.state.Status,
								"Running":   test.state.Running,
								"OOMKilled": test.state.OOMKilled,
								"ExitCode":  test.state.ExitCode,
							},
						})
					}
				case "/v" + version + "/containers/" + resource.GatewayName() + "/json",
					"/v" + version + "/containers/" + resource.PublisherName() + "/json",
					"/v" + version + "/networks/" + resource.InternalNetworkName(),
					"/v" + version + "/networks/" + resource.OutboundNetworkName():
					http.NotFound(w, r)
				default:
					t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
					http.NotFound(w, r)
				}
			})
			engine := engineForSocket(t, fakeDockerSocket(t, handler), nil)
			state, err := engine.Inspect(t.Context(), resource)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("inspect error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state != test.want {
				t.Fatalf("state = %#v, want %#v", state, test.want)
			}
		})
	}
}
