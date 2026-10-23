package lifecycle

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
)

// TransactionMaintenance is implemented by a deployment adapter that owns the
// launcher channel. Begin must finish exact Job cleanup before returning. End
// may release only the same operation after verifying cleanup again. Both are
// replay-safe; neither method may recreate an OCI execution.
type TransactionMaintenance interface {
	BeginJobMaintenance(context.Context, string, bool) error
	EndJobMaintenance(context.Context, string) error
}

type maintenanceInterruptionKey struct{}

// Carry the manager's explicit choice only until it is durably recorded in the
// host journal. Recovery reads the record, never a later caller's context.
func maintenanceContext(ctx context.Context, options MutationOptions) context.Context {
	return context.WithValue(ctx, maintenanceInterruptionKey{}, options.InterruptActiveJobs)
}

// MaintenanceOperationID is a lossless representation of the host journal ID,
// not a new retry identity. An uncertain response always uses this same UUID.
func MaintenanceOperationID(id string) (string, error) {
	if len(id) != 32 || id != strings.ToLower(id) {
		return "", errors.New("invalid host operation identity")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", errors.New("invalid host operation identity")
	}
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:], nil
}

func (j *OperationJournal) requireMaintenance(id string, interrupt bool) (OperationRecord, error) {
	record, err := j.require(id)
	if err != nil {
		return OperationRecord{}, err
	}
	if record.Maintenance != nil {
		if record.Maintenance.Interrupt != interrupt {
			return OperationRecord{}, errors.New("host interruption choice changed")
		}
		return record, nil
	}
	if record.State != OperationRecovering && (record.State != OperationApplying || record.Phase != PhaseAdmitted) {
		return OperationRecord{}, errors.New("maintenance must precede the host snapshot")
	}
	record.Maintenance = &OperationMaintenance{Interrupt: interrupt}
	if err := j.persist(record, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (e *TransactionEngine) beginMaintenance(ctx context.Context, journal *OperationJournal, record OperationRecord, interrupt bool) error {
	guard, ok := e.Backend.(TransactionMaintenance)
	if !ok {
		if record.Maintenance != nil {
			return errors.New("deployment adapter cannot honor the recorded Job fence")
		}
		return nil
	}
	record, err := journal.requireMaintenance(record.ID, interrupt)
	if err != nil {
		return err
	}
	id, err := MaintenanceOperationID(record.ID)
	if err != nil {
		return err
	}
	return guard.BeginJobMaintenance(hostOperationContext(ctx, journal, record.ID), id, record.Maintenance.Interrupt)
}

func (e *TransactionEngine) releaseMaintenance(ctx context.Context, journal *OperationJournal, record OperationRecord) error {
	if record.Maintenance == nil || record.Maintenance.Released {
		return nil
	}
	if record.State != OperationSucceeded && record.State != OperationRolledBack {
		return errors.New("host recovery has not completed; Job admission remains fenced")
	}
	guard, ok := e.Backend.(TransactionMaintenance)
	if !ok {
		return errors.New("deployment adapter cannot release the recorded Job fence")
	}
	id, err := MaintenanceOperationID(record.ID)
	if err != nil {
		return err
	}
	if err := guard.EndJobMaintenance(hostOperationContext(ctx, journal, record.ID), id); err != nil {
		return err
	}
	maintenance := *record.Maintenance
	maintenance.Released = true
	record.Maintenance = &maintenance
	if err := journal.persist(record, true); err != nil {
		return err
	}
	journal.records[record.ID] = record
	return nil
}

func (e *TransactionEngine) markSucceeded(ctx context.Context, journal *OperationJournal, id string) (OperationRecord, error) {
	record, err := journal.MarkSucceeded(id, e.now())
	if err != nil {
		return record, err
	}
	return record, e.releaseMaintenance(ctx, journal, record)
}

func (e *TransactionEngine) markRolledBack(ctx context.Context, journal *OperationJournal, id string) (OperationRecord, error) {
	record, err := journal.MarkRolledBack(id, e.now())
	if err != nil {
		return record, err
	}
	return record, e.releaseMaintenance(ctx, journal, record)
}
