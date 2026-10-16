package state

import "errors"

// RecoveryError distinguishes recognized persistent store failures from
// transient I/O failures. Reason contains no paths, keys or stored values.
type RecoveryError struct {
	Reason string
}

func (e *RecoveryError) Error() string {
	switch e.Reason {
	case "missing_key", "missing_data":
		return "secret store is only partially initialized without a recovery record; prior initialization cannot be determined"
	case "invalid_record":
		return "secret store initialization recovery record is invalid"
	case "conflicting_record":
		return "secret store files conflict with initialization recovery record"
	default:
		return "secret store needs recovery"
	}
}

func RecoveryReason(err error) string {
	var recovery *RecoveryError
	if errors.As(err, &recovery) {
		return recovery.Reason
	}
	if errors.Is(err, ErrInitializationPending) {
		return "initialization_pending"
	}
	if errors.Is(err, ErrDecrypt) {
		return "unreadable_store"
	}
	return ""
}
