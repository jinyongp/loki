//go:build linux

package secretbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	controlpolicy "loki/internal/control/policy"
	provider "loki/modules/secrets/devtools"
)

func TestRealMigrationPrivateComparisonRequiresDurableBinding(t *testing.T) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider required")
		}
		t.Skip("native values suite supplies the pinned provider")
	}
	root, err := os.MkdirTemp("/tmp", "loki-mc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	p, err := provider.New(ctx, binary, filepath.Join(root, "dvt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Protection(ctx, "init", []byte("synthetic-migration-compare-passphrase")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _ = p.Protection(cleanup, "lock", nil)
	})
	r := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174451", Alias: "web", Repository: "owner/repo", Network: "none",
		Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret", "EMPTY": "secret", "URL": "variable"}}
	expected := []MigrationExpected{{Name: "TOKEN", Kind: "secret", Value: []byte("private-comparison-canary\n'\"<&>")}, {Name: "EMPTY", Kind: "secret", Value: []byte{}}, {Name: "URL", Kind: "variable", Value: []byte("https://public.example")}}
	for i, v := range expected {
		revision, err := p.Revision(ctx, r.Profile(), "common")
		if err != nil {
			t.Fatal(err)
		}
		request := fmt.Sprintf("123e4567-e89b-42d3-a456-42661417445%d", i+2)
		if v.Kind == "secret" {
			_, err = p.SetSecret(ctx, r.Profile(), "common", v.Name, v.Value, revision, request, false)
		} else {
			_, err = p.SetPublic(ctx, r.Profile(), "common", v.Name, string(v.Value), revision, request)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	options := MigrationComparisonOptions{Provider: p, Operation: "123e4567-e89b-42d3-a456-426614174461", Attempt: "123e4567-e89b-42d3-a456-426614174462", Runner: "123e4567-e89b-42d3-a456-426614174463", Instance: "123e4567-e89b-42d3-a456-426614174464", Policy: strings.Repeat("a", 64), Resource: r, Environment: "common", Expected: expected}
	errCheckpoint := errors.New("synthetic durable host checkpoint failure")
	options.Persist = func(binding MigrationComparisonBinding) error {
		if binding.Operation != options.Operation || binding.Policy != options.Policy || binding.Resource != r.ID || binding.Execution == "" || binding.Input == "" || binding.CorrelationID == "" {
			t.Fatal("incomplete original-operation private binding")
		}
		return errCheckpoint
	}
	if result, err := CompareMigration(ctx, options); !errors.Is(err, errCheckpoint) || result.Matched {
		t.Fatal("approved delivery despite failed host checkpoint", err)
	}
	options.Attempt = "123e4567-e89b-42d3-a456-426614174465"
	checkpointed := false
	options.Persist = func(MigrationComparisonBinding) error { checkpointed = true; return nil }
	result, err := CompareMigration(ctx, options)
	if err != nil || !result.Matched || result.Count != len(expected) || !checkpointed {
		t.Fatal("private exact comparison failed", err)
	}
	if _, err := json.Marshal(expected[0]); err == nil || fmt.Sprintf("%+v", expected[0]) != "[REDACTED]" {
		t.Fatal("expected values are serializable/formattable")
	}
	address, err := p.Address(ctx, options.Runner, options.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(address.Endpoint); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed comparison left its owned receiver endpoint", err)
	}
	// A fresh validation attempt reads again after a lost host acknowledgement.
	// It does not construct or retry any external application execution.
	options.Attempt = "123e4567-e89b-42d3-a456-426614174466"
	options.Instance = "123e4567-e89b-42d3-a456-426614174467"
	if result, err := CompareMigration(ctx, options); err != nil || !result.Matched {
		t.Fatal("fresh private comparison failed after lost acknowledgement", err)
	}
	options.Attempt = "123e4567-e89b-42d3-a456-426614174468"
	options.Instance = "123e4567-e89b-42d3-a456-426614174469"
	options.Expected = []MigrationExpected{{Name: "TOKEN", Kind: "secret", Value: []byte("incorrect-expected-value")}}
	if result, err := CompareMigration(ctx, options); err == nil || result.Matched {
		t.Fatal("mismatching logical value reported a match")
	}
}
