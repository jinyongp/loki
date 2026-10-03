package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loki/internal/process"
	"loki/internal/rpc"
)

// Discovery checks many bindings together. Share one short-lived observation
// of each protected peer, while activation is checked independently on every
// call. Readiness observations never confer authority.
type mcpPeerReadiness struct {
	mu      sync.Mutex
	entries map[string]mcpPeerObservation
}

func (r *mcpPeerReadiness) native(ctx context.Context, module, binary string) error {
	if module != "git" && module != "workspace" {
		return nil
	}
	if !filepath.IsAbs(binary) {
		return fmt.Errorf("%s has no declared native program; run loki doctor", module)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := "native:" + module + ":" + binary
	if cached, ok := r.entries[key]; ok && time.Now().Before(cached.until) {
		return cached.err
	}
	result, err := process.Run(ctx, process.Spec{Argv: []string{binary, "--version"}, CWD: "/", Env: process.Environment(), Timeout: 2 * time.Second, MaxOutput: 4096})
	prefix := "git version "
	if module == "workspace" {
		prefix = "ripgrep "
	}
	if err != nil || result.ExitCode != 0 || result.Truncated || result.TimedOut || result.Canceled || !strings.HasPrefix(result.Output, prefix) {
		err = fmt.Errorf("%s native program is unavailable; run loki doctor on its execution host", module)
	}
	if r.entries == nil {
		r.entries = map[string]mcpPeerObservation{}
	}
	if ctx.Err() == nil {
		r.entries[key] = mcpPeerObservation{until: time.Now().Add(time.Second), err: err}
	}
	return err
}

type mcpPeerObservation struct {
	until  time.Time
	status mcpRuntimeReadiness
	err    error
}

type mcpRuntimeReadiness struct {
	Initialized bool            `json:"initialized"`
	Tools       map[string]bool `json:"tools"`
	GitHub      struct {
		Configured          bool `json:"configured"`
		CredentialAvailable bool `json:"credential_available"`
		Targets             int  `json:"target_count"`
	} `json:"github"`
}

func (r *mcpPeerReadiness) observe(ctx context.Context, peer string, caller rpc.Caller, operation string) (mcpRuntimeReadiness, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached, ok := r.entries[peer]; ok && time.Now().Before(cached.until) {
		return cached.status, cached.err
	}
	var status mcpRuntimeReadiness
	requestContext := ctx
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err := rpc.DecodeCall(ctx, caller, map[string]any{"operation": operation}, &status)
	if err == nil && !status.Initialized {
		err = errors.New("protected service has not initialized")
	}
	if r.entries == nil {
		r.entries = map[string]mcpPeerObservation{}
	}
	// Cancellation belongs to this request and must not poison other clients.
	if requestContext.Err() == nil {
		r.entries[peer] = mcpPeerObservation{until: time.Now().Add(time.Second), status: status, err: err}
	}
	return status, err
}

func (r *mcpPeerReadiness) authorize(ctx context.Context, module string, runtime, executor rpc.Caller) error {
	switch module {
	case "github", "secrets", "coordination", "execution", "sharing":
		status, err := r.observe(ctx, "runtime", runtime, "status")
		if err != nil {
			return errors.New("protected runtime is unavailable; run loki doctor on its execution host")
		}
		if !status.Tools[module] {
			return fmt.Errorf("%s is not initialized in the protected runtime; restart the selected services", module)
		}
		if module == "github" && (!status.GitHub.Configured || !status.GitHub.CredentialAvailable || status.GitHub.Targets == 0) {
			return errors.New("GitHub requires configuration and installation access; run loki integrations setup github")
		}
	}
	switch module {
	case "git", "execution", "sharing":
		if _, err := r.observe(ctx, "executor", executor, "health"); err != nil {
			return errors.New("confined executor is unavailable; run loki doctor on its execution host")
		}
	}
	return nil
}
