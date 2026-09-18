package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeLauncher struct {
	workload Workload
	result   LaunchResult
	err      error
	calls    int
}

func (f *fakeLauncher) Run(_ context.Context, workload Workload) (LaunchResult, error) {
	f.calls++
	f.workload = Workload{ID: workload.ID, CWD: workload.CWD, Argv: append([]string(nil), workload.Argv...)}
	return f.result, f.err
}

func fixedID(value string) IDSource {
	return func() (string, error) { return value, nil }
}

func validRequest() RunRequest {
	return RunRequest{CWD: ".", Argv: []string{"/usr/bin/git", "status", "--short"}}
}

func TestServiceGeneratesJobIdentityAndCopiesRequest(t *testing.T) {
	launcher := &fakeLauncher{result: LaunchResult{ExitCode: 7}}
	id := strings.Repeat("a", 32)
	service, err := NewService(launcher, fixedID(id))
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	result, err := service.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Argv[1] = "mutated"
	if result.JobID != id || result.ExitCode != 7 {
		t.Fatalf("result = %#v", result)
	}
	if launcher.workload.ID != id || launcher.workload.CWD != "." ||
		len(launcher.workload.Argv) != 3 || launcher.workload.Argv[1] != "status" {
		t.Fatalf("workload = %#v", launcher.workload)
	}
}

func TestServiceRejectsInvalidRequestBeforeLauncher(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunRequest)
	}{
		{"cwd-empty", func(r *RunRequest) { r.CWD = "" }},
		{"cwd-absolute", func(r *RunRequest) { r.CWD = "/workspace" }},
		{"cwd-traversal", func(r *RunRequest) { r.CWD = "../outside" }},
		{"cwd-unclean", func(r *RunRequest) { r.CWD = "repo/../other" }},
		{"argv-empty", func(r *RunRequest) { r.Argv = nil }},
		{"argv-relative", func(r *RunRequest) { r.Argv[0] = "git" }},
		{"argv-root", func(r *RunRequest) { r.Argv[0] = "/" }},
		{"argv-nul", func(r *RunRequest) { r.Argv[1] = "bad\x00arg" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			launcher := &fakeLauncher{}
			service, err := NewService(launcher, fixedID(strings.Repeat("a", 32)))
			if err != nil {
				t.Fatal(err)
			}
			request := validRequest()
			request.Argv = append([]string(nil), request.Argv...)
			test.mutate(&request)
			if _, err := service.Run(t.Context(), request); err == nil {
				t.Fatal("invalid request was accepted")
			}
			if launcher.calls != 0 {
				t.Fatalf("launcher calls = %d", launcher.calls)
			}
		})
	}
	request := validRequest()
	request.Argv = make([]string, maxArgs+1)
	request.Argv[0] = "/bin/true"
	for index := 1; index < len(request.Argv); index++ {
		request.Argv[index] = "x"
	}
	launcher := &fakeLauncher{}
	service, err := NewService(launcher, fixedID(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Run(t.Context(), request); err == nil {
		t.Fatal("oversized argv was accepted")
	}
}

func TestServiceFailsClosedOnDependenciesAndLauncherErrors(t *testing.T) {
	if _, err := NewService(nil, RandomID); err == nil {
		t.Fatal("nil launcher was accepted")
	}
	if _, err := NewService(&fakeLauncher{}, nil); err == nil {
		t.Fatal("nil ID source was accepted")
	}

	launcher := &fakeLauncher{}
	service, err := NewService(launcher, fixedID("bad"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Run(t.Context(), validRequest()); err == nil {
		t.Fatal("invalid generated ID was accepted")
	}
	if launcher.calls != 0 {
		t.Fatal("launcher called with invalid ID")
	}

	want := errors.New("synthetic launcher failure")
	launcher = &fakeLauncher{err: want}
	service, err = NewService(launcher, fixedID(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Run(t.Context(), validRequest()); !errors.Is(err, want) {
		t.Fatalf("launcher error = %v", err)
	}

	launcher = &fakeLauncher{result: LaunchResult{ExitCode: 999}}
	service, err = NewService(launcher, fixedID(strings.Repeat("c", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Run(t.Context(), validRequest()); err == nil {
		t.Fatal("invalid exit code was accepted")
	}
}

func TestRandomIDProducesOpaqueUniqueIDs(t *testing.T) {
	seen := map[string]struct{}{}
	for range 128 {
		id, err := RandomID()
		if err != nil {
			t.Fatal(err)
		}
		if !jobIDPattern.MatchString(id) {
			t.Fatalf("ID = %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = struct{}{}
	}
}
