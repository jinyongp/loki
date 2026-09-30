package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
)

func TestIntegrationDoctorRendersFailureAndPreservesExitCode(t *testing.T) {
	probe := windowshost.NativeProbe{ExitCode: 1, Stdout: `{"schema_version":1,"name":"github","state":"degraded","configured":true,"enabled":true,"ready":false,"detail":"GitHub App token exchange was rejected"}`}
	var stdout, stderr bytes.Buffer
	code := writeWindowsIntegrationInspection("doctor", probe, false, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, text := range []string{"Loki integration: github", "State: degraded", "Ready: false", "GitHub App token exchange was rejected"} {
		if !strings.Contains(stdout.String(), text) {
			t.Fatalf("missing %q in %q", text, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "schema_version") || strings.Contains(stdout.String(), `{"`) {
		t.Fatalf("raw JSON leaked into human report: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code = writeWindowsIntegrationInspection("doctor", probe, true, &stdout, &stderr); code != 1 || !json.Valid(stdout.Bytes()) {
		t.Fatalf("machine report code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestIntegrationInspectionHandlesProcessAndSchemaFailures(t *testing.T) {
	for _, fixture := range []struct {
		probe windowshost.NativeProbe
		want  int
		text  string
	}{
		{windowshost.NativeProbe{ExitCode: 7, Stderr: "appliance is unavailable"}, 7, "appliance is unavailable"},
		{windowshost.NativeProbe{Stdout: `{"schema_version":999}`}, 1, "Cannot decode"},
		{windowshost.NativeProbe{Stdout: `{"schema_version":1}`}, 1, "Cannot decode"},
	} {
		var stdout, stderr bytes.Buffer
		if code := writeWindowsIntegrationInspection("status", fixture.probe, false, &stdout, &stderr); code != fixture.want || !strings.Contains(stderr.String(), fixture.text) {
			t.Fatalf("code=%d output=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}
