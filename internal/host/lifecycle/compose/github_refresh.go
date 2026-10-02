package compose

import (
	"context"
	"encoding/json"
	"errors"
)

// GitHubRefresh clears installation-token caches through the host-only runtime
// operation. Credentials and arbitrary Docker commands never cross this API.
func (b *Backend) GitHubRefresh(ctx context.Context) error {
	state, found, err := b.loadRuntime()
	if err != nil || !found {
		return errors.New("Loki runtime is unavailable")
	}
	raw, err := b.compose(ctx, state, "exec", "-T", "runtime", "/opt/loki/bin/loki", "github", "refresh",
		"--runtime-socket", "/run/loki/runtime/control.sock")
	if err != nil {
		return errors.New("GitHub token refresh failed; run integration doctor github and retry")
	}
	var response struct {
		Refreshed bool `json:"refreshed"`
	}
	if json.Unmarshal(raw, &response) != nil || !response.Refreshed {
		return errors.New("invalid GitHub token refresh response")
	}
	return nil
}
