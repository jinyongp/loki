package sandbox

import (
	"errors"
	"regexp"
)

const (
	resourceOwnerLabel  = "io.loki.owner"
	resourceJobLabel    = "io.loki.job.id"
	resourcePolicyLabel = "io.loki.policy.sha256"
	resourceOwnerValue  = "job"
)

var resourceNamePattern = regexp.MustCompile(`^loki-job-[0-9a-f]{32}$`)

// Resource is Loki's stable identity for one sandbox workload.
// It intentionally does not expose a Docker container ID.
type Resource struct {
	jobID        string
	policySHA256 string
}

func NewResource(jobID, policySHA256 string) (Resource, error) {
	if !jobIDPattern.MatchString(jobID) || !digestPattern.MatchString(policySHA256) {
		return Resource{}, errors.New("sandbox resource identity is invalid")
	}
	return Resource{jobID: jobID, policySHA256: policySHA256}, nil
}

func (r Resource) Valid() bool {
	return jobIDPattern.MatchString(r.jobID) && digestPattern.MatchString(r.policySHA256)
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

func (r Resource) Name() string {
	if !r.Valid() {
		return ""
	}
	return "loki-job-" + r.jobID
}

func (r Resource) labels() map[string]string {
	if !r.Valid() {
		return nil
	}
	return map[string]string{
		resourceOwnerLabel:  resourceOwnerValue,
		resourceJobLabel:    r.jobID,
		resourcePolicyLabel: r.policySHA256,
	}
}

func (r Resource) owns(labels map[string]string) bool {
	if !r.Valid() {
		return false
	}
	expected := r.labels()
	for key, value := range expected {
		if labels[key] != value {
			return false
		}
	}
	return true
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
	CleanupNotRequired CleanupStatus = "not_required"
	CleanupComplete    CleanupStatus = "complete"
	CleanupFailed      CleanupStatus = "failed"
)

func (s CleanupStatus) Valid() bool {
	return s == CleanupNotRequired || s == CleanupComplete || s == CleanupFailed
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
