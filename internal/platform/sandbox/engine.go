package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRequestBytes        = 256 << 10
	defaultResponseBytes       = 1 << 20
	defaultOutputBytes         = 256 << 10
	defaultControlTimeout      = 15 * time.Second
	defaultCleanupTimeout      = 10 * time.Second
	defaultGracefulStopTimeout = 5 * time.Second
)

var (
	apiVersionPattern  = regexp.MustCompile(`^1\.[0-9]{1,3}$`)
	containerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type EngineOptions struct {
	Socket              string
	ExpectedUID         *uint32
	RequestBytes        int
	ResponseBytes       int
	OutputBytes         int
	ControlTimeout      time.Duration
	CleanupTimeout      time.Duration
	GracefulStopTimeout time.Duration
}

type Engine struct {
	socket              string
	expectedUID         uint32
	requestBytes        int
	responseBytes       int
	outputBytes         int
	controlTimeout      time.Duration
	cleanupTimeout      time.Duration
	gracefulStopTimeout time.Duration
	client              *http.Client
}

type Result struct {
	ExitCode        int64
	ExitCodeKnown   bool
	Outcome         Outcome
	Output          []byte
	OutputTruncated bool
	Cleanup         CleanupStatus
}

func NewEngine(options EngineOptions) (*Engine, error) {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket || options.Socket == string(filepath.Separator) || strings.ContainsRune(options.Socket, 0) {
		return nil, errors.New("sandbox Docker socket must be an absolute clean path")
	}
	if options.ExpectedUID == nil {
		return nil, errors.New("sandbox Docker socket requires an expected peer UID")
	}
	requestBytes := options.RequestBytes
	if requestBytes <= 0 {
		requestBytes = defaultRequestBytes
	}
	responseBytes := options.ResponseBytes
	if responseBytes <= 0 {
		responseBytes = defaultResponseBytes
	}
	outputBytes := options.OutputBytes
	if outputBytes <= 0 {
		outputBytes = defaultOutputBytes
	}
	if requestBytes < 4096 || requestBytes > 4<<20 || responseBytes < 4096 || responseBytes > 16<<20 ||
		outputBytes < 1 || outputBytes > 16<<20 {
		return nil, errors.New("sandbox Docker protocol limits are outside the supported range")
	}
	controlTimeout := options.ControlTimeout
	if controlTimeout <= 0 {
		controlTimeout = defaultControlTimeout
	}
	cleanupTimeout := options.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = defaultCleanupTimeout
	}
	gracefulStopTimeout := options.GracefulStopTimeout
	if gracefulStopTimeout <= 0 {
		gracefulStopTimeout = defaultGracefulStopTimeout
	}
	if controlTimeout < time.Second || controlTimeout > time.Minute || cleanupTimeout < time.Second || cleanupTimeout > time.Minute ||
		gracefulStopTimeout < time.Second || gracefulStopTimeout > 30*time.Second {
		return nil, errors.New("sandbox Docker timeouts are outside the supported range")
	}
	engine := &Engine{
		socket:              options.Socket,
		expectedUID:         *options.ExpectedUID,
		requestBytes:        requestBytes,
		responseBytes:       responseBytes,
		outputBytes:         outputBytes,
		controlTimeout:      controlTimeout,
		cleanupTimeout:      cleanupTimeout,
		gracefulStopTimeout: gracefulStopTimeout,
	}
	transport := &http.Transport{
		DialContext:            engine.dialContext,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxConnsPerHost:        64,
		MaxResponseHeaderBytes: 64 << 10,
	}
	engine.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("sandbox Docker daemon redirect is not allowed")
		},
	}
	return engine, nil
}

func (e *Engine) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", e.socket)
	if err != nil {
		return nil, err
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, errors.New("sandbox Docker connection is not a Unix socket")
	}
	uid, err := unixPeerUID(unixConn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if uid != e.expectedUID {
		conn.Close()
		return nil, errors.New("sandbox Docker socket peer is not trusted")
	}
	return conn, nil
}

