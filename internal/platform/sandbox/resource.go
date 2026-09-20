package sandbox

import (
	"errors"
	"regexp"
)

const (
	resourceOwnerLabel     = "io.loki.owner"
	resourceJobLabel       = "io.loki.job.id"
	resourcePolicyLabel    = "io.loki.policy.sha256"
	resourceSandboxLabel   = "io.loki.sandbox.sha256"
	resourceComponentLabel = "io.loki.resource.component"
	resourceOwnerValue     = "job"

	resourceComponentWorkload        = "workload"
	resourceComponentGateway         = "gateway"
	resourceComponentInternalNetwork = "internal-network"
	resourceComponentOutboundNetwork = "outbound-network"
)

var resourceNamePattern = regexp.MustCompile(`^loki-job-[0-9a-f]{32}$`)

var ErrInstanceMismatch = errors.New("sandbox resource instance does not match")

// Resource is Loki's stable identity for one sandbox resource domain.
// It intentionally does not expose Docker container or network IDs.
type Resource struct {
	jobID         string
	policySHA256  string
	sandboxSHA256 string
}

func NewResource(jobID, policySHA256, sandboxSHA256 string) (Resource, error) {
	if !jobIDPattern.MatchString(jobID) || !digestPattern.MatchString(policySHA256) ||
		!digestPattern.MatchString(sandboxSHA256) {
		return Resource{}, errors.New("sandbox resource identity is invalid")
	}
	return Resource{jobID: jobID, policySHA256: policySHA256, sandboxSHA256: sandboxSHA256}, nil
}

func (r Resource) Valid() bool {
	return jobIDPattern.MatchString(r.jobID) && digestPattern.MatchString(r.policySHA256) &&
		digestPattern.MatchString(r.sandboxSHA256)
}

func (r Resource) JobID() string {
	if !r.Valid() {
		return ""
	}
	return r.jobID
}

func (r Resource) PolicySHA256() string {
	if !r.Valid() {
		return ""
	}
	return r.policySHA256
}

func (r Resource) SandboxSHA256() string {
	if !r.Valid() {
		return ""
	}
	return r.sandboxSHA256
}

func (r Resource) Name() string {
	if !r.Valid() {
		return ""
	}
	return "loki-job-" + r.jobID
}

func (r Resource) GatewayName() string {
	if !r.Valid() {
		return ""
	}
	return "loki-job-gateway-" + r.jobID
}

func (r Resource) InternalNetworkName() string {
	if !r.Valid() {
		return ""
	}
	return "loki-job-net-" + r.jobID
}

func (r Resource) OutboundNetworkName() string {
	if !r.Valid() {
		return ""
	}
	return "loki-job-egress-" + r.jobID
}

func validResourceComponent(component string) bool {
	switch component {
	case resourceComponentWorkload, resourceComponentGateway,
		resourceComponentInternalNetwork, resourceComponentOutboundNetwork:
		return true
	default:
		return false
	}
}

func (r Resource) labelsFor(component string) map[string]string {
	if !r.Valid() || !validResourceComponent(component) {
		return nil
	}
	return map[string]string{
		resourceOwnerLabel:     resourceOwnerValue,
		resourceJobLabel:       r.jobID,
		resourcePolicyLabel:    r.policySHA256,
		resourceSandboxLabel:   r.sandboxSHA256,
		resourceComponentLabel: component,
	}
}

func (r Resource) labels() map[string]string {
	return r.labelsFor(resourceComponentWorkload)
}

func (r Resource) ownsComponent(labels map[string]string, component string) bool {
	if !r.Valid() {
		return false
	}
	expected := r.labelsFor(component)
	if expected == nil {
		return false
	}
	for key, value := range expected {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func (r Resource) owns(labels map[string]string) bool {
	return r.ownsComponent(labels, resourceComponentWorkload)
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
	CleanupNotRequired CleanupStatus = "not_required"
	CleanupComplete    CleanupStatus = "complete"
	CleanupFailed      CleanupStatus = "failed"
)

func (s CleanupStatus) Valid() bool {
	return s == CleanupPending || s == CleanupNotRequired || s == CleanupComplete || s == CleanupFailed
}

// ResourceState is the bounded, Docker-independent view of one sandbox resource.
type ResourceState struct {
	Exists    bool
	Running   bool
	Terminal  bool
	OOMKilled bool
	ExitCode  int64
}

func (s ResourceState) valid() bool {
	if !s.Exists {
		return !s.Running && !s.Terminal && !s.OOMKilled && s.ExitCode == 0
	}
	if s.Running {
		return !s.Terminal && !s.OOMKilled
	}
	if s.OOMKilled && !s.Terminal {
		return false
	}
	if s.Terminal {
		return s.ExitCode >= 0 && s.ExitCode <= 255
	}
	return !s.OOMKilled && s.ExitCode == 0
}
