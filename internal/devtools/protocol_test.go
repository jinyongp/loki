package devtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvelopeContract(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":1,"ok":true,"data":{}}`,
		`{"schema_version":1,"ok":true,"data":null}`,
		`{"schema_version":1,"ok":false,"error":{"code":"conflict","message":"Retry after reading current state.","details":{"revision":2}}}`,
	} {
		if _, err := decodeEnvelope([]byte(raw)); err != nil {
			t.Fatalf("valid envelope rejected: %v", err)
		}
	}
	invalid := map[string]string{
		"case-alias":         `{"schema_version":1,"OK":true,"data":{}}`,
		"case-duplicate":     `{"schema_version":1,"ok":false,"OK":true,"data":{}}`,
		"error-case-alias":   `{"schema_version":1,"ok":false,"error":{"Code":"failed","message":"failed"}}`,
		"empty":              `{}`,
		"null":               `null`,
		"array":              `[]`,
		"missing-version":    `{"ok":true,"data":{}}`,
		"cli-as-envelope":    `{"schema_version":3,"ok":true,"data":{}}`,
		"null-version":       `{"schema_version":null,"ok":true,"data":{}}`,
		"missing-ok":         `{"schema_version":1,"data":{}}`,
		"null-ok":            `{"schema_version":1,"ok":null,"data":{}}`,
		"missing-data":       `{"schema_version":1,"ok":true}`,
		"success-and-error":  `{"schema_version":1,"ok":true,"data":{},"error":null}`,
		"failure-and-data":   `{"schema_version":1,"ok":false,"data":null,"error":{"code":"failed","message":"failed"}}`,
		"missing-error":      `{"schema_version":1,"ok":false}`,
		"null-error":         `{"schema_version":1,"ok":false,"error":null}`,
		"missing-error-code": `{"schema_version":1,"ok":false,"error":{"message":"failed"}}`,
		"missing-message":    `{"schema_version":1,"ok":false,"error":{"code":"failed"}}`,
		"null-message":       `{"schema_version":1,"ok":false,"error":{"code":"failed","message":null}}`,
		"invalid-details":    `{"schema_version":1,"ok":false,"error":{"code":"failed","message":"failed","details":[]}}`,
		"unknown-field":      `{"schema_version":1,"ok":true,"data":{},"extra":true}`,
		"duplicate-ok":       `{"schema_version":1,"ok":false,"ok":true,"data":{}}`,
		"duplicate-error":    `{"schema_version":1,"ok":false,"error":{"code":"first","code":"second","message":"failed"}}`,
		"concatenated":       `{"schema_version":1,"ok":true,"data":{}} {}`,
		"truncated":          `{"schema_version":1,"ok":true,"data":`,
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeEnvelope([]byte(raw)); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
}

func TestVersionRequiresIndependentProtocol(t *testing.T) {
	for _, protocol := range []string{"0", "1", "2", "4", "null", `"3"`} {
		t.Run(protocol, func(t *testing.T) {
			raw := []byte(`{"schema_version":1,"ok":true,"data":{"version":"0.17.0","commit":"test","protocol_version":` + protocol + `}}`)
			if _, err := ParseVersion(raw); err == nil {
				t.Fatal("unsupported or malformed CLI protocol accepted")
			}
		})
	}
	for _, raw := range []string{
		`{"schema_version":1,"ok":true,"data":{"version":"0.17.0","commit":"test"}}`,
		`{"schema_version":3,"ok":true,"data":{"version":"0.17.0","commit":"test","protocol_version":3}}`,
		`{"schema_version":1,"ok":true,"data":{"version":"0.17.0","commit":"test","protocol_version":1,"protocol_version":3}}`,
	} {
		if _, err := ParseVersion([]byte(raw)); err == nil {
			t.Fatal("invalid version contract accepted")
		}
	}
	got, err := ParseVersion([]byte(`{"schema_version":1,"ok":true,"data":{"version":"0.17.0","commit":"test","protocol_version":3}}`))
	if err != nil || got.ProtocolVersion != 3 || got.Version != "0.17.0" {
		t.Fatalf("version = %+v, error = %v", got, err)
	}
}

