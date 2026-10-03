package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type readinessCaller struct {
	calls    int
	response string
	err      error
}

func (c *readinessCaller) Call(context.Context, any) (json.RawMessage, error) {
	c.calls++
	return json.RawMessage(c.response), c.err
}

func TestMCPReadinessSharesObservationsAndRecoversAfterConfiguration(t *testing.T) {
	r := &mcpPeerReadiness{}
	runtime := &readinessCaller{response: `{"initialized":true,"tools":{"github":true,"secrets":true},"github":{"configured":false}}`}
	if r.authorize(t.Context(), "github", runtime, nil) == nil {
		t.Fatal("unconfigured GitHub was exposed")
	}
	if err := r.authorize(t.Context(), "secrets", runtime, nil); err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 {
		t.Fatal("discovery repeatedly queried the same protected peer")
	}
	runtime.response = `{"initialized":true,"tools":{"github":true},"github":{"configured":true,"credential_available":true,"target_count":1}}`
	r.entries["runtime"] = mcpPeerObservation{until: time.Time{}}
	if err := r.authorize(t.Context(), "github", runtime, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.authorize(t.Context(), "workspace", nil, nil); err != nil {
		t.Fatal("workspace acquired an unrelated protected service dependency")
	}
}

func TestMCPGitReadinessRequiresExecutorWithoutProviderOrSecretRuntime(t *testing.T) {
	r := &mcpPeerReadiness{}
	executor := &readinessCaller{response: `{"initialized":true}`}
	if err := r.authorize(t.Context(), "git", nil, executor); err != nil {
		t.Fatal(err)
	}
	r.entries["executor"] = mcpPeerObservation{until: time.Time{}}
	executor.err = errors.New("private diagnostic")
	if err := r.authorize(t.Context(), "git", nil, executor); err == nil || err.Error() == "private diagnostic" {
		t.Fatal("unavailable executor was accepted or private diagnostics leaked")
	}
}
