package secret

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"loki/internal/fault"
)

func TestGuardedSecretMutationsReplayAndCAS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := Controller{StateDirectory: dir}
	if _, err := controller.Initialize(ctx); err != nil {
		t.Fatal(err)
	}

	createID := "81000000-0000-4000-8000-000000000001"
	created, err := controller.CreateProfileRequest(ctx, "web", createID, 1)
	if err != nil || created["revision"] != uint64(2) || created["request_id"] != createID || created["created"] != true {
		t.Fatalf("create = %#v err=%v", created, err)
	}
	replayed, err := controller.CreateProfileRequest(ctx, "web", createID, 1)
	if err != nil || replayed["revision"] != uint64(2) || replayed["request_id"] != createID {
		t.Fatalf("create replay = %#v err=%v", replayed, err)
	}
	if _, err = controller.CreateProfileRequest(ctx, "other", createID, 1); err == nil || fault.Describe(err).Code != fault.CodeConflict {
		t.Fatalf("changed request replay error = %#v", fault.Describe(err))
	}
	if _, err = controller.CreateProfileRequest(ctx, "other", "81000000-0000-4000-8000-000000000002", 1); err == nil || fault.Describe(err).Code != fault.CodeConflict {
		t.Fatalf("stale revision error = %#v", fault.Describe(err))
	}

	setID := "81000000-0000-4000-8000-000000000003"
	stored, err := controller.SetPublicRequest(ctx, "web", "APP_MODE", "local", setID, 2)
	if err != nil || stored["revision"] != uint64(3) || stored["stored"] != true || stored["name"] != "APP_MODE" {
		t.Fatalf("set public = %#v err=%v", stored, err)
	}
	generateID := "81000000-0000-4000-8000-000000000004"
	generated, err := controller.GenerateRequest(ctx, "web", "SESSION_KEY", generateID, 3, 32)
	if err != nil || generated["revision"] != uint64(4) || generated["generated"] != true || generated["bytes"] != 32 {
		t.Fatalf("generate = %#v err=%v", generated, err)
	}
	generatedReplay, err := controller.GenerateRequest(ctx, "web", "SESSION_KEY", generateID, 3, 32)
	if err != nil || generatedReplay["revision"] != uint64(4) || generatedReplay["request_id"] != generateID {
		t.Fatalf("generate replay = %#v err=%v", generatedReplay, err)
	}
	profile, err := controller.Profile(ctx, "web")
	if err != nil || profile["revision"] != uint64(4) || profile["secret_count"] != 2 {
		t.Fatalf("profile after replay = %#v err=%v", profile, err)
	}

	removeSecretID := "81000000-0000-4000-8000-000000000005"
	removedSecret, err := controller.RemoveSecretRequest(ctx, "web", "SESSION_KEY", removeSecretID, 4)
	if err != nil || removedSecret["revision"] != uint64(5) || removedSecret["removed"] != true {
		t.Fatalf("remove secret = %#v err=%v", removedSecret, err)
	}
	removedSecretReplay, err := controller.RemoveSecretRequest(ctx, "web", "SESSION_KEY", removeSecretID, 4)
	if err != nil || removedSecretReplay["revision"] != uint64(5) {
		t.Fatalf("remove secret replay = %#v err=%v", removedSecretReplay, err)
	}

	removeProfileID := "81000000-0000-4000-8000-000000000006"
	removedProfile, err := controller.RemoveProfileRequest(ctx, "web", removeProfileID, 5)
	if err != nil || removedProfile["revision"] != uint64(6) || removedProfile["removed"] != true {
		t.Fatalf("remove profile = %#v err=%v", removedProfile, err)
	}
	removedProfileReplay, err := controller.RemoveProfileRequest(ctx, "web", removeProfileID, 5)
	if err != nil || removedProfileReplay["revision"] != uint64(6) {
		t.Fatalf("remove profile replay = %#v err=%v", removedProfileReplay, err)
	}
	profiles, err := controller.ProfilesPage(ctx, 0, 10)
	if err != nil || profiles["revision"] != uint64(6) || profiles["total"] != 0 {
		t.Fatalf("profiles after replay = %#v err=%v", profiles, err)
	}
}

