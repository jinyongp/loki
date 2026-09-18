package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	applauncher "loki/internal/app/launcher"
	"loki/internal/daemon"
	"loki/internal/platform/sandbox"
)

func validLauncherLayout(t *testing.T) launcherLayout {
	t.Helper()
	return launcherLayout{
		Socket:                 filepath.Join(t.TempDir(), "launcher.sock"),
		SocketGID:              os.Getgid(),
		ExecutorUID:            1001,
		DockerSocket:           filepath.Join(t.TempDir(), "docker.sock"),
		DockerPeerUID:          0,
		PolicySHA256:           strings.Repeat("a", 64),
		Image:                  "registry.example/loki@sha256:" + strings.Repeat("b", 64),
		Workspace:              t.TempDir(),
		Environment:            []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"},
		WorkloadUID:            2001,
		WorkloadGID:            2001,
		MemoryBytes:            512 << 20,
		PIDs:                   128,
		TmpfsBytes:             64 << 20,
		RunTimeoutSeconds:      30,
		ResultRetentionSeconds: 60,
		MaxJobs:                64,
	}
}

func TestBuildLauncherConstructsNarrowRoleInputs(t *testing.T) {
	layout := validLauncherLayout(t)
	options, err := buildLauncher(layout)
	if err != nil {
		t.Fatal(err)
	}
	if options.Socket != layout.Socket || options.SocketGID != layout.SocketGID ||
		options.ExecutorUID != layout.ExecutorUID || options.RunTimeout != 30*time.Second ||
		options.ResultRetention != 60*time.Second || options.MaxJobs != 64 {
		t.Fatalf("launcher options = %#v", options)
	}
	if !options.Policy.Valid() {
		t.Fatal("constructed policy is invalid")
	}
	if _, ok := options.Runner.(*sandbox.Engine); !ok {
		t.Fatalf("runner type = %T", options.Runner)
	}
	if options.Ready != nil {
		t.Fatal("builder installed runtime readiness callback")
	}
}

func TestBuildLauncherRejectsUnsafeLayout(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*launcherLayout)
	}{
		{"socket-relative", func(l *launcherLayout) { l.Socket = "launcher.sock" }},
		{"socket-root", func(l *launcherLayout) { l.Socket = "/" }},
		{"socket-gid", func(l *launcherLayout) { l.SocketGID = -1 }},
		{"executor-root", func(l *launcherLayout) { l.ExecutorUID = 0 }},
		{"shared-identity", func(l *launcherLayout) { l.ExecutorUID = l.WorkloadUID }},
		{"workload-root", func(l *launcherLayout) { l.WorkloadUID = 0 }},
		{"workload-root-group", func(l *launcherLayout) { l.WorkloadGID = 0 }},
		{"docker-relative", func(l *launcherLayout) { l.DockerSocket = "docker.sock" }},
		{"policy-digest", func(l *launcherLayout) { l.PolicySHA256 = "bad" }},
		{"image-tag", func(l *launcherLayout) { l.Image = "loki:latest" }},
		{"workspace-relative", func(l *launcherLayout) { l.Workspace = "workspace" }},
		{"memory", func(l *launcherLayout) { l.MemoryBytes = 1 }},
		{"pids", func(l *launcherLayout) { l.PIDs = 1 }},
		{"tmpfs", func(l *launcherLayout) { l.TmpfsBytes = 1 }},
		{"timeout-zero", func(l *launcherLayout) { l.RunTimeoutSeconds = 0 }},
		{"timeout-high", func(l *launcherLayout) { l.RunTimeoutSeconds = maxLauncherRunTimeoutSeconds + 1 }},
		{"retention-zero", func(l *launcherLayout) { l.ResultRetentionSeconds = 0 }},
		{"retention-high", func(l *launcherLayout) { l.ResultRetentionSeconds = maxLauncherResultRetentionSeconds + 1 }},
		{"jobs-zero", func(l *launcherLayout) { l.MaxJobs = 0 }},
		{"jobs-high", func(l *launcherLayout) { l.MaxJobs = maxLauncherJobs + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			layout := validLauncherLayout(t)
			test.mutate(&layout)
			if _, err := buildLauncher(layout); err == nil {
				t.Fatal("unsafe launcher layout was accepted")
			}
		})
	}
}

func TestLauncherLayoutStrictlyRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.json")
	if err := os.WriteFile(path, []byte(`{"Unknown":"value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var layout launcherLayout
	if err := daemon.ReadJSON(path, &layout); err == nil {
		t.Fatal("unknown launcher layout field was accepted")
	}
}

func TestRequireLauncherRoot(t *testing.T) {
	if err := requireLauncherRoot(0); err != nil {
		t.Fatal(err)
	}
	if err := requireLauncherRoot(1000); err == nil {
		t.Fatal("non-root launcher was accepted")
	}
}

func TestLauncherLayoutDoesNotContainRequestAuthorityTypes(t *testing.T) {
	layout := validLauncherLayout(t)
	options, err := buildLauncher(layout)
	if err != nil {
		t.Fatal(err)
	}
	var _ applauncher.Runner = options.Runner
}
