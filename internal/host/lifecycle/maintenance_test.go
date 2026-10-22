package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type guardedTransactionBackend struct {
	*fakeTransactionBackend
	t          *testing.T
	root       string
	active     bool
	ids        []string
	interrupts []bool
	endCalls   int
	loseEnd    bool
	busy       bool
}

func (b *guardedTransactionBackend) BeginJobMaintenance(_ context.Context, id string, interrupt bool) error {
	b.t.Helper()
	records, err := ReadOperationSnapshot(b.root)
	if err != nil {
		b.t.Fatal(err)
	}
	found := false
	for _, record := range records {
		expected, _ := MaintenanceOperationID(record.ID)
		if expected == id && record.Maintenance != nil && record.Maintenance.Interrupt == interrupt {
			found = true
		}
	}
	if !found {
		b.t.Fatal("launcher intent was sent before its durable host checkpoint")
	}
	b.ids = append(b.ids, id)
	b.interrupts = append(b.interrupts, interrupt)
	if b.busy {
		return errors.New("active protected Job")
	}
	b.active = true
	return nil
}

func (b *guardedTransactionBackend) EndJobMaintenance(_ context.Context, id string) error {
	b.t.Helper()
	records, err := ReadOperationSnapshot(b.root)
	if err != nil {
		b.t.Fatal(err)
	}
	found := false
	for _, record := range records {
		expected, _ := MaintenanceOperationID(record.ID)
		if expected == id && (record.State == OperationSucceeded || record.State == OperationRolledBack) {
			found = true
		}
	}
	if !found {
		b.t.Fatal("admission reopened before a successful terminal host checkpoint")
	}
	b.endCalls++
	b.active = false
	if b.loseEnd {
		b.loseEnd = false
		return errors.New("end response lost")
	}
	return nil
}

func (b *guardedTransactionBackend) Snapshot(ctx context.Context, kind OperationKind, state Snapshot) (RuntimeSnapshot, error) {
	if !b.active {
		b.t.Fatal("host snapshot stopped services before fencing admission")
	}
	return b.fakeTransactionBackend.Snapshot(ctx, kind, state)
}

func TestTransactionMaintenanceTerminalCheckpointAndLostResponse(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	guard := &guardedTransactionBackend{fakeTransactionBackend: backend, t: t, root: store.Root, loseEnd: true}
	engine.Backend = guard
	if _, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan, Options: ApplyOptions{InterruptActiveJobs: true}}); err == nil {
		t.Fatal("lost fence release response was hidden")
	}
	records, err := ReadOperationSnapshot(store.Root)
	if err != nil || len(records) != 1 {
		t.Fatal(err)
	}
	if records[0].State != OperationSucceeded || records[0].Maintenance == nil || records[0].Maintenance.Released {
		t.Fatal("uncertain release was marked acknowledged")
	}
	if err := engine.RecoverManagedIntegration(t.Context()); err != nil {
		t.Fatal(err)
	}
	records, err = ReadOperationSnapshot(store.Root)
	if err != nil || !records[0].Maintenance.Released || guard.endCalls != 2 {
		t.Fatal("original release was not reconciled", err)
	}
	for index, id := range guard.ids {
		if id != guard.ids[0] || !guard.interrupts[index] {
			t.Fatal("operation identity or explicit choice changed")
		}
	}
}

func TestTransactionMaintenanceBusyDoesNotSnapshot(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	guard := &guardedTransactionBackend{fakeTransactionBackend: backend, t: t, root: store.Root, busy: true}
	engine.Backend = guard
	if _, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan}); err == nil {
		t.Fatal("busy Job permitted mutation")
	}
	if backend.nextSnapshot != 0 || backend.restartCalls != 0 || guard.active {
		t.Fatal("busy rejection mutated deployment")
	}
}

func TestInterruptedMaintenanceReusesPersistedChoice(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	guard := &guardedTransactionBackend{fakeTransactionBackend: backend, t: t, root: store.Root}
	engine.Backend = guard
	lock, journal, err := engine.openJournal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record, err := journal.Begin(OperationUpdateIntegration, plan, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.beginMaintenance(t.Context(), journal, record, true); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.RecoverManagedIntegration(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(guard.ids) != 2 || guard.ids[0] != guard.ids[1] || !guard.interrupts[1] || guard.active {
		t.Fatal("recovery changed the interruption grant or operation ID")
	}
}

func TestFailedHostRecoveryRetainsAdmissionFence(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	backend.healthErr = errors.New("new deployment is unhealthy")
	backend.restoreErr = errors.New("original deployment could not be restored")
	guard := &guardedTransactionBackend{fakeTransactionBackend: backend, t: t, root: store.Root}
	engine.Backend = guard
	if _, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan}); err == nil {
		t.Fatal("failed recovery was hidden")
	}
	records, err := ReadOperationSnapshot(store.Root)
	if err != nil || len(records) != 1 {
		t.Fatal(err)
	}
	if records[0].State != OperationRecoveryFailed || records[0].Maintenance.Released || !guard.active || guard.endCalls != 0 {
		t.Fatal("failed recovery reopened Job admission")
	}
	if err := engine.RecoverManagedIntegration(t.Context()); err == nil {
		t.Fatal("unresolved terminal failure permitted a fresh host operation")
	}
}

func TestMaintenanceOperationIDPreservesIdentity(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	uuid, err := MaintenanceOperationID(id)
	if err != nil || strings.ReplaceAll(uuid, "-", "") != id {
		t.Fatal("operation mapping lost identity")
	}
	for _, bad := range []string{"", "not-an-id", strings.Repeat("A", 32), strings.Repeat("z", 32)} {
		if _, err := MaintenanceOperationID(bad); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
}

func TestManagerBackupPersistsExplicitInterruption(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine, _ := preparedTransaction(t, store, backend, now)
	guard := &guardedTransactionBackend{fakeTransactionBackend: backend, t: t, root: store.Root}
	engine.Backend = guard
	manager := Manager{Store: store, Jobs: &fakeJobInventory{}, Maintainer: engine}
	if _, err := manager.Backup(t.Context(), MutationOptions{InterruptActiveJobs: true}); err != nil {
		t.Fatal(err)
	}
	if len(guard.interrupts) == 0 || !guard.interrupts[0] {
		t.Fatal("manager discarded the explicit interruption choice")
	}
}
