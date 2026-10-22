package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"time"

	controlpolicy "loki/internal/control/policy"
	hostpolicy "loki/internal/host/policy"
)

// NativeSwitchIntent describes an immutable, already installed release. This
// operation changes service links/layouts only; it must never claim a volume
// snapshot or a data migration. Data-changing operations use the snapshot engine.
type NativeSwitchIntent struct {
	SourceRelease string                   `json:"source_release,omitempty"`
	TargetRelease string                   `json:"target_release"`
	SourceReceipt string                   `json:"source_receipt,omitempty"`
	TargetReceipt string                   `json:"target_receipt"`
	Interrupt     bool                     `json:"interrupt"`
	Application   *NativeApplicationChange `json:"application,omitempty"`
}

// Both catalogs are public host metadata. They are retained in the same
// operation as the link change so a failed health check restores exact grants.
type NativeApplicationChange struct {
	Before []controlpolicy.ApplicationResource `json:"before"`
	Plan   hostpolicy.ApplicationPlan          `json:"plan"`
}

var nativeReleasePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (n NativeSwitchIntent) Valid() bool {
	if n.Application != nil {
		change := n.Application
		if change.Plan.Version != 1 || change.Plan.Resources == nil || len(change.Plan.Policy) != 64 || len(change.Plan.ExpectedPolicy) != 64 {
			return false
		}
		if _, err := hex.DecodeString(change.Plan.Policy); err != nil {
			return false
		}
		if _, err := hex.DecodeString(change.Plan.ExpectedPolicy); err != nil {
			return false
		}
		if _, err := controlpolicy.CanonicalApplications(change.Before, nil); err != nil {
			return false
		}
		if _, err := controlpolicy.CanonicalApplications(change.Plan.Resources, nil); err != nil {
			return false
		}
	}
	return nativeReleasePattern.MatchString(n.TargetRelease) && n.TargetRelease != "." && n.TargetRelease != ".." &&
		digestPattern.MatchString(n.TargetReceipt) && (n.SourceRelease == "" && n.SourceReceipt == "" ||
		nativeReleasePattern.MatchString(n.SourceRelease) && n.SourceRelease != "." && n.SourceRelease != ".." && digestPattern.MatchString(n.SourceReceipt))
}

func (n NativeSwitchIntent) digest() string {
	raw, _ := json.Marshal(n)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r OperationRecord) validNativeSwitch() bool {
	n := r.NativeSwitch
	if n == nil || !n.Valid() || r.PlanID != n.digest() || r.CandidateGenerationID != n.TargetReceipt ||
		r.ActiveGenerationID != n.SourceReceipt || r.ObservedHostRevision != n.digest() || r.RecoveryBackupID != "" ||
		r.Maintenance == nil || r.Maintenance.Interrupt != n.Interrupt {
		return false
	}
	started, e1 := time.Parse(time.RFC3339Nano, r.StartedAt)
	updated, e2 := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if e1 != nil || e2 != nil || started.Location() != time.UTC || updated.Location() != time.UTC || updated.Before(started) {
		return false
	}
	switch r.State {
	case OperationApplying:
		return r.Phase == PhaseAdmitted && r.CompletedAt == "" && r.OriginalError == "" && r.RecoveryError == "" && !r.Maintenance.Released
	case OperationSucceeded:
		return r.Phase == PhaseComplete && r.OriginalError == "" && r.RecoveryError == "" && validOperationCompletion(r.CompletedAt, updated)
	case OperationRolledBack:
		return r.Phase == PhaseComplete && r.OriginalError != "" && r.RecoveryError == "" && validOperationCompletion(r.CompletedAt, updated)
	default:
		return false
	}
}

// BeginNativeSwitch resumes the exact original intent after an uncertain
// response. A terminal operation with a pending release must be acknowledged
// before admitting another mutation, even if the shell already changed links.
func (j *OperationJournal) BeginNativeSwitch(intent NativeSwitchIntent, now time.Time) (OperationRecord, error) {
	if err := j.ready(); err != nil {
		return OperationRecord{}, err
	}
	if !intent.Valid() {
		return OperationRecord{}, errors.New("invalid native release intent")
	}
	for _, r := range j.records {
		pending := !r.State.Terminal() || r.Maintenance != nil && !r.Maintenance.Released
		if !pending {
			continue
		}
		if r.Kind != OperationNativeSwitch || r.NativeSwitch.TargetRelease != intent.TargetRelease || r.NativeSwitch.TargetReceipt != intent.TargetReceipt || r.NativeSwitch.Interrupt != intent.Interrupt || !reflect.DeepEqual(r.NativeSwitch.Application, intent.Application) ||
			!(intent.SourceRelease == r.NativeSwitch.SourceRelease && intent.SourceReceipt == r.NativeSwitch.SourceReceipt ||
				intent.SourceRelease == r.NativeSwitch.TargetRelease && intent.SourceReceipt == r.NativeSwitch.TargetReceipt) {
			return OperationRecord{}, errors.New("resume the original native release operation and interruption choice")
		}
		return r, nil
	}
	id, err := newOperationID()
	if err != nil {
		return OperationRecord{}, err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	r := OperationRecord{ID: id, Kind: OperationNativeSwitch, PlanID: intent.digest(), ObservedHostRevision: intent.digest(),
		ActiveGenerationID: intent.SourceReceipt, CandidateGenerationID: intent.TargetReceipt, State: OperationApplying,
		Phase: PhaseAdmitted, StartedAt: stamp, UpdatedAt: stamp, NativeSwitch: &intent, Maintenance: &OperationMaintenance{Interrupt: intent.Interrupt}}
	if err := j.persist(r, false); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = r
	return r, nil
}

// CompleteNativeSwitch is called only after the selected release passes its
// actual service health check. Persisting this precedes launcher fence release.
func (j *OperationJournal) CompleteNativeSwitch(id string, restored bool, now time.Time) (OperationRecord, error) {
	r, err := j.require(id)
	if err != nil {
		return r, err
	}
	if r.NativeSwitch == nil || r.Kind != OperationNativeSwitch {
		return r, errors.New("not a native release operation")
	}
	want := OperationSucceeded
	if restored {
		want = OperationRolledBack
	}
	if r.State.Terminal() {
		if r.State != want {
			return r, errors.New("native release completion changed")
		}
		return r, nil
	}
	r.UpdatedAt, err = nextOperationTime(r, now)
	if err != nil {
		return r, err
	}
	r.State, r.Phase, r.CompletedAt = want, PhaseComplete, r.UpdatedAt
	if restored {
		r.OriginalError = "candidate activation did not complete; original release restored"
	}
	if err := j.persist(r, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = r
	return r, nil
}

func (j *OperationJournal) AcknowledgeNativeRelease(id string) error {
	r, err := j.require(id)
	if err != nil {
		return err
	}
	if r.NativeSwitch == nil || r.Maintenance == nil || r.State != OperationSucceeded && r.State != OperationRolledBack {
		return errors.New("native release health checkpoint is incomplete")
	}
	m := *r.Maintenance
	m.Released = true
	r.Maintenance = &m
	if err := j.persist(r, true); err != nil {
		return err
	}
	j.records[id] = r
	return nil
}
