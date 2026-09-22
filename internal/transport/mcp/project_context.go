package mcptransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/agentcontext"
	"loki/internal/devtools"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
	"loki/internal/work/workspace"
)

type projectContextRequest struct {
	CWD          string   `json:"cwd"`
	Target       string   `json:"target"`
	TaskID       string   `json:"task_id"`
	WorkstreamID string   `json:"workstream_id"`
	Skills       []string `json:"skills"`
}

type projectContextWriteRequest struct {
	CWD              string   `json:"cwd"`
	Target           string   `json:"target"`
	TaskID           string   `json:"task_id"`
	WorkstreamID     string   `json:"workstream_id"`
	Skills           []string `json:"skills"`
	RequestID        string   `json:"request_id"`
	ExpectedBasis    string   `json:"expected_basis"`
	ExpectedPrevious string   `json:"expected_previous"`
	Summary          string   `json:"summary"`
	Decisions        []string `json:"decisions"`
	Remaining        []string `json:"remaining"`
	NextAction       string   `json:"next_action"`
	Blockers         []string `json:"blockers"`
}

type projectContextTransition struct {
	Action        string `json:"action"`
	TaskID        string `json:"task_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	ExpectedRunID string `json:"expected_run_id,omitempty"`
	Reason        string `json:"reason"`
}

type RepositoryEvidenceProvider interface {
	ContextEvidence(context.Context, string, string) (workspace.ContextEvidence, error)
}

type ProjectContextController struct {
	Runtime  rpc.Caller
	Guidance AgentGuidanceProvider
	Git      RepositoryEvidenceProvider
	Claims   *DevtoolsSessionClaims
}

type projectContextResolution struct {
	basis              agentcontext.ContextBasis
	basisFingerprint   string
	guidance           agentcontext.ContextResult
	coordination       map[string]any
	transition         projectContextTransition
	ambiguous          bool
	selectedSkillNames []string
}

func decodeCoordinationRaw(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("devtools coordination item is invalid")
	}
	return value, nil
}

func coordinationString(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	text, _ := value[key].(string)
	return text
}

func coordinationNestedID(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	child, _ := value[key].(map[string]any)
	return coordinationString(child, "id")
}

func projectionPublic(p devtools.CoordinationProjection) map[string]any {
	value := map[string]any{
		"profile": p.Profile, "revision": p.Revision, "truncated": p.Truncated,
		"reason": p.Reason, "omitted_ids": append([]string{}, p.OmittedIDs...),
	}
	if len(p.Item) != 0 {
		value["item"] = json.RawMessage(append([]byte(nil), p.Item...))
	}
	if len(p.Items) != 0 {
		value["items"] = append([]json.RawMessage(nil), p.Items...)
	}
	if p.NextCursor != nil {
		value["next_cursor"] = *p.NextCursor
	}
	if len(p.Documents) != 0 {
		value["documents"] = json.RawMessage(append([]byte(nil), p.Documents...))
	}
	if len(p.Tasks) != 0 {
		value["tasks"] = append([]json.RawMessage(nil), p.Tasks...)
	}
	if len(p.Validations) != 0 {
		value["validations"] = append([]json.RawMessage(nil), p.Validations...)
	}
	if len(p.History) != 0 {
		value["history"] = append([]json.RawMessage(nil), p.History...)
	}
	return value
}

func uniqueGapCodes(values []string) []string {
	sort.Strings(values)
	return compactStrings(values)
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func selectedSkillRevisions(catalog agentcontext.SkillCatalog, names []string) ([]agentcontext.ContextSkillRevision, []string, error) {
	if len(names) > 64 {
		return nil, nil, errors.New("project context selects too many Skills")
	}
	requested := append([]string{}, names...)
	sort.Strings(requested)
	requested = compactStrings(requested)
	if len(requested) != len(names) {
		return nil, nil, errors.New("project context Skill names must be unique")
	}
	available := make(map[string]agentcontext.SkillSummary, len(catalog.Items))
	for _, item := range catalog.Items {
		available[item.Name] = item
	}
	revisions := make([]agentcontext.ContextSkillRevision, 0, len(requested))
	for _, name := range requested {
		item, ok := available[name]
		if !ok {
			return nil, nil, fmt.Errorf("selected Skill %q is not available", name)
		}
		revisions = append(revisions, agentcontext.ContextSkillRevision{
			Name: item.Name, Scope: item.Scope, Revision: item.Revision,
		})
	}
	return revisions, requested, nil
}

func validationIDs(values []json.RawMessage) []string {
	ids := []string{}
	for _, raw := range values {
		item, err := decodeCoordinationRaw(raw)
		if err != nil {
			continue
		}
		id := coordinationString(item, "id")
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return compactStrings(ids)
}

func workstreamID(item map[string]any) string {
	if value := coordinationString(item, "workstream_id"); value != "" {
		return value
	}
	return coordinationNestedID(item, "workstream")
}

func (c *ProjectContextController) query(ctx context.Context, operation, cwd string, fields map[string]any) (devtools.CoordinationProjection, error) {
	if c == nil || c.Runtime == nil {
		return devtools.CoordinationProjection{}, errors.New("project context coordination is unavailable")
	}
	request := map[string]any{"operation": operation, "cwd": cwd}
	for key, value := range fields {
		request[key] = value
	}
	var projection devtools.CoordinationProjection
	if err := rpc.DecodeCall(ctx, c.Runtime, request, &projection); err != nil {
		return devtools.CoordinationProjection{}, err
	}
	return projection, nil
}

func (c *ProjectContextController) resolveCoordination(ctx context.Context, cwd, explicitTask, explicitWorkstream string) (map[string]any, string, string, string, projectContextTransition, bool, []string, []string, string, error) {
	current, err := c.query(ctx, "devtools_task_current", cwd, map[string]any{"limit": 2})
	if err != nil {
		return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
	}
	currentPublic := projectionPublic(current)
	allCurrentItems := make([]map[string]any, 0, len(current.Items))
	currentItems := make([]map[string]any, 0, len(current.Items))
	for _, raw := range current.Items {
		item, decodeErr := decodeCoordinationRaw(raw)
		if decodeErr != nil {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", decodeErr
		}
		allCurrentItems = append(allCurrentItems, item)
		if explicitTask == "" || coordinationString(item, "task_id") == explicitTask {
			currentItems = append(currentItems, item)
		}
	}
	gaps := []string{}
	taskMismatch := explicitTask != "" && len(allCurrentItems) > 0 && len(currentItems) == 0
	ambiguous := current.Truncated || current.NextCursor != nil || len(currentItems) > 1 || taskMismatch
	if current.Truncated {
		gaps = append(gaps, "coordination.current.truncated")
	}
	if current.NextCursor != nil {
		gaps = append(gaps, "coordination.current.incomplete")
	}
	if len(currentItems) > 1 {
		gaps = append(gaps, "coordination.current.ambiguous")
	}
	if taskMismatch {
		gaps = append(gaps, "coordination.current.task_mismatch")
	}

	taskID, runID, workstream := "", "", explicitWorkstream
	var taskContext devtools.CoordinationProjection
	var next devtools.CoordinationProjection
	transition := projectContextTransition{Action: "inspect", Reason: "work_identity_is_ambiguous"}
	if !ambiguous && len(currentItems) == 1 {
		run := currentItems[0]
		runID = coordinationString(run, "id")
		taskID = coordinationString(run, "task_id")
		if taskID == "" || runID == "" {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", errors.New("current devtools run is missing id or task_id")
		}
		observedWorkstream := workstreamID(run)
		if workstream != "" && observedWorkstream != "" && workstream != observedWorkstream {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", errors.New("current devtools run does not match requested workstream")
		}
		if workstream == "" {
			workstream = observedWorkstream
		}
		taskContext, err = c.query(ctx, "devtools_task_context", cwd, map[string]any{"task_id": taskID})
		if err != nil {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
		}
		taskItem, err := decodeCoordinationRaw(taskContext.Item)
		if err != nil {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
		}
		taskWorkstream := workstreamID(taskItem)
		if workstream != "" && taskWorkstream != "" && workstream != taskWorkstream {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", errors.New("devtools task context does not match requested workstream")
		}
		if workstream == "" {
			workstream = taskWorkstream
		}
		sessionID, sessionOK := mcpserver.SessionID(ctx)
		binding, owned := c.Claims.get(sessionID)
		switch {
		case sessionOK && owned && binding.RunID == runID && binding.TaskID == taskID:
			transition = projectContextTransition{Action: "none", TaskID: taskID, RunID: runID, Reason: "session_owns_current_run"}
		case sessionOK && owned:
			transition = projectContextTransition{Action: "inspect", TaskID: taskID, RunID: runID, Reason: "session_owns_different_run"}
			ambiguous = true
			gaps = append(gaps, "coordination.session.binding_conflict")
		default:
			transition = projectContextTransition{Action: "takeover", TaskID: taskID, RunID: runID, ExpectedRunID: runID, Reason: "active_run_requires_session_ownership"}
		}
	} else if !ambiguous {
		nextFields := map[string]any{}
		if explicitWorkstream != "" {
			nextFields["workstream_id"] = explicitWorkstream
		}
		next, err = c.query(ctx, "devtools_task_next", cwd, nextFields)
		if err != nil {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
		}
		nextItem, err := decodeCoordinationRaw(next.Item)
		if err != nil {
			return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
		}
		if next.Truncated {
			gaps = append(gaps, "coordination.next.truncated")
			ambiguous = true
		}
		nextTaskID := coordinationString(nextItem, "id")
		if explicitTask != "" && nextTaskID != "" && nextTaskID != explicitTask {
			nextTaskID = ""
		}
		if nextTaskID != "" {
			taskID = nextTaskID
			if workstream == "" {
				workstream = workstreamID(nextItem)
			}
			taskContext, err = c.query(ctx, "devtools_task_context", cwd, map[string]any{"task_id": taskID})
			if err != nil {
				return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
			}
			taskItem, err := decodeCoordinationRaw(taskContext.Item)
			if err != nil {
				return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
			}
			if workstream == "" {
				workstream = workstreamID(taskItem)
			}
			if ambiguous {
				transition = projectContextTransition{Action: "inspect", TaskID: taskID, Reason: "next_task_context_is_incomplete"}
			} else {
				transition = projectContextTransition{Action: "claim", TaskID: taskID, Reason: "next_ready_task_is_unclaimed"}
			}
		} else if explicitTask != "" {
			taskID = explicitTask
			taskContext, err = c.query(ctx, "devtools_task_context", cwd, map[string]any{"task_id": taskID})
			if err != nil {
				return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
			}
			taskItem, err := decodeCoordinationRaw(taskContext.Item)
			if err != nil {
				return nil, "", "", "", projectContextTransition{}, false, nil, nil, "", err
			}
			if workstream == "" {
				workstream = workstreamID(taskItem)
			}
			transition = projectContextTransition{Action: "inspect", TaskID: taskID, Reason: "explicit_task_is_not_the_next_claimable_task"}
		} else {
			transition = projectContextTransition{Action: "none", Reason: "no_current_or_next_task"}
		}
	}

	if taskContext.Truncated || len(taskContext.OmittedIDs) > 0 {
		gaps = append(gaps, "coordination.task.truncated")
	}
	coordination := map[string]any{"current": currentPublic, "transition": transition}
	if taskID != "" {
		public := projectionPublic(taskContext)
		coordination["task_context"] = public
	}
	if len(next.Item) != 0 || len(next.Items) != 0 || next.Profile != "" {
		public := projectionPublic(next)
		coordination["next"] = public
	}
	revisions := []string{current.Profile + ":" + strconv.Itoa(current.Revision)}
	if taskID != "" {
		revisions = append(revisions, taskContext.Profile+":"+strconv.Itoa(taskContext.Revision))
	}
	if next.Profile != "" {
		revisions = append(revisions, next.Profile+":"+strconv.Itoa(next.Revision))
	}
	return coordination, taskID, runID, workstream, transition, ambiguous,
		validationIDs(taskContext.Validations), uniqueGapCodes(gaps), strings.Join(revisions, ","), nil
}

func staleReasons(previous, current agentcontext.ContextBasis) []string {
	reasons := []string{}
	if previous.RepositoryID != current.RepositoryID || previous.WorktreeID != current.WorktreeID {
		reasons = append(reasons, "worktree")
	}
	if previous.TaskID != current.TaskID || previous.RunID != current.RunID || previous.WorkstreamID != current.WorkstreamID ||
		previous.CoordinationFingerprint != current.CoordinationFingerprint ||
		previous.CoordinationRevision != current.CoordinationRevision {
		reasons = append(reasons, "coordination")
	}
	if previous.CodeBasis != current.CodeBasis {
		reasons = append(reasons, "code")
	}
	if previous.GuidanceRevision != current.GuidanceRevision {
		reasons = append(reasons, "guidance")
	}
	left, _ := json.Marshal(previous.Skills)
	right, _ := json.Marshal(current.Skills)
	if string(left) != string(right) {
		reasons = append(reasons, "skills")
	}
	if strings.Join(previous.Gaps, "\x00") != strings.Join(current.Gaps, "\x00") {
		reasons = append(reasons, "evidence_gaps")
	}
	return reasons
}

func (c *ProjectContextController) resolve(ctx context.Context, request projectContextRequest) (projectContextResolution, error) {
	if c == nil || c.Runtime == nil || c.Guidance == nil || c.Git == nil || c.Claims == nil {
		return projectContextResolution{}, errors.New("project context is unavailable")
	}
	if request.CWD == "" {
		request.CWD = "."
	}
	if request.Target == "" {
		request.Target = "."
	}
	guidance, err := c.Guidance.Context(ctx, request.CWD, request.Target)
	if err != nil {
		return projectContextResolution{}, err
	}
	evidence, err := c.Git.ContextEvidence(ctx, request.CWD, request.Target)
	if err != nil {
		return projectContextResolution{}, err
	}
	skills, selectedNames, err := selectedSkillRevisions(guidance.Skills, request.Skills)
	if err != nil {
		return projectContextResolution{}, err
	}
	coordination, taskID, runID, workstreamID, transition, ambiguous, validationIDs, gaps, revision, err :=
		c.resolveCoordination(ctx, evidence.TargetCWD, request.TaskID, request.WorkstreamID)
	if err != nil {
		return projectContextResolution{}, err
	}
	gaps = append(gaps, evidence.Gaps...)
	if !guidance.Guidance.Complete {
		gaps = append(gaps, "guidance.incomplete")
	}
	if !guidance.Skills.Complete {
		gaps = append(gaps, "skills.incomplete")
	}
	if workstreamID == "" && taskID != "" {
		gaps = append(gaps, "coordination.workstream.unavailable")
	}
	basis := agentcontext.ContextBasis{
		RepositoryID: evidence.RepositoryID, WorktreeID: evidence.WorktreeID,
		WorkstreamID: workstreamID, TaskID: taskID, RunID: runID,
		CoordinationRevision: revision, CodeBasis: evidence.CodeBasis,
		GuidanceRevision: guidance.Guidance.Revision, Skills: skills,
		ValidationRecordIDs: validationIDs, EvidenceRefs: []string{}, Gaps: uniqueGapCodes(gaps),
	}
	canonicalCoordination := make(map[string]any, len(coordination)-1)
	for key, value := range coordination {
		if key != "transition" {
			canonicalCoordination[key] = value
		}
	}
	canonical, err := json.Marshal(canonicalCoordination)
	if err != nil {
		return projectContextResolution{}, err
	}
	sum := sha256.Sum256(canonical)
	basis.CoordinationFingerprint = hex.EncodeToString(sum[:])
	fingerprint, err := agentcontext.ContextBasisFingerprint(basis)
	if err != nil {
		return projectContextResolution{}, err
	}
	return projectContextResolution{
		basis: basis, basisFingerprint: fingerprint, guidance: guidance, coordination: coordination,
		transition: transition, ambiguous: ambiguous, selectedSkillNames: selectedNames,
	}, nil
}

func (c *ProjectContextController) latest(ctx context.Context, basis agentcontext.ContextBasis) (agentcontext.ContextLatestResult, error) {
	var latest agentcontext.ContextLatestResult
	err := rpc.DecodeCall(ctx, c.Runtime, map[string]any{
		"operation": "context_checkpoint_latest", "repository_id": basis.RepositoryID,
		"worktree_id": basis.WorktreeID, "workstream_id": basis.WorkstreamID,
	}, &latest)
	return latest, err
}

func (c *ProjectContextController) read(ctx context.Context, request projectContextRequest) (*mcp.CallToolResult, error) {
	resolved, err := c.resolve(ctx, request)
	if err != nil {
		return nil, err
	}
	latest, err := c.latest(ctx, resolved.basis)
	if err != nil {
		return nil, err
	}
	checkpoint := map[string]any{
		"found": latest.Found, "stale": false, "stale_reasons": []string{},
		"expected_previous": "missing",
	}
	if latest.RetentionGap != nil {
		checkpoint["retention_gap"] = latest.RetentionGap
	}
	if latest.Record != nil {
		checkpoint["record"] = latest.Record
		checkpoint["expected_previous"] = latest.Record.ID
		reasons := staleReasons(latest.Record.Basis, resolved.basis)
		if len(resolved.basis.Gaps) != 0 {
			reasons = append(reasons, "current_evidence_incomplete")
		}
		reasons = uniqueGapCodes(reasons)
		checkpoint["stale"] = len(reasons) != 0
		checkpoint["stale_reasons"] = reasons
	}
	complete := !resolved.ambiguous && len(resolved.basis.Gaps) == 0
	return mcpserver.Object(map[string]any{
		"complete": complete,
		"basis":    resolved.basis, "basis_fingerprint": resolved.basisFingerprint,
		"guidance": resolved.guidance.Guidance, "skills": resolved.guidance.Skills,
		"selected_skills": resolved.selectedSkillNames, "coordination": resolved.coordination,
		"transition": resolved.transition, "ambiguous": resolved.ambiguous, "checkpoint": checkpoint,
	})
}

func (c *ProjectContextController) write(ctx context.Context, request projectContextWriteRequest) (*mcp.CallToolResult, error) {
	resolved, err := c.resolve(ctx, projectContextRequest{
		CWD: request.CWD, Target: request.Target, TaskID: request.TaskID,
		WorkstreamID: request.WorkstreamID, Skills: request.Skills,
	})
	if err != nil {
		return nil, err
	}
	if resolved.ambiguous {
		return nil, errors.New("project context write requires an unambiguous current work identity")
	}
	if request.ExpectedBasis == "" || request.ExpectedBasis != resolved.basisFingerprint {
		return nil, errors.New("project context basis changed; inspect project_context and retry")
	}
	if resolved.basis.RunID != "" {
		sessionID, ok := mcpserver.SessionID(ctx)
		binding, owned := c.Claims.get(sessionID)
		if !ok || !owned || binding.RunID != resolved.basis.RunID || binding.TaskID != resolved.basis.TaskID {
			return nil, errors.New("project context write requires current MCP session ownership of the active run")
		}
	}
	draft := agentcontext.ContextDraft{
		Basis: resolved.basis, Summary: request.Summary, Decisions: request.Decisions,
		Remaining: request.Remaining, Blockers: request.Blockers, NextAction: request.NextAction,
		SessionRef: auditSessionRef(ctx),
	}
	var result agentcontext.ContextPutResult
	if err := rpc.DecodeCall(ctx, c.Runtime, map[string]any{
		"operation": "context_checkpoint_put", "request_id": request.RequestID,
		"expected_previous": request.ExpectedPrevious, "draft": draft,
	}, &result); err != nil {
		return nil, err
	}
	return mcpserver.Object(map[string]any{
		"basis_fingerprint": resolved.basisFingerprint, "record": result.Record,
		"replayed": result.Replayed, "pruned_records": result.PrunedRecords,
		"retention_gap": result.RetentionGap,
	})
}

func ProjectContextHandlers(controller *ProjectContextController) map[string]mcpserver.Handler {
	read := mcpserver.Typed(func(ctx context.Context, request projectContextRequest) (*mcp.CallToolResult, error) {
		return controller.read(ctx, request)
	})
	write := mcpserver.Typed(func(ctx context.Context, request projectContextWriteRequest) (*mcp.CallToolResult, error) {
		return controller.write(ctx, request)
	})
	return map[string]mcpserver.Handler{"project_context": read, "project_context_write": write}
}