func (e *Engine) Run(ctx context.Context, plan Plan) (Result, error) {
	if e == nil || !plan.Valid() {
		return Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, errors.New("sandbox workload plan is invalid")
	}
	if plan.NeedsGateway() {
		return Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, errors.New("networked sandbox workloads require the durable Job lifecycle")
	}
	resource := plan.Resource()
	version, err := e.apiVersion(ctx)
	if err != nil {
		return Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, err
	}
	containerID, created, err := e.create(ctx, version, plan)
	if err != nil {
		result := Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}
		if created {
			result.Cleanup, err = e.joinCleanup(version, resource, containerID, err)
		}
		return result, err
	}
	if err = e.start(ctx, version, resource); err != nil {
		result := Result{Outcome: OutcomeLaunchFailed}
		result.Cleanup, err = e.joinCleanup(version, resource, containerID, err)
		return result, err
	}

	exitCode, err := e.wait(ctx, version, resource)
	if err != nil {
		result := Result{Outcome: outcomeForError(err)}
		result.Cleanup, err = e.joinCleanup(version, resource, containerID, err)
		return result, err
	}

	inspected, inspectErr := e.inspectRef(context.Background(), version, resource.Name(), resource)
	state := inspected.state
	if inspectErr != nil || !state.Exists || inspected.id != containerID || !state.Terminal || state.Running || state.ExitCode != exitCode {
		if inspectErr == nil {
			inspectErr = errors.New("sandbox Docker daemon returned contradictory terminal state")
		}
		result := Result{ExitCode: exitCode, ExitCodeKnown: true, Outcome: OutcomeUnknown}
		result.Cleanup, err = e.joinCleanup(version, resource, containerID, inspectErr)
		return result, err
	}

	outcome := OutcomeExited
	if state.OOMKilled {
		outcome = OutcomeOOMKilled
	}
	result := Result{ExitCode: exitCode, ExitCodeKnown: true, Outcome: outcome}
	result.Cleanup, err = e.joinCleanup(version, resource, containerID, nil)
	if err != nil {
		return result, err
	}
	return result, nil
}

// Inspect returns a bounded Docker-independent view of any Loki-owned resource
// in the deterministic Job domain. A subordinate gateway/network without a
// workload is reported as Exists so unbound crash recovery cannot discard it.
func (e *Engine) Inspect(ctx context.Context, resource Resource) (ResourceState, error) {
	if e == nil || !resource.Valid() {
		return ResourceState{}, errors.New("sandbox resource inspection is not configured")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return ResourceState{}, err
	}
	workload, err := e.inspectRef(ctx, version, resource.Name(), resource)
	if err != nil {
		return ResourceState{}, err
	}
	if workload.state.Exists {
		return workload.state, nil
	}
	gateway, err := e.inspectComponentRef(
		ctx, version, resource.GatewayName(), resource, resourceComponentGateway,
	)
	if err != nil {
		return ResourceState{}, err
	}
	if gateway.state.Exists {
		return ResourceState{Exists: true}, nil
	}
	internal, err := e.inspectNetwork(
		ctx, version, resource.InternalNetworkName(), resource.InternalNetworkName(),
		resource, resourceComponentInternalNetwork,
	)
	if err != nil {
		return ResourceState{}, err
	}
	if internal.exists {
		return ResourceState{Exists: true}, nil
	}
	outbound, err := e.inspectNetwork(
		ctx, version, resource.OutboundNetworkName(), resource.OutboundNetworkName(),
		resource, resourceComponentOutboundNetwork,
	)
	if err != nil {
		return ResourceState{}, err
	}
	if outbound.exists {
		return ResourceState{Exists: true}, nil
	}
	return ResourceState{}, nil
}

type inspectedResource struct {
	id                    string
	state                 ResourceState
	publishedPorts        map[string][]dockerPortBinding
	networkPortKeys       int
	exposedPortKeys       int
	requestedPortBindings map[string][]dockerPortBinding
	publishAllPorts       bool
	networkIDs            map[string]bool
}

func (e *Engine) inspect(ctx context.Context, version string, resource Resource) (ResourceState, error) {
	inspected, err := e.inspectRef(ctx, version, resource.Name(), resource)
	return inspected.state, err
}

func componentContainerName(resource Resource, component string) string {
	switch component {
	case resourceComponentWorkload:
		return resource.Name()
	case resourceComponentGateway:
		return resource.GatewayName()
	default:
		return ""
	}
}

func (e *Engine) inspectRef(ctx context.Context, version, ref string, resource Resource) (inspectedResource, error) {
	return e.inspectComponentRef(ctx, version, ref, resource, resourceComponentWorkload)
}

