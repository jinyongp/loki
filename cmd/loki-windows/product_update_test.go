package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

type fakeProductUpdateClient struct {
	pointer       windowshost.FrontendReleasePointer
	comparison    int
	resolveErr    error
	downloadErr   error
	download      []byte
	downloadCalls int
}

func (client *fakeProductUpdateClient) Resolve(context.Context) (windowshost.FrontendReleasePointer, error) {
	return client.pointer, client.resolveErr
}

func (client *fakeProductUpdateClient) Download(
	context.Context,
	windowshost.FrontendReleasePointer,
) ([]byte, error) {
	client.downloadCalls++
	return append([]byte(nil), client.download...), client.downloadErr
}

func (client *fakeProductUpdateClient) Compare(
	string,
	windowshost.FrontendReleasePointer,
) (int, error) {
	return client.comparison, nil
}

func TestProductUpdateCurrentFrontendConvergesApplianceWithoutDownload(t *testing.T) {
	client := &fakeProductUpdateClient{
		pointer:    windowshost.FrontendReleasePointer{ReleaseTag: "v0.1.23"},
		comparison: 0,
	}
	converged := 0
	var stdout, stderr bytes.Buffer
	code := runProductUpdateWith(t.Context(), productUpdateDependencies{
		CurrentBinding: func() (windowshost.ReleaseBinding, error) {
			return windowshost.ReleaseBinding{ReleaseTag: "v0.1.23"}, nil
		},
		Client: client,
		Stage: func(context.Context, []byte, windowshost.FrontendReleasePointer) (string, func(), error) {
			t.Fatal("stage called for current frontend")
			return "", func() {}, nil
		},
		Inspect: func(context.Context, string) (windowshost.ReleaseBinding, error) {
			t.Fatal("inspect called for current frontend")
			return windowshost.ReleaseBinding{}, nil
		},
		RunCandidate: func(context.Context, string, []string, io.Writer, io.Writer) int {
			t.Fatal("candidate executed for current frontend")
			return 1
		},
		CanonicalPath: func() (string, error) {
			t.Fatal("canonical path resolved for current frontend")
			return "", nil
		},
		Converge: func(context.Context, io.Writer, io.Writer) int {
			converged++
			return 0
		},
	}, &stdout, &stderr)
	if code != 0 || converged != 1 || client.downloadCalls != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d converged=%d downloads=%d stderr=%q", code, converged, client.downloadCalls, stderr.String())
	}
	if got, want := stdout.String(), "Windows Loki frontend is current at v0.1.23.\n"; got != want {
		t.Fatalf("stdout=%q want=%q", got, want)
	}
}

func TestProductUpdateOlderFrontendUsesVerifiedCandidateAndVerifiesCanonical(t *testing.T) {
	client := &fakeProductUpdateClient{
		pointer:    windowshost.FrontendReleasePointer{ReleaseTag: "v0.1.24"},
		comparison: -1,
		download:   []byte("candidate"),
	}
	cleanupCalls := 0
	runCalls := 0
	inspectCalls := 0
	var phases []string
	var stdout, stderr bytes.Buffer
	code := runProductUpdateWith(t.Context(), productUpdateDependencies{
		CurrentBinding: func() (windowshost.ReleaseBinding, error) {
			return windowshost.ReleaseBinding{ReleaseTag: "v0.1.23"}, nil
		},
		Client: client,
		Stage: func(_ context.Context, raw []byte, pointer windowshost.FrontendReleasePointer) (string, func(), error) {
			if string(raw) != "candidate" || pointer.ReleaseTag != "v0.1.24" {
				t.Fatalf("stage raw=%q pointer=%+v", raw, pointer)
			}
			return "candidate.exe", func() { cleanupCalls++ }, nil
		},
		Inspect: func(_ context.Context, executable string) (windowshost.ReleaseBinding, error) {
			inspectCalls++
			switch executable {
			case "candidate.exe", "canonical.exe":
				return windowshost.ReleaseBinding{ReleaseTag: "v0.1.24"}, nil
			default:
				return windowshost.ReleaseBinding{}, errors.New("unexpected executable")
			}
		},
		RunCandidate: func(_ context.Context, executable string, args []string, _, _ io.Writer) int {
			runCalls++
			if executable != "candidate.exe" || strings.Join(args, " ") != "bootstrap install" {
				t.Fatalf("candidate=%q args=%q", executable, args)
			}
			return 0
		},
		CanonicalPath: func() (string, error) { return "canonical.exe", nil },
		Converge: func(context.Context, io.Writer, io.Writer) int {
			t.Fatal("current-version converge called during frontend update")
			return 1
		},
		Progress: progress.ReporterFunc(func(event progress.Event) {
			phases = append(phases, event.Phase)
		}),
	}, &stdout, &stderr)
	if code != 0 || client.downloadCalls != 1 || cleanupCalls != 1 || runCalls != 1 || inspectCalls != 2 || stderr.Len() != 0 {
		t.Fatalf("code=%d downloads=%d cleanup=%d run=%d inspect=%d stderr=%q",
			code, client.downloadCalls, cleanupCalls, runCalls, inspectCalls, stderr.String())
	}
	if got, want := stdout.String(), "Loki is updated to v0.1.24.\n"; got != want {
		t.Fatalf("stdout=%q want=%q", got, want)
	}
	wantPhases := []string{"resolve", "download-frontend", "verify-frontend", "install-frontend", "verify-installed-frontend"}
	if !slices.Equal(phases, wantPhases) {
		t.Fatalf("phases=%#v want=%#v", phases, wantPhases)
	}
}

