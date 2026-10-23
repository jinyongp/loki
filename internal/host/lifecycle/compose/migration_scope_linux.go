//go:build linux

package compose

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"loki/internal/host/lifecycle"
)

type migrationScopeKey struct{}

type migrationScope struct {
	backend *Backend
	journal *lifecycle.OperationJournal
	id      string
	active  bool
}

// withMigration keeps progress in the SAME operation.lock and journal used by
// every other Compose host mutation. The scope is never serialized or reused
// after that lock closes; labels and a public operation ID are not authority.
func (b *Backend) withMigration(ctx context.Context, intent *lifecycle.SecretMigrationIntent, apply func(context.Context, *lifecycle.OperationJournal, lifecycle.OperationRecord) error) error {
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return err
	}
	var record lifecycle.OperationRecord
	if intent != nil {
		if !validComposeMigrationIntent(*intent) {
			return errComposeMigration
		}
		items, err := j.List()
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(intent)
		if err != nil {
			return errComposeMigration
		}
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
		for _, item := range items {
			if item.SecretMigration != nil && item.PlanID == digest {
				record = item
				break
			}
		}
		if record.ID == "" {
			state, found, err := b.loadRuntime()
			if err != nil || !found || state.CoreImage != intent.ContainerImage || state.GenerationID != intent.ContainerGeneration || state.ApplicationPolicySHA256 != intent.ExpectedPolicy {
				return errComposeMigration
			}
			metadata, err := b.migrationMetadata(ctx, state, intent.BeforeResources, true)
			if err != nil || metadata.Identity != intent.Before || metadata.Policy != intent.ExpectedPolicy {
				return errComposeMigration
			}
			record, err = j.BeginSecretMigration(*intent, time.Now())
			if err != nil {
				return err
			}
		}
	} else {
		var found bool
		record, found, err = j.Active()
		if err != nil || !found || record.SecretMigration == nil || record.SecretMigration.Intent.Mode != "compose" {
			return errComposeMigration
		}
	}
	scope := &migrationScope{backend: b, journal: j, id: record.ID, active: true}
	defer func() { scope.active = false }()
	return apply(context.WithValue(ctx, migrationScopeKey{}, scope), j, record)
}

func validComposeMigrationIntent(i lifecycle.SecretMigrationIntent) bool {
	return i.Valid() && i.Mode == "compose" && i.SourceRoot == "/var/lib/loki/runtime" && i.IdentityFile == "/var/lib/loki/app-store/deployment.json" && i.StateRoot == "/var/lib/loki/app-store" && i.ServiceStateRoot == "/var/lib/loki/secrets" && i.RuntimeRoot == "/run/loki/secrets" && i.Binary == "/opt/loki/bin/devtools" && i.Before.UID == 10006 && i.Before.GID == 10006 && i.Before.AgentUID == 10000 && i.Before.MetadataGID == 10001
}

func (b *Backend) migrationRecord(ctx context.Context) (lifecycle.OperationRecord, *lifecycle.OperationJournal, error) {
	scope, ok := ctx.Value(migrationScopeKey{}).(*migrationScope)
	if !ok || scope == nil || !scope.active || scope.backend != b || scope.journal == nil {
		return lifecycle.OperationRecord{}, nil, errComposeMigration
	}
	r, found, err := scope.journal.Active()
	if err != nil || !found || r.ID != scope.id || r.SecretMigration == nil || !validComposeMigrationIntent(r.SecretMigration.Intent) {
		return lifecycle.OperationRecord{}, nil, errComposeMigration
	}
	state, exists, err := b.loadRuntime()
	if err != nil || !exists || state.CoreImage != r.SecretMigration.Intent.ContainerImage || state.GenerationID != r.SecretMigration.Intent.ContainerGeneration {
		return lifecycle.OperationRecord{}, nil, errComposeMigration
	}
	return r, scope.journal, nil
}
