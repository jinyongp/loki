package main

import (
	"bytes"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
)

func TestRenderDoctorHuman(t *testing.T) {
	raw := `{"status":"healthy","generated_at":"2026-09-28T12:15:41Z","checks":[{"name":"runtime","status":"healthy","code":"runtime_ready","summary":"required runtime services are running","evidence":[{"name":"running_services","value":"mcp,runtime"}]}]}`
	var out bytes.Buffer
	if err := renderDoctor(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"Loki doctor: healthy",
		"runtime: healthy - required runtime services are running",
		"running_services: mcp,runtime",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("doctor output missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, `"status"`) {
		t.Fatalf("doctor human output leaked raw JSON: %s", got)
	}
}

func TestRenderPreparedUpdateHuman(t *testing.T) {
	raw := `{"id":"sha256:606c68e474c1d1b11a69a461d3dc3bb85cbbed2f4a63d42fbd4bfb626d3ecce6","active_generation_id":"sha256:84c73943f9ebfb4f9b7e103462fb410c8bea4ecbfd7d882d01d7e781f93d6041","candidate_generation_id":"sha256:3e10b2af89412a129edc4f02a629c050e9c7312e3ebce78d1406d5aaf15a76a7","impact":{"restart_required":true,"migration_required":false,"rollback_compatible":true}}`
	var out bytes.Buffer
	if err := renderPreparedUpdate(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"Prepared Loki update",
		"Plan: sha256:606c68e474c1",
		"Restart required: yes",
		"Migration required: no",
		"Rollback compatible: yes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("update output missing %q: %s", want, got)
		}
	}
}

func TestRenderBackupHuman(t *testing.T) {
	raw := `{"id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","created_at":"2026-09-28T12:22:01Z","installed":{"id":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","spec":{"version":"0.1.22"}}}`
	var out bytes.Buffer
	if err := renderBackup(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Created Loki backup", "ID: sha256:aaaa", "Release: v0.1.22"} {
		if !strings.Contains(got, want) {
			t.Fatalf("backup output missing %q: %s", want, got)
		}
	}
}

func TestRenderOperatorStatusLabelsDistributionVersionAsBaseImage(t *testing.T) {
	var out bytes.Buffer
	renderOperatorStatus(
		windowshost.OperatorStatus{State: "installed", Release: "v0.1.27"},
		windowshost.ReleaseBinding{ReleaseTag: "v0.1.27"},
		"loki-mcp",
		"0.1.19",
		&out,
	)
	got := out.String()
	if !strings.Contains(got, "Appliance base image: v0.1.19") ||
		!strings.Contains(got, "Appliance release: v0.1.27") ||
		strings.Contains(got, "  Appliance image:") {
		t.Fatalf("operator status did not distinguish base image from managed release: %s", got)
	}
}

func TestWriteMachineJSONRejectsTrailingData(t *testing.T) {
	var out bytes.Buffer
	if err := writeMachineJSON(&out, `{"ok":true}{"bad":true}`); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if out.Len() != 0 {
		t.Fatalf("invalid JSON was written: %q", out.String())
	}
}
