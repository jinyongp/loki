package jobs

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxOutputBytes = 4 << 20

type State string

const (
	StateAdmitted State = "admitted"
	StateRunning  State = "running"
	StateTerminal State = "terminal"
)

func (s State) Valid() bool {
	return s == StateAdmitted || s == StateRunning || s == StateTerminal
}

type Outcome string

const (
	OutcomeExited       Outcome = "exited"
	OutcomeCanceled     Outcome = "canceled"
	OutcomeTimedOut     Outcome = "timed_out"
	OutcomeOOMKilled    Outcome = "oom_killed"
	OutcomeLaunchFailed Outcome = "launch_failed"
	OutcomeUnknown      Outcome = "outcome_unknown"
)

func (o Outcome) Valid() bool {
	switch o {
	case OutcomeExited, OutcomeCanceled, OutcomeTimedOut, OutcomeOOMKilled, OutcomeLaunchFailed, OutcomeUnknown:
		return true
	default:
		return false
	}
}

type CleanupStatus string

const (
	CleanupPending     CleanupStatus = "pending"
	CleanupComplete    CleanupStatus = "complete"
	CleanupFailed      CleanupStatus = "failed"
	CleanupNotRequired CleanupStatus = "not_required"
)

func (s CleanupStatus) Valid() bool {
	switch s {
	case CleanupPending, CleanupComplete, CleanupFailed, CleanupNotRequired:
		return true
	default:
		return false
	}
}

type Output struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type Result struct {
	ExitCode *int64        `json:"exit_code,omitempty"`
	Outcome  Outcome       `json:"outcome"`
	Output   Output        `json:"output"`
	Cleanup  CleanupStatus `json:"cleanup"`
}

func (r Result) Valid(maxOutputBytes int) bool {
	if !r.Outcome.Valid() || !r.Cleanup.Valid() {
		return false
	}
	if r.ExitCode != nil && (*r.ExitCode < 0 || *r.ExitCode > 255) {
		return false
	}
	if (r.Outcome == OutcomeExited || r.Outcome == OutcomeOOMKilled) && r.ExitCode == nil {
		return false
	}
	if r.Outcome == OutcomeLaunchFailed && r.ExitCode != nil {
		return false
	}
	if len(r.Output.Text) > maxOutputBytes || !utf8.ValidString(r.Output.Text) {
		return false
	}
	return true
}

