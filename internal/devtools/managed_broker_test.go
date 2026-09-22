package devtools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loki/internal/secret"
)

func TestBrokerRejectsManagedCredentialInjection(t *testing.T) {
	client, _ := fakeClient(t)
	controller := testVault(t)
	if _, err := controller.ManagedCredentials().Set(t.Context(), secret.ManagedGitHubAppPrivateKey, "synthetic-platform"); err != nil {
		t.Fatal(err)
	}
	broker := Broker{Client: client, ResolveSecrets: testSecretResolver(controller)}
	input := json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`)
	if _, err := broker.Call(context.Background(), "process start", input, "github-app", []string{"PRIVATE_KEY"}); err == nil || !strings.Contains(err.Error(), "managed platform") {
		t.Fatalf("managed credential injection error = %v", err)
	}
	if _, err := broker.Call(context.Background(), "process restart", input, "github-app", []string{"PRIVATE_KEY"}); err == nil || !strings.Contains(err.Error(), "managed platform") {
		t.Fatalf("managed credential restart injection error = %v", err)
	}
}
