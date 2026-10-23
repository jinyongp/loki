package jobs

import (
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"

	"loki/internal/platform/safeio"
)

// PreserveRequestHistory merges the latest root-lifetime admission history into
// an offline restore STAGE. Both journals must retain their existing writer
// locks, with all Jobs stopped and cleaned. It restores no execution record,
// protected input, receiver/session or output. Publish the stage only after
// success; an interrupted merge must be retried before activation.
func (j *Journal) PreserveRequestHistory(latest *Journal) error {
	return j.mergeRequestHistory(latest, true)
}

// MergeHistoricalRequestHistory imports only missing monotonic request
// tombstones into the retained current journal. The current maintenance owner
// stays authoritative; no historical execution record is activated or copied.
func (j *Journal) MergeHistoricalRequestHistory(older *Journal) error {
	return j.mergeRequestHistory(older, false)
}

func (j *Journal) mergeRequestHistory(latest *Journal, adoptMaintenance bool) error {
	if j == nil || latest == nil || j == latest || j.dir == latest.dir {
		return errors.New("history preservation requires separate current and staged journals")
	}
	first, second := j, latest
	if first.dir > second.dir {
		first, second = second, first
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	second.mu.Lock()
	defer second.mu.Unlock()
	for _, journal := range []*Journal{j, latest} {
		if journal.closed || journal.lockFile == nil {
			return errors.New("both history owners must remain locked")
		}
		for _, record := range journal.records {
			if record.State != StateTerminal || record.Result == nil || record.Result.Cleanup != CleanupComplete && record.Result.Cleanup != CleanupNotRequired {
				return ErrJobsBusy
			}
		}
	}
	spent := maps.Clone(j.spent)
	maintenance := maps.Clone(j.maintenanceRequests)
	for id, incoming := range latest.spent {
		if previous, found := spent[id]; found && previous != incoming {
			return ErrReplayConflict
		}
		spent[id] = incoming
	}
	for id, incoming := range latest.maintenanceRequests {
		if previous, found := maintenance[id]; found {
			if previous.Interrupt != incoming.Interrupt {
				return ErrReplayConflict
			}
			// Completion dominates an older active backup in either input.
			incoming.Active = previous.Active && incoming.Active
		}
		maintenance[id] = incoming
	}
	var budget int64
	for _, entry := range spent {
		raw, err := json.Marshal(entry)
		if err != nil || !entry.valid() {
			return errors.New("invalid protected history")
		}
		budget += int64(len(raw))
	}
	for _, entry := range maintenance {
		raw, err := json.Marshal(entry)
		if err != nil || !validMaintenance(entry) {
			return errors.New("invalid maintenance history")
		}
		budget += maintenanceRequestBudget(raw, entry.Active)
	}
	if budget > protectedLedgerBudget {
		return ErrJournalCapacity
	}
	// Reconcile before any stage write, including multiple pending operations.
	check := &Journal{maintenance: j.maintenance, maintenanceRequests: maintenance}
	if adoptMaintenance && latest.maintenance != nil {
		copy := *latest.maintenance
		check.maintenance = &copy
	}
	if err := check.reconcileMaintenanceRequests(); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(spent)) {
		entry := spent[id]
		if previous, found := j.spent[id]; found && previous == entry {
			continue
		}
		raw, _ := json.Marshal(entry)
		if err := safeio.PublishPrivate(filepath.Join(j.dir, "requests", id+".json"), raw, true); err != nil {
			return err
		}
	}
	for _, id := range slices.Sorted(maps.Keys(maintenance)) {
		entry := maintenance[id]
		if previous, found := j.maintenanceRequests[id]; found && previous == entry {
			continue
		}
		raw, _ := json.Marshal(entry)
		if err := safeio.PublishPrivate(filepath.Join(j.dir, "requests", maintenanceRequestName(id)), raw, true); err != nil {
			return err
		}
	}
	if check.maintenance != nil {
		raw, _ := json.Marshal(check.maintenance)
		if err := safeio.PublishPrivate(filepath.Join(j.dir, maintenanceFile), raw, true); err != nil {
			return err
		}
	}
	j.spent, j.maintenanceRequests, j.spentBytes, j.maintenance = spent, maintenance, budget, check.maintenance
	return nil
}
