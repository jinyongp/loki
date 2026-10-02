package runtime

import (
	"context"
	"encoding/json"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	githubapp "loki/internal/integrations/github"
	"loki/internal/rpc"
)

func GitHubUserOperations(users *githubapp.UserAuthorization) map[string]rpc.Operation {
	operations := map[string]rpc.Operation{}
	for _, action := range []string{"begin", "poll", "status", "logout"} {
		operations["github_user_"+action] = rpc.Operation{
			Grant: controlpolicy.HostAdministration,
			Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
				request, err := rpc.Decode[struct {
					Account   string `json:"account"`
					SessionID string `json:"session_id"`
				}](raw)
				if err != nil {
					return nil, err
				}
				if users == nil {
					return githubapp.UserAuthorizationView{}, fault.Error("GitHub user authorization is not configured")
				}
				if action == "poll" {
					if request.Account != "" || len(request.SessionID) != 64 {
						return githubapp.UserAuthorizationView{}, fault.Error("invalid GitHub authorization session")
					}
					return users.Poll(ctx, request.SessionID)
				}
				if request.SessionID != "" {
					return githubapp.UserAuthorizationView{}, fault.Error("invalid GitHub user authorization arguments")
				}
				switch action {
				case "begin":
					return users.Begin(ctx, request.Account)
				case "logout":
					return users.Logout(ctx, request.Account)
				default:
					return users.Status(ctx, request.Account)
				}
			},
		}
	}
	return operations
}
