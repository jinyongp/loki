//go:build linux

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	"loki/internal/host/lifecycle"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/tools"
)

type fullSecretMigrationRecord = lifecycle.OperationRecord

func fullMigrationPhase(record lifecycle.OperationRecord) tools.Phase {
	if record.Maintenance != nil && record.Maintenance.Released {
		if record.State == lifecycle.OperationSucceeded {
			return tools.Committed
		}
		if record.State == lifecycle.OperationRolledBack {
			return tools.Aborted
		}
	}
	return tools.Staged
}

func (t transaction) validateSecretMigration() error {
	r := t.SecretMigration
	if r == nil || !r.Valid() || r.Kind != lifecycle.OperationSecretMigration || r.SecretMigration == nil || r.SecretMigration.Intent.Mode != "managed-full" || t.ID != "op-"+r.ID || t.Phase != fullMigrationPhase(*r) {
		return fmt.Errorf("invalid canonical full secret migration transaction")
	}
	a, _ := json.Marshal(t.Previous.Config)
	b, _ := json.Marshal(t.Candidate.Config)
	if !bytes.Equal(a, b) || !reflect.DeepEqual(t.Candidate.ApplicationResources, r.SecretMigration.Intent.Plan.Resources) {
		return fmt.Errorf("secret migration cannot change tools, host or activation configuration")
	}
	return nil
}

type fullMigrationRecordStore struct {
	store Store
	held  *bool
}

func (s *fullMigrationRecordStore) Identity() string {
	return filepath.Join(s.store.Root, "transaction.json")
}

func (s *fullMigrationRecordStore) Locked() bool { return s != nil && s.held != nil && *s.held }
func (s *fullMigrationRecordStore) Load() ([]lifecycle.OperationRecord, error) {
	if !s.Locked() {
		return nil, fmt.Errorf("full migration owner lock is not held")
	}
	t, err := s.store.readTransaction()
	if err != nil {
		return nil, err
	}
	if t == nil || t.Action != "secret_migration" {
		return nil, nil
	}
	return []lifecycle.OperationRecord{*t.SecretMigration}, nil
}

func (s *fullMigrationRecordStore) Publish(record lifecycle.OperationRecord, overwrite bool) error {
	if !s.Locked() || record.Kind != lifecycle.OperationSecretMigration || record.SecretMigration == nil || record.SecretMigration.Intent.Mode != "managed-full" {
		return fmt.Errorf("full migration publication requires its original owner")
	}
	previous, err := s.store.readTransaction()
	if err != nil {
		return err
	}
	var t transaction
	if overwrite {
		if previous == nil || previous.Action != "secret_migration" || previous.SecretMigration.ID != record.ID || previous.SecretMigration.PlanID != record.PlanID {
			return fmt.Errorf("full migration publication changed original operation")
		}
		t = *previous
	} else {
		if previous != nil && previous.Phase != tools.Committed && previous.Phase != tools.Aborted {
			return fmt.Errorf("complete the original full transaction before migration")
		}
		before, err := s.store.Load()
		if err != nil {
			return err
		}
		if before.Config.Mode != tools.Full {
			return fmt.Errorf("secret migration requires the installed full deployment")
		}
		c, contract, err := s.store.fullApplicationInputs()
		if err != nil {
			return err
		}
		oldPolicy, err := hostpolicy.CompileApplications(c, contract, before.ApplicationResources)
		if err != nil || oldPolicy.Digest() != record.SecretMigration.Intent.ExpectedPolicy {
			return fmt.Errorf("full migration source policy changed")
		}
		newPolicy, err := hostpolicy.CompileApplications(c, contract, record.SecretMigration.Intent.Plan.Resources)
		if err != nil || newPolicy.Digest() != record.SecretMigration.Intent.Policy {
			return fmt.Errorf("full migration target policy differs from installed inputs")
		}
		candidate := before
		candidate.ApplicationResources = record.SecretMigration.Intent.Plan.Resources
		t = transaction{Schema: 1, ID: "op-" + record.ID, Action: "secret_migration", Previous: before, Candidate: candidate}
	}
	t.SecretMigration = &record
	t.Phase = fullMigrationPhase(record)
	if err := t.validate(); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(s.store.Root, "transaction.json"), t)
}

// WithSecretMigrationJournal uses the EXISTING full transaction and Store.Lock.
// Migration progress is not copied into another file or another authority.
// The journal becomes unusable as soon as this callback releases the lock.
func (s Store) WithSecretMigrationJournal(ctx context.Context, options lifecycle.OperationJournalOptions, use func(*lifecycle.OperationJournal) error) error {
	if use == nil {
		return fmt.Errorf("secret migration callback is required")
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	held := true
	defer func() { held = false; unlock() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.mutableMigration(true); err != nil {
		return err
	}
	j, err := lifecycle.OpenOperationJournalStore(&fullMigrationRecordStore{store: s, held: &held}, options)
	if err != nil {
		return err
	}
	return use(j)
}

func (s Store) requireNoPendingSecretMigration() error {
	t, err := s.readTransaction()
	if err != nil {
		return err
	}
	if t != nil && t.Action == "secret_migration" && t.Phase != tools.Committed && t.Phase != tools.Aborted {
		return fmt.Errorf("secret migration %s requires loki host secret-migration recover", t.SecretMigration.ID)
	}
	return nil
}
