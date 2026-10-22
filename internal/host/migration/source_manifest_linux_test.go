//go:build linux

package migration

import (
	"context"
	"encoding/json"
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
)

func TestSourceManifestRejectsAliasesAndUnknownFieldsBeforeSourceAccess(t *testing.T) {
	source := filepath.Join(t.TempDir(), "untouched-source")
	resource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174561", Alias: "app", Repository: "owner/repo", Network: "none", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}}
	manifest := SourceTransferManifest{Version: 1, Operation: "123e4567-e89b-42d3-a456-426614174562", Policy: strings.Repeat("a", 64), SourceRoot: source, TargetSocket: "/tmp/source-manifest-broker.sock", TargetUID: 10006, Plan: appvalues.MigrationPlan{Version: 1, Revision: 1, Values: 1, Resources: []controlpolicy.ApplicationResource{resource}, Mappings: []appvalues.MigrationProfileMapping{{Profile: "app", Resource: resource.ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "TOKEN", Name: "TOKEN", Kind: "secret"}}}}}, Writes: []lifecycle.MigrationWriteCheckpoint{}}
	canonical, err := json.Marshal(manifest)
	if err != nil || !manifest.valid() {
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
