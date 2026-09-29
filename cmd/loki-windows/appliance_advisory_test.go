package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
)

func TestResolveApplianceUpdateAdvisory(t *testing.T) {
	tests := []struct {
		name       string
		status     windowshost.OperatorStatus
		published  string
		comparison int
		want       applianceAdvisoryState
	}{
		{name: "current", status: windowshost.OperatorStatus{Release: "v0.1.28"}, published: "v0.1.28", comparison: 0, want: applianceAdvisoryCurrent},
		{name: "prepare", status: windowshost.OperatorStatus{Release: "v0.1.28"}, published: "v0.1.29", comparison: -1, want: applianceAdvisoryPrepare},
		{name: "prepared", status: windowshost.OperatorStatus{Release: "v0.1.28", UpdatePrepared: true}, want: applianceAdvisoryPrepared},
		{name: "available-local", status: windowshost.OperatorStatus{Release: "v0.1.28", UpdateAvailable: true}, want: applianceAdvisoryPrepare},
		{name: "newer", status: windowshost.OperatorStatus{Release: "v0.1.30"}, published: "v0.1.29", comparison: 1, want: applianceAdvisoryNewer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolveCalls := 0
			advisory, err := resolveApplianceUpdateAdvisory(t.Context(), applianceAdvisoryDependencies{
				Status: func(context.Context) (windowshost.OperatorStatus, error) { return test.status, nil },
				Resolve: func(context.Context) (windowshost.ApplianceReleasePointer, error) {
					resolveCalls++
					return windowshost.ApplianceReleasePointer{ReleaseTag: test.published}, nil
				},
				Compare: func(string, windowshost.ApplianceReleasePointer) (int, error) { return test.comparison, nil },
			})
			if err != nil || advisory.State != test.want {
				t.Fatalf("advisory=%#v err=%v", advisory, err)
			}
			if (test.status.UpdatePrepared || test.status.UpdateAvailable) && resolveCalls != 0 {
				t.Fatalf("local lifecycle state unexpectedly triggered remote release check: %d", resolveCalls)
			}
		})
	}
}

func TestRenderApplianceUpdateAdvisoryGuidance(t *testing.T) {
	var out bytes.Buffer
	if err := renderApplianceUpdateAdvisory(applianceUpdateAdvisory{
		State: applianceAdvisoryPrepare, Installed: "v0.1.28", Published: "v0.1.29",
	}, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "v0.1.28 has an update available at v0.1.29") ||
		!strings.Contains(got, "loki update prepare") {
		t.Fatalf("guidance=%q", got)
	}
}

func TestResolveApplianceUpdateAdvisoryPropagatesInspectionFailure(t *testing.T) {
	_, err := resolveApplianceUpdateAdvisory(t.Context(), applianceAdvisoryDependencies{
		Status: func(context.Context) (windowshost.OperatorStatus, error) {
			return windowshost.OperatorStatus{}, errors.New("offline")
		},
		Resolve: func(context.Context) (windowshost.ApplianceReleasePointer, error) {
			return windowshost.ApplianceReleasePointer{}, nil
		},
		Compare: func(string, windowshost.ApplianceReleasePointer) (int, error) { return 0, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("error=%v", err)
	}
}
