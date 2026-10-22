package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
)

var errSecretMigration = errors.New("resume the original secret migration and its immutable checkpoints")

// SecretMigrationIntent is public deployment/source metadata, retained in the
// canonical host journal. Source decoding and private equality remain in their
// respective owners; this package never imports the legacy vault or broker.
type SecretMigrationIntent struct {
	Plan   appvalues.MigrationPlan       `json:"plan"`
	Before appvalues.ApplicationIdentity `json:"before"`
	// Native/Compose owners retain the original public catalog here. Full
	// management already owns it in the enclosing transaction's Previous.
	BeforeResources     []controlpolicy.ApplicationResource `json:"before_resources,omitempty"`
	CandidateGeneration string                              `json:"candidate_generation"`
	ExpectedPolicy      string                              `json:"expected_policy"`
	Policy              string                              `json:"policy"`
	Mode                string                              `json:"mode"`
	SourceRoot          string                              `json:"source_root"`
	IdentityFile        string                              `json:"identity_file"`
	StateRoot           string                              `json:"state_root"`
	ServiceStateRoot    string                              `json:"service_state_root"`
	RuntimeRoot         string                              `json:"runtime_root"`
	Binary              string                              `json:"binary"`
	Recipient           string                              `json:"recipient"`
	BackupPath          string                              `json:"backup_path"`
	Interrupt           bool                                `json:"interrupt"`
}

func migrationHex(value string, size int) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == size && hex.EncodeToString(b) == value
}

func (i SecretMigrationIntent) Valid() bool {
	if i.Mode == "managed-full" && len(i.BeforeResources) != 0 {
		return false
	}
	if len(i.BeforeResources) != 0 {
		before, err := controlpolicy.CanonicalApplications(i.BeforeResources, nil)
		if err != nil || !reflect.DeepEqual(before, i.BeforeResources) {
			return false
		}
	}
	if i.Plan.Version != 1 || i.Plan.Revision == 0 || i.Plan.Values < 0 || i.Plan.Values > 128*512 || i.Plan.Mappings == nil || len(i.Plan.Mappings) > 128*512+128 || len(i.Plan.Resources) == 0 ||
		!migrationHex(i.CandidateGeneration, 16) || i.CandidateGeneration == i.Before.ActiveGeneration || !migrationHex(i.Before.ActiveGeneration, 16) ||
		!migrationHex(i.Policy, 32) || !migrationHex(i.ExpectedPolicy, 32) || i.Mode != "native" && i.Mode != "compose" && i.Mode != "managed-full" ||
		i.Before.UID == 0 || i.Before.GID == 0 || i.Before.AgentUID == 0 || i.Before.MetadataGID == 0 || i.Before.UID == i.Before.AgentUID || i.Before.GID == i.Before.MetadataGID ||
		!controlpolicy.ApplicationUUID.MatchString(i.Before.RunnerID) || !strings.HasPrefix(i.Before.DeploymentID, "deployment-") || !migrationHex(strings.TrimPrefix(i.Before.DeploymentID, "deployment-"), 16) {
		return false
	}
	if i.Before.Version == 1 {
		if i.Before.PolicySHA256 != "" {
			return false
		}
	} else if i.Before.Version != 2 || i.Before.PolicySHA256 != i.ExpectedPolicy {
		return false
	}
	r, err := age.ParseX25519Recipient(i.Recipient)
	if err != nil || r.String() != i.Recipient {
		return false
	}
	for _, path := range []string{i.SourceRoot, i.IdentityFile, i.StateRoot, i.ServiceStateRoot, i.RuntimeRoot, i.Binary, i.BackupPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
			return false
		}
	}
	resources, err := controlpolicy.CanonicalApplications(i.Plan.Resources, nil)
	if err != nil || !reflect.DeepEqual(resources, i.Plan.Resources) {
		return false
	}
	targets := i.targets()
	count := 0
	sources := map[[2]string]bool{}
	for _, mapping := range i.Plan.Mappings {
		resource, found := i.resource(mapping.Resource)
		if mapping.Profile == "" || len(mapping.Profile) > 128 || mapping.Keys == nil || !found || !slices.Contains(resource.Environments, mapping.Environment) {
			return false
		}
		for _, key := range mapping.Keys {
			source := [2]string{mapping.Profile, key.Source}
			if key.Source == "" || len(key.Source) > 128 || sources[source] || !controlpolicy.ApplicationName.MatchString(key.Name) || key.Kind != "secret" && key.Kind != "variable" || resource.Keys[key.Name] != key.Kind {
				return false
			}
			sources[source] = true
			count++
		}
	}
	return count == i.Plan.Values && len(targets) == count
}