func (e *Engine) inspectComponentRef(
	ctx context.Context, version, ref string, resource Resource, component string,
) (inspectedResource, error) {
	name := componentContainerName(resource, component)
	if !resource.Valid() || name == "" || ref == "" || ref != name && !containerIDPattern.MatchString(ref) {
		return inspectedResource{}, errors.New("sandbox resource identity is invalid")
	}
	controlCtx, cancel := context.WithTimeout(ctx, e.controlTimeout)
	defer cancel()
	endpoint := "/v" + version + "/containers/" + url.PathEscape(ref) + "/json"
	response, err := e.call(controlCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return inspectedResource{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		if _, err = readBounded(response.Body, e.responseBytes); err != nil {
			return inspectedResource{}, err
		}
		return inspectedResource{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return inspectedResource{}, e.unexpectedStatus(response)
	}
	raw, err := readBounded(response.Body, e.responseBytes)
	if err != nil {
		return inspectedResource{}, err
	}
	var decoded struct {
		ID     string `json:"Id"`
		Config struct {
			Labels       map[string]string   `json:"Labels"`
			ExposedPorts map[string]struct{} `json:"ExposedPorts"`
		} `json:"Config"`
		HostConfig struct {
			PortBindings    map[string][]dockerPortBinding `json:"PortBindings"`
			PublishAllPorts bool                           `json:"PublishAllPorts"`
		} `json:"HostConfig"`
		NetworkSettings struct {
			Ports    map[string][]dockerPortBinding `json:"Ports"`
			Networks map[string]struct {
				NetworkID string `json:"NetworkID"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
		State struct {
			Status    string `json:"Status"`
			Running   bool   `json:"Running"`
			OOMKilled bool   `json:"OOMKilled"`
			ExitCode  int64  `json:"ExitCode"`
		} `json:"State"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&decoded); err != nil {
		return inspectedResource{}, errors.New("sandbox Docker daemon returned invalid JSON")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return inspectedResource{}, errors.New("sandbox Docker daemon returned trailing JSON")
	}
	if !containerIDPattern.MatchString(decoded.ID) {
		return inspectedResource{}, errors.New("sandbox Docker daemon returned an invalid container ID")
	}
	if !resource.ownsComponent(decoded.Config.Labels, component) {
		return inspectedResource{}, errors.New("sandbox resource ownership does not match")
	}
	state := ResourceState{Exists: true, Running: decoded.State.Running, OOMKilled: decoded.State.OOMKilled, ExitCode: decoded.State.ExitCode}
	switch decoded.State.Status {
	case "created", "removing":
	case "running", "restarting", "paused":
		if !state.Running {
			return inspectedResource{}, errors.New("sandbox Docker daemon returned contradictory resource state")
		}
	case "exited", "dead":
		state.Terminal = true
	default:
		return inspectedResource{}, errors.New("sandbox Docker daemon returned an invalid resource state")
	}
	if !state.valid() {
		return inspectedResource{}, errors.New("sandbox Docker daemon returned contradictory resource state")
	}
	ports := make(map[string][]dockerPortBinding, len(decoded.NetworkSettings.Ports))
	for key, bindings := range decoded.NetworkSettings.Ports {
		if len(bindings) == 0 {
			continue
		}
		copyBindings := append([]dockerPortBinding(nil), bindings...)
		ports[key] = copyBindings
	}
	requestedBindings := make(map[string][]dockerPortBinding, len(decoded.HostConfig.PortBindings))
	for key, bindings := range decoded.HostConfig.PortBindings {
		requestedBindings[key] = append([]dockerPortBinding(nil), bindings...)
	}
	networkIDs := make(map[string]bool, len(decoded.NetworkSettings.Networks))
	for _, settings := range decoded.NetworkSettings.Networks {
		if settings.NetworkID == "" {
			continue
		}
		if !containerIDPattern.MatchString(settings.NetworkID) {
			return inspectedResource{}, errors.New("sandbox Docker daemon returned an invalid network identity")
		}
		networkIDs[settings.NetworkID] = true
	}
	return inspectedResource{
		id: decoded.ID, state: state, publishedPorts: ports,
		networkPortKeys:       len(decoded.NetworkSettings.Ports),
		exposedPortKeys:       len(decoded.Config.ExposedPorts),
		requestedPortBindings: requestedBindings,
		publishAllPorts:       decoded.HostConfig.PublishAllPorts,
		networkIDs:            networkIDs,
	}, nil
}

func (e *Engine) apiVersion(ctx context.Context) (string, error) {
	var response struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := e.controlJSON(ctx, http.MethodGet, "/version", nil, http.StatusOK, &response); err != nil {
		return "", err
	}
	if !apiVersionPattern.MatchString(response.APIVersion) {
		return "", errors.New("sandbox Docker daemon returned an invalid API version")
	}
	ok, err := apiAtLeast(response.APIVersion, 1, 41)
	if err != nil || !ok {
		return "", errors.New("sandbox Docker daemon API is too old")
	}
	return response.APIVersion, nil
}

func apiAtLeast(value string, major, minor int) (bool, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false, errors.New("invalid API version")
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, err
	}
	gotMinor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false, err
	}
	return gotMajor > major || gotMajor == major && gotMinor >= minor, nil
}

func (e *Engine) create(ctx context.Context, version string, plan Plan) (string, bool, error) {
	resource := plan.Resource()
	if !resource.Valid() {
		return "", false, errors.New("sandbox resource identity is invalid")
	}
	return e.createContainer(ctx, version, resource.Name(), plan.create)
}

func (e *Engine) createContainer(
	ctx context.Context, version, name string, request dockerCreateRequest,
) (string, bool, error) {
	if name == "" || strings.ContainsAny(name, "/?&#\r\n") {
		return "", false, errors.New("sandbox container name is invalid")
	}
	controlCtx, cancel := context.WithTimeout(ctx, e.controlTimeout)
	defer cancel()
	logBytes := max(e.outputBytes, request.outputBytes, 64<<10)
	request.HostConfig.LogConfig = dockerLogConfig{
		Type: "local",
		Config: map[string]string{
			"max-size": strconv.Itoa(logBytes),
			"max-file": "2",
		},
	}
	endpoint := "/v" + version + "/containers/create?name=" + url.QueryEscape(name)
	response, err := e.call(controlCtx, http.MethodPost, endpoint, request)
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return "", false, e.unexpectedStatus(response)
	}
	raw, err := readBounded(response.Body, e.responseBytes)
	if err != nil {
		return "", true, err
	}
	var decoded struct {
		ID string `json:"Id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&decoded); err != nil {
		return "", true, errors.New("sandbox Docker daemon returned invalid JSON")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", true, errors.New("sandbox Docker daemon returned trailing JSON")
	}
	if !containerIDPattern.MatchString(decoded.ID) {
		return "", true, errors.New("sandbox Docker daemon returned an invalid container ID")
	}
	return decoded.ID, true, nil
}

func validContainerRef(ref string, resource Resource) bool {
	return resource.Valid() && (ref == resource.Name() || containerIDPattern.MatchString(ref))
}

func (e *Engine) start(ctx context.Context, version string, resource Resource) error {
	return e.startRef(ctx, version, resource.Name(), resource)
}

func (e *Engine) startRef(ctx context.Context, version, ref string, resource Resource) error {
	if !validContainerRef(ref, resource) {
		return errors.New("sandbox resource identity is invalid")
	}
	return e.controlJSON(ctx, http.MethodPost, "/v"+version+"/containers/"+url.PathEscape(ref)+"/start", nil, http.StatusNoContent, nil)
}

func (e *Engine) wait(ctx context.Context, version string, resource Resource) (int64, error) {
	return e.waitRef(ctx, version, resource.Name(), resource)
}

func (e *Engine) waitRef(ctx context.Context, version, ref string, resource Resource) (int64, error) {
	if !validContainerRef(ref, resource) {
		return 0, errors.New("sandbox resource identity is invalid")
	}
	var response struct {
		StatusCode int64 `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error,omitempty"`
	}
	if err := e.callJSON(ctx, http.MethodPost, "/v"+version+"/containers/"+url.PathEscape(ref)+"/wait?condition=not-running", nil, http.StatusOK, &response); err != nil {
		return 0, err
	}
	if response.Error != nil && response.Error.Message != "" {
		return 0, errors.New("sandbox Docker wait reported a container error")
	}
	if response.StatusCode < 0 || response.StatusCode > 255 {
		return 0, errors.New("sandbox Docker wait returned an invalid exit code")
	}
	return response.StatusCode, nil
}

func (e *Engine) stop(ctx context.Context, version string, resource Resource) error {
	return e.stopRef(ctx, version, resource.Name(), resource)
}

func (e *Engine) stopRef(ctx context.Context, version, ref string, resource Resource) error {
	if !validContainerRef(ref, resource) {
		return errors.New("sandbox resource identity is invalid")
	}
	seconds := int((e.gracefulStopTimeout + time.Second - 1) / time.Second)
	status, err := e.call(ctx, http.MethodPost, "/v"+version+"/containers/"+url.PathEscape(ref)+"/stop?t="+strconv.Itoa(seconds), nil)
	if err != nil {
		return err
	}
	defer status.Body.Close()
	if status.StatusCode == http.StatusNoContent || status.StatusCode == http.StatusNotModified || status.StatusCode == http.StatusNotFound {
		_, err = readBounded(status.Body, e.responseBytes)
		return err
	}
	return e.unexpectedStatus(status)
}

func (e *Engine) kill(ctx context.Context, version string, resource Resource) error {
	return e.killRef(ctx, version, resource.Name(), resource)
}

func (e *Engine) killRef(ctx context.Context, version, ref string, resource Resource) error {
	if !validContainerRef(ref, resource) {
		return errors.New("sandbox resource identity is invalid")
	}
	status, err := e.call(ctx, http.MethodPost, "/v"+version+"/containers/"+url.PathEscape(ref)+"/kill?signal=KILL", nil)
	if err != nil {
		return err
	}
	defer status.Body.Close()
	if status.StatusCode == http.StatusNoContent || status.StatusCode == http.StatusNotFound || status.StatusCode == http.StatusConflict {
		_, err = readBounded(status.Body, e.responseBytes)
		return err
	}
	return e.unexpectedStatus(status)
}

func (e *Engine) remove(ctx context.Context, version string, resource Resource) error {
	return e.removeRef(ctx, version, resource.Name(), resource)
}

func (e *Engine) removeRef(ctx context.Context, version, ref string, resource Resource) error {
	if !validContainerRef(ref, resource) {
		return errors.New("sandbox resource identity is invalid")
	}
	status, err := e.call(ctx, http.MethodDelete, "/v"+version+"/containers/"+url.PathEscape(ref)+"?force=1&v=1", nil)
	if err != nil {
		return err
	}
	defer status.Body.Close()
	if status.StatusCode == http.StatusNoContent || status.StatusCode == http.StatusNotFound {
		_, err = readBounded(status.Body, e.responseBytes)
		return err
	}
	return e.unexpectedStatus(status)
}

func (e *Engine) controlJSON(ctx context.Context, method, endpoint string, body any, want int, out any) error {
	controlCtx, cancel := context.WithTimeout(ctx, e.controlTimeout)
	defer cancel()
	return e.callJSON(controlCtx, method, endpoint, body, want, out)
}

func (e *Engine) callJSON(ctx context.Context, method, endpoint string, body any, want int, out any) error {
	response, err := e.call(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		return e.unexpectedStatus(response)
	}
	if out == nil {
		_, err = readBounded(response.Body, e.responseBytes)
		return err
	}
	raw, err := readBounded(response.Body, e.responseBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(out); err != nil {
		return errors.New("sandbox Docker daemon returned invalid JSON")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("sandbox Docker daemon returned trailing JSON")
	}
	return nil
}

func (e *Engine) call(ctx context.Context, method, endpoint string, body any) (*http.Response, error) {
	if endpoint == "" || !strings.HasPrefix(endpoint, "/") || strings.ContainsAny(endpoint, "\r\n") {
		return nil, errors.New("sandbox Docker endpoint is invalid")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		if len(raw) > e.requestBytes {
			return nil, errors.New("sandbox Docker request exceeds limit")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://docker"+endpoint, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := e.client.Do(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (e *Engine) unexpectedStatus(response *http.Response) error {
	raw, err := readBounded(response.Body, min(e.responseBytes, 64<<10))
	if err != nil {
		return fmt.Errorf("sandbox Docker daemon returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &payload) == nil {
		message := strings.TrimSpace(payload.Message)
		if message != "" && len(message) <= 1024 && !strings.ContainsAny(message, "\r\n\x00") {
			return fmt.Errorf("sandbox Docker daemon returned HTTP %d: %s", response.StatusCode, message)
		}
	}
	return fmt.Errorf("sandbox Docker daemon returned HTTP %d", response.StatusCode)
}

func readBounded(reader io.Reader, maximum int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, errors.New("sandbox Docker response exceeds limit")
	}
	return raw, nil
}
