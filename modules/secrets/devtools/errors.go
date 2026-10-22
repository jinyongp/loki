package devtools

import "encoding/json"

// Failure retains only reviewed identifiers. Upstream messages, arbitrary
// details, paths, values and remedies are never copied into an error.
type Failure struct {
	Code   string
	Reason string
}

func (e *Failure) Error() string        { return "application value provider: " + e.Code }
func (e *Failure) Is(target error) bool { return target == ErrUnavailable }

func providerFailure(raw []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ErrUnavailable
	}
	code := envelope.Error.Code
	switch code {
	case "invalid_argument", "revision_conflict", "request_conflict", "request_expired", "request_not_found", "value_exists", "kind_conflict", "value_expired", "env_not_found", "key_not_found", "vault_locked", "vault_recovery_required", "key_missing", "integrity_failed", "vault_input_too_large", "key_backend_unavailable", "lock_pending", "key_session_protocol_mismatch", "key_access_denied", "input_changed", "limit_exceeded", "storage_error":
	default:
		code = "unavailable"
	}
	reason := envelope.Error.Details.Reason
	switch reason {
	case "vault_locked", "manual_lock", "idle_expired", "request_not_found", "request_expired", "revision_conflict", "request_conflict", "value_exists", "kind_conflict", "value_expired", "key_missing", "integrity", "recovery_required", "limit_exceeded":
	default:
		reason = ""
	}
	return &Failure{Code: code, Reason: reason}
}