func (i SecretMigrationIntent) digest() string {
	b, _ := json.Marshal(i)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

func (i SecretMigrationIntent) resource(id string) (controlpolicy.ApplicationResource, bool) {
	for _, r := range i.Plan.Resources {
		if r.ID == id {
			return r, true
		}
	}
	return controlpolicy.ApplicationResource{}, false
}

func (i SecretMigrationIntent) targets() map[[3]string]bool {
	result := map[[3]string]bool{}
	for _, mapping := range i.Plan.Mappings {
		for _, key := range mapping.Keys {
			result[[3]string{mapping.Resource, mapping.Environment, key.Name}] = true
		}
	}
	return result
}

type MigrationArchiveCheckpoint struct {
	CipherSHA256 string `json:"cipher_sha256"`
	Bytes        int64  `json:"bytes"`
	Published    bool   `json:"published"`
}

type MigrationWriteCheckpoint struct {
	Resource    string `json:"resource"`
	Environment string `json:"env"`
	Action      string `json:"action"`
	Name        string `json:"name,omitempty"`
	Revision    string `json:"revision"`
	Request     string `json:"request"`
	Complete    bool   `json:"complete"`
}

type MigrationComparisonCheckpoint struct {
	Binding appvalues.MigrationComparisonBinding `json:"binding"`
	Matched bool                                 `json:"matched"`
}

type SecretMigrationOperation struct {
	Intent            SecretMigrationIntent           `json:"intent"`
	Archive           *MigrationArchiveCheckpoint     `json:"archive,omitempty"`
	Writes            []MigrationWriteCheckpoint      `json:"writes"`
	Comparisons       []MigrationComparisonCheckpoint `json:"comparisons"`
	RollbackRequested bool                            `json:"rollback_requested,omitempty"`
}

func MigrationOperationUUID(id string) string {
	uuid, err := MaintenanceOperationID(id)
	if err != nil {
		return ""
	}
	return uuid
}

func MigrationWriteRequest(operation string, write MigrationWriteCheckpoint) string {
	if MigrationOperationUUID(operation) == "" {
		return ""
	}
	return privateio.OperationID(MigrationOperationUUID(operation), write.Resource, write.Action+"/"+write.Environment+"/"+write.Name)
}

func migrationPhase(phase OperationPhase) int {
	for rank, candidate := range []OperationPhase{PhaseAdmitted, PhaseSnapshot, PhaseMigrate, PhaseSwitch, PhaseRestart, PhaseHealth, PhaseComplete} {
		if phase == candidate {
			return rank
		}
	}
	return -1
}

func (r OperationRecord) validSecretMigration() bool {
	p := r.SecretMigration
	if p == nil || !p.Intent.Valid() || r.PlanID != p.Intent.digest() || r.CandidateGenerationID != r.PlanID || r.ObservedHostRevision != r.PlanID || r.ActiveGenerationID != "" || r.Maintenance == nil || r.Maintenance.Interrupt != p.Intent.Interrupt || p.Writes == nil || p.Comparisons == nil {
		return false
	}
	if p.Archive != nil {
		if !migrationHex(p.Archive.CipherSHA256, 32) || p.Archive.Bytes <= 0 || p.Archive.Bytes > 129<<20 || r.RecoveryBackupID != "sha256:"+p.Archive.CipherSHA256 || r.Phase == PhaseAdmitted {
			return false
		}
	} else if r.RecoveryBackupID != "" {
		return false
	}
	rank := migrationPhase(r.Phase)
	if p.RollbackRequested && (r.State == OperationSucceeded || r.State == OperationApplying && rank >= migrationPhase(PhaseRestart)) {
		return false
	}
	if rank < 0 || rank >= migrationPhase(PhaseMigrate) && r.State != OperationRolledBack && (p.Archive == nil || !p.Archive.Published) {
		return false
	}
	requests := map[string]bool{}
	targets := p.Intent.targets()
	for _, w := range p.Writes {
		resource, found := p.Intent.resource(w.Resource)
		if !found || !slices.Contains(resource.Environments, w.Environment) || !migrationHex(w.Revision, 32) || w.Request != MigrationWriteRequest(r.ID, w) || requests[w.Request] {
			return false
		}
		switch w.Action {
		case "set_secret", "set_public":
			kind := "secret"
			if w.Action == "set_public" {
				kind = "variable"
			}
			if resource.Keys[w.Name] != kind || !targets[[3]string{w.Resource, w.Environment, w.Name}] {
				return false
			}
		case "env_create":
			if w.Environment == "common" || w.Name != "" {
				return false
			}
		case "bundle_set":
			if w.Environment != "common" || resource.Bundle == "" || w.Name != resource.Bundle {
				return false
			}
		default:
			return false
		}
		requests[w.Request] = true
	}
	if len(p.Writes) > 0 && rank < migrationPhase(PhaseMigrate) {
		return false
	}
	if len(p.Comparisons) > 0 && !p.writesComplete() {
		return false
	}
	attempts := map[string]bool{}
	for _, comparison := range p.Comparisons {
		b := comparison.Binding
		if b.Operation != MigrationOperationUUID(r.ID) || !controlpolicy.ApplicationUUID.MatchString(b.Attempt) || attempts[b.Attempt] || b.Policy != p.Intent.Policy || !controlpolicy.ApplicationUUID.MatchString(b.Runner) || !controlpolicy.ApplicationUUID.MatchString(b.Instance) || !migrationHex(b.RootID, 32) || !migrationHex(b.VaultID, 16) || !migrationHex(b.Execution, 16) || !migrationHex(b.Input, 16) || !migrationHex(b.CorrelationID, 16) || b.Revision == 0 || len(b.Names) == 0 || len(b.Names) > controlpolicy.MaxApplicationKeys {
			return false
		}
		for index, name := range b.Names {
			if !targets[[3]string{b.Resource, b.Environment, name}] || index > 0 && b.Names[index-1] >= name {
				return false
			}
		}
		attempts[b.Attempt] = true
	}
	if rank >= migrationPhase(PhaseSwitch) && r.State != OperationRolledBack && (!p.writesComplete() || !p.comparisonsComplete()) {
		return false
	}
	started, e1 := time.Parse(time.RFC3339Nano, r.StartedAt)
	updated, e2 := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if e1 != nil || e2 != nil || started.Location() != time.UTC || updated.Location() != time.UTC || updated.Before(started) {
		return false
	}
	switch r.State {
	case OperationApplying:
		return rank < migrationPhase(PhaseComplete) && r.CompletedAt == "" && r.OriginalError == "" && r.RecoveryError == "" && !r.Maintenance.Released
	case OperationSucceeded:
		return r.Phase == PhaseComplete && r.OriginalError == "" && r.RecoveryError == "" && validOperationCompletion(r.CompletedAt, updated)
	case OperationRolledBack:
		return r.Phase == PhaseComplete && r.OriginalError != "" && r.RecoveryError == "" && validOperationCompletion(r.CompletedAt, updated)
	default:
		return false
	}
}

func (p SecretMigrationOperation) writesComplete() bool {
	values := map[[3]string]bool{}
	environments := map[[2]string]bool{}
	bundles := map[string]bool{}
	for _, w := range p.Writes {
		if !w.Complete {
			return false
		}
		switch w.Action {
		case "set_secret", "set_public":
			values[[3]string{w.Resource, w.Environment, w.Name}] = true
		case "env_create":
			environments[[2]string{w.Resource, w.Environment}] = true
		case "bundle_set":
			bundles[w.Resource] = true
		}
	}
	if len(values) != p.Intent.Plan.Values {
		return false
	}
	for _, resource := range p.Intent.Plan.Resources {
		for _, env := range resource.Environments {
			if env != "common" && !environments[[2]string{resource.ID, env}] {
				return false
			}
		}
		if resource.Bundle != "" && !bundles[resource.ID] {
			return false
		}
	}
	return true
}

func (p SecretMigrationOperation) comparisonsComplete() bool {
	covered := map[[3]string]bool{}
	for _, c := range p.Comparisons {
		if c.Matched {
			for _, name := range c.Binding.Names {
				covered[[3]string{c.Binding.Resource, c.Binding.Environment, name}] = true
			}
		}
	}
	return len(covered) == p.Intent.Plan.Values
}

func detachedMigrationRecord(r OperationRecord) OperationRecord {
	b, _ := json.Marshal(r)
	var detached OperationRecord
	_ = json.Unmarshal(b, &detached)
	return detached
}

func (j *OperationJournal) BeginSecretMigration(intent SecretMigrationIntent, now time.Time) (OperationRecord, error) {
	if err := j.ready(); err != nil {
		return OperationRecord{}, err
	}
	if !intent.Valid() {
		return OperationRecord{}, errSecretMigration
	}
	for _, r := range j.records {
		pending := !r.State.Terminal() || r.Maintenance != nil && !r.Maintenance.Released
		if pending && (r.Kind != OperationSecretMigration || r.PlanID != intent.digest()) {
			return OperationRecord{}, errSecretMigration
		}
	}
	for _, r := range j.records {
		if r.Kind == OperationSecretMigration && r.PlanID == intent.digest() {
			return detachedMigrationRecord(r), nil
		}
	}
	id, err := newOperationID()
	if err != nil {
		return OperationRecord{}, err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	r := OperationRecord{ID: id, Kind: OperationSecretMigration, PlanID: intent.digest(), ObservedHostRevision: intent.digest(), CandidateGenerationID: intent.digest(), State: OperationApplying, Phase: PhaseAdmitted, StartedAt: stamp, UpdatedAt: stamp,
		Maintenance: &OperationMaintenance{Interrupt: intent.Interrupt}, SecretMigration: &SecretMigrationOperation{Intent: intent, Writes: []MigrationWriteCheckpoint{}, Comparisons: []MigrationComparisonCheckpoint{}}}
	if err := j.persist(r, false); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = detachedMigrationRecord(r)
	return detachedMigrationRecord(r), nil
}

func (j *OperationJournal) updateSecretMigration(id string, now time.Time, change func(*OperationRecord) error) (OperationRecord, error) {
	r, err := j.require(id)
	if err != nil {
		return r, err
	}
	if r.Kind != OperationSecretMigration || r.SecretMigration == nil {
		return OperationRecord{}, errSecretMigration
	}
	r = detachedMigrationRecord(r)
	if err := change(&r); err != nil {
		return OperationRecord{}, err
	}
	r.UpdatedAt, err = nextOperationTime(r, now)
	if err != nil {
		return OperationRecord{}, err
	}
	if r.State.Terminal() {
		r.CompletedAt = r.UpdatedAt
	}
	if err := j.persist(r, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = detachedMigrationRecord(r)
	return r, nil
}

// AdvanceSecretMigration accepts only consecutive verified checkpoints. Host
// code supplies the actual source fence, archive, selector and health evidence.
func (j *OperationJournal) AdvanceSecretMigration(id string, phase OperationPhase, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.SecretMigration.RollbackRequested {
			return errSecretMigration
		}
		if r.State == OperationApplying && phase == r.Phase {
			return nil
		}
		if r.State != OperationApplying || phase == PhaseComplete || migrationPhase(phase) != migrationPhase(r.Phase)+1 {
			return errSecretMigration
		}
		if phase == PhaseMigrate && (r.SecretMigration.Archive == nil || !r.SecretMigration.Archive.Published) || phase == PhaseSwitch && (!r.SecretMigration.writesComplete() || !r.SecretMigration.comparisonsComplete()) {
			return errSecretMigration
		}
		r.Phase = phase
		return nil
	})
}

func (j *OperationJournal) CheckpointMigrationArchive(id, cipher string, size int64, published bool, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.SecretMigration.RollbackRequested {
			return errSecretMigration
		}
		if r.State != OperationApplying || r.Phase != PhaseSnapshot || !migrationHex(cipher, 32) || size <= 0 || size > 129<<20 {
			return errSecretMigration
		}
		old := r.SecretMigration.Archive
		if old != nil && (old.Published && (old.CipherSHA256 != cipher || old.Bytes != size || !published) || published && (old.CipherSHA256 != cipher || old.Bytes != size)) {
			return errSecretMigration
		}
		if published && old == nil {
			return errSecretMigration
		}
		r.SecretMigration.Archive = &MigrationArchiveCheckpoint{CipherSHA256: cipher, Bytes: size, Published: published}
		r.RecoveryBackupID = "sha256:" + cipher
		return nil
	})
}

