package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"filippo.io/age"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
)

var ErrApplicationBackup = errors.New("application backup requires its original locked host operation and encrypted logical artifacts")

// ApplicationBackupIntent retains public scope before closing the app write
// gate. The recipient belongs to the operator; a live key session, private
// identity, task root or value can never appear in the canonical host record.
type ApplicationBackupIntent struct {
	Version   int                                 `json:"version"`
	Operation string                              `json:"operation_id"`
	Identity  appvalues.ApplicationIdentity       `json:"identity"`
	Policy    string                              `json:"policy"`
	Resources []controlpolicy.ApplicationResource `json:"resources"`
	Recipient string                              `json:"recipient"`
	Directory string                              `json:"directory"`
}

type ApplicationBackupCheckpoint struct {
	Intent  ApplicationBackupIntent            `json:"intent"`
	Exports []appvalues.ApplicationExportProof `json:"exports"`
}

func (i ApplicationBackupIntent) Valid() bool {
	identity := i.Identity
	if i.Version != 1 || !controlpolicy.ApplicationUUID.MatchString(i.Operation) || identity.Version != 2 || identity.PolicySHA256 != i.Policy || !migrationHex(i.Policy, 32) || !migrationHex(identity.ActiveGeneration, 16) || !strings.HasPrefix(identity.DeploymentID, "deployment-") || !migrationHex(strings.TrimPrefix(identity.DeploymentID, "deployment-"), 16) || !controlpolicy.ApplicationUUID.MatchString(identity.RunnerID) || identity.UID == 0 || identity.GID == 0 || identity.AgentUID == 0 || identity.MetadataGID == 0 || identity.UID == identity.AgentUID || identity.GID == identity.MetadataGID || !filepath.IsAbs(i.Directory) || filepath.Clean(i.Directory) != i.Directory || i.Directory == "/" || len(i.Directory) > 4096 || strings.ContainsAny(i.Directory, "\x00\r\n") || i.Resources == nil {
		return false
	}
	r, err := age.ParseX25519Recipient(i.Recipient)
	if err != nil || r.String() != i.Recipient {
		return false
	}
	canonical, err := controlpolicy.CanonicalApplications(i.Resources, nil)
	return err == nil && reflect.DeepEqual(canonical, i.Resources)
}

func (b ApplicationBackupCheckpoint) Valid() bool {
	if !b.Intent.Valid() || b.Exports == nil || len(b.Exports) > len(b.Intent.Resources) {
		return false
	}
	allowed := map[string]bool{}
	for _, r := range b.Intent.Resources {
		allowed[r.ID] = true
	}
	for _, p := range b.Exports {
		if !allowed[p.Resource] || p.Version != 1 || p.Operation != b.Intent.Operation || p.Policy != b.Intent.Policy || p.Recipient != b.Intent.Recipient || !migrationHex(p.Revision, 32) || !migrationHex(p.CipherSHA256, 32) || p.Bytes < 1 || p.Bytes > appvalues.MaxApplicationArchive {
			return false
		}
		delete(allowed, p.Resource)
	}
	return true
}

func (b ApplicationBackupCheckpoint) Complete() bool {
	return b.Valid() && len(b.Exports) == len(b.Intent.Resources)
}

type hostOperationKey struct{}
type hostOperationScope struct {
	journal *OperationJournal
	id      string
}

func hostOperationContext(ctx context.Context, j *OperationJournal, id string) context.Context {
	return context.WithValue(ctx, hostOperationKey{}, hostOperationScope{j, id})
}

// CurrentHostOperation exposes only the capability carried by the live
// transaction engine. An operation UUID supplied in JSON is insufficient.
// Journal methods recheck that the original kernel/store lock is still held.
func CurrentHostOperation(ctx context.Context) (OperationRecord, *OperationJournal, error) {
	scope, ok := ctx.Value(hostOperationKey{}).(hostOperationScope)
	if !ok || scope.journal == nil || scope.journal.ready() != nil {
		return OperationRecord{}, nil, ErrApplicationBackup
	}
	r, err := scope.journal.require(scope.id)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	return detachedMigrationRecord(r), scope.journal, nil
}

func (j *OperationJournal) HoldsOperationRoot(root string) bool {
	return j != nil && j.store == nil && j.root == root && j.ready() == nil
}

func (j *OperationJournal) BeginApplicationBackup(id string, intent ApplicationBackupIntent, now time.Time) (OperationRecord, error) {
	r, err := j.require(id)
	if err != nil {
		return r, err
	}
	if !intent.Valid() || intent.Operation != MigrationOperationUUID(id) || r.Maintenance == nil || r.Maintenance.Released || r.State != OperationApplying || r.Phase != PhaseAdmitted || r.Kind == OperationSecretMigration || r.Kind == OperationNativeSwitch {
		return OperationRecord{}, ErrApplicationBackup
	}
	if r.ApplicationBackup != nil {
		if !reflect.DeepEqual(r.ApplicationBackup.Intent, intent) {
			return OperationRecord{}, ErrApplicationBackup
		}
		return detachedMigrationRecord(r), nil
	}
	r.ApplicationBackup = &ApplicationBackupCheckpoint{Intent: intent, Exports: []appvalues.ApplicationExportProof{}}
	r.UpdatedAt, err = nextOperationTime(r, now)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := j.persist(r, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = detachedMigrationRecord(r)
	return detachedMigrationRecord(r), nil
}

func (j *OperationJournal) CheckpointApplicationExport(id string, proof appvalues.ApplicationExportProof, now time.Time) (OperationRecord, error) {
	r, err := j.require(id)
	if err != nil {
		return r, err
	}
	if r.ApplicationBackup == nil || r.State != OperationApplying || r.Phase != PhaseAdmitted {
		return OperationRecord{}, ErrApplicationBackup
	}
	r = detachedMigrationRecord(r)
	for _, previous := range r.ApplicationBackup.Exports {
		if previous.Resource == proof.Resource {
			if previous != proof {
				return OperationRecord{}, ErrApplicationBackup
			}
			return r, nil
		}
	}
	r.ApplicationBackup.Exports = append(r.ApplicationBackup.Exports, proof)
	if !r.ApplicationBackup.Valid() {
		return OperationRecord{}, ErrApplicationBackup
	}
	r.UpdatedAt, err = nextOperationTime(r, now)
	if err != nil {
		return OperationRecord{}, err
	}
	if err := j.persist(r, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = detachedMigrationRecord(r)
	return r, nil
}

// MarshalApplicationBackup keeps a completed immutable manifest independent
// from any later journal update. It contains ciphertext integrity metadata only.
func MarshalApplicationBackup(b ApplicationBackupCheckpoint) ([]byte, error) {
	if !b.Complete() {
		return nil, ErrApplicationBackup
	}
	return json.Marshal(b)
}
