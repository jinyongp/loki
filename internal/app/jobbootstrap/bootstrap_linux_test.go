//go:build linux

package jobbootstrap

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"loki/internal/platform/privateio"
)

func TestEnvironmentIsCompleteAndBounded(t *testing.T) {
	t.Setenv("PARENT_SECRET", "must-not-inherit")
	for _, tc := range []struct {
		body string
		good bool
	}{
		{`{"protocol":1,"environment":[{"name":"TOKEN","value":"line\nvalue"}]}`, true},
		{`{"protocol":1,"environment":[]}`, true},
		{`{"protocol":1,"environment":[{"name":"TOKEN","value":"a"},{"name":"TOKEN","value":"b"}]}`, false},
		{`{"protocol":1,"environment":[{"name":"TOKEN","value":null}]}`, false},
		{`{"protocol":1,"environment":[{"Name":"TOKEN","value":"a"}]}`, false},
		{`{"protocol":1,"environment":[{"name":"TOKEN","value":"a","value":"b"}]}`, false},
		{`{"protocol":1,"environment":[{"name":"TOKEN","value":"\u0000"}]}`, false},
	} {
		var frame bytes.Buffer
		if err := privateio.WriteBytes(&frame, []byte(tc.body), privateio.MaxPayloadFrame); err != nil {
			t.Fatal(err)
		}
		env, err := Environment(&frame, []string{"/bin/true"}, 8<<20)
		if (err == nil) != tc.good {
			t.Fatal("incorrect input verdict")
		}
		for _, entry := range env {
			if strings.HasPrefix(entry, "PARENT_SECRET=") {
				t.Fatal("parent inherited")
			}
		}
	}
	for _, size := range []int{128 << 10, 3 << 20} {
		body, _ := json.Marshal(privateio.Bootstrap{Protocol: 1, Environment: []privateio.EnvironmentEntry{{Name: "TOKEN", Value: strings.Repeat("a", size)}}})
		var frame bytes.Buffer
		_ = privateio.WriteBytes(&frame, body, privateio.MaxPayloadFrame)
		if _, err := Environment(&frame, []string{"/bin/true"}, 8<<20); err == nil {
			t.Fatal("oversized exec accepted")
		}
	}
}

func TestBootstrapExecRemovesInheritedEnvironmentInputAndFDs(t *testing.T) {
	var input bytes.Buffer
	frame := privateio.Bootstrap{Protocol: 1, Environment: []privateio.EnvironmentEntry{{Name: "PATH", Value: "/usr/bin:/bin"}, {Name: "TOKEN", Value: "synthetic-private-input"}}}
	body, _ := json.Marshal(frame)
	defer clear(body)
	if privateio.WriteBytes(&input, body, privateio.MaxPayloadFrame) != nil {
		t.Fatal("fixture frame")
	}
	file, err := os.CreateTemp(t.TempDir(), "private-fd")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBootstrapChild$")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LOKI_BOOTSTRAP_CHILD=1", "PARENT_SECRET=must-not-inherit"}
	cmd.ExtraFiles = []*os.File{file}
	cmd.Stdin = &input
	output, err := cmd.Output()
	if err != nil || string(output) != "ok" {
		t.Fatal("bootstrap exec leaked parent state or lost private input")
	}
}

func TestBootstrapChild(t *testing.T) {
	if os.Getenv("LOKI_BOOTSTRAP_CHILD") != "1" {
		return
	}
	if Run([]string{"/bin/sh", "-c", `test ! -e /proc/self/fd/3 && test -z "$PARENT_SECRET" && test "$(readlink /proc/self/fd/0)" = /dev/null && test -n "$TOKEN" && printf ok`}) != nil {
		os.Exit(126)
	}
}
