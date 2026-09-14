package devtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/secret"
)

func TestBrokerInjectsVaultSecretsWithoutReturningThem(t *testing.T) {
	client, _ := fakeClient(t)
	script := client.Binary
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.8.2\",\"commit\":\"test\"}}'; exit 0; fi\n" +
		"if [ \"$TOKEN\" != \"private-token-value\" ]; then exit 9; fi\n" +
		"printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"started\":true}}'\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	controller := testVault(t)
	broker := Broker{Client: client, Secrets: controller}
	input := json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`)
	result, err := broker.Call(context.Background(), "process start", input, "project", []string{"TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result), "private-token-value") {
		t.Fatal("secret returned by broker")
	}
}

func TestBrokerRejectsPrivateOutputAndUnapprovedInjection(t *testing.T) {
	client, _ := fakeClient(t)
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.8.2\",\"commit\":\"test\"}}'; exit 0; fi\n" +
		"printf '%s\\n' \"{\\\"schema_version\\\":1,\\\"ok\\\":true,\\\"data\\\":{\\\"value\\\":\\\"$TOKEN\\\"}}\"\n"
	if err := os.WriteFile(client.Binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	broker := Broker{Client: client, Secrets: testVault(t)}
	input := json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`)
	if _, err := broker.Call(context.Background(), "process start", input, "project", []string{"TOKEN"}); err == nil || !strings.Contains(err.Error(), "private output") {
		t.Fatalf("private response error = %v", err)
	}
	if _, err := broker.Call(context.Background(), "env list", json.RawMessage(`{"profile":"project"}`), "project", []string{"TOKEN"}); err == nil {
		t.Fatal("secret injection accepted for metadata command")
	}
}

func TestMergeEnvironmentOverridesAndSorts(t *testing.T) {
	got, err := mergeEnvironment([]string{"Z=old", "A=one"}, []string{"Z=new"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "A=one,Z=new" {
		t.Fatalf("environment = %v", got)
	}
	if _, err = mergeEnvironment(nil, []string{"BAD-NAME=value"}); err == nil {
		t.Fatal("invalid environment accepted")
	}
}

func TestContainsPrivateJSONDecodesEscapedValues(t *testing.T) {
	if !containsPrivateJSON(json.RawMessage(`{"nested":["line\nbreak"]}`), []string{"line\nbreak"}) {
		t.Fatal("escaped private value was not detected")
	}
	if containsPrivateJSON(json.RawMessage(`{"value":"public"}`), []string{"private"}) {
		t.Fatal("public response rejected")
	}
}

func testVault(t *testing.T) secret.Controller {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	controller := secret.Controller{StateDirectory: dir}
	ctx := context.Background()
	if _, err := controller.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CreateProfile(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.SetSecret(ctx, "project", "TOKEN", "private-token-value", false); err != nil {
		t.Fatal(err)
	}
	return controller
}
