package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"loki/internal/state"
)

type Credential string

const (
	AppPrivateKey      Credential = "app-private-key"
	PersonalUserTokens Credential = "personal-user-tokens"
)

// Credentials owns a distinct encrypted provider store. GitHub API-only
// operation requires neither application-secret profiles nor a Git repository.
type Credentials struct{ StateDirectory string }
type credentialDocument struct {
	Schema int                   `json:"schema"`
	Values map[Credential]string `json:"values"`
}

func validCredential(id Credential) bool { return id == AppPrivateKey || id == PersonalUserTokens }

func credentialValidation(data json.RawMessage) error {
	var d credentialDocument
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if d.Schema != 1 || d.Values == nil {
		return fmt.Errorf("invalid GitHub credential schema")
	}
	for id, value := range d.Values {
		if !validCredential(id) || value == "" {
			return fmt.Errorf("invalid GitHub credential entry")
		}
		if id == AppPrivateKey {
			if err := ValidatePrivateKey(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c Credentials) backend() state.Store {
	return state.Store{Dir: c.StateDirectory, Validate: credentialValidation}
}

func (c Credentials) Initialize(ctx context.Context) (map[string]any, error) {
	created, err := c.backend().Initialize(ctx, json.RawMessage(`{"schema":1,"values":{}}`))
	if err != nil {
		return nil, fmt.Errorf("GitHub credential store could not initialize")
	}
	return map[string]any{"initialized": true, "created": created}, nil
}

func (c Credentials) Get(ctx context.Context, id Credential) (string, error) {
	if !validCredential(id) {
		return "", fmt.Errorf("unknown GitHub credential")
	}
	snapshot, err := c.backend().Load(ctx)
	if errors.Is(err, state.ErrUninitialized) && id == PersonalUserTokens {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("GitHub credential store is unavailable")
	}
	var d credentialDocument
	if err := json.Unmarshal(snapshot.Data, &d); err != nil {
		return "", fmt.Errorf("GitHub credential store is invalid")
	}
	value := d.Values[id]
	if value == "" && id == AppPrivateKey {
		return "", fmt.Errorf("GitHub App private key is not configured")
	}
	return value, nil
}

func (c Credentials) Configured(ctx context.Context, id Credential) (bool, error) {
	if !validCredential(id) {
		return false, fmt.Errorf("unknown GitHub credential")
	}
	snapshot, err := c.backend().Load(ctx)
	if errors.Is(err, state.ErrUninitialized) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("GitHub credential store is unavailable")
	}
	var d credentialDocument
	if err := json.Unmarshal(snapshot.Data, &d); err != nil {
		return false, fmt.Errorf("GitHub credential store is invalid")
	}
	return d.Values[id] != "", nil
}

func (c Credentials) Set(ctx context.Context, id Credential, value string) (map[string]any, error) {
	if !validCredential(id) || value == "" {
		return nil, fmt.Errorf("invalid GitHub credential")
	}
	if id == AppPrivateKey {
		if err := ValidatePrivateKey(value); err != nil {
			return nil, err
		}
	}
	if id == PersonalUserTokens && !json.Valid([]byte(value)) {
		return nil, fmt.Errorf("invalid GitHub user-token document")
	}
	store := c.backend()
	if _, err := store.Initialize(ctx, json.RawMessage(`{"schema":1,"values":{}}`)); err != nil {
		return nil, fmt.Errorf("GitHub credential store could not initialize")
	}
	rotated := false
	snapshot, err := store.Update(ctx, nil, func(data json.RawMessage) (json.RawMessage, error) {
		var d credentialDocument
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		rotated = d.Values[id] != ""
		d.Values[id] = value
		return json.Marshal(d)
	})
	if err != nil {
		return nil, fmt.Errorf("GitHub credential could not be saved")
	}
	return map[string]any{"configured": true, "credential": id, "revision": snapshot.Revision, "rotated": rotated}, nil
}
