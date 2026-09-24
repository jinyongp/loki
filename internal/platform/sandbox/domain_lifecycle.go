package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	domainInstanceRefPrefix = "oci-domain-v1:"
	domainWorkloadAlias     = "loki-workload"
	domainGatewayAlias      = "loki-gateway"
)

type domainReference struct {
	aggregate string
	workload  string
	gateway   string
	internal  string
	outbound  string
}

type inspectedNetwork struct {
	id       string
	exists   bool
	internal bool
	members  map[string]bool
}

type domainSnapshot struct {
	workload inspectedResource
	gateway  inspectedResource
	internal inspectedNetwork
	outbound inspectedNetwork
}

func opaqueIDDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func validOpaqueIDDigest(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}

func domainInstanceReference(workloadID, gatewayID, internalID, outboundID string) string {
	for _, id := range []string{workloadID, gatewayID, internalID} {
		if !containerIDPattern.MatchString(id) {
			return ""
		}
	}
	if outboundID != "" && !containerIDPattern.MatchString(outboundID) {
		return ""
	}
	outboundPart := "-"
	if outboundID != "" {
		outboundPart = opaqueIDDigest(outboundID)
	}
	aggregateRaw := workloadID + "\x00" + gatewayID + "\x00" + internalID + "\x00" + outboundID
	return domainInstanceRefPrefix + strings.Join([]string{
		opaqueIDDigest(aggregateRaw),
		opaqueIDDigest(workloadID),
		opaqueIDDigest(gatewayID),
		opaqueIDDigest(internalID),
		outboundPart,
	}, ":")
}

func parseDomainReference(value string) (domainReference, bool) {
	if !strings.HasPrefix(value, domainInstanceRefPrefix) {
		return domainReference{}, false
	}
	parts := strings.Split(strings.TrimPrefix(value, domainInstanceRefPrefix), ":")
	if len(parts) != 5 {
		return domainReference{}, false
	}
	for index := 0; index < 4; index++ {
		if !validOpaqueIDDigest(parts[index]) {
			return domainReference{}, false
		}
	}
	if parts[4] != "-" && !validOpaqueIDDigest(parts[4]) {
		return domainReference{}, false
	}
	return domainReference{
		aggregate: parts[0], workload: parts[1], gateway: parts[2],
		internal: parts[3], outbound: parts[4],
	}, true
}

func validDomainInstanceReference(value string) bool {
	_, ok := parseDomainReference(value)
	return ok
}

func isDomainInstanceReference(value string) bool {
	return strings.HasPrefix(value, domainInstanceRefPrefix)
}

func (r domainReference) expectsOutbound() bool {
	return r.outbound != "-"
}

func (r domainReference) matches(workloadID, gatewayID, internalID, outboundID string) bool {
	if !containerIDPattern.MatchString(workloadID) ||
		!containerIDPattern.MatchString(gatewayID) ||
		!containerIDPattern.MatchString(internalID) {
		return false
	}
	if r.workload != opaqueIDDigest(workloadID) ||
		r.gateway != opaqueIDDigest(gatewayID) ||
		r.internal != opaqueIDDigest(internalID) {
		return false
	}
	if r.expectsOutbound() {
		if !containerIDPattern.MatchString(outboundID) || r.outbound != opaqueIDDigest(outboundID) {
			return false
		}
	} else if outboundID != "" {
		return false
	}
	aggregateRaw := workloadID + "\x00" + gatewayID + "\x00" + internalID + "\x00" + outboundID
	return r.aggregate == opaqueIDDigest(aggregateRaw)
}

func (r domainReference) matchesPresent(snapshot domainSnapshot) bool {
	if snapshot.workload.state.Exists && r.workload != opaqueIDDigest(snapshot.workload.id) {
		return false
	}
	if snapshot.gateway.state.Exists && r.gateway != opaqueIDDigest(snapshot.gateway.id) {
		return false
	}
	if snapshot.internal.exists && r.internal != opaqueIDDigest(snapshot.internal.id) {
		return false
	}
	if snapshot.outbound.exists {
		if !r.expectsOutbound() || r.outbound != opaqueIDDigest(snapshot.outbound.id) {
			return false
		}
	}
	return true
}

