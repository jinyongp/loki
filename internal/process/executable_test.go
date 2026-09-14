package process

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutionUsesChildPathOnly(t *testing.T) {
	untrusted, trusted := t.TempDir(), t.TempDir()
	for _, item := range []struct{ directory, text string }{{untrusted, "ambient"}, {trusted, "child"}} {
		if err := os.WriteFile(filepath.Join(item.directory, "fixture"), []byte("#!/bin/sh\nprintf "+item.text), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", untrusted)
	spec := Spec{Argv: []string{"fixture"}, CWD: trusted, Env: []string{"PATH=" + trusted}, Timeout: time.Second, MaxOutput: 4096}
	r, err := Run(t.Context(), spec)
	if err != nil || r.Output != "child" {
		t.Fatalf("finite command inherited server PATH: %q %v", r.Output, err)
	}
	spec.Env = []string{"PATH="}
	r, err = Run(t.Context(), spec)
	if err != nil || r.Output != "child" {
		t.Fatalf("empty PATH did not use command cwd: %q %v", r.Output, err)
	}
	spec.Env = []string{}
	if _, err = Run(t.Context(), spec); err == nil {
		t.Fatal("missing child PATH used server PATH")
	}
	spec.Argv = []string{"printf", "default"}
	r, err = Run(t.Context(), spec)
	if err != nil || r.Output != "default" {
		t.Fatalf("POSIX default PATH: %q %v", r.Output, err)
	}
	spec.Env = nil
	r, err = Run(t.Context(), spec)
	if err != nil || r.Output != "default" {
		t.Fatalf("default environment PATH: %q %v", r.Output, err)
	}
}
