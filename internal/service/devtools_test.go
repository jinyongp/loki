package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