func exactNetworkIDs(actual map[string]bool, ids ...string) bool {
	if len(actual) != len(ids) {
		return false
	}
	for _, id := range ids {
		if !actual[id] {
			return false
		}
	}
	return true
}

func networkMembersAllowed(members map[string]bool, ids ...string) bool {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	for id := range members {
		if !allowed[id] {
			return false
		}
	}
	return true
}

func (r domainReference) completenessError(snapshot domainSnapshot) error {
	if !snapshot.workload.state.Exists {
		return errors.New("sandbox resource domain is missing workload")
	}
	if !snapshot.gateway.state.Exists {
		return errors.New("sandbox resource domain is missing gateway")
	}
	if !snapshot.internal.exists {
		return errors.New("sandbox resource domain is missing internal network")
	}
	if r.expectsOutbound() != snapshot.outbound.exists {
		return errors.New("sandbox resource domain outbound network presence changed")
	}
	if !exactNetworkIDs(snapshot.workload.networkIDs, snapshot.internal.id) {
		return errors.New(
			"sandbox workload network set mismatch: count=" + strconv.Itoa(len(snapshot.workload.networkIDs)) +
				" internal=" + strconv.FormatBool(snapshot.workload.networkIDs[snapshot.internal.id]),
		)
	}
	gatewayNetworks := []string{snapshot.internal.id}
	if snapshot.outbound.exists {
		gatewayNetworks = append(gatewayNetworks, snapshot.outbound.id)
	}
	if !exactNetworkIDs(snapshot.gateway.networkIDs, gatewayNetworks...) {
		return errors.New(
			"sandbox gateway network set mismatch: count=" + strconv.Itoa(len(snapshot.gateway.networkIDs)) +
				" internal=" + strconv.FormatBool(snapshot.gateway.networkIDs[snapshot.internal.id]) +
				" outbound=" + strconv.FormatBool(snapshot.gateway.networkIDs[snapshot.outbound.id]),
		)
	}
	if !networkMembersAllowed(snapshot.internal.members, snapshot.workload.id, snapshot.gateway.id) {
		return errors.New("sandbox internal network has unexpected members: count=" + strconv.Itoa(len(snapshot.internal.members)))
	}
	if snapshot.outbound.exists && !networkMembersAllowed(snapshot.outbound.members, snapshot.gateway.id) {
		return errors.New("sandbox outbound network has unexpected members: count=" + strconv.Itoa(len(snapshot.outbound.members)))
	}
	if !r.matches(snapshot.workload.id, snapshot.gateway.id, snapshot.internal.id, snapshot.outbound.id) {
		return errors.New("sandbox resource domain identity changed")
	}
	return nil
}

func (r domainReference) complete(snapshot domainSnapshot) bool {
	return r.completenessError(snapshot) == nil
}

func endpointBindingsFromSnapshot(snapshot domainSnapshot, endpoints []EndpointSpec) ([]EndpointBinding, error) {
	if len(snapshot.workload.publishedPorts) != 0 {
		return nil, errors.New("sandbox workload unexpectedly publishes host ports")
	}
	if len(endpoints) == 0 {
		if len(snapshot.gateway.publishedPorts) != 0 {
			return nil, errors.New("sandbox gateway unexpectedly publishes host ports")
		}
		return nil, nil
	}
	if len(snapshot.gateway.publishedPorts) != len(endpoints) {
		return nil, errors.New("sandbox gateway endpoint publication is incomplete")
	}
	result := make([]EndpointBinding, 0, len(endpoints))
	seenHostPorts := map[int]bool{}
	for _, endpoint := range endpoints {
		key := strconv.Itoa(endpoint.Port) + "/tcp"
		bindings, ok := snapshot.gateway.publishedPorts[key]
		if !ok || len(bindings) != 1 {
			return nil, errors.New("sandbox gateway endpoint binding is missing")
		}
		binding := bindings[0]
		if binding.HostIP != "127.0.0.1" {
			return nil, errors.New("sandbox gateway endpoint is not loopback-bound")
		}
		hostPort, err := strconv.Atoi(binding.HostPort)
		if err != nil || hostPort < 1024 || hostPort > 65535 || seenHostPorts[hostPort] {
			return nil, errors.New("sandbox gateway endpoint host port is invalid")
		}
		seenHostPorts[hostPort] = true
		result = append(result, EndpointBinding{Name: endpoint.Name, Port: endpoint.Port, HostPort: hostPort})
	}
	return result, nil
}

