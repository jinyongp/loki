package devtools

import (
	"context"
	"encoding/json"
	"errors"
)

type CoordinationMutation string

const (
	CoordinationClaim      CoordinationMutation = "task claim"
	CoordinationTakeover   CoordinationMutation = "task takeover"
	CoordinationResume     CoordinationMutation = "task resume"
	CoordinationCheckpoint CoordinationMutation = "task checkpoint"
	CoordinationRelease    CoordinationMutation = "task release"
	CoordinationDone       CoordinationMutation = "task done"
)

type CoordinationMutationRequest struct {
	RequestID           string
	Target              string
	Workstream          string
	ExpectedRun         string
	Context             string
	Summary             string
	Decisions           []string
	ValidationRecordIDs []string
	Remaining           []string
	NextAction          string
	Blockers            []string
}

type CoordinationMutationResult struct {
	Public       json.RawMessage `json:"-"`
	Profile      string          `json:"-"`
	Context      string          `json:"-"`
	ContextValid bool            `json:"-"`
	RunID        string          `json:"-"`
	TaskID       string          `json:"-"`
}

func isCoordinationMutation(name string) bool {
	switch CoordinationMutation(name) {
	case CoordinationClaim, CoordinationTakeover, CoordinationResume, CoordinationCheckpoint, CoordinationRelease, CoordinationDone:
		return true
	default:
		return false
	}
}

func (c *Client) MutateCoordination(ctx context.Context, directory string, action CoordinationMutation, request CoordinationMutationRequest) (CoordinationMutationResult, error) {
	if !isCoordinationMutation(string(action)) {
		return CoordinationMutationResult{}, errors.New("unsupported devtools coordination mutation")
	}
	project, err := c.InspectProject(ctx, directory)
	if err != nil {
		return CoordinationMutationResult{}, err
	}
	input := map[string]any{
		"profile":    project.Profile,
		"request-id": request.RequestID,
	}
	addTarget := func(required bool) error {
		if request.Target == "" {
			if required {
				return errors.New("devtools coordination target is required")
			}
			return nil
		}
		input["args"] = []string{request.Target}
		return nil
	}
	addContext := func() error {
		if request.Context == "" {
			return errors.New("devtools coordination context is required")
		}
		input["context"] = request.Context
		return nil
	}
	addProgress := func(requireSummary bool) error {
		if requireSummary && request.Summary == "" {
			return errors.New("devtools coordination summary is required")
		}
		if request.Summary != "" {
			input["summary"] = request.Summary
		}
		if len(request.Decisions) > 0 {
			input["decisions"] = request.Decisions
		}
		if len(request.ValidationRecordIDs) > 0 {
			input["validation-record-ids"] = request.ValidationRecordIDs
		}
		if len(request.Remaining) > 0 {
			input["remaining"] = request.Remaining
		}
		if request.NextAction != "" {
			input["next-action"] = request.NextAction
		}
		if len(request.Blockers) > 0 {
			input["blockers"] = request.Blockers
		}
		return nil
	}

	switch action {
	case CoordinationClaim:
		if err := addTarget(false); err != nil {
			return CoordinationMutationResult{}, err
		}
		if request.Workstream != "" && request.Target != "" {
			return CoordinationMutationResult{}, errors.New("choose a task target or workstream filter")
		}
		if request.Workstream != "" {
			input["workstream"] = request.Workstream
		}
		confined, err := c.coordinationExecutionDirectory(directory)
		if err != nil {
			return CoordinationMutationResult{}, err
		}
		input["dir"] = confined
	case CoordinationTakeover:
		if err := addTarget(true); err != nil {
			return CoordinationMutationResult{}, err
		}
		if request.ExpectedRun == "" {
			return CoordinationMutationResult{}, errors.New("devtools expected run is required")
		}
		input["expected-run"] = request.ExpectedRun
		confined, err := c.coordinationExecutionDirectory(directory)
		if err != nil {
			return CoordinationMutationResult{}, err
		}
		input["dir"] = confined
	case CoordinationResume:
		if err := addTarget(false); err != nil {
			return CoordinationMutationResult{}, err
		}
		if err := addContext(); err != nil {
			return CoordinationMutationResult{}, err
		}
	case CoordinationCheckpoint:
		if err := addTarget(true); err != nil {
			return CoordinationMutationResult{}, err
		}
		if err := addContext(); err != nil {
			return CoordinationMutationResult{}, err
		}
		if err := addProgress(true); err != nil {
			return CoordinationMutationResult{}, err
		}
	case CoordinationRelease:
		if err := addTarget(true); err != nil {
			return CoordinationMutationResult{}, err
		}
		if err := addContext(); err != nil {
			return CoordinationMutationResult{}, err
		}
		if err := addProgress(false); err != nil {
			return CoordinationMutationResult{}, err
		}
	case CoordinationDone:
		if err := addTarget(true); err != nil {
			return CoordinationMutationResult{}, err
		}
		if err := addContext(); err != nil {
			return CoordinationMutationResult{}, err
		}
		if request.Summary == "" {
			return CoordinationMutationResult{}, errors.New("devtools coordination summary is required")
		}
		input["summary"] = request.Summary
		if len(request.ValidationRecordIDs) > 0 {
			input["validation-record-ids"] = request.ValidationRecordIDs
		}
	}

	rawInput, err := json.Marshal(input)
	if err != nil {
		return CoordinationMutationResult{}, errors.New("encode devtools coordination mutation")
	}
	raw, err := c.call(ctx, string(action), rawInput, c.Env)
	if err != nil {
		return CoordinationMutationResult{}, err
	}
	return c.decodeCoordinationMutation(raw, project.Profile)
}

