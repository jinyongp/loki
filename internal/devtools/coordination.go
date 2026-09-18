package devtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"loki/internal/policy"
)

type CoordinationQuery string

const (
	CoordinationTaskNext          CoordinationQuery = "task next"
	CoordinationTaskShow          CoordinationQuery = "task show"
	CoordinationTaskCurrent       CoordinationQuery = "task current"
	CoordinationTaskContext       CoordinationQuery = "task context"
	CoordinationTaskHistory       CoordinationQuery = "task history"
	CoordinationCheckpointList    CoordinationQuery = "task checkpoint list"
	CoordinationWorkstreamList    CoordinationQuery = "task workstream list"
	CoordinationWorkstreamShow    CoordinationQuery = "task workstream show"
	CoordinationWorkstreamContext CoordinationQuery = "task workstream context"
	CoordinationWorkstreamHistory CoordinationQuery = "task workstream history"
)

type CoordinationRequest struct {
	Target     string
	Workstream string
	Cursor     string
	Limit      int
}

type CoordinationProjection struct {
	Profile      string            `json:"profile"`
	Revision     int               `json:"revision"`
	Item         json.RawMessage   `json:"item,omitempty"`
	Items        []json.RawMessage `json:"items,omitempty"`
	NextCursor   *string           `json:"next_cursor,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	Truncated    bool              `json:"truncated,omitempty"`
	Documents    json.RawMessage   `json:"documents,omitempty"`
	Tasks        []json.RawMessage `json:"tasks,omitempty"`
	Validations  []json.RawMessage `json:"validations,omitempty"`
	History      []json.RawMessage `json:"history,omitempty"`
	OmittedIDs   []string          `json:"omitted_ids,omitempty"`
	ContextBasis json.RawMessage   `json:"context_basis,omitempty"`
	Compaction   json.RawMessage   `json:"compaction,omitempty"`
	Delta        []json.RawMessage `json:"delta,omitempty"`
	DeltaStatus  json.RawMessage   `json:"delta_status,omitempty"`
}

type coordinationWire struct {
	Profile      string            `json:"profile"`
	Revision     int               `json:"revision"`
	Item         json.RawMessage   `json:"item"`
	Items        []json.RawMessage `json:"items"`
	NextCursor   *string           `json:"next_cursor"`
	Reason       string            `json:"reason"`
	Truncated    bool              `json:"truncated"`
	Documents    json.RawMessage   `json:"documents"`
	Tasks        []json.RawMessage `json:"tasks"`
	Validations  []json.RawMessage `json:"validations"`
	History      []json.RawMessage `json:"history"`
	OmittedIDs   []string          `json:"omitted_ids"`
	ContextBasis json.RawMessage   `json:"context_basis"`
	Compaction   json.RawMessage   `json:"compaction"`
	Delta        []json.RawMessage `json:"delta"`
	DeltaStatus  json.RawMessage   `json:"delta_status"`
}

func (c *Client) QueryCoordination(ctx context.Context, directory string, query CoordinationQuery, request CoordinationRequest) (CoordinationProjection, error) {
	if !isCoordinationCommand(string(query)) {
		return CoordinationProjection{}, errors.New("unsupported devtools coordination query")
	}
	project, err := c.InspectProject(ctx, directory)
	if err != nil {
		return CoordinationProjection{}, err
	}
	input := map[string]any{"profile": project.Profile}
	if request.Target != "" {
		input["args"] = []string{request.Target}
	}
	if request.Workstream != "" {
		input["workstream"] = request.Workstream
	}
	if request.Cursor != "" {
		input["cursor"] = request.Cursor
	}
	if request.Limit != 0 {
		if request.Limit < 1 || request.Limit > 200 {
			return CoordinationProjection{}, errors.New("devtools coordination limit must be between 1 and 200")
		}
		input["limit"] = strconv.Itoa(request.Limit)
	}
	if query == CoordinationTaskCurrent {
		if c.Workspace == nil {
			return CoordinationProjection{}, errors.New("devtools workspace is unavailable")
		}
		if directory == "" {
			directory = "."
		}
		confined, resolveErr := c.Workspace.ResolveCWD(directory)
		if resolveErr != nil {
			return CoordinationProjection{}, errors.New("devtools current-run directory is outside the workspace")
		}
		input["dir"] = confined
	}
	rawInput, err := json.Marshal(input)
	if err != nil {
		return CoordinationProjection{}, errors.New("encode devtools coordination query")
	}
	raw, err := c.call(ctx, string(query), rawInput, c.Env)
	if err != nil {
		return CoordinationProjection{}, err
	}
	var wire coordinationWire
	if err := decodeObject(raw, &wire); err != nil || wire.Profile == "" || wire.Profile != project.Profile || wire.Revision < 0 {
		return CoordinationProjection{}, errors.New("devtools returned invalid coordination data")
	}
	projection := CoordinationProjection{
		Profile: wire.Profile, Revision: wire.Revision, NextCursor: wire.NextCursor,
		Reason: wire.Reason, Truncated: wire.Truncated, OmittedIDs: append([]string(nil), wire.OmittedIDs...),
	}
	if projection.Item, err = c.sanitizeCoordinationRaw(wire.Item); err != nil {
		return CoordinationProjection{}, err
	}
	if projection.Documents, err = c.sanitizeCoordinationRaw(wire.Documents); err != nil {
		return CoordinationProjection{}, err
	}
	if projection.ContextBasis, err = c.sanitizeCoordinationRaw(wire.ContextBasis); err != nil {
		return CoordinationProjection{}, err
	}
	if projection.Compaction, err = c.sanitizeCoordinationRaw(wire.Compaction); err != nil {
		return CoordinationProjection{}, err
	}
	if projection.DeltaStatus, err = c.sanitizeCoordinationRaw(wire.DeltaStatus); err != nil {
		return CoordinationProjection{}, err
	}
	groups := []struct {
		source []json.RawMessage
		target *[]json.RawMessage
	}{
		{wire.Items, &projection.Items},
		{wire.Tasks, &projection.Tasks},
		{wire.Validations, &projection.Validations},
		{wire.History, &projection.History},
		{wire.Delta, &projection.Delta},
	}
	for _, group := range groups {
		for _, item := range group.source {
			sanitized, sanitizeErr := c.sanitizeCoordinationRaw(item)
			if sanitizeErr != nil {
				return CoordinationProjection{}, sanitizeErr
			}
			*group.target = append(*group.target, sanitized)
		}
	}
	return projection, nil
}

func isCoordinationCommand(name string) bool {
	switch CoordinationQuery(name) {
	case CoordinationTaskNext, CoordinationTaskShow, CoordinationTaskCurrent, CoordinationTaskContext,
		CoordinationTaskHistory, CoordinationCheckpointList, CoordinationWorkstreamList,
		CoordinationWorkstreamShow, CoordinationWorkstreamContext, CoordinationWorkstreamHistory:
		return true
	default:
		return false
	}
}

func (c *Client) sanitizeCoordinationRaw(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("devtools returned malformed coordination data")
	}
	if err := c.sanitizeCoordinationValue(value); err != nil {
		return nil, err
	}
	sanitized, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode sanitized coordination data")
	}
	return sanitized, nil
}

func (c *Client) sanitizeCoordinationValue(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "context", "credential", "claim_context":
				return errors.New("devtools coordination response contained private context")
			case "directory":
				text, ok := child.(string)
				if !ok {
					return errors.New("devtools coordination response contained invalid directory")
				}
				relative, err := c.coordinationDirectory(text)
				if err != nil {
					return err
				}
				typed[key] = relative
			default:
				if err := c.sanitizeCoordinationValue(child); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, child := range typed {
			if err := c.sanitizeCoordinationValue(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) coordinationDirectory(value string) (string, error) {
	if c.Workspace == nil || value == "" {
		return "", errors.New("devtools coordination directory is unavailable")
	}
	if !filepath.IsAbs(value) {
		relative, err := policy.CWD(value)
		if err != nil {
			return "", errors.New("devtools coordination directory is outside the workspace")
		}
		return filepath.ToSlash(relative), nil
	}
	relative, err := filepath.Rel(c.Workspace.Root(), value)
	if err != nil {
		return "", errors.New("devtools coordination directory is outside the workspace")
	}
	relative = filepath.ToSlash(relative)
	if relative == "" {
		relative = "."
	}
	if relative == ".." || strings.HasPrefix(relative, "../") {
		return "", errors.New("devtools coordination directory is outside the workspace")
	}
	return relative, nil
}
