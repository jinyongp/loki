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
		{[]string{"bootstrap", "repo"}, `{"operation":"bootstrap_project","cwd":"repo","workflow":"development"}`},
		{[]string{"project", "register", "repo", "--name=Example"}, `{"operation":"project_register","cwd":"repo","name":"Example"}`},
		{[]string{"secret", "generate", "profile", "NAME", "--bytes", "48"}, `{"operation":"secret_generate","profile":"profile","secret":"NAME","bytes":48}`},
		{[]string{"action", "read", "session", "--offset", "0"}, `{"operation":"read_process","session_id":"session","offset":0,"limit":65536}`},
		{[]string{"project", "workflow", "set", "repo", "development", "--step", "dev/install", "--step", "dev/start", "--require", "dev/TOKEN"}, `{"operation":"project_set_workflow","cwd":"repo","workflow":"development","steps":[["dev","install"],["dev","start"]],"required_secrets":{"dev":["TOKEN"]},"timeout_seconds":3600}`},
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

func TestActionDefinitionPreservesArgv(t *testing.T) {
	args := []string{"action", "set", "dev", "web", "--cwd", "repo", "--secret", "TOKEN", "--secret", "OTHER", "--preferred-port", "3000", "--port-env", "PORT", "--origin-env", "ORIGIN", "--", "npm", "run", "dev", "--", "--port", "3000", "--cwd", "literal"}
	request, err := Request(args)
	if err != nil {
		t.Fatal(err)
	}
	action := request["action"].(map[string]any)
	if !reflect.DeepEqual(action["command"], []string{"npm", "run", "dev", "--", "--port", "3000", "--cwd", "literal"}) {
		t.Fatalf("argv %v", action["command"])
	}
	if !reflect.DeepEqual(action["secrets"], []string{"TOKEN", "OTHER"}) || action["timeout_seconds"] != 3600 {
		t.Fatalf("action %v", action)
	}
	if action["dynamic_port"].(map[string]any)["origin_environment"] != "ORIGIN" {
		t.Fatal(action)
	}
}

func TestAdministrativeInputErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"secret", "set", "profile", "TOKEN", "secret-value"}, {"secret", "list", "extra"}, {"secret", "generate", "p", "n", "--bytes", "x"}, {"project", "workflow", "set", "repo", "flow"}, {"project", "workflow", "set", "repo", "flow", "--step", "invalid"}, {"action", "run", "p", "n", "--cwd"}, {"action", "set", "p", "n", "--cwd", "repo", "--", "true"}, {"action", "set", "p", "n", "--cwd", "repo", "--all-secrets", "--secret", "TOKEN", "--", "true"}, {"action", "set", "p", "n", "--cwd", "repo", "--all-secrets", "--origin-env", "ORIGIN", "--", "true"}} {
		if _, err := Request(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
