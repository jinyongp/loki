package secret

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// Legacy source writers exist only in migration fixtures. Shipping code has
// no application AES mutation or environment injection API.
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
	return c
}

func (c Controller) CreateProfile(ctx context.Context, name string) (map[string]any, error) {
	return c.mutateScoped(ctx, nil, false, func(d document) (map[string]any, error) {
		object(d["profiles"])[name] = map[string]any{"secrets": map[string]any{}}
		return map[string]any{}, nil
	})
}

func (c Controller) SetSecret(ctx context.Context, name, key, value string, public bool) (map[string]any, error) {
	return c.mutateScoped(ctx, nil, false, func(d document) (map[string]any, error) {
		p, err := profile(d, name)
		if err != nil {
			return nil, err
		}
		object(p["secrets"])[key] = value
		return map[string]any{}, nil
	})
}
