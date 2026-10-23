package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacySecretEntryPointsRefuseBeforeRuntimeCall(t *testing.T) {
	for _, args := range [][]string{{"secret-process", "start", "--profile", "example", "command"}, {"secret", "set", "example", "TOKEN"}, {"secret", "generate", "example", "TOKEN"}, {"secret", "init"}} {
		var out, diagnostic bytes.Buffer
		if code := run(args, &out, &diagnostic); code != 2 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "retired") {
			t.Fatal("legacy secret route remained available", args[0], code)
		}
		calls := 0
		client := adminCall(func(context.Context, any) (json.RawMessage, error) { calls++; return nil, nil })
		if args[0] == "secret" && executeAdministration(args, client, &out, &diagnostic) != 2 {
			t.Fatal("legacy admin parser accepted retired secret request")
		}
		if calls != 0 {
			t.Fatal("retired input reached runtime")
		}
	}
}
