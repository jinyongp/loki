//go:build linux

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/privateio"
	legacyfixture "loki/internal/state"
	secret "loki/modules/secrets"
)

func TestSourceDispositionAcknowledgementLossPreservesExactOriginalFence(t *testing.T) {
	for _, action := range []string{"commit_source", "rollback_source"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			source := secret.Controller{StateDirectory: filepath.Join(t.TempDir(), "source")}
			if _, err := (legacyfixture.Store{Dir: source.StateDirectory}).Initialize(ctx, json.RawMessage(`{"version":1,"profiles":{"app":{"secrets":{"TOKEN":"synthetic-source-disposition-canary"}}}}`)); err != nil {
				t.Fatal(err)
			}
			resource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174571", Alias: "app", Repository: "owner/repo", Network: "none", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}}
			plan, err := source.PrepareApplicationMigration(ctx, []secret.MigrationProfileMapping{{Profile: "app", Resource: resource.ID, Environment: "common", Keys: []secret.MigrationKeyMapping{{Source: "TOKEN"}}}}, []controlpolicy.ApplicationResource{resource}, nil)
			if err != nil {
				t.Fatal(err)
			}
			operation := "123e4567-e89b-42d3-a456-426614174572"
			if err := source.BeginApplicationMigration(ctx, plan.Revision, operation); err != nil {
				t.Fatal(err)
			}
			manifest := SourceTransferManifest{Action: action, Version: 1, Operation: operation, Policy: strings.Repeat("a", 64), SourceRoot: source.StateDirectory, Plan: plan, Writes: []lifecycle.MigrationWriteCheckpoint{}}
			lost := errors.New("synthetic source disposition ACK loss")
			for attempt := 0; attempt < 2; attempt++ {
				host, helper := net.Pipe()
				done := make(chan error, 1)
				go func() { done <- ServeSourceTransferManifest(ctx, helper) }()
				if err := WriteSourceTransferManifest(host, manifest); err != nil {
					t.Fatal(err)
				}
				err := CheckpointSourceTransfer(ctx, host, operation, func(event SourceEvent) error {
					if event != (SourceEvent{Operation: operation, Type: "source_finished"}) {
						t.Fatal("source disposition returned noncanonical metadata")
					}
					if attempt == 0 {
						return lost
					}
					return nil
				})
				producerErr := <-done
				if attempt == 0 && (!errors.Is(err, lost) || producerErr == nil) || attempt == 1 && (err != nil || producerErr != nil) {
					t.Fatal("original source disposition ACK replay", err, producerErr)
				}
			}
			fence, err := source.ApplicationMigrationState(ctx)
			if err != nil || action == "rollback_source" && fence != nil || action == "commit_source" && (fence == nil || fence.State != "committed" || fence.Operation != operation) {
				t.Fatal("source disposition differs from original host result", err)
			}
		})
	}
}

func TestSourceManifestRejectsAliasesAndUnknownFieldsBeforeSourceAccess(t *testing.T) {
	source := filepath.Join(t.TempDir(), "untouched-source")
	resource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174561", Alias: "app", Repository: "owner/repo", Network: "none", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}}
	manifest := SourceTransferManifest{Version: 1, Operation: "123e4567-e89b-42d3-a456-426614174562", Policy: strings.Repeat("a", 64), SourceRoot: source, TargetSocket: "/tmp/source-manifest-broker.sock", TargetUID: 10006, Plan: appvalues.MigrationPlan{Version: 1, Revision: 1, Values: 1, Resources: []controlpolicy.ApplicationResource{resource}, Mappings: []appvalues.MigrationProfileMapping{{Profile: "app", Resource: resource.ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "TOKEN", Name: "TOKEN", Kind: "secret"}}}}}, Writes: []lifecycle.MigrationWriteCheckpoint{}}
	canonical, err := json.Marshal(manifest)
	if err != nil || !manifest.Valid() {
		t.Fatal("invalid public manifest fixture", err)
	}
	for _, test := range []struct{ name, suffix string }{
		{"duplicate field", `,"version":1}`},
		{"unknown field", `,"value":"not-a-source-control-field"}`},
		{"alias field", `,"Version":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			host, helper := net.Pipe()
			defer host.Close()
			done := make(chan error, 1)
			go func() { done <- ServeSourceTransferManifest(ctx, helper) }()
			body := append(append([]byte{}, canonical[:len(canonical)-1]...), []byte(test.suffix)...)
			if err := privateio.WriteBytes(host, body, sourceManifestLimit); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("noncanonical privileged manifest was accepted")
			}
			if _, err := os.Lstat(source); !os.IsNotExist(err) {
				t.Fatal("invalid manifest touched a source root", err)
			}
		})
	}
}
