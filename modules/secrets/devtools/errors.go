package devtools

import (
	"encoding/json"
	"loki/internal/secretoutput"
)

// Failure retains only reviewed identifiers. Upstream messages, arbitrary
// details, paths, values and remedies are never copied into an error.
type Failure struct {
	Code      string
	Reason    string
	Retry     string
	Effects   string
	RequestID string
	Limit     *secretoutput.LimitError
}

func (e *Failure) Error() string        { return "application value provider: " + e.Code }
func (e *Failure) Is(target error) bool { return target == ErrUnavailable }

func providerFailure(raw []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Reason  string `json:"reason"`
				Retry   string `json:"retry"`
				Effects string `json:"effects"`
				Target  struct {
					RequestID string `json:"request_id"`
				} `json:"target"`
				Facts struct {
					Kind     string `json:"limit_kind"`
					Observed int    `json:"observed"`
					Limit    int    `json:"limit"`
				} `json:"facts"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ErrUnavailable
	}
	code := envelope.Error.Code
	switch code {
	case "invalid_argument", "revision_conflict", "request_conflict", "request_expired", "request_not_found", "value_exists", "kind_conflict", "value_expired", "env_not_found", "key_not_found", "vault_locked", "vault_recovery_required", "vault_already_initialized", "key_missing", "integrity_failed", "vault_input_too_large", "key_backend_unavailable", "lock_pending", "key_session_protocol_mismatch", "key_access_denied", "input_changed", "limit_exceeded", "storage_error", "redaction_unavailable", "delivery_outcome_unknown", "delivery_pending", "key_interaction_required", "key_backend_locked", "passphrase_required", "passphrase_mismatch":
	default:
		code = "unavailable"
	}
	reason := envelope.Error.Details.Reason
	switch reason {
	case "vault_locked", "manual_lock", "idle_expired", "request_not_found", "request_expired", "revision_conflict", "request_conflict", "value_exists", "kind_conflict", "value_expired", "key_missing", "integrity", "recovery_required", "limit_exceeded":
	default:
		reason = ""
	}
	f := &Failure{Code: code, Reason: reason}
	if code == "unavailable" {
		return f
	}
	d := envelope.Error.Details
	switch d.Retry {
	case "same_request", "inspect_first", "after_input_change", "after_dependency_change":
		f.Retry = d.Retry
	}
	switch d.Effects {
	case "none", "unknown", "committed", "partial":
		f.Effects = d.Effects
	}
	if uuid.MatchString(d.Target.RequestID) {
		f.RequestID = d.Target.RequestID
	}
	limit := &secretoutput.LimitError{Kind: d.Facts.Kind, Observed: d.Facts.Observed, Limit: d.Facts.Limit}
	if code == "redaction_unavailable" && limit.Valid() {
		f.Limit = limit
	}
	return f
}
