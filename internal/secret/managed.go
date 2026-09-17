package secret

import (
	"context"

	"loki/internal/fault"
)

// ManagedCredential is a closed identifier for a platform credential owned by
// Loki itself. Application-secret APIs never accept these identifiers or their
// backing profile/key names.
type ManagedCredential uint8

const (
	ManagedGitHubAppPrivateKey ManagedCredential = iota + 1
)

type managedLocation struct {
	profile string
	secret  string
}

var managedCredentialLocations = map[ManagedCredential]managedLocation{
	ManagedGitHubAppPrivateKey: {profile: "github-app", secret: "PRIVATE_KEY"},
}

// ManagedController exposes the narrow trusted API for platform credentials.
// It intentionally does not expose arbitrary profile or secret addresses.
type ManagedController struct {
	vault Controller
}

func (c Controller) ManagedCredentials() ManagedController {
	return ManagedController{vault: c}
}

func managedCredentialLocation(id ManagedCredential) (managedLocation, error) {
	location, ok := managedCredentialLocations[id]
	if !ok {
		return managedLocation{}, fault.Error("unknown managed platform credential")
	}
	return location, nil
}

func isManagedProfile(name string) bool {
	for _, location := range managedCredentialLocations {
		if location.profile == name {
			return true
		}
	}
	return false
}

func applicationProfileName(name string) error {
	if err := ProfileName(name); err != nil {
		return err
	}
	if isManagedProfile(name) {
		return fault.Error("managed platform profile is unavailable to application secret operations")
	}
	return nil
}

func (m ManagedController) Get(ctx context.Context, id ManagedCredential) (string, error) {
	location, err := managedCredentialLocation(id)
	if err != nil {
		return "", err
	}
	document, err := m.vault.load(ctx)
	if err != nil {
		return "", err
	}
	valueProfile, err := profile(document, location.profile)
	if err != nil {
		return "", fault.Error("managed credential is not configured")
	}
	value, ok := object(valueProfile["secrets"])[location.secret].(string)
	if !ok || value == "" {
		return "", fault.Error("managed credential is not configured")
	}
	return value, nil
}

func (m ManagedController) Set(ctx context.Context, id ManagedCredential, value string) (map[string]any, error) {
	location, err := managedCredentialLocation(id)
	if err != nil {
		return nil, err
	}
	return m.vault.mutate(ctx, func(document document) (map[string]any, error) {
		profiles := object(document["profiles"])
		valueProfile := object(profiles[location.profile])
		if valueProfile == nil {
			valueProfile = map[string]any{"secrets": map[string]any{}}
			profiles[location.profile] = valueProfile
		}
		secrets := object(valueProfile["secrets"])
		_, rotated := secrets[location.secret]
		secrets[location.secret] = value
		return map[string]any{"configured": true, "rotated": rotated}, nil
	})
}

func (m ManagedController) Configured(ctx context.Context, id ManagedCredential) (bool, error) {
	location, err := managedCredentialLocation(id)
	if err != nil {
		return false, err
	}
	document, err := m.vault.load(ctx)
	if err != nil {
		return false, err
	}
	valueProfile := object(object(document["profiles"])[location.profile])
	if valueProfile == nil {
		return false, nil
	}
	value, ok := object(valueProfile["secrets"])[location.secret].(string)
	return ok && value != "", nil
}
