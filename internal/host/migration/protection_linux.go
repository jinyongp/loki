//go:build linux

package migration

import "encoding/json"

func protectionState(body []byte) (string, error) {
	var result struct {
		State string `json:"state"`
	}
	if json.Unmarshal(body, &result) != nil {
		return "", ErrSourceSession
	}
	switch result.State {
	case "uninitialized", "unlocked", "locked", "locked_manual", "locking", "recovery_required":
		return result.State, nil
	default:
		return "", ErrSourceSession
	}
}
