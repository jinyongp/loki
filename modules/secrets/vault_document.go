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

func faultSchema() error   { return fault.Error("secret store schema is invalid") }
func faultProfiles() error { return fault.Error("secret profile collection is invalid") }
func faultProfile() error  { return fault.Error("secret profile is invalid") }
func faultSecrets() error  { return fault.Error("secret collection is invalid") }
func faultSecret() error   { return fault.Error("secret value is invalid") }

const (
	MaxProfiles          = 128
	MaxSecrets           = 512
	MaxSecretBytes       = 1_048_576
	MaxReplayRequests    = 256
	MaxReplayResultBytes = 16 * 1024
)

var (
	profilePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	secretPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	idPattern          = regexp.MustCompile(`^[a-f0-9]{32}$`)
	requestIDPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
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

func validateReplayRequests(value document) error {
	raw, exists := value["requests"]
	if !exists {
		return nil
	}
	requests := object(raw)
	if requests == nil || len(requests) > MaxReplayRequests {
		return faultSchema()
	}
	for requestID, rawRecord := range requests {
		if !requestIDPattern.MatchString(requestID) {
			return faultSchema()
		}
		record := object(rawRecord)
		if record == nil || len(record) != 3 {
			return faultSchema()
		}
		fingerprint, ok := record["fingerprint"].(string)
		if !ok || !fingerprintPattern.MatchString(fingerprint) {
			return faultSchema()
		}
		revision, ok := record["revision"].(json.Number)
		if !ok {
			return faultSchema()
		}
		revisionValue, err := revision.Int64()
		if err != nil || revisionValue < 1 {
			return faultSchema()
		}
		resultJSON, ok := record["result_json"].(string)
		if !ok || len(resultJSON) > MaxReplayResultBytes || !utf8.ValidString(resultJSON) || !json.Valid([]byte(resultJSON)) {
			return faultSchema()
		}
		var result map[string]any
		if json.Unmarshal([]byte(resultJSON), &result) != nil || result == nil {
			return faultSchema()
		}
	}
	return nil
}

func validateDocument(value document) error {
	version, ok := value["version"].(json.Number)
	if !ok || version.String() != "1" || (len(value) != 2 && len(value) != 3) {
		return faultSchema()
	}
	if err := validateReplayRequests(value); err != nil {
		return err
	}
	profiles := object(value["profiles"])
	if profiles == nil || len(profiles) > MaxProfiles {
		return faultProfiles()
	}
	for _, name := range keys(profiles) {
		if err := ProfileName(name); err != nil {
			return err
		}
		profile := object(profiles[name])
		if profile == nil || len(profile) != 1 {
			return faultProfile()
		}
		secrets := object(profile["secrets"])
		if secrets == nil || len(secrets) > MaxSecrets {
			return faultSecrets()
		}
		for _, key := range keys(secrets) {
			if err := SecretName(key); err != nil {
				return err
			}
			text, ok := secrets[key].(string)
			if !ok || len(text) > MaxSecretBytes {
				return faultSecret()
			}
		}
	}
	return nil
}
