package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"loki/internal/devtools"
	"loki/internal/mcpserver"
)

const maxDevtoolsSessionClaims = 256

type devtoolsClaimBinding struct {
	Profile string
	TaskID  string
	RunID   string
	context string
}

type DevtoolsSessionClaims struct {
	mu       sync.Mutex
	bindings map[string]devtoolsClaimBinding
}

func NewDevtoolsSessionClaims() *DevtoolsSessionClaims {
	return &DevtoolsSessionClaims{bindings: map[string]devtoolsClaimBinding{}}
}

func (s *DevtoolsSessionClaims) get(sessionID string) (devtoolsClaimBinding, bool) {
	if s == nil || sessionID == "" {
		return devtoolsClaimBinding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[sessionID]
	return binding, ok
}

func (s *DevtoolsSessionClaims) bind(sessionID string, binding devtoolsClaimBinding) error {
	if s == nil || sessionID == "" || binding.context == "" || binding.RunID == "" || binding.TaskID == "" || binding.Profile == "" {
		return errors.New("invalid devtools session claim")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.bindings[sessionID]; !exists && len(s.bindings) >= maxDevtoolsSessionClaims {
		return errors.New("devtools session claim capacity is exhausted")
	}
	s.bindings[sessionID] = binding
	return nil
}

func (s *DevtoolsSessionClaims) clear(sessionID, runID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if binding, ok := s.bindings[sessionID]; ok && (runID == "" || binding.RunID == runID) {
		delete(s.bindings, sessionID)
	}
}

type DevtoolsSessionMutationRequest struct {
	CWD                 string
	TargetID            string
	WorkstreamID        string
	ExpectedRunID       string
	RequestID           string
	Summary             string
	Decisions           []string
	ValidationRecordIDs []string
	Remaining           []string
	NextAction          string
	Blockers            []string
}

type DevtoolsSessionCoordination struct {
	Runtime RuntimeCaller
	Claims  *DevtoolsSessionClaims
}

func (c *DevtoolsSessionCoordination) MutateMCP(ctx context.Context, action devtools.CoordinationMutation, request DevtoolsSessionMutationRequest) (map[string]any, error) {
	sessionID, ok := mcpserver.SessionID(ctx)
	if !ok {
		return nil, errors.New("MCP session binding is unavailable")
	}
	return c.Mutate(ctx, sessionID, action, request)
}

type devtoolsRuntimeMutationResult struct {
	Public       json.RawMessage `json:"public"`
	Context      string          `json:"context"`
	ContextValid bool            `json:"context_valid"`
	RunID        string          `json:"run_id"`
	TaskID       string          `json:"task_id"`
	Profile      string          `json:"profile"`
}

func (c *DevtoolsSessionCoordination) Mutate(ctx context.Context, sessionID string, action devtools.CoordinationMutation, request DevtoolsSessionMutationRequest) (map[string]any, error) {
	if c == nil || c.Runtime == nil || c.Claims == nil {
		return nil, errors.New("devtools session coordination is unavailable")
	}
	if sessionID == "" {
		return nil, errors.New("MCP session binding is unavailable")
	}
	binding, owned := c.Claims.get(sessionID)
	if action == devtools.CoordinationClaim && owned {
		return nil, errors.New("MCP session already owns a devtools run")
	}
	internal := map[string]any{
		"operation": "devtools_coordination_mutate", "action": string(action), "cwd": request.CWD,
		"target_id": request.TargetID, "workstream_id": request.WorkstreamID, "expected_run_id": request.ExpectedRunID,
		"request_id": request.RequestID, "summary": request.Summary, "decisions": request.Decisions,
		"validation_record_ids": request.ValidationRecordIDs, "remaining": request.Remaining,
		"next_action": request.NextAction, "blockers": request.Blockers,
	}
	needsOwnership := action == devtools.CoordinationResume || action == devtools.CoordinationCheckpoint || action == devtools.CoordinationRelease || action == devtools.CoordinationDone
	if needsOwnership {
		if !owned {
			return nil, errors.New("MCP session does not own a devtools run")
		}
		switch action {
		case devtools.CoordinationResume, devtools.CoordinationDone:
			if request.TargetID == "" {
				internal["target_id"] = binding.TaskID
			} else if request.TargetID != binding.TaskID {
				return nil, errors.New("devtools task does not match the MCP session claim")
			}
		case devtools.CoordinationCheckpoint, devtools.CoordinationRelease:
			if request.TargetID == "" {
				internal["target_id"] = binding.RunID
			} else if request.TargetID != binding.RunID {
				return nil, errors.New("devtools run does not match the MCP session claim")
			}
		}
		internal["context"] = binding.context
	}
	var result devtoolsRuntimeMutationResult
	if err := runtimeDecode(ctx, c.Runtime, internal, &result); err != nil {
		return nil, err
	}
	public, err := publicCoordinationMutation(result.Public)
	if err != nil {
		return nil, err
	}
	if action == devtools.CoordinationClaim || action == devtools.CoordinationTakeover {
		claimed, _ := public["claimed"].(bool)
		if claimed {
			if result.Context == "" || !result.ContextValid || result.RunID == "" || result.TaskID == "" || result.Profile == "" {
				return nil, errors.New("devtools claim returned incomplete private ownership state")
			}
			if err := c.Claims.bind(sessionID, devtoolsClaimBinding{Profile: result.Profile, TaskID: result.TaskID, RunID: result.RunID, context: result.Context}); err != nil {
				return nil, err
			}
		}
		return public, nil
	}
	if result.Profile != "" && result.Profile != binding.Profile {
		return nil, errors.New("devtools mutation returned a different profile")
	}
	if action == devtools.CoordinationCheckpoint || action == devtools.CoordinationResume {
		if !result.ContextValid {
			c.Claims.clear(sessionID, binding.RunID)
			return nil, errors.New("devtools session claim is no longer valid")
		}
	}
	if action == devtools.CoordinationRelease || action == devtools.CoordinationDone {
		c.Claims.clear(sessionID, binding.RunID)
	}
	return public, nil
}

func publicCoordinationMutation(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, errors.New("devtools mutation returned no public result")
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, errors.New("devtools mutation returned invalid public result")
	}
	if containsPrivateCoordinationField(value) {
		return nil, errors.New("devtools mutation leaked private context")
	}
	return value, nil
}

func containsPrivateCoordinationField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "context" || key == "credential" || key == "claim_context" || containsPrivateCoordinationField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsPrivateCoordinationField(child) {
				return true
			}
		}
	}
	return false
}
