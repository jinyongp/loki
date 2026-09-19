package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/devtools"
)

type recordingDevtoolsCaller struct {
	command, profile string
	input            json.RawMessage
	secrets          []string
	result           json.RawMessage
	err              error
}

func (c *recordingDevtoolsCaller) Call(_ context.Context, command string, input json.RawMessage, profile string, secrets []string) (json.RawMessage, error) {
	c.command, c.input, c.profile = command, input, profile
	c.secrets = append([]string(nil), secrets...)
	return c.result, c.err
}

func TestDevtoolsOperationForwardsOnlyTypedFields(t *testing.T) {
	caller := &recordingDevtoolsCaller{result: json.RawMessage(`{"schema_version":1,"ok":true}`)}
	operation := DevtoolsOperations(caller)["devtools_call"]
	result, err := operation.Handle(context.Background(), json.RawMessage(`{
		"operation":"devtools_call",
		"command":"process start",
		"input":{"name":"web"},
		"profile":"local",
		"secrets":["API_TOKEN"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if operation.Grant != controlpolicy.Agent {
		t.Fatalf("grant = %v", operation.Grant)
	}
	if caller.command != "process start" || caller.profile != "local" || string(caller.input) != `{"name":"web"}` {
		t.Fatalf("forwarded call = %#v", caller)
	}
	if !reflect.DeepEqual(caller.secrets, []string{"API_TOKEN"}) {
		t.Fatalf("secrets = %#v", caller.secrets)
	}
	if string(result.(json.RawMessage)) != `{"schema_version":1,"ok":true}` {
		t.Fatalf("result = %s", result)
	}
}

func TestDevtoolsOperationRejectsInvalidRequest(t *testing.T) {
	caller := &recordingDevtoolsCaller{}
	_, err := DevtoolsOperations(caller)["devtools_call"].Handle(context.Background(), json.RawMessage(`{"input":`))
	if err == nil {
		t.Fatal("expected invalid request to fail")
	}
	if caller.command != "" || caller.input != nil {
		t.Fatal("caller ran for invalid request")
	}
}

func TestDevtoolsOperationPreservesCallerFailure(t *testing.T) {
	want := errors.New("devtools unavailable")
	caller := &recordingDevtoolsCaller{err: want}
	_, err := DevtoolsOperations(caller)["devtools_call"].Handle(context.Background(), json.RawMessage(`{"command":"doctor","input":{}}`))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

type recordingMetadataReader struct {
	projectCWD string
	listCWD    string
	inspectCWD string
	name       string
	err        error
}

func (r *recordingMetadataReader) InspectProject(_ context.Context, cwd string) (devtools.ProjectMetadata, error) {
	r.projectCWD = cwd
	return devtools.ProjectMetadata{Profile: "fixture", Source: "file", Root: ".", ConfigPath: "devtools.toml"}, r.err
}

func (r *recordingMetadataReader) ListCommands(_ context.Context, cwd string) (devtools.CommandCatalog, error) {
	r.listCWD = cwd
	return devtools.CommandCatalog{Profile: "fixture", Items: []devtools.CommandSummary{{Name: "web"}}}, r.err
}

func (r *recordingMetadataReader) InspectCommand(_ context.Context, cwd, name string) (devtools.CommandDetail, error) {
	r.inspectCWD, r.name = cwd, name
	return devtools.CommandDetail{Profile: "fixture", Item: devtools.CommandMetadata{Name: name}}, r.err
}

func TestDevtoolsMetadataOperationsUseFixedTypedMethods(t *testing.T) {
	reader := &recordingMetadataReader{}
	ops := DevtoolsMetadataOperations(reader)
	if len(ops) != 3 {
		t.Fatalf("metadata operation count = %d", len(ops))
	}
	project, err := ops["devtools_project_inspect"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_project_inspect\",\"cwd\":\"repo\"}"))
	if err != nil || reader.projectCWD != "repo" || project.(devtools.ProjectMetadata).Profile != "fixture" {
		t.Fatalf("project result=%#v cwd=%q err=%v", project, reader.projectCWD, err)
	}
	list, err := ops["devtools_command_list"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_command_list\",\"cwd\":\"repo\"}"))
	if err != nil || reader.listCWD != "repo" || len(list.(devtools.CommandCatalog).Items) != 1 {
		t.Fatalf("list result=%#v cwd=%q err=%v", list, reader.listCWD, err)
	}
	detail, err := ops["devtools_command_inspect"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_command_inspect\",\"cwd\":\"repo\",\"name\":\"web\"}"))
	if err != nil || reader.inspectCWD != "repo" || reader.name != "web" || detail.(devtools.CommandDetail).Item.Name != "web" {
		t.Fatalf("detail result=%#v cwd=%q name=%q err=%v", detail, reader.inspectCWD, reader.name, err)
	}
	for name, operation := range ops {
		if operation.Grant != controlpolicy.Agent {
			t.Fatalf("%s grant = %v", name, operation.Grant)
		}
	}
}

func TestDevtoolsMetadataOperationsRejectUnknownFieldsAndUnavailableReader(t *testing.T) {
	reader := &recordingMetadataReader{}
	if _, err := DevtoolsMetadataOperations(reader)["devtools_project_inspect"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_project_inspect\",\"cwd\":\".\",\"command\":\"doctor\"}")); err == nil {
		t.Fatal("unknown command field accepted")
	}
	if reader.projectCWD != "" {
		t.Fatal("metadata reader ran for invalid request")
	}
	if _, err := DevtoolsMetadataOperations(nil)["devtools_command_list"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_command_list\"}")); err == nil {
		t.Fatal("unavailable metadata reader accepted")
	}
}

func TestDevtoolsMetadataOperationsPreserveReaderFailure(t *testing.T) {
	want := errors.New("metadata unavailable")
	reader := &recordingMetadataReader{err: want}
	_, err := DevtoolsMetadataOperations(reader)["devtools_command_inspect"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_command_inspect\",\"name\":\"web\"}"))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

type recordingCoordinationReader struct {
	cwd     string
	query   devtools.CoordinationQuery
	request devtools.CoordinationRequest
	err     error
}

func (r *recordingCoordinationReader) QueryCoordination(_ context.Context, cwd string, query devtools.CoordinationQuery, request devtools.CoordinationRequest) (devtools.CoordinationProjection, error) {
	r.cwd, r.query, r.request = cwd, query, request
	return devtools.CoordinationProjection{Profile: "fixture", Revision: 9}, r.err
}

func TestDevtoolsCoordinationOperationsMapFixedQueries(t *testing.T) {
	reader := &recordingCoordinationReader{}
	ops := DevtoolsCoordinationOperations(reader)
	if len(ops) != 10 {
		t.Fatalf("coordination operation count = %d", len(ops))
	}
	cases := map[string]struct {
		raw        string
		query      devtools.CoordinationQuery
		target     string
		workstream string
		limit      int
		cursor     string
	}{
		"devtools_task_next":          {`{"operation":"devtools_task_next","cwd":"repo","workstream_id":"33333333-3333-4333-8333-333333333333"}`, devtools.CoordinationTaskNext, "", "33333333-3333-4333-8333-333333333333", 0, ""},
		"devtools_task_show":          {`{"operation":"devtools_task_show","cwd":"repo","task_id":"11111111-1111-4111-8111-111111111111"}`, devtools.CoordinationTaskShow, "11111111-1111-4111-8111-111111111111", "", 0, ""},
		"devtools_task_current":       {`{"operation":"devtools_task_current","cwd":"repo","limit":25,"cursor":"next"}`, devtools.CoordinationTaskCurrent, "", "", 25, "next"},
		"devtools_task_context":       {`{"operation":"devtools_task_context","task_id":"11111111-1111-4111-8111-111111111111"}`, devtools.CoordinationTaskContext, "11111111-1111-4111-8111-111111111111", "", 0, ""},
		"devtools_task_history":       {`{"operation":"devtools_task_history","task_id":"11111111-1111-4111-8111-111111111111","limit":50}`, devtools.CoordinationTaskHistory, "11111111-1111-4111-8111-111111111111", "", 50, ""},
		"devtools_checkpoint_list":    {`{"operation":"devtools_checkpoint_list","run_id":"22222222-2222-4222-8222-222222222222"}`, devtools.CoordinationCheckpointList, "22222222-2222-4222-8222-222222222222", "", 0, ""},
		"devtools_workstream_list":    {`{"operation":"devtools_workstream_list","limit":20}`, devtools.CoordinationWorkstreamList, "", "", 20, ""},
		"devtools_workstream_show":    {`{"operation":"devtools_workstream_show","workstream_id":"33333333-3333-4333-8333-333333333333"}`, devtools.CoordinationWorkstreamShow, "33333333-3333-4333-8333-333333333333", "", 0, ""},
		"devtools_workstream_context": {`{"operation":"devtools_workstream_context","workstream_id":"33333333-3333-4333-8333-333333333333"}`, devtools.CoordinationWorkstreamContext, "33333333-3333-4333-8333-333333333333", "", 0, ""},
		"devtools_workstream_history": {`{"operation":"devtools_workstream_history","workstream_id":"33333333-3333-4333-8333-333333333333","cursor":"next"}`, devtools.CoordinationWorkstreamHistory, "33333333-3333-4333-8333-333333333333", "", 0, "next"},
	}
	for name, test := range cases {
		reader.cwd, reader.query, reader.request = "", "", devtools.CoordinationRequest{}
		result, err := ops[name].Handle(t.Context(), json.RawMessage(test.raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ops[name].Grant != controlpolicy.Agent || result.(devtools.CoordinationProjection).Revision != 9 {
			t.Fatalf("%s result/grant = %#v / %v", name, result, ops[name].Grant)
		}
		if reader.query != test.query || reader.request.Target != test.target || reader.request.Workstream != test.workstream || reader.request.Limit != test.limit || reader.request.Cursor != test.cursor {
			t.Fatalf("%s mapping = query=%q request=%#v", name, reader.query, reader.request)
		}
	}
}

func TestDevtoolsCoordinationOperationsRejectIrrelevantFieldsAndUnavailableReader(t *testing.T) {
	reader := &recordingCoordinationReader{}
	ops := DevtoolsCoordinationOperations(reader)
	for name, raw := range map[string]string{
		"devtools_task_show":       `{"operation":"devtools_task_show","task_id":"11111111-1111-4111-8111-111111111111","run_id":"22222222-2222-4222-8222-222222222222"}`,
		"devtools_task_context":    `{"operation":"devtools_task_context","task_id":"11111111-1111-4111-8111-111111111111","limit":10}`,
		"devtools_workstream_show": `{"operation":"devtools_workstream_show","workstream_id":"33333333-3333-4333-8333-333333333333","task_id":"11111111-1111-4111-8111-111111111111"}`,
	} {
		if _, err := ops[name].Handle(t.Context(), json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted irrelevant fields", name)
		}
	}
	if reader.query != "" {
		t.Fatal("coordination reader ran for rejected request")
	}
	if _, err := DevtoolsCoordinationOperations(nil)["devtools_task_next"].Handle(t.Context(), json.RawMessage(`{"operation":"devtools_task_next"}`)); err == nil {
		t.Fatal("unavailable coordination reader accepted")
	}
}

type recordingAgentGuidanceReader struct {
	cwd    string
	name   string
	target string
}

func (r *recordingAgentGuidanceReader) ListSkills(_ context.Context, cwd string) (devtools.SkillCatalog, error) {
	r.cwd = cwd
	return devtools.SkillCatalog{Items: []devtools.SkillSummary{{Name: "review-skill", Description: "Use for review.", Scope: "project", Revision: strings.Repeat("a", 64)}}, Diagnostics: []devtools.SkillDiagnostic{}, Shadowed: []devtools.SkillShadow{}}, nil
}

func (r *recordingAgentGuidanceReader) InspectSkill(_ context.Context, cwd, name string) (devtools.SkillInspection, error) {
	r.cwd, r.name = cwd, name
	return devtools.SkillInspection{Item: devtools.SkillDetail{SkillSummary: devtools.SkillSummary{Name: name, Description: "Use for review.", Scope: "project", Revision: strings.Repeat("a", 64)}, Content: "# Skill\n", Resources: []devtools.SkillResource{}}}, nil
}

func (r *recordingAgentGuidanceReader) ResolveGuidance(_ context.Context, cwd, target string) (devtools.GuidanceResult, error) {
	r.cwd, r.target = cwd, target
	return devtools.GuidanceResult{Target: target, TargetDir: ".", Revision: strings.Repeat("b", 64), Complete: true, Sources: []devtools.GuidanceSource{}, Diagnostics: []devtools.GuidanceDiagnostic{}}, nil
}

func TestDevtoolsAgentGuidanceOperationsUseFixedTypedMethods(t *testing.T) {
	reader := &recordingAgentGuidanceReader{}
	ops := DevtoolsAgentGuidanceOperations(reader)
	if len(ops) != 3 {
		t.Fatalf("operation count = %d", len(ops))
	}
	result, err := ops["devtools_skill_list"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_skill_list\",\"cwd\":\"repo\"}"))
	if err != nil || reader.cwd != "repo" || len(result.(devtools.SkillCatalog).Items) != 1 {
		t.Fatalf("list result=%#v reader=%#v err=%v", result, reader, err)
	}
	result, err = ops["devtools_skill_inspect"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_skill_inspect\",\"cwd\":\"repo\",\"name\":\"review-skill\"}"))
	if err != nil || reader.name != "review-skill" || result.(devtools.SkillInspection).Item.Content == "" {
		t.Fatalf("inspect result=%#v reader=%#v err=%v", result, reader, err)
	}
	result, err = ops["devtools_guidance_resolve"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_guidance_resolve\",\"cwd\":\"repo\",\"target\":\"src/new.go\"}"))
	if err != nil || reader.target != "src/new.go" || result.(devtools.GuidanceResult).Target != "src/new.go" {
		t.Fatalf("guidance result=%#v reader=%#v err=%v", result, reader, err)
	}
	for name, operation := range ops {
		if operation.Grant != controlpolicy.Agent {
			t.Fatalf("%s grant = %v", name, operation.Grant)
		}
	}
}

func TestDevtoolsAgentGuidanceOperationsRejectUnknownFields(t *testing.T) {
	reader := &recordingAgentGuidanceReader{}
	ops := DevtoolsAgentGuidanceOperations(reader)
	if _, err := ops["devtools_skill_list"].Handle(t.Context(), json.RawMessage("{\"operation\":\"devtools_skill_list\",\"cwd\":\".\",\"command\":\"run\"}")); err == nil {
		t.Fatal("unknown field accepted")
	}
	if reader.cwd != "" {
		t.Fatal("reader ran for rejected request")
	}
}
