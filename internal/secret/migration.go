package secret

import (
	"bytes"
	"encoding/json"
)

// MigrateLegacyDocument reduces a validated Python v1 vault document to the
// profile and secret state owned by the Go runtime.
func MigrateLegacyDocument(data json.RawMessage) (json.RawMessage, error) {
	var legacy map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&legacy) != nil || legacy == nil {
		return nil, faultSchema()
	}
	version, ok := legacy["version"].(json.Number)
	if !ok || version.String() != "1" {
		return nil, faultSchema()
	}
	profiles := object(legacy["profiles"])
	if profiles == nil || len(profiles) > MaxProfiles {
		return nil, faultProfiles()
	}
	clean := make(map[string]any, len(profiles))
	for name, raw := range profiles {
		if err := ProfileName(name); err != nil {
			return nil, err
		}
		profile := object(raw)
		if profile == nil {
			return nil, faultProfile()
		}
		secrets := object(profile["secrets"])
		if secrets == nil || len(secrets) > MaxSecrets {
			return nil, faultSecrets()
		}
		values := make(map[string]any, len(secrets))
		for key, value := range secrets {
			if err := SecretName(key); err != nil {
				return nil, err
			}
			text, valid := value.(string)
			if !valid || len(text) > MaxSecretBytes {
				return nil, faultSecret()
			}
			values[key] = text
		}
		clean[name] = map[string]any{"secrets": values}
	}
	result, err := json.Marshal(map[string]any{"version": 1, "profiles": clean})
	if err != nil {
		return nil, err
	}
	if err = Validate(result); err != nil {
		return nil, err
	}
	return result, nil
}
