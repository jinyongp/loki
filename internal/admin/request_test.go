package admin

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAdministrativeRequests(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"secret", "generate", "profile", "NAME", "--bytes", "48"}, `{"operation":"secret_generate","profile":"profile","secret":"NAME","bytes":48}`},
	} {
		got, err := Request(test.args)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(got)
		var actual, want any
		json.Unmarshal(encoded, &actual)
		json.Unmarshal([]byte(test.want), &want)
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("%q: %s want %s", test.args, encoded, test.want)
		}
	}
}

func TestAdministrativeInputErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"secret", "set", "profile", "TOKEN", "secret-value"}, {"secret", "list", "extra"}, {"secret", "generate", "p", "n", "--bytes", "x"}, {"project", "workflow", "set", "repo", "flow"}, {"project", "workflow", "set", "repo", "flow", "--step", "invalid"}, {"action", "run", "p", "n", "--cwd"}, {"action", "set", "p", "n", "--cwd", "repo", "--", "true"}, {"action", "set", "p", "n", "--cwd", "repo", "--all-secrets", "--secret", "TOKEN", "--", "true"}, {"action", "set", "p", "n", "--cwd", "repo", "--all-secrets", "--origin-env", "ORIGIN", "--", "true"}} {
		if _, err := Request(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
