package secret

import (
	"context"
	"encoding/json"
	"errors"

	"loki/internal/fault"
	"loki/internal/state"
)

type Controller struct {
	StateDirectory string
	InboxDirectory string
}

func (c Controller) backend() state.Store {
	return state.Store{Dir: c.StateDirectory, Validate: Validate}
}
func publicStateError(err error) error {
	if reason := state.RecoveryReason(err); reason != "" {
		return &fault.StoreRecovery{Schema: 1, Owner: "secrets", Reason: reason}
	}
	switch {
	case errors.Is(err, state.ErrConflict):
		return fault.New(fault.CodeConflict, "platform credential revision changed", false, "inspect the current credential state before retrying")
	case errors.Is(err, state.ErrUninitialized), errors.Is(err, state.ErrDecrypt):
		return fault.Error(err.Error())
	default:
		return err
	}
}
func (c Controller) Initialize(ctx context.Context) (map[string]any, error) {
	created, err := c.backend().Initialize(ctx, json.RawMessage(`{"version":1,"profiles":{}}`))
	if err != nil {
		return nil, publicStateError(err)
	}
	return map[string]any{"initialized": true, "created": created}, nil
}
func (c Controller) loadSnapshot(ctx context.Context) (document, uint64, error) {
	snapshot, err := c.backend().Load(ctx)
	if err != nil {
		return nil, 0, publicStateError(err)
	}
	document, err := decode(snapshot.Data)
	if err != nil {
		return nil, 0, err
	}
	return document, snapshot.Revision, nil
}

func (c Controller) load(ctx context.Context) (document, error) {
	document, _, err := c.loadSnapshot(ctx)
	return document, err
}

func (c Controller) mutateScoped(ctx context.Context, expected *uint64, managed bool, change func(document) (map[string]any, error)) (map[string]any, error) {
	var result map[string]any
	snapshot, err := c.backend().Update(ctx, expected, func(data json.RawMessage) (json.RawMessage, error) {
		// Check under the same lock as the mutation. A host process dying must
		// not reopen the source while its migration operation remains pending.
		if err := c.checkApplicationMigrationWrite(managed); err != nil {
			return nil, err
		}
		document, err := decode(data)
		if err != nil {
			return nil, err
		}
		result, err = change(document)
		if err != nil {
			return nil, err
		}
		return json.Marshal(document)
	})
	if err != nil {
		return nil, publicStateError(err)
	}
	result["revision"] = snapshot.Revision
	return result, nil
}

func profile(value document, name string) (map[string]any, error) {
	result := object(object(value["profiles"])[name])
	if result == nil {
		return nil, fault.Error("unknown profile")
	}
	return result, nil
}
