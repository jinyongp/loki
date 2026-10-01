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

func noProductApplianceAdvisory(context.Context, io.Writer) error {
	return nil
}

func TestProductUpdateAllContinuesOnlyAfterVerifiedFrontend(t *testing.T) {
	for _, scenario := range []string{"current", "updated", "replacement-fails", "invalid-installed"} {
		t.Run(scenario, func(t *testing.T) {
			client := &fakeProductUpdateClient{pointer: windowshost.FrontendReleasePointer{ReleaseTag: "v0.1.40"}, comparison: -1, download: []byte("candidate")}
			if scenario == "current" || scenario == "invalid-installed" {
				client.comparison = 0
			}
			continued := 0
			deps := productUpdateDependencies{
				CurrentBinding: func() (windowshost.ReleaseBinding, error) {
					return windowshost.ReleaseBinding{ReleaseTag: "v0.1.40"}, nil
				},
				Client: client,
				Stage: func(context.Context, []byte, windowshost.FrontendReleasePointer) (string, func(), error) {
					return "candidate.exe", func() {}, nil
				},
				Inspect: func(_ context.Context, path string) (windowshost.ReleaseBinding, error) {
					tag := "v0.1.40"
					if scenario == "invalid-installed" && path == "canonical.exe" {
						tag = "v0.1.39"
					}
					return windowshost.ReleaseBinding{ReleaseTag: tag}, nil
				},
				RunCandidate: func(_ context.Context, path string, args []string, _, _ io.Writer) int {
					if path != "candidate.exe" || !slices.Equal(args, []string{"bootstrap", "update"}) {
						t.Fatalf("candidate=%s args=%v", path, args)
					}
					if scenario == "replacement-fails" {
						return 5
					}
					return 0
				},
				CanonicalPath: func() (string, error) { return "canonical.exe", nil },
				ApplianceAdvisory: func(context.Context, io.Writer) error {
					t.Fatal("combined update must not print manual-update advisory")
					return nil
				},
				AfterFrontend: func(_ context.Context, path, tag string, _, _ io.Writer) int {
					if path != "canonical.exe" || tag != "v0.1.40" {
						t.Fatalf("continuation path=%s tag=%s", path, tag)
					}
					continued++
					return 6 // Appliance failures must remain failures of --all.
				},
			}
			var out, errOut bytes.Buffer
			code := runProductUpdateWith(t.Context(), deps, &out, &errOut)
			wantCode, wantContinued := 6, 1
			if scenario == "replacement-fails" {
				wantCode, wantContinued = 5, 0
			}
			if scenario == "invalid-installed" {
				wantCode, wantContinued = 1, 0
			}
			if code != wantCode || continued != wantContinued {
				t.Fatalf("code=%d continued=%d stderr=%s", code, continued, errOut.String())
			}
		})
	}
}

func TestProductUpdateCurrentFrontendDoesNotMutateAppliance(t *testing.T) {
	client := &fakeProductUpdateClient{
		pointer:    windowshost.FrontendReleasePointer{ReleaseTag: "v0.1.23"},
		comparison: 0,
	}
	advisoryCalls := 0
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
		ApplianceAdvisory: func(context.Context, io.Writer) error {
			advisoryCalls++
			return nil
		},
	}, &stdout, &stderr)
	if code != 0 || advisoryCalls != 1 || client.downloadCalls != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d advisory=%d downloads=%d stderr=%q", code, advisoryCalls, client.downloadCalls, stderr.String())
	}
	if got, want := stdout.String(), "Windows Loki frontend is current at v0.1.23.\n"; got != want {
		t.Fatalf("stdout=%q want=%q", got, want)
	}
}

func TestProductUpdateAdvisoryFailureDoesNotFailFrontendUpdate(t *testing.T) {
	client := &fakeProductUpdateClient{
		pointer:    windowshost.FrontendReleasePointer{ReleaseTag: "v0.1.23"},
		comparison: 0,
	}
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
		ApplianceAdvisory: func(context.Context, io.Writer) error {
			return errors.New("offline")
		},
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "Could not determine Loki appliance update readiness: offline") ||
		!strings.Contains(got, "loki update status") {
		t.Fatalf("stderr=%q", got)
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
	advisoryCalls := 0
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
			if executable != "candidate.exe" || strings.Join(args, " ") != "bootstrap update" {
				t.Fatalf("candidate=%q args=%q", executable, args)
			}
			return 0
		},
		CanonicalPath: func() (string, error) { return "canonical.exe", nil },
		ApplianceAdvisory: func(context.Context, io.Writer) error {
			advisoryCalls++
			return nil
		},
		Progress: progress.ReporterFunc(func(event progress.Event) {
			phases = append(phases, event.Phase)
		}),
	}, &stdout, &stderr)
	if code != 0 || advisoryCalls != 1 || client.downloadCalls != 1 || cleanupCalls != 1 || runCalls != 1 || inspectCalls != 2 || stderr.Len() != 0 {
		t.Fatalf("code=%d advisory=%d downloads=%d cleanup=%d run=%d inspect=%d stderr=%q",
			code, advisoryCalls, client.downloadCalls, cleanupCalls, runCalls, inspectCalls, stderr.String())
	}
	if got, want := stdout.String(), "Windows Loki frontend is updated to v0.1.24.\n"; got != want {
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
		RunCandidate:      func(context.Context, string, []string, io.Writer, io.Writer) int { return 0 },
		CanonicalPath:     func() (string, error) { return "", nil },
		ApplianceAdvisory: noProductApplianceAdvisory,
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
		CanonicalPath:     func() (string, error) { return "canonical.exe", nil },
		ApplianceAdvisory: noProductApplianceAdvisory,
	}, &stdout, &stderr)
	if code != 1 || runCalls != 0 {
		t.Fatalf("code=%d run=%d stderr=%q", code, runCalls, stderr.String())
	}
	if got, want := stderr.String(), "downloaded Windows frontend binding v0.1.25 does not match published release v0.1.24\n"; got != want {
		t.Fatalf("stderr=%q want=%q", got, want)
	}
}
