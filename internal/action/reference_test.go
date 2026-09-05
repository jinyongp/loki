package action

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loki/internal/secret"
)

type actionReferenceCase struct {
	Command   []string `json:"command"`
	Hint      *string  `json:"hint,omitempty"`
	NVM       *string  `json:"nvm,omitempty"`
	Installed []string `json:"installed,omitempty"`
	TokenMode string   `json:"token_mode,omitempty"`
	Dotenv    *string  `json:"dotenv,omitempty"`
}

func TestPython0471ActionDifferential(t *testing.T) {
	python := os.Getenv("LOKI_REFERENCE_PYTHON")
	if python == "" {
		python = "../../.tmp/python-baseline/bin/python"
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(python); err != nil {
		t.Skip("isolated Python reference unavailable")
	}
	str := func(value string) *string { return &value }
	var cases []actionReferenceCase
	for _, metadata := range []actionReferenceCase{
		{}, {Hint: str("  v24.20.0\n"), NVM: str("v22")}, {Hint: str(""), NVM: str("\u200322\u2003")},
		{Hint: str("bad/path"), NVM: str("lts-iron")}, {Hint: str("bad\\path"), NVM: str("24")}, {Hint: str("bad\x00value"), NVM: str("v24")},
		{Hint: str(strings.Repeat("x", 128))}, {Hint: str(strings.Repeat("x", 129)), NVM: str("22")},
		{Installed: []string{"v9.99.9", "v24.9.0", "v24.10.1", "v24.10.0", "v25.0.0-beta"}},
		{Installed: []string{"v999999999999999999999999.0.0", "v22.99.9"}},
		{Installed: []string{"v24.01.0", "v24.1.0"}},
	} {
		for _, command := range []string{"node", "npm", "pnpm", "just", "actions-up", "pwd"} {
			tc := metadata
			tc.Command = []string{command, "--version"}
			cases = append(cases, tc)
		}
	}
	for _, mode := range []string{"valid", "expired", "unknown", "malformed", "uppercase", "profile", "action", "cwd"} {
		cases = append(cases, actionReferenceCase{TokenMode: mode})
	}
	for _, value := range []string{"", "simple", "a=b", "abc#def", "a b", "\t", "\r", "\n", "a'b", "a\"b", `a\b`, "a \\b\r\n\t\"'", "한국어", " spaced ", "${VALUE}", "a\u2003b"} {
		cases = append(cases, actionReferenceCase{Dotenv: str(value)})
	}
	reference, err := os.MkdirTemp(t.TempDir(), "action-reference-")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(cases)
	cmd := exec.CommandContext(t.Context(), python, "testdata/python_reference.py", reference)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(encoded)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Python reference: %v %s", err, stderr.String())
	}
	var expected []map[string]any
	if err := json.Unmarshal(output, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected) != len(cases) {
		t.Fatal("missing action reference results")
	}
	result := func(value any, err error) map[string]any {
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{"result": value}
	}
	for index, tc := range cases {
		var actual map[string]any
		if tc.Dotenv != nil {
			actual = result(dotenvValue(*tc.Dotenv), nil)
		} else if tc.TokenMode != "" {
			p := testPorts()
			token := strings.Repeat("0", 32)
			r := &Runtime{ports: p, launches: map[string]preparedAction{token: {profile: "fixture", action: "web", cwd: "/synthetic/repo", lease: &portLease{port: 43210, expires: time.Now().Add(portHold)}}}}
			plan := secret.ActionPlan{Profile: "fixture", Action: "web", CWD: "/synthetic/repo"}
			switch tc.TokenMode {
			case "expired":
				r.launches[token].lease.expires = time.Time{}
			case "unknown":
				delete(r.launches, token)
			case "malformed":
				token = "invalid"
			case "uppercase":
				token = strings.Repeat("A", 32)
			case "profile":
				plan.Profile = "other"
			case "action":
				plan.Action = "other"
			case "cwd":
				plan.CWD = "/synthetic/feature"
			}
			values := []map[string]any{}
			for range 2 {
				lease, err := r.consume(token, plan)
				port := 0
				if lease != nil {
					port = lease.port
				}
				values = append(values, result(port, err))
			}
			actual = result(values, nil)
		} else {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "repo"), 0700); err != nil {
				t.Fatal(err)
			}
			for filename, hint := range map[string]*string{".node-version": tc.Hint, ".nvmrc": tc.NVM} {
				if hint != nil {
					if err := os.WriteFile(filepath.Join(root, "repo", filename), []byte(*hint), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, version := range tc.Installed {
				if err := os.MkdirAll(filepath.Join(root, ".loki/fnm/node-versions", version), 0700); err != nil {
					t.Fatal(err)
				}
			}
			workspace, err := pinned(root, true)
			if err != nil {
				t.Fatal(err)
			}
			value, err := resolvedCommand(tc.Command, workspace, "repo")
			workspace.Close()
			actual = result(value, err)
		}
		data, _ := json.Marshal(actual)
		var normalized map[string]any
		if err := json.Unmarshal(data, &normalized); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(normalized, expected[index]) {
			t.Errorf("case %d: Go %v Python %v", index, normalized, expected[index])
		}
	}
	t.Logf("Compared %d Node resolution/token consumption/dotenv cases with Python 0.47.1", len(cases))
}
