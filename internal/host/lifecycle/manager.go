package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Snapshot struct {
	Installed *Generation   `json:"installed,omitempty"`
	Available *Generation   `json:"available,omitempty"`
	Prepared  *PreparedPlan `json:"prepared,omitempty"`
	Host      HostState     `json:"host"`
}

type Store interface {
	Snapshot(context.Context) (Snapshot, error)
	SavePrepared(context.Context, PreparedPlan) error
}

type JobInventory interface {
	ActiveJobs(context.Context) ([]string, error)
}

type ApplyOptions struct {
	InterruptActiveJobs bool `json:"interrupt_active_jobs"`
}

type ApplyRequest struct {
	Plan       PreparedPlan `json:"plan"`
	ActiveJobs []string     `json:"active_jobs,omitempty"`
	Options    ApplyOptions `json:"options"`
}

type ApplyResult struct {
	PlanID          string   `json:"plan_id"`
	InterruptedJobs []string `json:"interrupted_jobs,omitempty"`
}

type Applier interface {
	Apply(context.Context, ApplyRequest) (ApplyResult, error)
}

type BlockedJobsError struct {
	Jobs []string
}

func (e *BlockedJobsError) Error() string {
	if e == nil || len(e.Jobs) == 0 {
		return "host update is blocked by active jobs"
	}
	return fmt.Sprintf("host update is blocked by active jobs: %s", strings.Join(e.Jobs, ", "))
}

type Manager struct {
	Store   Store
	Jobs    JobInventory
	Applier Applier
	Now     func() time.Time
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m Manager) Status(ctx context.Context) (UpdateStatus, error) {
	if m.Store == nil {
		return UpdateStatus{}, errors.New("host lifecycle store is not configured")
	}
	snapshot, err := m.Store.Snapshot(ctx)
	if err != nil {
		return UpdateStatus{}, err
	}
	return Status(snapshot.Installed, snapshot.Available, snapshot.Prepared, snapshot.Host)
}

func (m Manager) Prepare(ctx context.Context) (PreparedPlan, error) {
	if m.Store == nil {
		return PreparedPlan{}, errors.New("host lifecycle store is not configured")
	}
	snapshot, err := m.Store.Snapshot(ctx)
	if err != nil {
		return PreparedPlan{}, err
	}
	if snapshot.Available == nil {
		return PreparedPlan{}, errors.New("no available release generation is prepared for inspection")
	}
	plan, err := Prepare(snapshot.Installed, *snapshot.Available, snapshot.Host, m.now())
	if err != nil {
		return PreparedPlan{}, err
	}
	if err = m.Store.SavePrepared(ctx, plan); err != nil {
		return PreparedPlan{}, err
	}
	return plan, nil
}

func (m Manager) Apply(ctx context.Context, options ApplyOptions) (ApplyResult, error) {
	if m.Store == nil {
		return ApplyResult{}, errors.New("host lifecycle store is not configured")
	}
	snapshot, err := m.Store.Snapshot(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if snapshot.Available == nil || snapshot.Prepared == nil {
		return ApplyResult{}, errors.New("host update must be prepared before apply")
	}
	if !snapshot.Prepared.Valid() {
		return ApplyResult{}, errors.New("prepared host update plan is invalid")
	}
	expected, err := Prepare(snapshot.Installed, *snapshot.Available, snapshot.Host, m.now())
	if err != nil {
		return ApplyResult{}, err
	}
	if expected.ID != snapshot.Prepared.ID ||
		expected.ObservedHostRevision != snapshot.Prepared.ObservedHostRevision ||
		expected.ActiveGenerationID != snapshot.Prepared.ActiveGenerationID ||
		expected.CandidateGenerationID != snapshot.Prepared.CandidateGenerationID {
		return ApplyResult{}, errors.New("prepared host update plan is stale; run prepare again")
	}

	activeJobs, err := m.activeJobs(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(activeJobs) != 0 && !options.InterruptActiveJobs {
		return ApplyResult{}, &BlockedJobsError{Jobs: activeJobs}
	}
	if m.Applier == nil {
		return ApplyResult{}, errors.New("host apply transaction engine is not configured")
	}
	request := ApplyRequest{
		Plan:       *snapshot.Prepared,
		ActiveJobs: activeJobs,
		Options:    options,
	}
	result, err := m.Applier.Apply(ctx, request)
	if err != nil {
		return ApplyResult{}, err
	}
	if result.PlanID != snapshot.Prepared.ID {
		return ApplyResult{}, errors.New("host apply engine returned a mismatched plan identity")
	}
	return result, nil
}

func (m Manager) activeJobs(ctx context.Context) ([]string, error) {
	if m.Jobs == nil {
		return nil, errors.New("host lifecycle job inventory is not configured")
	}
	jobs, err := m.Jobs.ActiveJobs(ctx)
	if err != nil {
		return nil, err
	}
	result := append([]string(nil), jobs...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
		if result[index] == "" || len(result[index]) > 128 || strings.ContainsAny(result[index], "\r\n\x00") {
			return nil, errors.New("host lifecycle job inventory returned an invalid job id")
		}
	}
	slices.Sort(result)
	result = slices.Compact(result)
	return result, nil
}
