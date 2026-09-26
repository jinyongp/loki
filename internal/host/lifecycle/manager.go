package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type AvailableReleaseMetadata struct {
	GenerationID       string `json:"generation_id"`
	DockerMin          string `json:"docker_min"`
	ComposeMin         string `json:"compose_min"`
	ReleaseNotesPath   string `json:"release_notes_path"`
	ReleaseNotesLength int64  `json:"release_notes_length"`
	ReleaseNotesSHA256 string `json:"release_notes_sha256"`
	ReleaseNotes       string `json:"release_notes"`
}

func (m AvailableReleaseMetadata) Valid() bool {
	notesSum := sha256.Sum256([]byte(m.ReleaseNotes))
	return digestPattern.MatchString(m.GenerationID) &&
		m.DockerMin != "" && m.DockerMin == strings.TrimSpace(m.DockerMin) &&
		m.ComposeMin != "" && m.ComposeMin == strings.TrimSpace(m.ComposeMin) &&
		m.ReleaseNotesPath != "" && m.ReleaseNotesPath == strings.TrimSpace(m.ReleaseNotesPath) &&
		strings.HasPrefix(m.ReleaseNotesPath, "releases/") && !strings.ContainsAny(m.ReleaseNotesPath, "\\\x00") &&
		m.ReleaseNotesLength > 0 && m.ReleaseNotesLength == int64(len(m.ReleaseNotes)) &&
		digestPattern.MatchString("sha256:"+m.ReleaseNotesSHA256) &&
		hex.EncodeToString(notesSum[:]) == m.ReleaseNotesSHA256 &&
		strings.TrimSpace(m.ReleaseNotes) != "" && len(m.ReleaseNotes) <= 1<<20 && !strings.ContainsRune(m.ReleaseNotes, 0)
}

type Snapshot struct {
	Installed         *Generation               `json:"installed,omitempty"`
	Available         *Generation               `json:"available,omitempty"`
	AvailableMetadata *AvailableReleaseMetadata `json:"available_metadata,omitempty"`
	Prepared          *PreparedPlan             `json:"prepared,omitempty"`
	Installation      *InstallationState        `json:"installation,omitempty"`
	Host              HostState                 `json:"host"`
}

type Store interface {
	Snapshot(context.Context) (Snapshot, error)
	SavePrepared(context.Context, PreparedPlan) error
}

type JobInventory interface {
	ActiveJobs(context.Context) ([]string, error)
}

type MutationOptions struct {
	InterruptActiveJobs bool `json:"interrupt_active_jobs"`
}

type ApplyOptions = MutationOptions

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

type Maintainer interface {
	Backup(context.Context) (BackupRecord, error)
	Restore(context.Context, string) error
	Rollback(context.Context) error
	Uninstall(context.Context) error
	SetComponent(context.Context, string, bool) error
	SetIngressHosts(context.Context, []string) error
}

type BlockedJobsError struct {
	Jobs []string
}

func (e *BlockedJobsError) Error() string {
	if e == nil || len(e.Jobs) == 0 {
		return "host lifecycle operation is blocked by active jobs"
	}
	return fmt.Sprintf("host lifecycle operation is blocked by active jobs: %s", strings.Join(e.Jobs, ", "))
}

type Manager struct {
	Store      Store
	Jobs       JobInventory
	Applier    Applier
	Maintainer Maintainer
	Now        func() time.Time
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
	status, err := Status(snapshot.Installed, snapshot.Available, snapshot.Prepared, snapshot.Host)
	if err != nil {
		return UpdateStatus{}, err
	}
	status.AvailableMetadata = snapshot.AvailableMetadata
	return status, nil
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

	activeJobs, err := m.mutationJobs(ctx, options)
	if err != nil {
		return ApplyResult{}, err
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

func (m Manager) Backup(ctx context.Context, options MutationOptions) (BackupRecord, error) {
	if _, err := m.mutationJobs(ctx, options); err != nil {
		return BackupRecord{}, err
	}
	if m.Maintainer == nil {
		return BackupRecord{}, errors.New("host lifecycle maintenance engine is not configured")
	}
	return m.Maintainer.Backup(ctx)
}

func (m Manager) Restore(ctx context.Context, backupID string, options MutationOptions) error {
	if _, err := m.mutationJobs(ctx, options); err != nil {
		return err
	}
	if m.Maintainer == nil {
		return errors.New("host lifecycle maintenance engine is not configured")
	}
	return m.Maintainer.Restore(ctx, backupID)
}

func (m Manager) Rollback(ctx context.Context, options MutationOptions) error {
	if _, err := m.mutationJobs(ctx, options); err != nil {
		return err
	}
	if m.Maintainer == nil {
		return errors.New("host lifecycle maintenance engine is not configured")
	}
	return m.Maintainer.Rollback(ctx)
}

func (m Manager) Uninstall(ctx context.Context, options MutationOptions) error {
	if _, err := m.mutationJobs(ctx, options); err != nil {
		return err
	}
	if m.Maintainer == nil {
		return errors.New("host lifecycle maintenance engine is not configured")
	}
	return m.Maintainer.Uninstall(ctx)
}

func (m Manager) SetComponent(ctx context.Context, name string, enabled bool, options MutationOptions) error {
	if _, err := m.mutationJobs(ctx, options); err != nil {
		return err
	}
	if m.Maintainer == nil {
		return errors.New("host lifecycle maintenance engine is not configured")
	}
	return m.Maintainer.SetComponent(ctx, name, enabled)
}

func (m Manager) SetIngressHosts(ctx context.Context, hosts []string, options MutationOptions) error {
	if _, err := m.mutationJobs(ctx, options); err != nil {
		return err
	}
	if m.Maintainer == nil {
		return errors.New("host lifecycle maintenance engine is not configured")
	}
	return m.Maintainer.SetIngressHosts(ctx, hosts)
}

func (m Manager) mutationJobs(ctx context.Context, options MutationOptions) ([]string, error) {
	activeJobs, err := m.activeJobs(ctx)
	if err != nil {
		return nil, err
	}
	if len(activeJobs) != 0 && !options.InterruptActiveJobs {
		return nil, &BlockedJobsError{Jobs: activeJobs}
	}
	return activeJobs, nil
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
