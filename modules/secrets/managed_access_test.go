package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Seed the existing on-disk shape directly, without using managed provisioning.
// The API guard must protect data written by the previous implementation too.
func managedAccessFixture(t *testing.T, configured bool) Controller {
	t.Helper()
	c := Controller{StateDirectory: filepath.Join(t.TempDir(), "vault")}
	raw := json.RawMessage(`{"version":1,"profiles":{"web":{"secrets":{"TOKEN":"synthetic-application"}}}}`)
	if configured {
		raw = json.RawMessage(`{"version":1,"profiles":{"web":{"secrets":{"TOKEN":"synthetic-application"}},"github-app":{"secrets":{"PRIVATE_KEY":"synthetic-platform","EMPTY":""}}}}`)
	}
	if _, err := c.backend().Initialize(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.inbox(), 0700); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestApplicationOperationsRejectManagedProfiles(t *testing.T) {
	const importID = "0123456789abcdef0123456789abcdef"
	operations := []struct {
		name string
		run  func(context.Context, Controller) error
	}{
		{"profile", func(ctx context.Context, c Controller) error { _, err := c.Profile(ctx, "github-app"); return err }},
		{"create", func(ctx context.Context, c Controller) error {
			_, err := c.CreateProfile(ctx, "github-app")
			return err
		}},
		{"remove-profile", func(ctx context.Context, c Controller) error {
			_, err := c.RemoveProfile(ctx, "github-app")
			return err
		}},
		{"set-public", func(ctx context.Context, c Controller) error {
			_, err := c.SetSecret(ctx, "github-app", "PRIVATE_KEY", "replacement", true)
			return err
		}},
		{"set-private", func(ctx context.Context, c Controller) error {
			_, err := c.SetSecret(ctx, "github-app", "PRIVATE_KEY", "replacement", false)
			return err
		}},
		{"set-other-key", func(ctx context.Context, c Controller) error {
			_, err := c.SetSecret(ctx, "github-app", "OTHER", "replacement", true)
			return err
		}},
		{"generate", func(ctx context.Context, c Controller) error {
			_, err := c.Generate(ctx, "github-app", "EMPTY", 32)
			return err
		}},
		{"remove-secret", func(ctx context.Context, c Controller) error {
			_, err := c.RemoveSecret(ctx, "github-app", "PRIVATE_KEY")
			return err
		}},
		{"import-values", func(ctx context.Context, c Controller) error {
			_, err := c.ImportValues(ctx, "github-app", map[string]string{"PRIVATE_KEY": "replacement"})
			return err
		}},
		{"import-staged", func(ctx context.Context, c Controller) error {
			_, err := c.ImportStaged(ctx, "github-app", importID)
			return err
		}},
		{"environment", func(ctx context.Context, c Controller) error {
			_, err := c.ResolveEnvironment(ctx, "github-app", []string{"PRIVATE_KEY"})
			return err
		}},
		{"empty-environment", func(ctx context.Context, c Controller) error {
			_, err := c.ResolveEnvironment(ctx, "github-app", nil)
			return err
		}},
	}
	for _, state := range []struct {
		name       string
		configured bool
	}{{"unconfigured", false}, {"existing", true}} {
		t.Run(state.name, func(t *testing.T) {
			for _, operation := range operations {
				t.Run(operation.name, func(t *testing.T) {
					c := managedAccessFixture(t, state.configured)
					source := filepath.Join(c.inbox(), importID+".env")
					input := []byte("PRIVATE_KEY=synthetic-import\n")
					if err := os.WriteFile(source, input, 0600); err != nil {
						t.Fatal(err)
					}
					before, err := c.backend().Load(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if err = operation.run(t.Context(), c); err == nil || !strings.Contains(err.Error(), "managed platform") {
						t.Fatal("application operation did not reject the managed platform profile")
					}
					after, err := c.backend().Load(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before.Data, after.Data) {
						t.Fatal("denied operation changed the stored document")
					}
					if data, err := os.ReadFile(source); err != nil || !bytes.Equal(data, input) {
						t.Fatal("denied operation consumed or changed staged input")
					}
				})
			}
		})
	}
}

func TestApplicationDiscoveryHidesManagedProfiles(t *testing.T) {
	c := managedAccessFixture(t, true)
	result, err := c.Profiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items := result["profiles"].([]map[string]any)
	if len(items) != 1 || items[0]["name"] != "web" {
		t.Fatal("managed profile was exposed through application discovery")
	}
	plan, err := c.ResolveEnvironment(t.Context(), "web", []string{"TOKEN"})
	if err != nil || len(plan.Entries()) != 1 {
		t.Fatal("ordinary application environment was blocked")
	}
}