func TestProductUpdateRefusesPublishedDowngradeBeforeDownload(t *testing.T) {
	client := &fakeProductUpdateClient{
		pointer:    windowshost.FrontendReleasePointer{ReleaseTag: "v0.1.23"},
		comparison: 1,
	}
	var stdout, stderr bytes.Buffer
	code := runProductUpdateWith(t.Context(), productUpdateDependencies{
		CurrentBinding: func() (windowshost.ReleaseBinding, error) {
			return windowshost.ReleaseBinding{ReleaseTag: "v0.1.24"}, nil
		},
		Client: client,
		Stage: func(context.Context, []byte, windowshost.FrontendReleasePointer) (string, func(), error) {
			return "", func() {}, nil
		},
		Inspect: func(context.Context, string) (windowshost.ReleaseBinding, error) {
			return windowshost.ReleaseBinding{}, nil
		},
		RunCandidate:  func(context.Context, string, []string, io.Writer, io.Writer) int { return 0 },
		CanonicalPath: func() (string, error) { return "", nil },
		Converge:      func(context.Context, io.Writer, io.Writer) int { return 0 },
	}, &stdout, &stderr)
	if code != 1 || client.downloadCalls != 0 {
		t.Fatalf("code=%d downloads=%d stderr=%q", code, client.downloadCalls, stderr.String())
	}
	if got, want := stderr.String(), "installed Windows frontend v0.1.24 is newer than published release v0.1.23; refusing downgrade\n"; got != want {
		t.Fatalf("stderr=%q want=%q", got, want)
	}
}

func TestProductUpdateRejectsCandidateBindingMismatch(t *testing.T) {
	client := &fakeProductUpdateClient{
		pointer:    windowshost.FrontendReleasePointer{ReleaseTag: "v0.1.24"},
		comparison: -1,
		download:   []byte("candidate"),
	}
	runCalls := 0
	var stdout, stderr bytes.Buffer
	code := runProductUpdateWith(t.Context(), productUpdateDependencies{
		CurrentBinding: func() (windowshost.ReleaseBinding, error) {
			return windowshost.ReleaseBinding{ReleaseTag: "v0.1.23"}, nil
		},
		Client: client,
		Stage: func(context.Context, []byte, windowshost.FrontendReleasePointer) (string, func(), error) {
			return "candidate.exe", func() {}, nil
		},
		Inspect: func(context.Context, string) (windowshost.ReleaseBinding, error) {
			return windowshost.ReleaseBinding{ReleaseTag: "v0.1.25"}, nil
		},
		RunCandidate: func(context.Context, string, []string, io.Writer, io.Writer) int {
			runCalls++
			return 0
		},
		CanonicalPath: func() (string, error) { return "canonical.exe", nil },
		Converge:      func(context.Context, io.Writer, io.Writer) int { return 0 },
	}, &stdout, &stderr)
	if code != 1 || runCalls != 0 {
		t.Fatalf("code=%d run=%d stderr=%q", code, runCalls, stderr.String())
	}
	if got, want := stderr.String(), "downloaded Windows frontend binding v0.1.25 does not match published release v0.1.24\n"; got != want {
		t.Fatalf("stderr=%q want=%q", got, want)
	}
}
