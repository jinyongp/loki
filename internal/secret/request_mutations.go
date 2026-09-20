package secret

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strconv"

	"loki/internal/fault"
)

func (c Controller) CreateProfileRequest(ctx context.Context, name, requestID string, expectedRevision uint64) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
		return nil, err
	}
	fingerprint := mutationFingerprint("profile_create", expectedRevision, name)
	return c.mutateRequest(ctx, requestID, fingerprint, expectedRevision, func(document document) (map[string]any, error) {
		profiles := object(document["profiles"])
		if _, exists := profiles[name]; exists {
			return nil, fault.Error("profile already exists")
		}
		profiles[name] = map[string]any{"secrets": map[string]any{}}
		return map[string]any{"profile": name, "created": true}, nil
	})
}

func (c Controller) SetPublicRequest(ctx context.Context, name, key, value, requestID string, expectedRevision uint64) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	for _, character := range value {
		if character == 0 {
			return nil, fault.Error("public configuration value contains NUL")
		}
	}
	if len(value) > 65536 {
		return nil, fault.Error("public configuration value exceeds 65536 bytes")
	}
	fingerprint := mutationFingerprint("public_value_set", expectedRevision, name, key, value)
	return c.mutateRequest(ctx, requestID, fingerprint, expectedRevision, func(document document) (map[string]any, error) {
		valueProfile, err := profile(document, name)
		if err != nil {
			return nil, err
		}
		object(valueProfile["secrets"])[key] = value
		return map[string]any{"profile": name, "name": key, "stored": true}, nil
	})
}

func (c Controller) GenerateRequest(ctx context.Context, name, key, requestID string, expectedRevision uint64, count int) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	if count < 16 || count > 128 {
		return nil, fault.Error("generated secret size must be between 16 and 128 bytes")
	}
	fingerprint := mutationFingerprint("secret_generate", expectedRevision, name, key, strconv.Itoa(count))
	return c.mutateRequest(ctx, requestID, fingerprint, expectedRevision, func(document document) (map[string]any, error) {
		valueProfile, err := profile(document, name)
		if err != nil {
			return nil, err
		}
		secrets := object(valueProfile["secrets"])
		if existing, ok := secrets[key]; ok && existing != "" {
			return nil, fault.Error("secret is already configured")
		}
		buffer := make([]byte, count)
		if _, err = rand.Read(buffer); err != nil {
			return nil, err
		}
		secrets[key] = base64.RawURLEncoding.EncodeToString(buffer)
		return map[string]any{"profile": name, "secret": key, "generated": true, "bytes": count}, nil
	})
}

func (c Controller) RemoveProfileRequest(ctx context.Context, name, requestID string, expectedRevision uint64) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
		return nil, err
	}
	fingerprint := mutationFingerprint("profile_remove", expectedRevision, name)
	return c.mutateRequest(ctx, requestID, fingerprint, expectedRevision, func(document document) (map[string]any, error) {
		if _, err := profile(document, name); err != nil {
			return nil, err
		}
		delete(object(document["profiles"]), name)
		return map[string]any{"profile": name, "removed": true}, nil
	})
}

func (c Controller) RemoveSecretRequest(ctx context.Context, name, key, requestID string, expectedRevision uint64) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	fingerprint := mutationFingerprint("secret_remove", expectedRevision, name, key)
	return c.mutateRequest(ctx, requestID, fingerprint, expectedRevision, func(document document) (map[string]any, error) {
		valueProfile, err := profile(document, name)
		if err != nil {
			return nil, err
		}
		secrets := object(valueProfile["secrets"])
		if _, exists := secrets[key]; !exists {
			return nil, fault.Error("unknown secret")
		}
		delete(secrets, key)
		return map[string]any{"profile": name, "secret": key, "removed": true}, nil
	})
}
