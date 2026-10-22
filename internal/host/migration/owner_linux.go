//go:build linux

package migration

import (
	"context"
	"time"

	"loki/internal/admin"
	"loki/internal/host/lifecycle"
	secret "loki/modules/secrets"
)

// SourceOwner executes source effects under the deployment owner's existing
// journal lock. It accepts an original journal identity, never a caller-supplied
// replacement intent. The source and private target run in the administrator's
// namespace; only immutable metadata is checkpointed in the host journal.
type SourceOwner struct {
	Journal *lifecycle.OperationJournal
	ID      string
}

func (o SourceOwner) current() (lifecycle.OperationRecord, error) {
	if o.Journal == nil || lifecycle.MigrationOperationUUID(o.ID) == "" {
		return lifecycle.OperationRecord{}, ErrSourceSession
	}
	r, found, err := o.Journal.Active()
	if err != nil || !found || r.ID != o.ID || r.Kind != lifecycle.OperationSecretMigration || r.SecretMigration == nil || r.State != lifecycle.OperationApplying {
		return lifecycle.OperationRecord{}, ErrSourceSession
	}
	return r, nil
}

// Backup freezes the exact source before publishing its encrypted recovery
// archive. A lost publication acknowledgement reuses the saved ciphertext proof;
// target writes remain impossible until publication is durably acknowledged.
func (o SourceOwner) Backup(ctx context.Context) error {
	r, err := o.current()
	if err != nil || r.Phase != lifecycle.PhaseAdmitted && r.Phase != lifecycle.PhaseSnapshot && r.Phase != lifecycle.PhaseMigrate {
		return ErrSourceSession
	}
	i := r.SecretMigration.Intent
	operation := lifecycle.MigrationOperationUUID(r.ID)
	source := secret.Controller{StateDirectory: i.SourceRoot}
	if err := source.BeginApplicationMigration(ctx, i.Plan.Revision, operation); err != nil {
		return err
	}
	if r.Phase == lifecycle.PhaseAdmitted {
		r, err = o.Journal.AdvanceSecretMigration(r.ID, lifecycle.PhaseSnapshot, time.Now())
		if err != nil {
			return err
		}
	}
	var previous *secret.MigrationBackupProof
	if a := r.SecretMigration.Archive; a != nil {
		previous = &secret.MigrationBackupProof{Version: 1, Operation: operation, Revision: i.Plan.Revision, Recipient: i.Recipient, CipherSHA256: a.CipherSHA256, Bytes: a.Bytes}
	}
	proof, err := source.BackupApplicationMigration(ctx, i.Plan.Revision, operation, i.Recipient, i.BackupPath, previous, func(proof secret.MigrationBackupProof) error {
		if r.Phase == lifecycle.PhaseMigrate {
			// Publication was already acknowledged. A missing backup cannot
			// be replaced with another ciphertext after target writes began.
			return ErrSourceSession
		}
		_, err := o.Journal.CheckpointMigrationArchive(r.ID, proof.CipherSHA256, proof.Bytes, false, time.Now())
		return err
	})
	if err != nil {
		return err
	}
	if r.Phase == lifecycle.PhaseMigrate {
		return nil
	}
	if _, err := o.Journal.CheckpointMigrationArchive(r.ID, proof.CipherSHA256, proof.Bytes, true, time.Now()); err != nil {
		return err
	}
	_, err = o.Journal.AdvanceSecretMigration(r.ID, lifecycle.PhaseMigrate, time.Now())
	return err
}

// Checkpoint acknowledges a finite source-helper event only after the original
// canonical host journal has durably accepted it. An event cannot select an
// operation, advance cutover or release maintenance.
func (o SourceOwner) Checkpoint(event SourceEvent) error {
	r, err := o.current()
	if err != nil || r.Phase != lifecycle.PhaseMigrate || event.Operation != lifecycle.MigrationOperationUUID(o.ID) {
		return ErrSourceSession
	}
	switch event.Type {
	case "write_prepared", "write_completed":
		if event.Write == nil || event.Binding != nil || event.Attempt != "" || event.Count != 0 || (event.Type == "write_completed") != event.Write.Complete {
			return ErrSourceSession
		}
		if event.Type == "write_prepared" {
			_, err = o.Journal.CheckpointMigrationWrite(o.ID, *event.Write, time.Now())
		} else {
			// Check the entire immutable original before accepting its completion.
			found := false
			for _, w := range r.SecretMigration.Writes {
				w.Complete = true
				found = found || w == *event.Write
			}
			if !found {
				return ErrSourceSession
			}
			_, err = o.Journal.CompleteMigrationWrite(o.ID, event.Write.Request, time.Now())
		}
	case "comparison_prepared":
		if event.Write != nil || event.Binding == nil || event.Attempt != "" || event.Count != 0 {
			return ErrSourceSession
		}
		_, err = o.Journal.CheckpointMigrationComparison(o.ID, *event.Binding, time.Now())
	case "comparison_completed":
		if event.Write != nil || event.Binding != nil || event.Attempt == "" || event.Count < 1 {
			return ErrSourceSession
		}
		_, err = o.Journal.CompleteMigrationComparison(o.ID, event.Attempt, event.Count, time.Now())
	default:
		return ErrSourceSession
	}
	return err
}

// Session reconstructs recovery exclusively from the current canonical record.
// Call Transfer only after the owned target has been manually unlocked.
func (o SourceOwner) Session(target admin.ApplicationClient, base map[string]string) (SourceSession, error) {
	r, err := o.current()
	if err != nil || r.Phase != lifecycle.PhaseMigrate || target.ExpectedUID != r.SecretMigration.Intent.Before.UID {
		return SourceSession{}, ErrSourceSession
	}
	i := r.SecretMigration.Intent
	return SourceSession{Source: secret.Controller{StateDirectory: i.SourceRoot}, Target: target, Operation: lifecycle.MigrationOperationUUID(o.ID), Policy: i.Policy, Plan: i.Plan, BaseEnvironment: base, Writes: r.SecretMigration.Writes, Checkpoint: o.Checkpoint}, nil
}
