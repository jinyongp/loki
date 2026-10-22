package devtools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestBrokerRejectsProcessSecretDelivery(t *testing.T) {
	client, log := fakeClient(t)
	broker := Broker{Client: client}
	for _, profile := range []string{"project", "github-app", ""} {
		for _, command := range []string{"process start", "process restart", "command run", "env list"} {
			for _, names := range [][]string{nil, {"TOKEN"}} {
				raw, err := broker.Call(t.Context(), command, json.RawMessage(`{}`), profile, names)
				if err == nil || raw != nil || !strings.Contains(err.Error(), "unavailable") {
					t.Fatalf("%s %s: result=%s err=%v", command, profile, raw, err)
				}
			}
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("rejected broker call launched devtools")
	}
}
