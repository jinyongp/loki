package secret

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"

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
	if errors.Is(err, state.ErrUninitialized) || errors.Is(err, state.ErrDecrypt) || errors.Is(err, state.ErrConflict) {
		return fault.Error(err.Error())
	}
	return err
}
func (c Controller) Initialize(ctx context.Context) (map[string]any, error) {
	created, err := c.backend().Initialize(ctx, json.RawMessage(`{"version":1,"profiles":{}}`))
	if err != nil {
		return nil, publicStateError(err)
	}
	return map[string]any{"initialized": true, "created": created}, nil
}
func (c Controller) load(ctx context.Context) (document, error) {
	snapshot, err := c.backend().Load(ctx)
	if err != nil {
		return nil, publicStateError(err)
	}
	return decode(snapshot.Data)
}
func (c Controller) mutate(ctx context.Context, change func(document) (map[string]any, error)) (map[string]any, error) {
	var result map[string]any
	_, err := c.backend().Update(ctx, nil, func(data json.RawMessage) (json.RawMessage, error) {
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
	return result, nil
}
func profile(value document, name string) (map[string]any, error) {
	result := object(object(value["profiles"])[name])
	if result == nil {
		return nil, fault.Error("unknown profile")
	}
	return result, nil
}
func profileMetadata(name string, value map[string]any) map[string]any {
	secrets := object(value["secrets"])
	empty := []string{}
	for _, key := range keys(secrets) {
		if secrets[key] == "" {
			empty = append(empty, key)
		}
	}
	return map[string]any{"name": name, "secret_names": keys(secrets), "secret_count": len(secrets), "configured_secret_count": len(secrets) - len(empty), "empty_secret_names": empty}
}
func (c Controller) Profiles(ctx context.Context) (map[string]any, error) {
	document, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	profiles := object(document["profiles"])
	items := make([]map[string]any, 0, len(profiles))
	for _, name := range keys(profiles) {
		items = append(items, profileMetadata(name, object(profiles[name])))
	}
	return map[string]any{"profiles": items}, nil
}
func (c Controller) Profile(ctx context.Context, name string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	document, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	value, err := profile(document, name)
	if err != nil {
		return nil, err
	}
	return profileMetadata(name, value), nil
}
func (c Controller) CreateProfile(ctx context.Context, name string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(document document) (map[string]any, error) {
		profiles := object(document["profiles"])
		if _, exists := profiles[name]; exists {
			return nil, fault.Error("profile already exists")
		}
		profiles[name] = map[string]any{"secrets": map[string]any{}}
		return map[string]any{"profile": name, "created": true}, nil
	})
}
func (c Controller) RemoveProfile(ctx context.Context, name string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(document document) (map[string]any, error) {
		if _, err := profile(document, name); err != nil {
			return nil, err
		}
		delete(object(document["profiles"]), name)
		return map[string]any{"profile": name, "removed": true}, nil
	})
}
func (c Controller) ImportValues(ctx context.Context, name string, values map[string]string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fault.Error("dotenv import must contain secrets")
	}
	for key := range values {
		if err := SecretName(key); err != nil {
			return nil, err
		}
	}
	return c.mutate(ctx, func(document document) (map[string]any, error) {
		value, err := profile(document, name)
		if err != nil {
			return nil, err
		}
		secrets := object(value["secrets"])
		names := make([]string, 0, len(values))
		for key, text := range values {
			secrets[key] = text
			names = append(names, key)
		}
		slices.Sort(names)
		return map[string]any{"profile": name, "imported": names, "count": len(names)}, nil
	})
}
func (c Controller) SetSecret(ctx context.Context, name, key, value string, public bool) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	if public {
		for _, character := range value {
			if character == 0 {
				return nil, fault.Error("public configuration value contains NUL")
			}
		}
		if len(value) > 65536 {
			return nil, fault.Error("public configuration value exceeds 65536 bytes")
		}
	}
	return c.mutate(ctx, func(document document) (map[string]any, error) {
		profile, err := profile(document, name)
		if err != nil {
			return nil, err
		}
		object(profile["secrets"])[key] = value
		return map[string]any{"profile": name, "secret": key, "stored": true}, nil
	})
}

// ManagedSecret returns one reserved secret to an in-process credential consumer.
func (c Controller) ManagedSecret(ctx context.Context, name, key string) (string, error) {
	if err := ProfileName(name); err != nil {
		return "", err
	}
	if err := SecretName(key); err != nil {
		return "", err
	}
	document, err := c.load(ctx)
	if err != nil {
		return "", err
	}
	valueProfile, err := profile(document, name)
	if err != nil {
		return "", err
	}
	value, ok := object(valueProfile["secrets"])[key].(string)
	if !ok || value == "" {
		return "", fault.Error("managed credential is not configured")
	}
	return value, nil
}

// SetManagedSecret atomically creates its reserved profile or replaces the value.
func (c Controller) SetManagedSecret(ctx context.Context, name, key, value string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(document document) (map[string]any, error) {
		profiles := object(document["profiles"])
		valueProfile := object(profiles[name])
		if valueProfile == nil {
			valueProfile = map[string]any{"secrets": map[string]any{}}
			profiles[name] = valueProfile
		}
		secrets := object(valueProfile["secrets"])
		_, rotated := secrets[key]
		secrets[key] = value
		return map[string]any{"configured": true, "rotated": rotated}, nil
	})
}

func (c Controller) Generate(ctx context.Context, name, key string, count int) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	if count < 16 || count > 128 {
		return nil, fault.Error("generated secret size must be between 16 and 128 bytes")
	}
	buffer := make([]byte, count)
	if _, err := rand.Read(buffer); err != nil {
		return nil, err
	}
	value := base64.RawURLEncoding.EncodeToString(buffer)
	return c.mutate(ctx, func(document document) (map[string]any, error) {
		profile, err := profile(document, name)
		if err != nil {
			return nil, err
		}
		secrets := object(profile["secrets"])
		if existing, ok := secrets[key]; ok && existing != "" {
			return nil, fault.Error("secret is already configured")
		}
		secrets[key] = value
		return map[string]any{"profile": name, "secret": key, "generated": true, "bytes": count}, nil
	})
}
func (c Controller) RemoveSecret(ctx context.Context, name, key string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(document document) (map[string]any, error) {
		profile, err := profile(document, name)
		if err != nil {
			return nil, err
		}
		secrets := object(profile["secrets"])
		if _, exists := secrets[key]; !exists {
			return nil, fault.Error("unknown secret")
		}
		delete(secrets, key)
		return map[string]any{"profile": name, "secret": key, "removed": true}, nil
	})
}