// CheckpointMigrationWrite saves the ORIGINAL revision and stable UUID before
// the first provider write. Retry it unchanged instead of rebasing to live state.
func (j *OperationJournal) CheckpointMigrationWrite(id string, write MigrationWriteCheckpoint, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.SecretMigration.RollbackRequested {
			return errSecretMigration
		}
		if r.State != OperationApplying || r.Phase != PhaseMigrate || write.Complete {
			return errSecretMigration
		}
		for _, previous := range r.SecretMigration.Writes {
			if previous.Request == write.Request {
				previous.Complete = false
				if previous != write {
					return errSecretMigration
				}
				return nil
			}
		}
		r.SecretMigration.Writes = append(r.SecretMigration.Writes, write)
		return nil
	})
}

func (j *OperationJournal) CompleteMigrationWrite(id, request string, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.SecretMigration.RollbackRequested {
			return errSecretMigration
		}
		if r.State != OperationApplying || r.Phase != PhaseMigrate {
			return errSecretMigration
		}
		for index, previous := range r.SecretMigration.Writes {
			if previous.Request == request {
				r.SecretMigration.Writes[index].Complete = true
				return nil
			}
		}
		return errSecretMigration
	})
}

func (j *OperationJournal) CheckpointMigrationComparison(id string, binding appvalues.MigrationComparisonBinding, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.SecretMigration.RollbackRequested {
			return errSecretMigration
		}
		if r.State != OperationApplying || r.Phase != PhaseMigrate || !r.SecretMigration.writesComplete() {
			return errSecretMigration
		}
		for _, previous := range r.SecretMigration.Comparisons {
			if previous.Binding.Attempt == binding.Attempt {
				if !reflect.DeepEqual(previous.Binding, binding) {
					return errSecretMigration
				}
				return nil
			}
		}
		r.SecretMigration.Comparisons = append(r.SecretMigration.Comparisons, MigrationComparisonCheckpoint{Binding: binding})
		return nil
	})
}

