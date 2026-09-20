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
	switch {
	case errors.Is(err, state.ErrConflict):
		return secretMutationConflict("secret vault revision changed")
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

func pageRange(total, offset, limit, maxLimit int) (int, int, int, bool, any, error) {
	if offset < 0 {
		return 0, 0, 0, false, nil, fault.Error("offset must be non-negative")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > maxLimit {
		return 0, 0, 0, false, nil, fault.Error("limit exceeds maximum")
	}
	start := min(offset, total)
	end := min(start+limit, total)
	hasMore := end < total
	var next any
	if hasMore {
		next = end
	}
	return start, end, limit, hasMore, next, nil
}
func (c Controller) mutateExpected(ctx context.Context, expected *uint64, change func(document) (map[string]any, error)) (map[string]any, error) {
	var result map[string]any
	snapshot, err := c.backend().Update(ctx, expected, func(data json.RawMessage) (json.RawMessage, error) {
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

func (c Controller) mutate(ctx context.Context, change func(document) (map[string]any, error)) (map[string]any, error) {
	return c.mutateExpected(ctx, nil, change)
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
func (c Controller) ProfilesPage(ctx context.Context, offset, limit int) (map[string]any, error) {
	document, revision, err := c.loadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	profiles := object(document["profiles"])
	items := make([]map[string]any, 0, len(profiles))
	for _, name := range keys(profiles) {
		if isManagedProfile(name) {
			continue
		}
		items = append(items, profileMetadata(name, object(profiles[name])))
	}
	start, end, pageLimit, hasMore, nextOffset, err := pageRange(len(items), offset, limit, MaxProfiles)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"profiles": items[start:end], "revision": revision,
		"offset": start, "limit": pageLimit, "has_more": hasMore,
		"next_offset": nextOffset, "total": len(items), "complete": true,
	}, nil
}

func (c Controller) Profiles(ctx context.Context) (map[string]any, error) {
	return c.ProfilesPage(ctx, 0, MaxProfiles)
}

func (c Controller) Profile(ctx context.Context, name string) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
		return nil, err
	}
	document, revision, err := c.loadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	value, err := profile(document, name)
	if err != nil {
		return nil, err
	}
	result := profileMetadata(name, value)
	result["revision"] = revision
	return result, nil
}
func (c Controller) CreateProfile(ctx context.Context, name string) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
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
	if err := applicationProfileName(name); err != nil {
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
func validateImportValues(name string, values map[string]string) error {
	if err := applicationProfileName(name); err != nil {
		return err
	}
	if len(values) == 0 {
		return fault.Error("dotenv import must contain secrets")
	}
	for key := range values {
		if err := SecretName(key); err != nil {
			return err
		}
	}
	return nil
}

func applyImportValues(document document, name string, values map[string]string) (map[string]any, error) {
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
}

func (c Controller) importValuesExpected(ctx context.Context, name string, values map[string]string, expected *uint64) (map[string]any, error) {
	if err := validateImportValues(name, values); err != nil {
		return nil, err
	}
	return c.mutateExpected(ctx, expected, func(document document) (map[string]any, error) {
		return applyImportValues(document, name, values)
	})
}

func (c Controller) ImportValues(ctx context.Context, name string, values map[string]string) (map[string]any, error) {
	return c.importValuesExpected(ctx, name, values, nil)
}

func (c Controller) ImportValuesExpected(ctx context.Context, name string, values map[string]string, expectedRevision uint64) (map[string]any, error) {
	return c.importValuesExpected(ctx, name, values, &expectedRevision)
}
func (c Controller) SetSecret(ctx context.Context, name, key, value string, public bool) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
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

func (c Controller) Generate(ctx context.Context, name, key string, count int) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
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
	if err := applicationProfileName(name); err != nil {
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
