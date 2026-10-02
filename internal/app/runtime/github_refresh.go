package runtime

import (
	"context"
	"encoding/json"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	githubapp "loki/internal/integrations/github"
	"loki/internal/rpc"
)

func GitHubRefreshOperations(broker *githubapp.Broker) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"github_refresh": {
			Grant: controlpolicy.HostAdministration,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				if _, err := rpc.Decode[struct{}](raw); err != nil {
					return nil, err
				}
				if broker == nil {
					return nil, fault.Error("GitHub refresh requires an enabled GitHub integration")
				}
				broker.Refresh()
				return map[string]any{"refreshed": true}, nil
			},
		},
	}
}
