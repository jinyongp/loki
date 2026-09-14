// Package secret owns the encrypted runtime vault. Secret values never cross
// the runtime boundary; callers receive metadata or a private EnvironmentPlan.
package secret

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"unicode/utf8"

	"loki/internal/fault"
)

const (
	MaxProfiles    = 128
	MaxSecrets     = 512
	MaxSecretBytes = 1_048_576
)

var (
	profilePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	secretPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	idPattern      = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

type document map[string]any

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func keys(values map[string]any) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
func ProfileName(name string) error {
	if !profilePattern.MatchString(name) {
		return fault.Error("invalid profile name")
	}
	return nil
}
func SecretName(name string) error {
	if !secretPattern.MatchString(name) {
		return fault.Error("invalid secret name")
	}
	return nil
}
func decode(data json.RawMessage) (document, error) {
	var value document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !utf8.Valid(data) || !json.Valid(data) || decoder.Decode(&value) != nil || value == nil {
		return nil, fault.Error("secret store schema is invalid")
	}
	if err := validateDocument(value); err != nil {
		return nil, err
	}
	return value, nil
}
func Validate(data json.RawMessage) error { _, err := decode(data); return err }
func validateDocument(value document) error {
	version, ok := value["version"].(json.Number)
	if !ok || version.String() != "1" || len(value) != 2 {
		return fault.Error("secret store schema is invalid")
	}
	profiles := object(value["profiles"])
	if profiles == nil || len(profiles) > MaxProfiles {
		return fault.Error("secret profile collection is invalid")
	}
	for _, name := range keys(profiles) {
		if err := ProfileName(name); err != nil {
			return err
		}
		profile := object(profiles[name])
		if profile == nil || len(profile) != 1 {
			return fault.Error("secret profile is invalid")
		}
		secrets := object(profile["secrets"])
		if secrets == nil || len(secrets) > MaxSecrets {
			return fault.Error("secret collection is invalid")
		}
		for _, key := range keys(secrets) {
			if err := SecretName(key); err != nil {
				return err
			}
			text, ok := secrets[key].(string)
			if !ok || len(text) > MaxSecretBytes {
				return fault.Error("secret value is invalid")
			}
		}
	}
	return nil
}