func catalogData(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(embeddedCatalog, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope, envelope["data"].(map[string]any)
}

func TestCatalogIgnoresUnapprovedNonJSONCommands(t *testing.T) {
	envelope, data := catalogData(t)
	commands := data["commands"].([]any)
	for _, mode := range []string{"text", "artifact", "passthrough"} {
		commands = append(commands, map[string]any{
			"name": "unapproved " + mode, "output_mode": mode,
			"accepts_child_args": true, "input_schema": map[string]any{"type": "object"},
		})
	}
	data["commands"] = commands
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseCatalog(raw)
	if err != nil || len(got) != len(approvedNames) {
		t.Fatalf("mixed catalog = %v, error = %v", got, err)
	}
	if _, err := compileCommands(got); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRejectsUnsafeApprovedContracts(t *testing.T) {
	mutations := map[string]func(map[string]any, map[string]any){
		"missing-mode":  func(c, _ map[string]any) { delete(c, "output_mode") },
		"text-mode":     func(c, _ map[string]any) { c["output_mode"] = "text" },
		"artifact-mode": func(c, _ map[string]any) { c["output_mode"] = "artifact" },
		"passthrough":   func(c, _ map[string]any) { c["output_mode"] = "passthrough" },
		"child-args":    func(c, _ map[string]any) { c["accepts_child_args"] = true },
		"missing-input": func(c, _ map[string]any) { delete(c, "input_schema") },
		"null-input":    func(c, _ map[string]any) { c["input_schema"] = nil },
		"missing-output": func(c, _ map[string]any) {
			delete(c, "output_schema")
		},
		"null-output":    func(c, _ map[string]any) { c["output_schema"] = nil },
		"boolean-output": func(c, _ map[string]any) { c["output_schema"] = true },
		"duplicate-name": func(c, d map[string]any) { d["commands"] = append(d["commands"].([]any), c) },
		"missing-command": func(_, d map[string]any) {
			d["commands"] = d["commands"].([]any)[1:]
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			envelope, data := catalogData(t)
			mutate(data["commands"].([]any)[0].(map[string]any), data)
			raw, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseCatalog(raw); err == nil {
				t.Fatal("unsafe approved contract accepted")
			}
		})
	}
	raw, err := os.ReadFile("testdata/catalog-v0.9.0.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseCatalog(raw); err == nil {
		t.Fatal("legacy protocol-1 catalog accepted")
	}
}

func TestClientChecksFailureEnvelopeBeforeCLIError(t *testing.T) {
	cases := []struct {
		name     string
		response string
		exit     int
		cliError bool
	}{
		{"valid-failure", `{"schema_version":1,"ok":false,"error":{"code":"conflict","message":"Refresh the observed revision."}}`, 3, true},
		{"wrong-failure-version", `{"schema_version":3,"ok":false,"error":{"code":"conflict","message":"failed"}}`, 3, false},
		{"malformed-failure", `{"schema_version":1,"ok":false,"error":null}`, 3, false},
		{"missing-failure-status", `{"schema_version":1,"error":{"code":"conflict","message":"failed"}}`, 3, false},
		{"failed-exit-success", `{"schema_version":1,"ok":true,"data":{"item":{},"changed":true,"replayed":false}}`, 3, false},
		{"successful-exit-failure", `{"schema_version":1,"ok":false,"error":{"code":"conflict","message":"failed"}}`, 0, false},
		{"old-process-output", `{"schema_version":1,"ok":true,"data":{"started":true}}`, 0, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client, _ := fakeClient(t)
			response := filepath.Join(client.CWD, "response.json")
			if err := os.WriteFile(response, []byte(test.response), 0600); err != nil {
				t.Fatal(err)
			}
			body := "#!/bin/sh\n" +
				"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.17.0\",\"commit\":\"test\",\"protocol_version\":3}}'; exit 0; fi\n" +
				"if [ \"$1 $2\" = \"schema --all\" ]; then cat \"" + filepath.Join(client.CWD, "catalog.json") + "\"; exit 0; fi\n"
			redirect := ""
			if test.exit != 0 {
				redirect = " >&2"
			}
			body += "cat \"" + response + "\"" + redirect + fmt.Sprintf("\nexit %d\n", test.exit)
			if err := os.WriteFile(client.Binary, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			_, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`))
			if err == nil {
				t.Fatal("invalid or failed command accepted")
			}
			var cliErr *CLIError
			if got := errors.As(err, &cliErr); got != test.cliError {
				t.Fatalf("CLI error = %v, want %v; error = %v", got, test.cliError, err)
			}
			if cliErr != nil && (cliErr.ExitCode != 3 || cliErr.Code != "conflict") {
				t.Fatalf("CLI error = %+v", cliErr)
			}
		})
	}
}