func (e *Engine) waitGatewayEndpointBindings(
	ctx context.Context, version string, resource Resource, gatewayID string, endpoints []EndpointSpec,
) error {
	if len(endpoints) == 0 {
		return nil
	}
	timeout := 2 * time.Second
	if e.controlTimeout < timeout {
		timeout = e.controlTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		gateway, err := e.inspectComponentRef(
			waitCtx, version, gatewayID, resource, resourceComponentGateway,
		)
		if err != nil {
			return err
		}
		if _, bindingErr := endpointBindingsFromSnapshot(
			domainSnapshot{gateway: gateway}, endpoints,
		); bindingErr == nil {
			return nil
		} else {
			lastErr = bindingErr
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return errors.New("sandbox gateway endpoint publication did not stabilize: " + lastErr.Error())
		case <-timer.C:
		}
	}
}

func (s domainSnapshot) allAbsent() bool {
	return !s.workload.state.Exists && !s.gateway.state.Exists && !s.internal.exists && !s.outbound.exists
}

func jobProxyToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (p Plan) gatewayCreateRequest(authToken string) dockerCreateRequest {
	gatewayTmpfs := "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(p.gateway.tmpfsBytes, 10) + ",mode=1777"
	command := []string{
		p.gateway.binary, "egress-proxy",
		"--host", "0.0.0.0",
		"--port", strconv.Itoa(p.gateway.proxyPort),
		"--execution-contract", p.gateway.executionContract,
		"--policy", p.gateway.egressPolicy,
		"--profile", "dependency-install",
		"--audit", "/tmp/egress-audit.jsonl",
		"--auth-token-env", "LOKI_JOB_PROXY_TOKEN",
	}
	exposedPorts := map[string]struct{}{}
	portBindings := map[string][]dockerPortBinding{}
	if len(p.endpoints) > 0 {
		command = append(command, "--forward-host", "0.0.0.0")
		for _, endpoint := range p.endpoints {
			command = append(
				command, "--forward",
				strconv.Itoa(endpoint.Port)+"="+domainWorkloadAlias+":"+strconv.Itoa(endpoint.Port),
			)
			key := strconv.Itoa(endpoint.Port) + "/tcp"
			exposedPorts[key] = struct{}{}
			portBindings[key] = []dockerPortBinding{{HostIP: "127.0.0.1", HostPort: "0"}}
		}
	}
	outboundNetwork := p.resource.OutboundNetworkName()
	return dockerCreateRequest{
		Image:           p.gateway.image,
		Cmd:             command,
		Env:             []string{"LOKI_JOB_PROXY_TOKEN=" + authToken},
		User:            p.create.User,
		NetworkDisabled: false,
		AttachStdout:    true,
		AttachStderr:    true,
		ExposedPorts:    exposedPorts,
		Labels:          p.resource.labelsFor(resourceComponentGateway),
		HostConfig: dockerHostConfig{
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			NetworkMode:    outboundNetwork,
			Memory:         p.gateway.memoryBytes,
			PidsLimit:      p.gateway.pids,
			Tmpfs:          map[string]string{"/tmp": gatewayTmpfs},
			PortBindings:   portBindings,
			Init:           true,
		},
		NetworkingConfig: &dockerNetworkingConfig{
			EndpointsConfig: map[string]dockerEndpointSettings{
				outboundNetwork: {},
			},
		},
	}
}