func TestGuardedStagedImportReplaysAfterSourceConsumption(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := Controller{StateDirectory: dir}
	if _, err := controller.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := controller.CreateProfileRequest(ctx, "web", "82500000-0000-4000-8000-000000000001", 1)
	if err != nil || created["revision"] != uint64(2) {
		t.Fatalf("create profile = %#v err=%v", created, err)
	}
	inbox := filepath.Join(controller.StateDirectory, "inbox")
	if err = os.Mkdir(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	importID := "0123456789abcdef0123456789abcdef"
	source := filepath.Join(inbox, importID+".env")
	if err = os.WriteFile(source, []byte("TOKEN=synthetic-private\nEMPTY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requestID := "82500000-0000-4000-8000-000000000002"
	first, err := controller.ImportStagedRequest(ctx, "web", importID, requestID, 2)
	if err != nil || first["revision"] != uint64(3) || first["request_id"] != requestID ||
		first["import_id"] != importID || first["count"] != 2 {
		t.Fatalf("staged import = %#v err=%v", first, err)
	}
	if _, err = os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("staged source survived successful import: %v", err)
	}
	replayed, err := controller.ImportStagedRequest(ctx, "web", importID, requestID, 2)
	if err != nil || replayed["revision"] != uint64(3) || replayed["request_id"] != requestID ||
		replayed["import_id"] != importID || replayed["count"] != 2 {
		t.Fatalf("staged import replay = %#v err=%v", replayed, err)
	}
	if _, err = controller.ImportStagedRequest(
		ctx, "web", "fedcba9876543210fedcba9876543210", requestID, 2,
	); err == nil || fault.Describe(err).Code != fault.CodeConflict {
		t.Fatalf("changed staged replay error = %#v", fault.Describe(err))
	}
	profile, err := controller.Profile(ctx, "web")
	if err != nil || profile["revision"] != uint64(3) || profile["secret_count"] != 2 {
		t.Fatalf("profile after import replay = %#v err=%v", profile, err)
	}
}

func TestConcurrentSameSecretMutationReplaysWinner(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := Controller{StateDirectory: dir}
	if _, err := controller.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	requestID := "82000000-0000-4000-8000-000000000001"
	var wg sync.WaitGroup
	results := make(chan map[string]any, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := controller.CreateProfileRequest(ctx, "web", requestID, 1)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent replay error = %v", err)
		}
	}
	for result := range results {
		if result["revision"] != uint64(2) || result["request_id"] != requestID {
			t.Fatalf("concurrent replay result = %#v", result)
		}
	}
	profiles, err := controller.ProfilesPage(ctx, 0, 10)
	if err != nil || profiles["revision"] != uint64(2) || profiles["total"] != 1 {
		t.Fatalf("concurrent final state = %#v err=%v", profiles, err)
	}
}

func TestSecretDocumentReplayLedgerValidation(t *testing.T) {
	if err := Validate(json.RawMessage(`{"version":1,"profiles":{}}`)); err != nil {
		t.Fatal(err)
	}
	valid := json.RawMessage(`{"version":1,"profiles":{},"requests":{"83000000-0000-4000-8000-000000000001":{"fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","revision":2,"result_json":"{}"}}}`)
	if err := Validate(valid); err != nil {
		t.Fatalf("valid request ledger rejected: %v", err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"version":1,"profiles":{},"requests":{"bad":{"fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","revision":2,"result_json":"{}"}}}`),
		json.RawMessage(`{"version":1,"profiles":{},"requests":{"83000000-0000-4000-8000-000000000001":{"fingerprint":"bad","revision":2,"result_json":"{}"}}}`),
		json.RawMessage(`{"version":1,"profiles":{},"requests":{"83000000-0000-4000-8000-000000000001":{"fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","revision":0,"result_json":"{}"}}}`),
	} {
		if err := Validate(raw); err == nil {
			t.Fatalf("invalid request ledger accepted: %s", raw)
		}
	}
}
