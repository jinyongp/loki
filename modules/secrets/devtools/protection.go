package devtools

import (
	"context"
)

type ProtectionResult struct {
	Changed   bool             `json:"changed"`
	Item      ProtectionStatus `json:"item"`
	RequestID string           `json:"request_id,omitempty"`
	Replayed  bool             `json:"replayed"`
}

func validProtectionStatus(s ProtectionStatus) bool {
	switch s.State {
	case "uninitialized", "locked", "unlocked", "locking", "locked_manual", "recovery_required":
	default:
		return false
	}
	if s.Backend != "" && s.Backend != "passphrase" {
		return false
	}
	return s.Rewrap == nil || uuid.MatchString(s.Rewrap.RequestID) && s.Rewrap.FromBackend == "passphrase" && s.Rewrap.Backend == "passphrase" && !s.Rewrap.IntegrityVerified
}

// Protection permits only explicit passphrase-backed administration. No path
// discovers an OS fallback backend, writes an unlock credential or auto-inits.
func (p *Provider) Protection(ctx context.Context, action string, input []byte) (ProtectionResult, error) {
	args := []string{"vault", action}
	switch action {
	case "init", "unlock":
		if len(input) == 0 || len(input) > 65536 {
			return ProtectionResult{}, ErrUnavailable
		}
		if action == "init" {
			args = append(args, "--backend", "passphrase")
		}
		args = append(args, "--passphrase-stdin")
	case "lock", "recover":
		if len(input) != 0 {
			return ProtectionResult{}, ErrUnavailable
		}
	default:
		return ProtectionResult{}, ErrUnavailable
	}
	var result ProtectionResult
	err := p.invoke(ctx, args, input, &result)
	if err == nil && !validProtectionStatus(result.Item) {
		return ProtectionResult{}, ErrUnavailable
	}
	return result, err
}
