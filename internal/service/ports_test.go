package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type runtimeFixture func(context.Context, any) (json.RawMessage, error)

func (f runtimeFixture) Call(ctx context.Context, r any) (json.RawMessage, error) { return f(ctx, r) }
func TestWorkspacePortOwnershipFallback(t *testing.T) {
	owned := map[string]any{"in_use": true, "listeners": []map[string]any{{"command": "node"}}}
	empty := map[string]any{"in_use": false, "listeners": []any{}}
	denied := errors.New("untrusted listener")
	for _, test := range []struct {
		name                string
		guard, docker       map[string]any
		guardErr, dockerErr error
		want                bool
		wantErr             bool
	}{
		{"guard", owned, nil, nil, denied, true, false},
		{"docker", empty, owned, nil, nil, true, false},
		{"docker-resolves-guard", nil, owned, denied, nil, true, false},
		{"empty", empty, empty, nil, nil, false, false},
		{"guard-denied", nil, empty, denied, nil, false, true},
		{"both-denied", nil, nil, denied, denied, false, true},
		{"empty-docker-unavailable", empty, nil, nil, denied, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspect := func(context.Context, int) (map[string]any, error) { return test.guard, test.guardErr }
			runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
				raw, _ := json.Marshal(test.docker)
				return raw, test.dockerErr
			})
			result, err := InspectWorkspacePort(t.Context(), inspect, runtime, 43000)
			if (err != nil) != test.wantErr || (portListener(result) != nil) != test.want {
				t.Fatal(result, err)
			}
		})
	}
	if _, err := InspectWorkspacePort(t.Context(), nil, nil, 8765); err == nil {
		t.Fatal("protected port accepted")
	}
}
