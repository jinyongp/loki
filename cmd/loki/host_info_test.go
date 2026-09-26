package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

func installedHostInfoFixture(t *testing.T) (string, string, lifecycle.Generation) {
	t.Helper()
	now := time.Date(2026, 9, 22, 11, 0, 0, 0, time.UTC)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	candidate := hostGenerationFixture(t, now)
	backend := &fakeHostRuntimeBackend{}
	var stdout, stderr bytes.Buffer
	if code := runHostInstallWith(t.Context(), hostInstallOptions{
		StateRoot: stateRoot, Workspace: workspace, DockerAccess: hostDockerAccessDirect, JSON: true,
	}, candidate, backend, &stdout, &stderr); code != 0 {
		t.Fatalf("install fixture code=%d stderr=%q", code, stderr.String())
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "mcp-token"), []byte("super-secret-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return stateRoot, workspace, candidate
}

func TestRunHostStatusTextAndJSON(t *testing.T) {
	stateRoot, workspace, candidate := installedHostInfoFixture(t)

	var textOut, textErr bytes.Buffer
	if code := runHostStatus([]string{"--state-root", stateRoot}, &textOut, &textErr); code != 0 || textErr.Len() != 0 {
		t.Fatalf("status text code=%d stdout=%q stderr=%q", code, textOut.String(), textErr.String())
	}
	for _, required := range []string{
		"Loki host", "Status: installed", "Release: " + candidate.Spec.Version,
		"Workspace: " + workspace, "Docker: direct",
	} {
		if !strings.Contains(textOut.String(), required) {
			t.Fatalf("status text lacks %q: %s", required, textOut.String())
		}
	}

	var jsonOut, jsonErr bytes.Buffer
	if code := runHostStatus([]string{"--state-root", stateRoot, "--json"}, &jsonOut, &jsonErr); code != 0 || jsonErr.Len() != 0 {
		t.Fatalf("status json code=%d stdout=%q stderr=%q", code, jsonOut.String(), jsonErr.String())
	}
	var report hostStatusReport
	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.State != "installed" || report.Release != candidate.Spec.Version || report.Workspace != workspace ||
		report.DockerAccess != hostDockerAccessDirect {
		t.Fatalf("status report = %#v", report)
	}
}

func TestRunHostConnectionDoesNotDiscloseToken(t *testing.T) {
	stateRoot, _, _ := installedHostInfoFixture(t)

	var stdout, stderr bytes.Buffer
	if code := runHostConnection([]string{"--state-root", stateRoot}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("connection code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), defaultMCPLocalOriginURL) ||
		!strings.Contains(stdout.String(), filepath.Join(stateRoot, "mcp-token")) ||
		!strings.Contains(stdout.String(), "Bearer token") ||
		!strings.Contains(stdout.String(), "local origin, not a public MCP URL") {
		t.Fatalf("connection output = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "super-secret-token") {
		t.Fatalf("connection output disclosed MCP token: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runHostConnection([]string{"--state-root", stateRoot, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("connection json code=%d stderr=%q", code, stderr.String())
	}
	var report hostConnectionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.LocalOrigin.URL != defaultMCPLocalOriginURL ||
		report.LocalOrigin.Transport != "streamable-http" || report.LocalOrigin.Reachability != "loopback" ||
		report.LocalOrigin.Authentication.Type != "bearer-token-file" ||
		report.LocalOrigin.Authentication.TokenFile != filepath.Join(stateRoot, "mcp-token") {
		t.Fatalf("connection report = %#v", report)
	}
	if strings.Contains(stdout.String(), "super-secret-token") {
		t.Fatalf("connection JSON disclosed MCP token: %q", stdout.String())
	}
}

func TestRunHostConnectionUsesInstalledMCPPort(t *testing.T) {
	now := time.Date(2026, 9, 22, 11, 15, 0, 0, time.UTC)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	candidate := hostGenerationFixture(t, now)
	var installOut, installErr bytes.Buffer
	if code := runHostInstallWith(t.Context(), hostInstallOptions{
		StateRoot: stateRoot, Workspace: workspace, DockerAccess: hostDockerAccessDirect,
		MCPPort: 19000, JSON: true,
	}, candidate, &fakeHostRuntimeBackend{}, &installOut, &installErr); code != 0 {
		t.Fatalf("install code=%d stderr=%q", code, installErr.String())
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "mcp-token"), []byte("super-secret-token"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runHostConnection([]string{"--state-root", stateRoot, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("connection code=%d stderr=%q", code, stderr.String())
	}
	var report hostConnectionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.LocalOrigin.URL != "http://127.0.0.1:19000/mcp" {
		t.Fatalf("local origin = %#v", report.LocalOrigin)
	}
}

func TestRunHostConnectionRejectsUnsafeTokenFile(t *testing.T) {
	stateRoot, _, _ := installedHostInfoFixture(t)
	token := filepath.Join(stateRoot, "mcp-token")
	if err := os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runHostConnection([]string{"--state-root", stateRoot}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "authentication state is unavailable") || stdout.Len() != 0 {
		t.Fatalf("unsafe token connection code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestPublishHostCLIUsesManagedVersionedSymlinkAndRefusesUnmanagedTarget(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	now := time.Date(2026, 9, 22, 11, 30, 0, 0, time.UTC)
	base := hostGenerationFixture(t, now)
	spec := base.Spec
	spec.HostBinaryDigest = "sha256:" + hex.EncodeToString(sum[:])
	generation, err := lifecycle.NewGeneration(spec)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	paths := hostCLIInstallPaths{
		ReleaseRoot: filepath.Join(root, "releases"),
		Binary:      filepath.Join(root, "releases", strings.TrimPrefix(generation.ID, "sha256:"), "loki"),
		Link:        filepath.Join(root, "bin", "loki"),
	}
	if err = publishHostCLI(paths, generation); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(paths.Link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("managed CLI link = %v err=%v", info, err)
	}
	target, err := os.Readlink(paths.Link)
	if err != nil || filepath.Clean(target) != filepath.Clean(paths.Binary) {
		t.Fatalf("CLI link target=%q err=%v", target, err)
	}
	if err = verifyFileDigest(paths.Binary, generation.Spec.HostBinaryDigest); err != nil {
		t.Fatal(err)
	}
	if err = publishHostCLI(paths, generation); err != nil {
		t.Fatalf("idempotent CLI publication failed: %v", err)
	}

	unmanaged := filepath.Join(root, "unmanaged", "loki")
	if err = os.MkdirAll(filepath.Dir(unmanaged), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(unmanaged, []byte("do not replace"), 0755); err != nil {
		t.Fatal(err)
	}
	paths.Link = unmanaged
	if err = preflightHostCLIInstall(paths); err == nil || !strings.Contains(err.Error(), "unmanaged file") {
		t.Fatalf("unmanaged CLI target error = %v", err)
	}
}

func TestRunHostInstallWithIsIdempotentForSameInstalledRelease(t *testing.T) {
	now := time.Date(2026, 9, 22, 11, 45, 0, 0, time.UTC)
	previousNow := lifecycleTimeNow
	lifecycleTimeNow = func() time.Time { return now }
	defer func() { lifecycleTimeNow = previousNow }()

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	options := hostInstallOptions{
		StateRoot: filepath.Join(t.TempDir(), "state"), Workspace: workspace,
		DockerAccess: hostDockerAccessDirect, JSON: true,
	}
	candidate := hostGenerationFixture(t, now)
	backend := &fakeHostRuntimeBackend{}
	var stdout, stderr bytes.Buffer
	if code := runHostInstallWith(t.Context(), options, candidate, backend, &stdout, &stderr); code != 0 {
		t.Fatalf("first install code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runHostInstallWith(t.Context(), options, candidate, backend, &stdout, &stderr); code != 0 {
		t.Fatalf("repeat install code=%d stderr=%q", code, stderr.String())
	}
	var report hostInstallResult
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Installed || !report.AlreadyInstalled || report.GenerationID != candidate.ID || report.PlanID != "" {
		t.Fatalf("repeat install report = %#v", report)
	}
}

func TestHostInfoCommandsDispatchThroughHostCLI(t *testing.T) {
	stateRoot, _, _ := installedHostInfoFixture(t)
	for _, command := range [][]string{
		{"status", "--state-root", stateRoot},
		{"connection", "--state-root", stateRoot},
	} {
		var stdout, stderr bytes.Buffer
		if code := runHost(command, &stdout, &stderr); code != 0 || stderr.Len() != 0 || stdout.Len() == 0 {
			t.Fatalf("host %v code=%d stdout=%q stderr=%q", command, code, stdout.String(), stderr.String())
		}
	}
}

func TestHostInstallTextAndJSONOutput(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	previousNow := lifecycleTimeNow
	lifecycleTimeNow = func() time.Time { return now }
	defer func() { lifecycleTimeNow = previousNow }()

	run := func(jsonMode bool) string {
		workspace := filepath.Join(t.TempDir(), "workspace")
		if err := os.Mkdir(workspace, 0700); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := runHostInstallWith(t.Context(), hostInstallOptions{
			StateRoot: filepath.Join(t.TempDir(), "state"), Workspace: workspace,
			DockerAccess: hostDockerAccessDirect, JSON: jsonMode,
		}, hostGenerationFixture(t, now), &fakeHostRuntimeBackend{}, &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("install output code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	text := run(false)
	if !strings.Contains(text, "Loki installed successfully.") ||
		!strings.Contains(text, "MCP local origin: "+defaultMCPLocalOriginURL) ||
		!strings.Contains(text, "External access: user-managed") ||
		!strings.Contains(text, "Connection details: loki host connection") {
		t.Fatalf("install text output = %q", text)
	}
	jsonText := run(true)
	if !strings.Contains(jsonText, "\"plan_id\":") || strings.Contains(jsonText, "Loki installed successfully.") {
		t.Fatalf("install JSON output = %q", jsonText)
	}
}
