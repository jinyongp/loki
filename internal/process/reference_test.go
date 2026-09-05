package process

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPython0471ProcessDifferential(t *testing.T) {
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
	type scenario struct {
		Chunks   [][]byte `json:"chunks"`
		Offset   *int64   `json:"offset"`
		Limit    int      `json:"limit"`
		ExitCode *int     `json:"exit_code"`
	}
	ptr := func(n int64) *int64 { return &n }
	zero, killed := 0, -9
	var cases []scenario
	for _, chunks := range [][][]byte{
		{[]byte("")}, {[]byte("abc")}, {[]byte("0123"), []byte("456789")},
		{[]byte("한"), []byte("글!")}, {[]byte("\xff\xff\xe2"), []byte("\x82")},
		{[]byte("abcdefgh"), []byte("\xf0\x90\x80\x80xy")},
	} {
		for _, offset := range []*int64{nil, ptr(-5), ptr(0), ptr(4), ptr(9), ptr(9999)} {
			for _, limit := range []int{0, 1, 64} {
				for _, code := range []*int{nil, &zero, &killed} {
					cases = append(cases, scenario{chunks, offset, limit, code})
				}
			}
		}
	}
	input, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), python, "testdata/python_reference.py")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Python reference %v: %s", err, stderr.String())
	}
	var expected []map[string]any
	if err = json.Unmarshal(output, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected) != len(cases) {
		t.Fatal("missing reference cases")
	}
	for index, tc := range cases {
		p := &managedProcess{id: "fixture", name: "reference", maximum: 6, startedAt: time.Unix(1234, 125000000).UTC(), metadata: json.RawMessage(`{"profile":"synthetic","port":32180}`)}
		for _, chunk := range tc.Chunks {
			p.Write(chunk)
		}
		if tc.ExitCode != nil {
			p.exited, p.exitCode = true, *tc.ExitCode
			p.timedOut = *tc.ExitCode == -9
		}
		encoded, err := json.Marshal(p.snapshot(tc.Offset, tc.Limit))
		if err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		if err = json.Unmarshal(encoded, &actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected[index]) {
			t.Errorf("case %d: Go %v; Python %v", index, actual, expected[index])
		}
	}
	t.Logf("Compared %d process output/status snapshots with Python 0.47.1", len(cases))
}
