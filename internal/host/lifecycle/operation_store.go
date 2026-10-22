package lifecycle

import "errors"

// OperationRecordStore adapts an existing deployment owner's canonical journal
// to the shared migration state machine. Managed-full embeds the record in its
// existing transaction.json under Store.Lock; it must not write a second
// lifecycle journal or duplicate migration progress/operation identity.
type OperationRecordStore interface {
	Identity() string
	Locked() bool
	Load() ([]OperationRecord, error)
	Publish(OperationRecord, bool) error
}

// StoreIdentity identifies the existing canonical owner while its lock is
// held. It lets mode-specific service effects reject a journal from another
// store even when a copied transaction contains the same public operation ID.
func (j *OperationJournal) StoreIdentity() (string, error) {
	if err := j.ready(); err != nil {
		return "", err
	}
	if j.store == nil {
		return "", errors.New("operation journal has no external owner")
	}
	return j.store.Identity(), nil
}

// OpenOperationJournalStore is for a deployment owner that already has a
// durable transaction and kernel lock. The adapter must atomically publish the
// complete owner record and refuse writes after its enclosing lock is released.
func OpenOperationJournalStore(store OperationRecordStore, options OperationJournalOptions) (*OperationJournal, error) {
	if store == nil || !store.Locked() {
		return nil, errors.New("host operation store requires its existing owner lock")
	}
	records, err := store.Load()
	if err != nil {
		return nil, err
	}
	if len(records) > maxOperationRecords {
		return nil, errors.New("host operation store exceeds record capacity")
	}
	j := &OperationJournal{store: store, failpoint: options.Failpoint, records: map[string]OperationRecord{}}
	for _, r := range records {
		if _, duplicate := j.records[r.ID]; duplicate || !r.Valid() {
			return nil, errors.New("host operation store contains invalid or duplicate records")
		}
		j.records[r.ID] = detachedMigrationRecord(r)
	}
	if _, _, err := j.Active(); err != nil {
		return nil, err
	}
	return j, nil
}
