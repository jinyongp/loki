package mcptransport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loki/internal/portguard"
)

func TestWorkspacePortOwnershipFallback(t *testing.T) {
	ports, err := portguard.NewPolicy(18765, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
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
			result, err := InspectWorkspacePort(t.Context(), ports, inspect, runtime, 43000)
			if (err != nil) != test.wantErr || (portListener(result) != nil) != test.want {
				t.Fatal(result, err)
			}
		})
	}
}

func TestProtectedWorkspacePortSkipsAllOwnershipFallback(t *testing.T) {
	ports, err := portguard.NewPolicy(18765)
	if err != nil {
		t.Fatal(err)
	}
	guardCalls := 0
	dockerCalls := 0
	inspect := func(context.Context, int) (map[string]any, error) {
		guardCalls++
		return map[string]any{"in_use": true}, nil
	}
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
		dockerCalls++
		return json.RawMessage(`{"in_use":true,"listeners":[{"command":"container"}]}`), nil
	})
	if _, err := InspectWorkspacePort(t.Context(), ports, inspect, runtime, 18765); !errors.Is(err, portguard.ErrProtected) {
		t.Fatalf("protected port error = %v", err)
	}
	if guardCalls != 0 || dockerCalls != 0 {
		t.Fatalf("protected port reached ownership fallback: guard=%d docker=%d", guardCalls, dockerCalls)
	}
}
