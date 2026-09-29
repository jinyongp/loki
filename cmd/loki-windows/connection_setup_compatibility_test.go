package main

import (
	"fmt"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
)

func TestConnectionSetupCompatibilityAcceptsMatchingReleases(t *testing.T) {
	if err := connectionSetupCompatibilityError("v0.1.29", "0.1.29", false); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionSetupCompatibilityExplainsOlderAppliance(t *testing.T) {
	err := connectionSetupCompatibilityError("v0.1.29", "0.1.28", false)
	if err == nil {
		t.Fatal("expected mismatch")
	}
	message := err.Error()
	for _, required := range []string{
		"OpenAI connection setup requires Loki server v0.1.29 or later; running server is v0.1.28",
		"loki update prepare",
		"loki update apply",
		"loki connection setup openai",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("message lacks %q: %s", required, message)
		}
	}
}

func TestConnectionSetupCompatibilityUsesPreparedUpdateShortcut(t *testing.T) {
	err := connectionSetupCompatibilityError("v0.1.29", "v0.1.28", true)
	if err == nil {
		t.Fatal("expected mismatch")
	}
	message := err.Error()
	if !strings.Contains(message, "already prepared") || !strings.Contains(message, "loki update apply") {
		t.Fatalf("message=%s", message)
	}
	if strings.Contains(message, "loki update prepare") {
		t.Fatalf("prepared update unnecessarily asks to prepare again: %s", message)
	}
}

func TestConnectionSetupCompatibilityAllowsIndependentFrontendUpdates(t *testing.T) {
	for _, versions := range [][2]string{
		{"v0.1.30", "v0.1.29"},
		{"v0.1.29", "v0.1.30"},
	} {
		if err := connectionSetupCompatibilityError(versions[0], versions[1], false); err != nil {
			t.Fatalf("compatible server %s must not require matching frontend %s: %v", versions[1], versions[0], err)
		}
	}
}

func TestConnectionSetupCompatibilityRejectsMatchingUnsupportedRelease(t *testing.T) {
	if err := connectionSetupCompatibilityError("v0.1.28", "v0.1.28", false); err == nil {
		t.Fatal("matching release numbers do not make the pre-fix MCP server compatible")
	}
}

func TestConnectionSetupUsesActiveReleaseInsteadOfInitialImage(t *testing.T) {
	for _, test := range []struct {
		name, image, active string
		wantError           bool
	}{
		{"updated-in-place", "0.1.28", "0.1.29", false},
		{"newer-active-server", "0.1.28", "0.1.31", false},
		{"rolled-back-server", "0.1.30", "0.1.28", true},
		{"no-active-release", "0.1.30", "", true},
		{"invalid-active-release", "0.1.30", "invalid", true},
		{"pre-fix-prerelease", "0.1.30", "0.1.29-rc.1", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := windowshost.OperatorResult{
				DistributionVersion: test.image,
				Probe: windowshost.NativeProbe{Stdout: fmt.Sprintf(
					`{"schema_version":1,"state":"installed","release":%q,"update_prepared":false}`, test.active)},
			}
			err := connectionSetupCompatibilityFromStatus("v0.1.30", result)
			if (err != nil) != test.wantError {
				t.Fatalf("image=%s active=%s err=%v", test.image, test.active, err)
			}
		})
	}
}

func TestConnectionSetupStatusFailureDoesNotFallBackToImage(t *testing.T) {
	for name, probe := range map[string]windowshost.NativeProbe{
		"failed-process": {ExitCode: 1, Stdout: `{"schema_version":1,"state":"installed","release":"0.1.30"}`},
		"bad-json":       {Stdout: `not JSON`},
		"bad-schema":     {Stdout: `{"schema_version":2,"state":"installed","release":"0.1.30"}`},
	} {
		t.Run(name, func(t *testing.T) {
			result := windowshost.OperatorResult{DistributionVersion: "0.1.30", Probe: probe}
			if err := connectionSetupCompatibilityFromStatus("v0.1.30", result); err == nil {
				t.Fatal("failed live status accepted using the initial image identity")
			}
		})
	}
}
