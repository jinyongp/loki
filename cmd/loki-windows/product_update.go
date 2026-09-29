package main

import (
	"context"
	"fmt"
	"io"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

type productUpdateClient interface {
	Resolve(context.Context) (windowshost.FrontendReleasePointer, error)
	Download(context.Context, windowshost.FrontendReleasePointer) ([]byte, error)
	Compare(string, windowshost.FrontendReleasePointer) (int, error)
}

type productUpdateDependencies struct {
	CurrentBinding    func() (windowshost.ReleaseBinding, error)
	Client            productUpdateClient
	Stage             func(context.Context, []byte, windowshost.FrontendReleasePointer) (string, func(), error)
	Inspect           func(context.Context, string) (windowshost.ReleaseBinding, error)
	RunCandidate      func(context.Context, string, []string, io.Writer, io.Writer) int
	CanonicalPath     func() (string, error)
	ApplianceAdvisory func(context.Context, io.Writer) error
	Progress          progress.Reporter
}

func runProductUpdateWith(
	ctx context.Context,
	deps productUpdateDependencies,
	stdout, stderr io.Writer,
) int {
	if deps.CurrentBinding == nil || deps.Client == nil || deps.Stage == nil || deps.Inspect == nil ||
		deps.RunCandidate == nil || deps.CanonicalPath == nil || deps.ApplianceAdvisory == nil {
		fmt.Fprintln(stderr, "Windows Loki product update is not configured")
		return 1
	}
	current, err := deps.CurrentBinding()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	progress.Emit(deps.Progress, progress.Event{Operation: "update", Phase: "resolve", State: progress.StateStarted, Message: "Checking the latest published Loki release..."})
	pointer, err := deps.Client.Resolve(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	comparison, err := deps.Client.Compare(current.ReleaseTag, pointer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if comparison > 0 {
		fmt.Fprintf(stderr, "installed Windows frontend %s is newer than published release %s; refusing downgrade\n",
			current.ReleaseTag, pointer.ReleaseTag)
		return 1
	}
	if comparison == 0 {
		fmt.Fprintf(stdout, "Windows Loki frontend is current at %s.\n", current.ReleaseTag)
		return finishProductUpdateAdvisory(ctx, deps, stdout, stderr)
	}

	progress.Emit(deps.Progress, progress.Event{Operation: "update", Phase: "download-frontend", State: progress.StateStarted, Message: fmt.Sprintf("Downloading Windows frontend %s...", pointer.ReleaseTag)})
	raw, err := deps.Client.Download(ctx, pointer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	progress.Emit(deps.Progress, progress.Event{Operation: "update", Phase: "verify-frontend", State: progress.StateStarted, Message: "Verifying the downloaded Windows frontend..."})
	candidate, cleanup, err := deps.Stage(ctx, raw, pointer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()

	candidateBinding, err := deps.Inspect(ctx, candidate)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if candidateBinding.ReleaseTag != pointer.ReleaseTag {
		fmt.Fprintf(stderr, "downloaded Windows frontend binding %s does not match published release %s\n",
			candidateBinding.ReleaseTag, pointer.ReleaseTag)
		return 1
	}

	progress.Emit(deps.Progress, progress.Event{
		Operation: "update", Phase: "install-frontend", State: progress.StateStarted,
		Message: fmt.Sprintf("Installing Windows frontend %s -> %s...", current.ReleaseTag, pointer.ReleaseTag),
	})
	if code := deps.RunCandidate(ctx, candidate, []string{"bootstrap", "update"}, stdout, stderr); code != 0 {
		return code
	}

	canonical, err := deps.CanonicalPath()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	progress.Emit(deps.Progress, progress.Event{Operation: "update", Phase: "verify-installed-frontend", State: progress.StateStarted, Message: "Verifying the installed Windows frontend..."})
	installedBinding, err := deps.Inspect(ctx, canonical)
	if err != nil {
		fmt.Fprintln(stderr, "verify updated Windows frontend:", err)
		return 1
	}
	if installedBinding.ReleaseTag != pointer.ReleaseTag {
		fmt.Fprintf(stderr, "updated Windows frontend is %s; expected %s\n",
			installedBinding.ReleaseTag, pointer.ReleaseTag)
		return 1
	}
	fmt.Fprintf(stdout, "Windows Loki frontend is updated to %s.\n", pointer.ReleaseTag)
	return finishProductUpdateAdvisory(ctx, deps, stdout, stderr)
}

func finishProductUpdateAdvisory(
	ctx context.Context,
	deps productUpdateDependencies,
	stdout, stderr io.Writer,
) int {
	if err := deps.ApplianceAdvisory(ctx, stdout); err != nil {
		fmt.Fprintln(stderr, "Could not determine Loki appliance update readiness:", err)
		fmt.Fprintln(stderr, "Run 'loki update status' to inspect the appliance lifecycle.")
	}
	return 0
}
