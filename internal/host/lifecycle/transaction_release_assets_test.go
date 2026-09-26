package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeManagedReleaseAssets struct {
	current      string
	validated    []string
	activated    []string
	restored     []string
	activateErr  error
	restoreErr   error
	failAfterSet bool
}

func (a *fakeManagedReleaseAssets) ValidateCurrent(_ context.Context, generation Generation) error {
	a.validated = append(a.validated, generation.ID)
	if a.current != generation.ID {
		return errors.New("managed release assets do not match installed release")
	}
	return nil
}

func (a *fakeManagedReleaseAssets) Activate(_ context.Context, generation Generation) error {
	a.activated = append(a.activated, generation.ID)
	if a.failAfterSet {
		a.current = generation.ID
	}
	if a.activateErr != nil {
		return a.activateErr
	}
	a.current = generation.ID
	return nil
}

func (a *fakeManagedReleaseAssets) Restore(_ context.Context, generation Generation) error {
	a.restored = append(a.restored, generation.ID)
	if a.restoreErr != nil {
		return a.restoreErr
	}
	a.current = generation.ID
	return nil
}

func TestTransactionApplySwitchesManagedReleaseAssets(t *testing.T) {
	store, backend, active, candidate, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	assets := &fakeManagedReleaseAssets{current: active.ID}
	engine.ReleaseAssets = assets

	if _, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if assets.current != candidate.ID {
		t.Fatalf("managed release assets = %q", assets.current)
	}
	if len(assets.validated) != 1 || assets.validated[0] != active.ID ||
		len(assets.activated) != 1 || assets.activated[0] != candidate.ID {
		t.Fatalf("managed release asset calls validated=%#v activated=%#v", assets.validated, assets.activated)
	}
}

func TestTransactionApplyReleaseAssetFailureRestoresPreviousRelease(t *testing.T) {
	store, backend, active, candidate, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	assets := &fakeManagedReleaseAssets{
		current: active.ID, activateErr: errors.New("CLI link sync failed"), failAfterSet: true,
	}
	engine.ReleaseAssets = assets

	if _, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan}); err == nil || !strings.Contains(err.Error(), "CLI link sync failed") {
		t.Fatalf("apply error = %v", err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.current != active.ID || assets.current != active.ID ||
		snapshot.Installed == nil || snapshot.Installed.ID != active.ID {
		t.Fatalf("recovery runtime=%q assets=%q snapshot=%#v candidate=%q",
			backend.current, assets.current, snapshot, candidate.ID)
	}
	if len(assets.restored) != 1 || assets.restored[0] != active.ID {
		t.Fatalf("managed release asset restores = %#v", assets.restored)
	}
}

func TestTransactionRollbackRestoresManagedReleaseAssets(t *testing.T) {
	store, backend, active, candidate, now, _ := transactionFixture(t)
	tick := now
	nextNow := func() time.Time {
		tick = tick.Add(time.Millisecond)
		return tick
	}
	assets := &fakeManagedReleaseAssets{current: active.ID}
	engine := &TransactionEngine{
		Store: store, Backend: backend, ReleaseAssets: assets, Now: nextNow,
	}
	manager := Manager{Store: store, Jobs: &fakeJobInventory{}, Applier: engine, Now: nextNow}
	if _, err := manager.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(t.Context(), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if assets.current != candidate.ID {
		t.Fatalf("assets after update = %q", assets.current)
	}
	if err := engine.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.current != active.ID || assets.current != active.ID ||
		snapshot.Installed == nil || snapshot.Installed.ID != active.ID {
		t.Fatalf("rollback runtime=%q assets=%q snapshot=%#v", backend.current, assets.current, snapshot)
	}
}

func TestTransactionRecoversInterruptedManagedReleaseSwitch(t *testing.T) {
	store, backend, active, candidate, now, _ := transactionFixture(t)
	assets := &fakeManagedReleaseAssets{current: active.ID}
	engine := &TransactionEngine{
		Store: store, Backend: backend, ReleaseAssets: assets, Now: func() time.Time { return now },
	}

	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(snapshot.Installed, candidate, snapshot.Host, now)
	if err != nil {
		t.Fatal(err)
	}
	lock, journal, err := engine.openJournal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record, err := journal.Begin(OperationApply, plan, now)
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	backup, err := engine.captureBackup(t.Context(), journal, record, snapshot)
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err = backend.Activate(t.Context(), candidate, *snapshot.Installation); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err = assets.Activate(t.Context(), candidate); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, now.Add(time.Second)); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if backup.ID == "" || assets.current != candidate.ID || backend.current != candidate.ID {
		_ = lock.Close()
		t.Fatalf("interrupted switch was not staged backup=%#v runtime=%q assets=%q", backup, backend.current, assets.current)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = engine.Backup(t.Context()); err != nil {
		t.Fatalf("next lifecycle operation did not recover interrupted managed release switch: %v", err)
	}
	recovered, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.current != active.ID || assets.current != active.ID ||
		recovered.Installed == nil || recovered.Installed.ID != active.ID {
		t.Fatalf("interrupted recovery runtime=%q assets=%q snapshot=%#v", backend.current, assets.current, recovered)
	}
}