type Record struct {
	ID               string            `json:"id"`
	BackendRef       string            `json:"backend_ref"`
	RequestID        string            `json:"request_id,omitempty"`
	RequestSHA256    string            `json:"request_sha256,omitempty"`
	Network          NetworkProfile    `json:"network"`
	EndpointRequests []EndpointRequest `json:"endpoint_requests,omitempty"`
	EndpointLeases   []EndpointLease   `json:"endpoint_leases,omitempty"`
	Toolchains       []ToolchainRef    `json:"toolchains,omitempty"`
	InstanceRef      string            `json:"instance_ref,omitempty"`
	State            State             `json:"state"`
	Result           *Result           `json:"result,omitempty"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
	DeadlineAt       string            `json:"deadline_at"`
	ExpiresAt        string            `json:"expires_at,omitempty"`
}

func (r Record) valid(maxOutputBytes int) bool {
	if !jobIDPattern.MatchString(r.ID) || !validBackendRef(r.BackendRef) ||
		!validReplayIdentity(r.RequestID, r.RequestSHA256) ||
		!r.Network.Valid() || !validEndpointRequests(r.EndpointRequests) || !validToolchainRefs(r.Toolchains) ||
		!validEndpointLeases(r.ID, r.InstanceRef, r.EndpointRequests, r.EndpointLeases) ||
		!validOptionalInstanceRef(r.InstanceRef) || !r.State.Valid() {
		return false
	}
	createdAt, createdErr := time.Parse(time.RFC3339Nano, r.CreatedAt)
	updatedAt, updatedErr := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	deadlineAt, deadlineErr := time.Parse(time.RFC3339Nano, r.DeadlineAt)
	if createdErr != nil || updatedErr != nil || deadlineErr != nil ||
		createdAt.Location() != time.UTC || updatedAt.Location() != time.UTC || deadlineAt.Location() != time.UTC ||
		updatedAt.Before(createdAt) || !deadlineAt.After(createdAt) || deadlineAt.Sub(createdAt) > 24*time.Hour {
		return false
	}
	switch r.State {
	case StateAdmitted:
		if r.Result != nil || r.ExpiresAt != "" {
			return false
		}
		return len(r.EndpointLeases) == 0 ||
			validRequiredInstanceRef(r.InstanceRef) && endpointLeasesExact(r.EndpointRequests, r.EndpointLeases, EndpointLeaseActive)
	case StateRunning:
		return r.Result == nil && r.ExpiresAt == "" && validRequiredInstanceRef(r.InstanceRef) &&
			endpointLeasesExact(r.EndpointRequests, r.EndpointLeases, EndpointLeaseActive)
	case StateTerminal:
		expiresAt, err := time.Parse(time.RFC3339Nano, r.ExpiresAt)
		if r.Result == nil || !r.Result.Valid(maxOutputBytes) || err != nil ||
			expiresAt.Location() != time.UTC || !expiresAt.After(updatedAt) {
			return false
		}
		if (r.Result.Outcome == OutcomeExited || r.Result.Outcome == OutcomeOOMKilled ||
			r.Result.Cleanup == CleanupPending) &&
			!validRequiredInstanceRef(r.InstanceRef) {
			return false
		}
		return len(r.EndpointLeases) == 0 ||
			endpointLeasesExact(r.EndpointRequests, r.EndpointLeases, EndpointLeaseReleased)
	default:
		return false
	}
}

func validEndpointRequests(values []EndpointRequest) bool {
	normalized, err := normalizeEndpointRequests(values)
	return err == nil && sameEndpointRequests(values, normalized)
}

func validToolchainRefs(values []ToolchainRef) bool {
	normalized, err := normalizeToolchainRefs(values)
	return err == nil && sameToolchainRefs(values, normalized)
}

func validEndpointLeases(jobID, instanceRef string, requests []EndpointRequest, leases []EndpointLease) bool {
	if len(leases) > len(requests) {
		return false
	}
	byName := make(map[string]EndpointRequest, len(requests))
	for _, request := range requests {
		byName[request.Name] = request
	}
	seenIDs := map[string]bool{}
	seenNames := map[string]bool{}
	seenPorts := map[int]bool{}
	for _, lease := range leases {
		request, ok := byName[lease.Name]
		expectedID, idErr := EndpointLeaseID(jobID, lease.Name, instanceRef)
		if !ok || idErr != nil || request.Port != lease.Port || lease.JobID != jobID ||
			!leaseIDPattern.MatchString(lease.ID) || lease.ID != expectedID ||
			lease.HostPort < 1024 || lease.HostPort > 65535 ||
			!lease.State.Valid() || seenIDs[lease.ID] || seenNames[lease.Name] || seenPorts[lease.HostPort] {
			return false
		}
		createdAt, createdErr := time.Parse(time.RFC3339Nano, lease.CreatedAt)
		updatedAt, updatedErr := time.Parse(time.RFC3339Nano, lease.UpdatedAt)
		if createdErr != nil || updatedErr != nil ||
			createdAt.Location() != time.UTC || updatedAt.Location() != time.UTC ||
			updatedAt.Before(createdAt) {
			return false
		}
		seenIDs[lease.ID] = true
		seenNames[lease.Name] = true
		seenPorts[lease.HostPort] = true
	}
	return true
}

func endpointLeasesExact(
	requests []EndpointRequest, leases []EndpointLease, state EndpointLeaseState,
) bool {
	if len(requests) != len(leases) {
		return false
	}
	for _, lease := range leases {
		if lease.State != state {
			return false
		}
	}
	return true
}

func validReplayIdentity(requestID, requestSHA256 string) bool {
	if requestID == "" && requestSHA256 == "" {
		return true
	}
	return requestID != "" && requestID == strings.ToLower(requestID) &&
		requestIDPattern.MatchString(requestID) && sha256Pattern.MatchString(requestSHA256)
}

func validBackendRef(value string) bool {
	if len(value) == 0 || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func validOptionalInstanceRef(value string) bool {
	return value == "" || validRequiredInstanceRef(value)
}

func validRequiredInstanceRef(value string) bool {
	return len(value) <= 256 && validBackendRef(value)
}

func NormalizeOutput(raw []byte, limit int, truncated bool) (Output, error) {
	if limit < 1 || limit > MaxOutputBytes {
		return Output{}, errors.New("job output limit is outside the supported range")
	}
	var text strings.Builder
	text.Grow(min(len(raw), limit))
	for len(raw) > 0 && text.Len() < limit {
		r, size := utf8.DecodeRune(raw)
		if r == utf8.RuneError && size == 1 {
			r = utf8.RuneError
			size = 1
		}
		encoded := string(r)
		if text.Len()+len(encoded) > limit {
			truncated = true
			break
		}
		text.WriteString(encoded)
		raw = raw[size:]
	}
	if len(raw) > 0 {
		truncated = true
	}
	return Output{Text: text.String(), Truncated: truncated}, nil
}
