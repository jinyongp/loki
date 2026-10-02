package windows

import (
	"slices"
	"testing"
)

func TestGitHubUserOperatorIsClosedHostRelay(t *testing.T) {
	valid := OperatorRequest{Command: "integration", Action: "login", Integration: "github", UseStdin: true, GitHubUser: true}
	args, machine, err := operatorCommandArguments(valid)
	if err != nil || machine || !slices.Equal(args, []string{"host", "integration", "login", "--system", "--browser-request", "github"}) {
		t.Fatal("invalid user authorization argv", args, err)
	}
	for _, change := range []func(*OperatorRequest){
		func(r *OperatorRequest) { r.Command = "status" }, func(r *OperatorRequest) { r.Action = "setup" },
		func(r *OperatorRequest) { r.Integration = "signing" }, func(r *OperatorRequest) { r.UseStdin = false },
		func(r *OperatorRequest) { r.GitHubBrowser = true }, func(r *OperatorRequest) { r.IdentityName = "Example" },
		func(r *OperatorRequest) { r.Approve = true }, func(r *OperatorRequest) { r.InterruptActiveJobs = true },
	} {
		request := valid
		change(&request)
		if _, _, err := operatorCommandArguments(request); err == nil {
			t.Fatal("unsafe user authorization relay accepted", request)
		}
	}
}