func (c *Client) coordinationExecutionDirectory(directory string) (string, error) {
	if c.Workspace == nil {
		return "", errors.New("devtools workspace is unavailable")
	}
	if directory == "" {
		directory = "."
	}
	confined, err := c.Workspace.ResolveCWD(directory)
	if err != nil {
		return "", errors.New("devtools coordination directory is outside the workspace")
	}
	return confined, nil
}

func (c *Client) decodeCoordinationMutation(raw json.RawMessage, expectedProfile string) (CoordinationMutationResult, error) {
	var public map[string]any
	if err := decodeObject(raw, &public); err != nil {
		return CoordinationMutationResult{}, errors.New("devtools returned invalid coordination mutation")
	}
	profile, _ := public["profile"].(string)
	if profile == "" || profile != expectedProfile {
		return CoordinationMutationResult{}, errors.New("devtools returned mutation for a different profile")
	}
	result := CoordinationMutationResult{Profile: profile}
	if contextValue, exists := public["context"]; exists {
		if contextValue != nil {
			contextText, ok := contextValue.(string)
			if !ok || contextText == "" {
				return CoordinationMutationResult{}, errors.New("devtools returned invalid coordination context")
			}
			result.Context = contextText
		}
		delete(public, "context")
	}
	if valid, exists := public["context_valid"]; exists {
		value, ok := valid.(bool)
		if !ok {
			return CoordinationMutationResult{}, errors.New("devtools returned invalid coordination context status")
		}
		result.ContextValid = value
	}
	if run, ok := public["run"].(map[string]any); ok {
		result.RunID, _ = run["id"].(string)
		result.TaskID, _ = run["task_id"].(string)
	}
	if item, ok := public["item"].(map[string]any); ok && result.TaskID == "" {
		if kind, _ := item["kind"].(string); kind == "task" {
			result.TaskID, _ = item["id"].(string)
		}
	}
	if err := c.sanitizeCoordinationValue(public); err != nil {
		return CoordinationMutationResult{}, err
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		return CoordinationMutationResult{}, errors.New("encode sanitized coordination mutation")
	}
	result.Public = encoded
	return result, nil
}
