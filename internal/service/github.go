package service

import (
	"context"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/githubapp"
	"loki/internal/rpc"
	"loki/internal/secret"
)

func GitHubOperations(c secret.Controller) map[string]rpc.Operation {
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
				return c.ManagedCredentials().Set(ctx, secret.ManagedGitHubAppPrivateKey, *r.Value)
			}),
		},
	}
}
