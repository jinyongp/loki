package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

type policyTransactionBackend struct {
	*guardedTransactionBackend
	validationError error
}

func (b *policyTransactionBackend) ValidateApplicationPolicy(context.Context) error {
	return b.validationError
}

func TestApplicationPolicyFailureRestoresCatalogBeforeFenceRelease(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	before := []byte(`{"resources":[]}`)
	if _, err := store.WriteManagedIntegrationFile(t.Context(), ManagedApplicationResourcesFile, before); err != nil {
		t.Fatal(err)
	}
	guard := &guardedTransactionBackend{fakeTransactionBackend: backend, t: t, root: store.Root}
	actual := &policyTransactionBackend{guardedTransactionBackend: guard, validationError: errors.New("role policy mismatch")}
	engine := &TransactionEngine{Store: store, Backend: actual, Now: func() time.Time { return now }}
	err := engine.UpdateManagedIntegration(t.Context(), "application", func(ctx context.Context, s *FileStore) error {
		_, err := s.WriteManagedIntegrationFile(ctx, ManagedApplicationResourcesFile, []byte(`{"resources":[{"alias":"candidate"}]}`))
		return err
	})
	if err == nil {
		t.Fatal("wrong policy was committed")
	}
	raw, err := store.ReadManagedIntegrationFile(t.Context(), ManagedApplicationResourcesFile, true)
	if err != nil || string(raw) != string(before) {
		t.Fatalf("catalog recovery: %s %v", raw, err)
	}
	if guard.active || guard.endCalls != 1 {
		t.Fatal("catalog recovery did not release exactly the original fence")
	}
}
