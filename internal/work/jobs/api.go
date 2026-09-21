package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	MaxTimeoutSeconds = 24 * 60 * 60
	MaxEndpoints      = 8
	MaxToolchains     = 8
)

var ErrReplayConflict = errors.New("job request_id conflicts with retained start input")

var (
	requestIDPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sha256Pattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	endpointNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	toolchainNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	leaseIDPattern       = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type NetworkProfile string

const (
	NetworkNone              NetworkProfile = "none"
	NetworkDependencyInstall NetworkProfile = "dependency-install"
)

func (p NetworkProfile) Valid() bool {
	return p == NetworkNone || p == NetworkDependencyInstall
}

type EndpointRequest struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type ToolchainRef struct {
	Family       string `json:"family"`
	Version      string `json:"version"`
	GenerationID string `json:"generation_id"`
}

type EndpointLeaseState string

const (
	EndpointLeaseActive   EndpointLeaseState = "active"
	EndpointLeaseReleased EndpointLeaseState = "released"
)

func (s EndpointLeaseState) Valid() bool {
	return s == EndpointLeaseActive || s == EndpointLeaseReleased
}

type EndpointLease struct {
	ID        string             `json:"lease_id"`
	JobID     string             `json:"job_id"`
	Name      string             `json:"name"`
	Port      int                `json:"port"`
	HostPort  int                `json:"host_port"`
	State     EndpointLeaseState `json:"state"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
}

type StartRequest struct {
	RequestID      string            `json:"request_id"`
	CWD            string            `json:"cwd,omitempty"`
	Argv           []string          `json:"argv"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Network        NetworkProfile    `json:"network,omitempty"`
	Endpoints      []EndpointRequest `json:"endpoints,omitempty"`
	Toolchains     []ToolchainRef    `json:"toolchains,omitempty"`
}

type StartResult struct {
	RequestID  string `json:"request_id"`
	JobID      string `json:"job_id"`
	State      State  `json:"state"`
	Replayed   bool   `json:"replayed"`
	Detached   bool   `json:"detached"`
	DeadlineAt string `json:"deadline_at"`
}

type Status struct {
	JobID      string          `json:"job_id"`
	State      State           `json:"state"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
	DeadlineAt string          `json:"deadline_at"`
	Network    NetworkProfile  `json:"network"`
	Endpoints  []EndpointLease `json:"endpoints,omitempty"`
	Toolchains []ToolchainRef  `json:"toolchains,omitempty"`
	Outcome    Outcome         `json:"outcome,omitempty"`
	ExitCode   *int64          `json:"exit_code,omitempty"`
	Cleanup    CleanupStatus   `json:"cleanup,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
}

type OutputSnapshot struct {
	JobID     string `json:"job_id"`
	State     State  `json:"state"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	Complete  bool   `json:"complete"`
}

type CancelResult struct {
	JobID    string `json:"job_id"`
	Canceled bool   `json:"canceled"`
	Status   Status `json:"status"`
}

type Launcher interface {
	Run(context.Context, Workload) (RunExecutionResult, error)
	Start(context.Context, Workload) (StartResult, error)
	Inspect(context.Context, string) (Status, error)
	Output(context.Context, string) (OutputSnapshot, error)
	Cancel(context.Context, string) (CancelResult, error)
}

type Runner interface {
	Run(context.Context, RunRequest) (RunResult, error)
}

type Controller interface {
	Start(context.Context, StartRequest) (StartResult, error)
	Inspect(context.Context, string) (Status, error)
	Output(context.Context, string) (OutputSnapshot, error)
	Cancel(context.Context, string) (CancelResult, error)
}

func validStartResult(result StartResult, requestID, jobID string) bool {
	if result.RequestID != requestID || result.JobID != jobID || !result.State.Valid() || !result.Detached {
		return false
	}
	deadline, err := time.Parse(time.RFC3339Nano, result.DeadlineAt)
	return err == nil && deadline.Location() == time.UTC
}

func NormalizeRequestID(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !requestIDPattern.MatchString(value) {
		return "", errors.New("job request_id must be a UUID")
	}
	return value, nil
}

func JobIDForRequestID(requestID string) (string, error) {
	normalized, err := NormalizeRequestID(requestID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("loki-job-request-v1\x00" + normalized))
	return hex.EncodeToString(sum[:16]), nil
}

func ResolveRequestedTimeout(timeoutSeconds int, trustedMaximum time.Duration) (time.Duration, error) {
	if trustedMaximum < time.Second || trustedMaximum > 24*time.Hour {
		return 0, errors.New("trusted job timeout is outside the supported range")
	}
	if timeoutSeconds < 0 || timeoutSeconds > MaxTimeoutSeconds {
		return 0, errors.New("job timeout is outside the supported range")
	}
	if timeoutSeconds == 0 {
		return trustedMaximum, nil
	}
	requested := time.Duration(timeoutSeconds) * time.Second
	if requested > trustedMaximum {
		return 0, errors.New("requested job timeout exceeds the trusted maximum")
	}
	return requested, nil
}

func NormalizeNetworkProfile(value NetworkProfile) (NetworkProfile, error) {
	if value == "" {
		return NetworkNone, nil
	}
	if !value.Valid() {
		return "", errors.New("job network profile is invalid")
	}
	return value, nil
}

func normalizeEndpointRequests(values []EndpointRequest) ([]EndpointRequest, error) {
	if len(values) > MaxEndpoints {
		return nil, errors.New("job endpoint count exceeds its limit")
	}
	result := make([]EndpointRequest, len(values))
	copy(result, values)
	for index := range result {
		if !endpointNamePattern.MatchString(result[index].Name) ||
			result[index].Port < 1024 || result[index].Port > 65535 {
			return nil, errors.New("job endpoint is invalid")
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Port < result[j].Port
		}
		return result[i].Name < result[j].Name
	})
	for index := 1; index < len(result); index++ {
		if result[index-1].Name == result[index].Name || result[index-1].Port == result[index].Port {
			return nil, errors.New("job endpoints must use unique names and ports")
		}
	}
	ports := map[int]bool{}
	for _, endpoint := range result {
		if ports[endpoint.Port] {
			return nil, errors.New("job endpoints must use unique names and ports")
		}
		ports[endpoint.Port] = true
	}
	return result, nil
}

func normalizeToolchainRefs(values []ToolchainRef) ([]ToolchainRef, error) {
	if len(values) > MaxToolchains {
		return nil, errors.New("job toolchain count exceeds its limit")
	}
	result := append([]ToolchainRef(nil), values...)
	for index := range result {
		result[index].Family = strings.TrimSpace(result[index].Family)
		result[index].Version = strings.TrimSpace(result[index].Version)
		result[index].GenerationID = strings.ToLower(strings.TrimSpace(result[index].GenerationID))
		if !toolchainNamePattern.MatchString(result[index].Family) ||
			result[index].Version == "" || len(result[index].Version) > 128 ||
			strings.ContainsAny(result[index].Version, "\r\n\x00/\\") ||
			!sha256Pattern.MatchString(result[index].GenerationID) {
			return nil, errors.New("job toolchain reference is invalid")
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Family < result[j].Family })
	for index := 1; index < len(result); index++ {
		if result[index-1].Family == result[index].Family {
			return nil, errors.New("job toolchain families must be unique")
		}
	}
	return result, nil
}

func sameToolchainRefs(left, right []ToolchainRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameEndpointRequests(left, right []EndpointRequest) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func startFingerprint(request StartRequest) (string, error) {
	network, err := NormalizeNetworkProfile(request.Network)
	if err != nil {
		return "", err
	}
	endpoints, err := normalizeEndpointRequests(request.Endpoints)
	if err != nil {
		return "", err
	}
	toolchains, err := normalizeToolchainRefs(request.Toolchains)
	if err != nil {
		return "", err
	}
	payload := struct {
		CWD            string            `json:"cwd"`
		Argv           []string          `json:"argv"`
		TimeoutSeconds int               `json:"timeout_seconds"`
		Network        NetworkProfile    `json:"network"`
		Endpoints      []EndpointRequest `json:"endpoints"`
		Toolchains     []ToolchainRef    `json:"toolchains"`
	}{
		CWD: request.CWD, Argv: request.Argv, TimeoutSeconds: request.TimeoutSeconds,
		Network: network, Endpoints: endpoints, Toolchains: toolchains,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func StartFingerprint(cwd string, argv []string, timeoutSeconds int) (string, error) {
	normalizedCWD, err := normalizeStartCWD(cwd)
	if err != nil {
		return "", err
	}
	normalizedArgv, err := validateArgv(argv)
	if err != nil {
		return "", err
	}
	if timeoutSeconds < 0 || timeoutSeconds > MaxTimeoutSeconds {
		return "", errors.New("job timeout is outside the supported range")
	}
	return startFingerprint(StartRequest{
		CWD: normalizedCWD, Argv: normalizedArgv, TimeoutSeconds: timeoutSeconds,
		Network: NetworkNone,
	})
}

func normalizeStartCWD(value string) (string, error) {
	if value == "" {
		value = "."
	}
	return validateCWD(value)
}

func NormalizeStartRequest(request StartRequest) (StartRequest, string, error) {
	return normalizeStartRequest(request)
}

func normalizeStartRequest(request StartRequest) (StartRequest, string, error) {
	requestID, err := NormalizeRequestID(request.RequestID)
	if err != nil {
		return StartRequest{}, "", err
	}
	cwd, err := normalizeStartCWD(request.CWD)
	if err != nil {
		return StartRequest{}, "", err
	}
	argv, err := validateArgv(request.Argv)
	if err != nil {
		return StartRequest{}, "", err
	}
	if request.TimeoutSeconds < 0 || request.TimeoutSeconds > MaxTimeoutSeconds {
		return StartRequest{}, "", errors.New("job timeout is outside the supported range")
	}
	network, err := NormalizeNetworkProfile(request.Network)
	if err != nil {
		return StartRequest{}, "", err
	}
	endpoints, err := normalizeEndpointRequests(request.Endpoints)
	if err != nil {
		return StartRequest{}, "", err
	}
	toolchains, err := normalizeToolchainRefs(request.Toolchains)
	if err != nil {
		return StartRequest{}, "", err
	}
	request.RequestID = requestID
	request.CWD = cwd
	request.Argv = argv
	request.Network = network
	request.Endpoints = endpoints
	request.Toolchains = toolchains
	fingerprint, err := startFingerprint(request)
	if err != nil {
		return StartRequest{}, "", err
	}
	return request, fingerprint, nil
}

func EndpointLeaseID(jobID, name, instanceRef string) (string, error) {
	if !jobIDPattern.MatchString(jobID) || !endpointNamePattern.MatchString(name) ||
		!validRequiredInstanceRef(instanceRef) {
		return "", errors.New("endpoint lease identity is invalid")
	}
	sum := sha256.Sum256([]byte(jobID + "\x00" + name + "\x00" + instanceRef))
	return hex.EncodeToString(sum[:16]), nil
}

func ValidateJobID(id string) error {
	if !jobIDPattern.MatchString(id) {
		return errors.New("job ID is invalid")
	}
	return nil
}

func statusFromRecord(record Record) Status {
	status := Status{
		JobID: record.ID, State: record.State,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, DeadlineAt: record.DeadlineAt,
		Network: record.Network, Endpoints: append([]EndpointLease(nil), record.EndpointLeases...),
		Toolchains: append([]ToolchainRef(nil), record.Toolchains...),
	}
	if record.Result != nil {
		status.Outcome = record.Result.Outcome
		status.ExitCode = record.Result.ExitCode
		status.Cleanup = record.Result.Cleanup
		status.Truncated = record.Result.Output.Truncated
	}
	return status
}

func StatusFromRecord(record Record) (Status, error) {
	if !record.valid(MaxOutputBytes) {
		return Status{}, errors.New("job record is invalid")
	}
	return statusFromRecord(record), nil
}

func OutputFromRecord(record Record) (OutputSnapshot, error) {
	if !record.valid(MaxOutputBytes) {
		return OutputSnapshot{}, errors.New("job record is invalid")
	}
	snapshot := OutputSnapshot{
		JobID:    record.ID,
		State:    record.State,
		Complete: record.State == StateTerminal,
	}
	if record.Result != nil {
		snapshot.Output = record.Result.Output.Text
		snapshot.Truncated = record.Result.Output.Truncated
	}
	return snapshot, nil
}