func (j *OperationJournal) CompleteMigrationComparison(id, attempt string, count int, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.SecretMigration.RollbackRequested {
			return errSecretMigration
		}
		if r.State != OperationApplying || r.Phase != PhaseMigrate {
			return errSecretMigration
		}
		for index, previous := range r.SecretMigration.Comparisons {
			if previous.Binding.Attempt == attempt && len(previous.Binding.Names) == count {
				r.SecretMigration.Comparisons[index].Matched = true
				return nil
			}
		}
		return errSecretMigration
	})
}

// RequestSecretMigrationRollback durably fixes the direction before any
// recovery effects. It cannot undo selection, or create another operation.
func (j *OperationJournal) RequestSecretMigrationRollback(id string, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.State == OperationRolledBack {
			return nil
		}
		if r.State != OperationApplying || migrationPhase(r.Phase) >= migrationPhase(PhaseRestart) {
			return errSecretMigration
		}
		r.SecretMigration.RollbackRequested = true
		return nil
	})
}

func (j *OperationJournal) CompleteSecretMigration(id string, restored bool, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		want := OperationSucceeded
		if restored {
			want = OperationRolledBack
		}
		if r.State.Terminal() {
			if r.State != want {
				return errSecretMigration
			}
			return nil
		}
		if restored {
			if migrationPhase(r.Phase) >= migrationPhase(PhaseRestart) {
				return errSecretMigration
			}
			r.OriginalError = "candidate was not selected; original source and service verified"
		} else if r.Phase != PhaseHealth || r.SecretMigration.RollbackRequested {
			return errSecretMigration
		}
		r.State, r.Phase = want, PhaseComplete
		return nil
	})
}

// AcknowledgeMigrationRelease follows the successful terminal checkpoint and
// actual source/Job-fence disposition. An interrupted release remains active.
func (j *OperationJournal) AcknowledgeMigrationRelease(id string, now time.Time) (OperationRecord, error) {
	return j.updateSecretMigration(id, now, func(r *OperationRecord) error {
		if r.State != OperationSucceeded && r.State != OperationRolledBack {
			return errSecretMigration
		}
		r.Maintenance.Released = true
		return nil
	})
}
