package runtime

import (
	"context"
	"encoding/json"
	"errors"

	controlpolicy "loki/internal/control/policy"
	githubsetup "loki/internal/integrations/github/setup"
	"loki/internal/rpc"
	githubapp "loki/modules/github"
)

func GitHubSetupOperations(s githubapp.Setup) map[string]rpc.Operation {
	return map[string]rpc.Operation{"github_setup": {
		Grant: controlpolicy.HostAdministration,
		Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			r, err := rpc.Decode[struct {
				Request *githubsetup.Request `json:"setup_request"`
			}](raw)
			if err != nil {
				return nil, err
			}
			if r.Request == nil {
				return githubsetup.View{}, errors.New("GitHub setup request is required")
			}
			return s.Handle(ctx, *r.Request)
		},
	}}
}

func GitHubOperations(c githubapp.Credentials) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"github_app_key_set": {
			Grant: controlpolicy.HostAdministration,
			Handle: runtimeTyped(func(ctx context.Context, r struct {
				Value *string `json:"value"`
			}) (map[string]any, error) {
				if r.Value == nil {
					return nil, githubapp.ValidatePrivateKey("")
				}
				if err := githubapp.ValidatePrivateKey(*r.Value); err != nil {
					return nil, err
				}
				return c.Set(ctx, githubapp.AppPrivateKey, *r.Value)
			}),
		},
	}
}