func (p Plan) workloadCreateRequest(authToken string) dockerCreateRequest {
	request := p.create
	request.Env = append([]string(nil), p.create.Env...)
	if p.NeedsGateway() {
		internalNetwork := p.resource.InternalNetworkName()
		request.HostConfig.NetworkMode = internalNetwork
		request.NetworkingConfig = &dockerNetworkingConfig{
			EndpointsConfig: map[string]dockerEndpointSettings{
				internalNetwork: {Aliases: []string{domainWorkloadAlias}},
			},
		}
	}
	if p.network == NetworkDependencyInstall {
		proxyURL := "http://loki:" + authToken + "@" + domainGatewayAlias + ":" + strconv.Itoa(p.gateway.proxyPort)
		request.Env = append(request.Env,
			"HTTPS_PROXY="+proxyURL,
			"https_proxy="+proxyURL,
			"NO_PROXY=localhost,127.0.0.1,::1",
			"no_proxy=localhost,127.0.0.1,::1",
		)
	}
	return request
}

func (e *Engine) createNetwork(
	ctx context.Context, version, name string, resource Resource, component string, internal bool,
) (string, error) {
	if name == "" || !resource.Valid() || !validResourceComponent(component) {
		return "", errors.New("sandbox network identity is invalid")
	}
	request := struct {
		Name           string            `json:"Name"`
		CheckDuplicate bool              `json:"CheckDuplicate"`
		Internal       bool              `json:"Internal"`
		Labels         map[string]string `json:"Labels"`
	}{
		Name: name, CheckDuplicate: true, Internal: internal, Labels: resource.labelsFor(component),
	}
	var result struct {
		ID string `json:"Id"`
	}
	if err := e.controlJSON(ctx, http.MethodPost, "/v"+version+"/networks/create", request, http.StatusCreated, &result); err != nil {
		return "", err
	}
	if !containerIDPattern.MatchString(result.ID) {
		return "", errors.New("sandbox Docker daemon returned an invalid network ID")
	}
	return result.ID, nil
}

