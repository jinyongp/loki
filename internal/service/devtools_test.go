package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	controlpolicy "loki/internal/control/policy"
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
