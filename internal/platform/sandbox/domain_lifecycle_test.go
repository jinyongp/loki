package sandbox

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type domainFixtureState struct {
	mu sync.Mutex

	workloadID string
	gatewayID  string
	internalID string
	outboundID string

	workloadRunning        bool
	gatewayRunning         bool
	gatewayRunningInspects int
	workloadRemoved        bool
	gatewayRemoved         bool
	internalRemoved        bool
	outboundRemoved        bool

	connects   []string
	proxyToken string
}

func domainPlan(t *testing.T) Plan {
	t.Helper()
	policy, err := NewPolicy(validPolicyOptions())
	if err != nil {
		t.Fatal(err)
	}
	spec := validWorkloadSpec()
	spec.Network = NetworkDependencyInstall
	spec.Endpoints = []EndpointSpec{{Name: "web", Port: 5173}, {Name: "api", Port: 3000}}
	plan, err := policy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func domainNetworkJSON(
	resource Resource, id, name, component string, internal bool, members ...string,
) map[string]any {
	containers := map[string]any{}
	for _, member := range members {
		containers[member] = map[string]any{}
	}
	return map[string]any{
		"Id": id, "Name": name, "Internal": internal,
		"Labels": resource.labelsFor(component), "Containers": containers,
	}
}

func TestDomainReferenceCompletenessUsesContainerAttachments(t *testing.T) {
	workloadID := strings.Repeat("1", 64)
	gatewayID := strings.Repeat("2", 64)
	internalID := strings.Repeat("3", 64)
	outboundID := strings.Repeat("4", 64)
	referenceRaw := domainInstanceReference(workloadID, gatewayID, internalID, outboundID)
	reference, ok := parseDomainReference(referenceRaw)
	if !ok {
		t.Fatal("domain reference did not parse")
	}
	snapshot := domainSnapshot{
		workload: inspectedResource{
			id: workloadID, state: ResourceState{Exists: true, Running: true},
			networkIDs: map[string]bool{internalID: true},
		},
		gateway: inspectedResource{
			id: gatewayID, state: ResourceState{Exists: true, Running: true},
			networkIDs: map[string]bool{internalID: true, outboundID: true},
		},
		internal: inspectedNetwork{
			id: internalID, exists: true, internal: true,
			members: map[string]bool{gatewayID: true},
		},
		outbound: inspectedNetwork{
			id: outboundID, exists: true,
			members: map[string]bool{gatewayID: true},
		},
	}
	if !reference.complete(snapshot) {
		t.Fatal("configured container attachments were rejected because an expected active endpoint was absent")
	}

	rogueID := strings.Repeat("5", 64)
	snapshot.internal.members[rogueID] = true
	if reference.complete(snapshot) {
		t.Fatal("unexpected active internal network member was accepted")
	}
	delete(snapshot.internal.members, rogueID)

	snapshot.workload.networkIDs[outboundID] = true
	if reference.complete(snapshot) {
		t.Fatal("workload attachment to the outbound network was accepted")
	}
}

func TestEndpointBindingsRequireLoopbackUniqueHostPorts(t *testing.T) {
	endpoints := []EndpointSpec{{Name: "api", Port: 3000}, {Name: "web", Port: 5173}}
	snapshot := domainSnapshot{
		workload: inspectedResource{publishedPorts: map[string][]dockerPortBinding{}},
		gateway: inspectedResource{publishedPorts: map[string][]dockerPortBinding{
			"3000/tcp": {{HostIP: "127.0.0.1", HostPort: "43001"}},
			"5173/tcp": {{HostIP: "127.0.0.1", HostPort: "43002"}},
		}},
	}
	bindings, err := endpointBindingsFromSnapshot(snapshot, endpoints)
	if err != nil || len(bindings) != 2 || bindings[0].HostPort != 43001 || bindings[1].HostPort != 43002 {
		t.Fatalf("bindings = %#v, %v", bindings, err)
	}

	snapshot.gateway.publishedPorts["3000/tcp"] = []dockerPortBinding{{HostIP: "0.0.0.0", HostPort: "43001"}}
	if _, err = endpointBindingsFromSnapshot(snapshot, endpoints); err == nil {
		t.Fatal("non-loopback host binding was accepted")
	}
	snapshot.gateway.publishedPorts["3000/tcp"] = []dockerPortBinding{{HostIP: "127.0.0.1", HostPort: "43002"}}
	if _, err = endpointBindingsFromSnapshot(snapshot, endpoints); err == nil {
		t.Fatal("reused host port was accepted")
	}
	snapshot.gateway.publishedPorts["3000/tcp"] = []dockerPortBinding{{HostIP: "127.0.0.1", HostPort: "43001"}}
	snapshot.gateway.publishedPorts["9999/tcp"] = []dockerPortBinding{{HostIP: "127.0.0.1", HostPort: "43003"}}
	if _, err = endpointBindingsFromSnapshot(snapshot, endpoints); err == nil {
		t.Fatal("unexpected published port was accepted")
	}
}

func TestDomainLifecycleCreatesAndCleansExactResourceDomain(t *testing.T) {
	const version = "1.44"
	plan := domainPlan(t)
	resource := plan.Resource()
	state := &domainFixtureState{
		workloadID: strings.Repeat("1", 64),
		gatewayID:  strings.Repeat("2", 64),
		internalID: strings.Repeat("3", 64),
		outboundID: strings.Repeat("4", 64),
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})

		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/networks/create":
			var request struct {
				Name     string
				Internal bool
				Labels   map[string]string
				Options  map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode network create: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch request.Name {
			case resource.InternalNetworkName():
				if !request.Internal || !resource.ownsComponent(request.Labels, resourceComponentInternalNetwork) ||
					len(request.Options) != 0 {
					t.Errorf("internal network request = %#v", request)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": state.internalID})
			case resource.OutboundNetworkName():
				if request.Internal || !resource.ownsComponent(request.Labels, resourceComponentOutboundNetwork) ||
					request.Options["com.docker.network.bridge.host_binding_ipv4"] != "127.0.0.1" {
					t.Errorf("outbound network request = %#v", request)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": state.outboundID})
			default:
				t.Errorf("unexpected network create %q", request.Name)
				http.NotFound(w, r)
			}

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v"+version+"/networks/"):
			ref := strings.TrimPrefix(r.URL.Path, "/v"+version+"/networks/")
			switch ref {
			case resource.InternalNetworkName(), state.internalID:
				if state.internalRemoved {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(domainNetworkJSON(
					resource, state.internalID, resource.InternalNetworkName(),
					resourceComponentInternalNetwork, true, state.workloadID, state.gatewayID,
				))
			case resource.OutboundNetworkName(), state.outboundID:
				if state.outboundRemoved {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(domainNetworkJSON(
					resource, state.outboundID, resource.OutboundNetworkName(),
					resourceComponentOutboundNetwork, false, state.gatewayID,
				))
			default:
				http.NotFound(w, r)
			}

		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/create":
			name := r.URL.Query().Get("name")
			var request dockerCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode container create: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch name {
			case resource.GatewayName():
				if request.Image != plan.gateway.image ||
					!resource.ownsComponent(request.Labels, resourceComponentGateway) ||
					request.HostConfig.NetworkMode != resource.OutboundNetworkName() || request.NetworkDisabled {
					t.Errorf("gateway create = %#v", request)
				}
				if request.NetworkingConfig == nil {
					t.Fatal("gateway create omitted networking config")
				}
				endpoint, ok := request.NetworkingConfig.EndpointsConfig[resource.OutboundNetworkName()]
				if !ok || len(endpoint.Aliases) != 0 {
					t.Errorf("gateway networking config = %#v", request.NetworkingConfig)
				}
				for _, value := range request.Env {
					if strings.HasPrefix(value, "LOKI_JOB_PROXY_TOKEN=") {
						state.proxyToken = strings.TrimPrefix(value, "LOKI_JOB_PROXY_TOKEN=")
					}
				}
				if state.proxyToken == "" || strings.Contains(strings.Join(request.Cmd, " "), state.proxyToken) ||
					!strings.Contains(strings.Join(request.Cmd, " "), "--auth-token-env LOKI_JOB_PROXY_TOKEN") ||
					!strings.Contains(strings.Join(request.Cmd, " "), "--forward 3000=loki-workload:3000") ||
					!strings.Contains(strings.Join(request.Cmd, " "), "--forward 5173=loki-workload:5173") {
					t.Errorf("gateway auth/forward config = env %#v cmd %#v", request.Env, request.Cmd)
				}
				if !request.HostConfig.PublishAllPorts || request.HostConfig.PortBindings == nil ||
					len(request.HostConfig.PortBindings) != 0 {
					t.Errorf("gateway publish-all config = %#v", request.HostConfig)
				}
				for _, port := range []int{3000, 5173} {
					key := strconv.Itoa(port) + "/tcp"
					if _, ok := request.ExposedPorts[key]; !ok {
						t.Errorf("gateway did not expose endpoint %s", key)
					}
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": state.gatewayID})
			case resource.Name():
				if !resource.owns(request.Labels) || request.NetworkDisabled ||
					request.HostConfig.NetworkMode != resource.InternalNetworkName() {
					t.Errorf("workload create = %#v", request)
				}
				if request.NetworkingConfig == nil {
					t.Fatal("workload create omitted networking config")
				}
				endpoint, ok := request.NetworkingConfig.EndpointsConfig[resource.InternalNetworkName()]
				if !ok || len(endpoint.Aliases) != 1 || endpoint.Aliases[0] != domainWorkloadAlias {
					t.Errorf("workload networking config = %#v", request.NetworkingConfig)
				}
				wantProxy := "http://loki:" + state.proxyToken + "@loki-gateway:18766"
				environment := strings.Join(request.Env, "\n")
				if state.proxyToken == "" ||
					!strings.Contains(environment, "HTTPS_PROXY="+wantProxy) ||
					!strings.Contains(environment, "https_proxy="+wantProxy) {
					t.Errorf("workload proxy environment = %#v", request.Env)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": state.workloadID})
			default:
				t.Errorf("unexpected container create %q", name)
				http.NotFound(w, r)
			}

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/connect"):
			var request struct {
				Container      string
				EndpointConfig struct {
					Aliases    []string
					GwPriority int
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode connect: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.Container == state.gatewayID {
				if !state.gatewayRunning {
					t.Error("gateway connected to internal network before port-publishing startup")
				}
				if state.gatewayRunningInspects < 2 {
					t.Error("gateway connected to internal network before endpoint publication stabilized")
				}
				if request.EndpointConfig.GwPriority != -1 ||
					len(request.EndpointConfig.Aliases) != 1 ||
					request.EndpointConfig.Aliases[0] != domainGatewayAlias {
					t.Errorf("gateway internal attachment = %#v", request.EndpointConfig)
				}
			}
			state.connects = append(state.connects, r.URL.Path+"="+request.Container)
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v"+version+"/containers/"), "/start")
			switch ref {
			case state.gatewayID:
				state.gatewayRunning = true
			case state.workloadID:
				state.workloadRunning = true
			default:
				t.Errorf("unexpected start %q", ref)
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/stop"):
			ref := strings.Split(strings.TrimPrefix(r.URL.Path, "/v"+version+"/containers/"), "/")[0]
			switch ref {
			case state.gatewayID:
				state.gatewayRunning = false
			case state.workloadID:
				state.workloadRunning = false
			default:
				t.Errorf("unexpected stop %q", ref)
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v"+version+"/containers/"), "/json")
			switch ref {
			case resource.Name(), state.workloadID:
				if state.workloadRemoved {
					http.NotFound(w, r)
					return
				}
				status := "created"
				if state.workloadRunning {
					status = "running"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id":     state.workloadID,
					"Config": map[string]any{"Labels": resource.labels()},
					"NetworkSettings": map[string]any{
						"Ports": map[string]any{},
						"Networks": map[string]any{
							resource.InternalNetworkName(): map[string]any{"NetworkID": state.internalID},
						},
					},
					"State": map[string]any{
						"Status": status, "Running": state.workloadRunning,
						"OOMKilled": false, "ExitCode": 0,
					},
				})
			case resource.GatewayName(), state.gatewayID:
				if state.gatewayRemoved {
					http.NotFound(w, r)
					return
				}
				status := "created"
				ports := map[string]any{}
				if state.gatewayRunning {
					status = "running"
					state.gatewayRunningInspects++
					if state.gatewayRunningInspects >= 2 {
						ports = map[string]any{
							"18766/tcp": nil,
							"3000/tcp":  []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "43001"}},
							"5173/tcp":  []map[string]string{{"HostIp": "127.0.0.1", "HostPort": "43002"}},
						}
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id":     state.gatewayID,
					"Config": map[string]any{"Labels": resource.labelsFor(resourceComponentGateway)},
					"NetworkSettings": map[string]any{
						"Ports": ports,
						"Networks": map[string]any{
							resource.InternalNetworkName(): map[string]any{"NetworkID": state.internalID},
							resource.OutboundNetworkName(): map[string]any{"NetworkID": state.outboundID},
						},
					},
					"State": map[string]any{
						"Status": status, "Running": state.gatewayRunning,
						"OOMKilled": false, "ExitCode": 0,
					},
				})
			default:
				http.NotFound(w, r)
			}

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v"+version+"/containers/"):
			ref := strings.TrimPrefix(r.URL.Path, "/v"+version+"/containers/")
			switch ref {
			case state.workloadID:
				state.workloadRemoved = true
			case state.gatewayID:
				state.gatewayRemoved = true
			default:
				t.Errorf("unexpected container delete %q", ref)
			}
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v"+version+"/networks/"):
			ref := strings.TrimPrefix(r.URL.Path, "/v"+version+"/networks/")
			switch ref {
			case state.internalID:
				state.internalRemoved = true
			case state.outboundID:
				state.outboundRemoved = true
			default:
				t.Errorf("unexpected network delete %q", ref)
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	engine := engineForSocket(t, fakeDockerSocket(t, handler), nil)
	started, err := engine.StartJob(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Created || !started.Started || !validDomainInstanceReference(started.InstanceRef) {
		t.Fatalf("domain start = %#v", started)
	}
	if len(started.EndpointBindings) != 2 ||
		started.EndpointBindings[0] != (EndpointBinding{Name: "api", Port: 3000, HostPort: 43001}) ||
		started.EndpointBindings[1] != (EndpointBinding{Name: "web", Port: 5173, HostPort: 43002}) {
		t.Fatalf("endpoint bindings = %#v", started.EndpointBindings)
	}
	if len(state.connects) != 1 ||
		!strings.Contains(state.connects[0], "/networks/"+state.internalID+"/connect="+state.gatewayID) {
		t.Fatalf("network connects = %#v", state.connects)
	}
	stateView, err := engine.InspectJob(t.Context(), resource, started.InstanceRef)
	if err != nil || !stateView.Exists || !stateView.Running {
		t.Fatalf("domain inspect = %#v, %v", stateView, err)
	}

	cleanup, err := engine.CleanupJob(t.Context(), resource, started.InstanceRef)
	if err != nil || cleanup != CleanupComplete {
		t.Fatalf("domain cleanup = %s, %v", cleanup, err)
	}
	if !state.workloadRemoved || !state.gatewayRemoved || !state.internalRemoved || !state.outboundRemoved {
		t.Fatalf("domain retained resources = %#v", state)
	}
}

func TestDomainRecoveryRejectsReplacedGatewayWithoutMutation(t *testing.T) {
	const version = "1.44"
	plan := domainPlan(t)
	resource := plan.Resource()
	workloadID := strings.Repeat("5", 64)
	gatewayID := strings.Repeat("6", 64)
	internalID := strings.Repeat("7", 64)
	outboundID := strings.Repeat("8", 64)
	replacementGatewayID := strings.Repeat("9", 64)
	instanceRef := domainInstanceReference(workloadID, gatewayID, internalID, outboundID)
	mutated := false

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
		case r.Method == http.MethodGet && r.URL.Path == "/v"+version+"/containers/"+resource.Name()+"/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": workloadID, "Config": map[string]any{"Labels": resource.labels()},
				"State": map[string]any{"Status": "running", "Running": true, "OOMKilled": false, "ExitCode": 0},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v"+version+"/containers/"+resource.GatewayName()+"/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     replacementGatewayID,
				"Config": map[string]any{"Labels": resource.labelsFor(resourceComponentGateway)},
				"State":  map[string]any{"Status": "running", "Running": true, "OOMKilled": false, "ExitCode": 0},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v"+version+"/networks/"+resource.InternalNetworkName():
			_ = json.NewEncoder(w).Encode(domainNetworkJSON(
				resource, internalID, resource.InternalNetworkName(),
				resourceComponentInternalNetwork, true,
			))
		case r.Method == http.MethodGet && r.URL.Path == "/v"+version+"/networks/"+resource.OutboundNetworkName():
			_ = json.NewEncoder(w).Encode(domainNetworkJSON(
				resource, outboundID, resource.OutboundNetworkName(),
				resourceComponentOutboundNetwork, false,
			))
		case r.Method == http.MethodDelete || r.Method == http.MethodPost:
			mutated = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	engine := engineForSocket(t, fakeDockerSocket(t, handler), nil)
	if _, err := engine.InspectJob(t.Context(), resource, instanceRef); !errors.Is(err, ErrInstanceMismatch) {
		t.Fatalf("replacement inspect = %v", err)
	}
	if status, err := engine.CleanupJob(t.Context(), resource, instanceRef); !errors.Is(err, ErrInstanceMismatch) ||
		status != CleanupFailed {
		t.Fatalf("replacement cleanup = %s, %v", status, err)
	}
	if mutated {
		t.Fatal("replacement gateway was mutated")
	}
}

func TestDomainCleanupContinuesAfterPriorPartialRemoval(t *testing.T) {
	const version = "1.44"
	plan := domainPlan(t)
	resource := plan.Resource()
	workloadID := strings.Repeat("a", 64)
	gatewayID := strings.Repeat("b", 64)
	internalID := strings.Repeat("c", 64)
	outboundID := strings.Repeat("d", 64)
	instanceRef := domainInstanceReference(workloadID, gatewayID, internalID, outboundID)
	gatewayRemoved := false
	internalRemoved := false
	outboundRemoved := false

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
		case r.Method == http.MethodGet && r.URL.Path == "/v"+version+"/containers/"+resource.Name()+"/json":
			http.NotFound(w, r)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/containers/"):
			if gatewayRemoved {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     gatewayID,
				"Config": map[string]any{"Labels": resource.labelsFor(resourceComponentGateway)},
				"State":  map[string]any{"Status": "created", "Running": false, "OOMKilled": false, "ExitCode": 0},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/networks/"+resource.InternalNetworkName()):
			if internalRemoved {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(domainNetworkJSON(
				resource, internalID, resource.InternalNetworkName(),
				resourceComponentInternalNetwork, true,
			))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/networks/"+resource.OutboundNetworkName()):
			if outboundRemoved {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(domainNetworkJSON(
				resource, outboundID, resource.OutboundNetworkName(),
				resourceComponentOutboundNetwork, false,
			))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
			gatewayRemoved = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/"+outboundID):
			outboundRemoved = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/"+internalID):
			internalRemoved = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	engine := engineForSocket(t, fakeDockerSocket(t, handler), nil)
	status, err := engine.CleanupJob(t.Context(), resource, instanceRef)
	if err != nil || status != CleanupComplete {
		t.Fatalf("partial cleanup = %s, %v", status, err)
	}
	if !gatewayRemoved || !internalRemoved || !outboundRemoved {
		t.Fatalf("partial cleanup retained resources gateway=%v internal=%v outbound=%v", gatewayRemoved, internalRemoved, outboundRemoved)
	}
}