func (e *Engine) inspectNetwork(
	ctx context.Context, version, ref, expectedName string, resource Resource, component string,
) (inspectedNetwork, error) {
	if !resource.Valid() || expectedName == "" || !validResourceComponent(component) ||
		ref == "" || ref != expectedName && !containerIDPattern.MatchString(ref) {
		return inspectedNetwork{}, errors.New("sandbox network identity is invalid")
	}
	controlCtx, cancel := context.WithTimeout(ctx, e.controlTimeout)
	defer cancel()
	response, err := e.call(controlCtx, http.MethodGet, "/v"+version+"/networks/"+url.PathEscape(ref), nil)
	if err != nil {
		return inspectedNetwork{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		if _, err = readBounded(response.Body, e.responseBytes); err != nil {
			return inspectedNetwork{}, err
		}
		return inspectedNetwork{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return inspectedNetwork{}, e.unexpectedStatus(response)
	}
	raw, err := readBounded(response.Body, e.responseBytes)
	if err != nil {
		return inspectedNetwork{}, err
	}
	var decoded struct {
		ID         string                     `json:"Id"`
		Name       string                     `json:"Name"`
		Internal   bool                       `json:"Internal"`
		Labels     map[string]string          `json:"Labels"`
		Containers map[string]json.RawMessage `json:"Containers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&decoded); err != nil {
		return inspectedNetwork{}, errors.New("sandbox Docker daemon returned invalid network JSON")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return inspectedNetwork{}, errors.New("sandbox Docker daemon returned trailing network JSON")
	}
	if !containerIDPattern.MatchString(decoded.ID) || decoded.Name != expectedName ||
		!resource.ownsComponent(decoded.Labels, component) {
		return inspectedNetwork{}, errors.New("sandbox network ownership does not match")
	}
	members := make(map[string]bool, len(decoded.Containers))
	for id := range decoded.Containers {
		if !containerIDPattern.MatchString(id) {
			return inspectedNetwork{}, errors.New("sandbox network contains an invalid container identity")
		}
		members[id] = true
	}
	return inspectedNetwork{id: decoded.ID, exists: true, internal: decoded.Internal, members: members}, nil
}

func (e *Engine) connectNetwork(
	ctx context.Context, version, networkID, containerID string, aliases []string, gatewayPriority int,
) error {
	if !containerIDPattern.MatchString(networkID) || !containerIDPattern.MatchString(containerID) {
		return errors.New("sandbox network connection identity is invalid")
	}
	request := struct {
		Container      string `json:"Container"`
		EndpointConfig struct {
			Aliases    []string `json:"Aliases,omitempty"`
			GwPriority int      `json:"GwPriority,omitempty"`
		} `json:"EndpointConfig"`
	}{Container: containerID}
	request.EndpointConfig.Aliases = append([]string(nil), aliases...)
	request.EndpointConfig.GwPriority = gatewayPriority
	return e.controlJSON(
		ctx, http.MethodPost, "/v"+version+"/networks/"+url.PathEscape(networkID)+"/connect",
		request, http.StatusOK, nil,
	)
}

func (e *Engine) removeNetwork(ctx context.Context, version, networkID string) error {
	if !containerIDPattern.MatchString(networkID) {
		return errors.New("sandbox network identity is invalid")
	}
	response, err := e.call(ctx, http.MethodDelete, "/v"+version+"/networks/"+url.PathEscape(networkID), nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		_, err = readBounded(response.Body, e.responseBytes)
		return err
	}
	return e.unexpectedStatus(response)
}

func (e *Engine) inspectDomain(
	ctx context.Context, version string, resource Resource,
) (domainSnapshot, error) {
	workload, err := e.inspectComponentRef(ctx, version, resource.Name(), resource, resourceComponentWorkload)
	if err != nil {
		return domainSnapshot{}, err
	}
	gateway, err := e.inspectComponentRef(ctx, version, resource.GatewayName(), resource, resourceComponentGateway)
	if err != nil {
		return domainSnapshot{}, err
	}
	internal, err := e.inspectNetwork(
		ctx, version, resource.InternalNetworkName(), resource.InternalNetworkName(),
		resource, resourceComponentInternalNetwork,
	)
	if err != nil {
		return domainSnapshot{}, err
	}
	outbound, err := e.inspectNetwork(
		ctx, version, resource.OutboundNetworkName(), resource.OutboundNetworkName(),
		resource, resourceComponentOutboundNetwork,
	)
	if err != nil {
		return domainSnapshot{}, err
	}
	if internal.exists && !internal.internal {
		return domainSnapshot{}, errors.New("sandbox internal network lost its isolation flag")
	}
	if outbound.exists && outbound.internal {
		return domainSnapshot{}, errors.New("sandbox outbound network became internal")
	}
	return domainSnapshot{workload: workload, gateway: gateway, internal: internal, outbound: outbound}, nil
}

func (e *Engine) resolveDomainWorkload(
	ctx context.Context, version string, resource Resource, instanceRef string, requireComplete bool,
) (inspectedResource, error) {
	reference, ok := parseDomainReference(instanceRef)
	if !ok {
		return inspectedResource{}, errors.New("sandbox domain instance reference is invalid")
	}
	snapshot, err := e.inspectDomain(ctx, version, resource)
	if err != nil {
		return inspectedResource{}, err
	}
	if snapshot.allAbsent() {
		return inspectedResource{}, nil
	}
	if !reference.matchesPresent(snapshot) {
		return inspectedResource{}, ErrInstanceMismatch
	}
	if requireComplete && !reference.complete(snapshot) {
		return inspectedResource{}, errors.New("sandbox resource domain is incomplete")
	}
	return snapshot.workload, nil
}

func (e *Engine) EndpointBindings(
	ctx context.Context, resource Resource, instanceRef string, endpoints []EndpointSpec,
) ([]EndpointBinding, error) {
	if e == nil || !resource.Valid() {
		return nil, errors.New("sandbox endpoint binding inspection is not configured")
	}
	if !validDomainInstanceReference(instanceRef) {
		if len(endpoints) == 0 && validInstanceReference(instanceRef) {
			return nil, nil
		}
		return nil, errors.New("sandbox endpoint binding inspection is not configured")
	}
	normalized, err := normalizeEndpointSpecs(endpoints, 0)
	if err != nil {
		return nil, err
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return nil, err
	}
	reference, _ := parseDomainReference(instanceRef)
	snapshot, err := e.inspectDomain(ctx, version, resource)
	if err != nil {
		return nil, err
	}
	if !reference.complete(snapshot) {
		if !reference.matchesPresent(snapshot) {
			return nil, ErrInstanceMismatch
		}
		return nil, errors.New("sandbox resource domain is incomplete")
	}
	return endpointBindingsFromSnapshot(snapshot, normalized)
}

func (e *Engine) cleanupPartialDomain(
	version string, resource Resource,
	workloadID, gatewayID, internalID, outboundID string,
) error {
	var result error
	if containerIDPattern.MatchString(workloadID) {
		ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
		result = errors.Join(result, e.removeRef(ctx, version, workloadID, resource))
		cancel()
	}
	if containerIDPattern.MatchString(gatewayID) {
		ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
		result = errors.Join(result, e.removeRef(ctx, version, gatewayID, resource))
		cancel()
	}
	if containerIDPattern.MatchString(outboundID) {
		ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
		result = errors.Join(result, e.removeNetwork(ctx, version, outboundID))
		cancel()
	}
	if containerIDPattern.MatchString(internalID) {
		ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
		result = errors.Join(result, e.removeNetwork(ctx, version, internalID))
		cancel()
	}
	return result
}

func (e *Engine) startDomainJob(ctx context.Context, version string, plan Plan) (StartResult, error) {
	resource := plan.Resource()
	result := StartResult{Resource: resource}
	authToken, err := jobProxyToken()
	if err != nil {
		return result, errors.New("sandbox gateway credential generation failed")
	}
	var internalID, outboundID, gatewayID, workloadID string
	fail := func(primary error) (StartResult, error) {
		cleanupErr := e.cleanupPartialDomain(version, resource, workloadID, gatewayID, internalID, outboundID)
		if cleanupErr != nil {
			result.Created = true
			if ref := domainInstanceReference(workloadID, gatewayID, internalID, outboundID); ref != "" {
				result.InstanceRef = ref
			}
		}
		return result, errors.Join(primary, cleanupErr)
	}

	internalCandidate, err := e.createNetwork(
		ctx, version, resource.InternalNetworkName(), resource, resourceComponentInternalNetwork, true,
	)
	if err != nil {
		return result, err
	}
	result.Created = true
	internalNetwork, err := e.inspectNetwork(
		ctx, version, internalCandidate, resource.InternalNetworkName(), resource, resourceComponentInternalNetwork,
	)
	if err != nil || !internalNetwork.exists || internalNetwork.id != internalCandidate || !internalNetwork.internal {
		if err == nil {
			err = errors.New("sandbox internal network identity changed after creation")
		}
		return result, err
	}
	internalID = internalCandidate

	if plan.NeedsOutboundNetwork() {
		outboundCandidate, createErr := e.createNetwork(
			ctx, version, resource.OutboundNetworkName(), resource, resourceComponentOutboundNetwork, false,
		)
		if createErr != nil {
			return fail(createErr)
		}
		outboundNetwork, inspectErr := e.inspectNetwork(
			ctx, version, outboundCandidate, resource.OutboundNetworkName(), resource, resourceComponentOutboundNetwork,
		)
		if inspectErr != nil || !outboundNetwork.exists || outboundNetwork.id != outboundCandidate || outboundNetwork.internal {
			if inspectErr == nil {
				inspectErr = errors.New("sandbox outbound network identity changed after creation")
			}
			return fail(inspectErr)
		}
		outboundID = outboundCandidate
	}

	gatewayCandidate, gatewayCreated, err := e.createContainer(
		ctx, version, resource.GatewayName(), plan.gatewayCreateRequest(authToken),
	)
	if gatewayCreated {
		result.Created = true
	}
	if err != nil {
		return fail(err)
	}
	gatewayOwned, err := e.inspectComponentRef(
		ctx, version, gatewayCandidate, resource, resourceComponentGateway,
	)
	if err != nil || !gatewayOwned.state.Exists || gatewayOwned.id != gatewayCandidate {
		if err == nil {
			err = errors.New("sandbox gateway identity changed after creation")
		}
		return fail(err)
	}
	gatewayID = gatewayCandidate

	if err = e.startRef(ctx, version, gatewayID, resource); err != nil {
		return fail(err)
	}
	if err = e.waitGatewayEndpointBindings(ctx, version, resource, gatewayID, plan.endpoints); err != nil {
		return fail(err)
	}
	if err = e.connectNetwork(ctx, version, internalID, gatewayID, []string{domainGatewayAlias}, -1); err != nil {
		return fail(err)
	}

	workloadCandidate, workloadCreated, err := e.createContainer(
		ctx, version, resource.Name(), plan.workloadCreateRequest(authToken),
	)
	if workloadCreated {
		result.Created = true
	}
	if err != nil {
		return fail(err)
	}
	workloadOwned, err := e.inspectComponentRef(
		ctx, version, workloadCandidate, resource, resourceComponentWorkload,
	)
	if err != nil || !workloadOwned.state.Exists || workloadOwned.id != workloadCandidate {
		if err == nil {
			err = errors.New("sandbox workload identity changed after creation")
		}
		return fail(err)
	}
	workloadID = workloadCandidate

	if err = e.startRef(ctx, version, workloadID, resource); err != nil {
		return fail(err)
	}
	instanceRef := domainInstanceReference(workloadID, gatewayID, internalID, outboundID)
	if instanceRef == "" {
		return fail(errors.New("sandbox resource domain identity is invalid"))
	}
	snapshot, err := e.inspectDomain(context.Background(), version, resource)
	if err != nil {
		return fail(err)
	}
	reference, _ := parseDomainReference(instanceRef)
	if completeErr := reference.completenessError(snapshot); completeErr != nil {
		return fail(completeErr)
	}
	bindings, err := endpointBindingsFromSnapshot(snapshot, plan.endpoints)
	if err != nil {
		return fail(err)
	}
	result.InstanceRef = instanceRef
	result.EndpointBindings = bindings
	result.Created = true
	result.Started = true
	return result, nil
}

func (e *Engine) cleanupDomain(
	version string, resource Resource, instanceRef string,
) (CleanupStatus, error) {
	reference, ok := parseDomainReference(instanceRef)
	if !ok {
		return CleanupFailed, errors.New("sandbox domain cleanup identity is invalid")
	}
	snapshot, err := e.inspectDomain(context.Background(), version, resource)
	if err != nil {
		return CleanupFailed, err
	}
	if snapshot.allAbsent() {
		return CleanupComplete, nil
	}
	if !reference.matchesPresent(snapshot) {
		return CleanupFailed, ErrInstanceMismatch
	}
	var cleanupErr error
	if snapshot.workload.state.Exists {
		_, err = e.cleanupComponentResource(
			version, resource, snapshot.workload.id, resourceComponentWorkload,
		)
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if snapshot.gateway.state.Exists {
		_, err = e.cleanupComponentResource(
			version, resource, snapshot.gateway.id, resourceComponentGateway,
		)
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if cleanupErr != nil {
		return CleanupFailed, cleanupErr
	}
	if snapshot.outbound.exists {
		ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
		err = e.removeNetwork(ctx, version, snapshot.outbound.id)
		cancel()
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if snapshot.internal.exists {
		ctx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
		err = e.removeNetwork(ctx, version, snapshot.internal.id)
		cancel()
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if cleanupErr != nil {
		return CleanupFailed, cleanupErr
	}
	verifyCtx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
	defer cancel()
	for {
		current, inspectErr := e.inspectDomain(verifyCtx, version, resource)
		if inspectErr != nil {
			return CleanupFailed, inspectErr
		}
		if current.allAbsent() {
			return CleanupComplete, nil
		}
		if !reference.matchesPresent(current) {
			return CleanupFailed, ErrInstanceMismatch
		}
		select {
		case <-verifyCtx.Done():
			return CleanupFailed, errors.New("sandbox resource domain remained after cleanup")
		case <-time.After(cleanupPollInterval):
		}
	}
}
