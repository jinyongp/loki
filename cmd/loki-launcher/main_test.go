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
	"loki/internal/work/jobs"
)

func validLauncherLayout(t *testing.T) launcherLayout {
	t.Helper()
	stateDirectory := filepath.Join(t.TempDir(), "jobs")
	if err := os.Mkdir(stateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	return launcherLayout{
		Socket:                   filepath.Join(t.TempDir(), "launcher.sock"),
		SocketGID:                os.Getgid(),
		ExecutorUID:              1001,
		StateDirectory:           stateDirectory,
		DockerSocket:             filepath.Join(t.TempDir(), "docker.sock"),
		DockerPeerUID:            0,
		PolicySHA256:             strings.Repeat("a", 64),
		Image:                    "registry.example/loki@sha256:" + strings.Repeat("b", 64),
		GatewayImage:             "registry.example/loki-gateway@sha256:" + strings.Repeat("c", 64),
		GatewayBinary:            "/opt/loki/bin/loki",
		GatewayExecutionContract: "/usr/share/doc/loki/execution-contract.json",
		GatewayEgressPolicy:      "/usr/share/doc/loki/egress-policy.json",
		GatewayProxyPort:         18766,
		GatewayMemoryBytes:       128 << 20,
		GatewayPIDs:              64,
		GatewayTmpfsBytes:        32 << 20,
		Workspace:                t.TempDir(),
		ToolchainStore:           filepath.Join(t.TempDir(), "toolchains"),
		Environment:              []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"},
		WorkloadUID:              2001,
		WorkloadGID:              2001,
		MemoryBytes:              512 << 20,
		PIDs:                     128,
		TmpfsBytes:               64 << 20,
		RunTimeoutSeconds:        30,
		ResultRetentionSeconds:   60,
		MaxJobs:                  64,
		MaxOutputBytes:           256 << 10,
	}
}

func TestBuildLauncherConstructsNarrowRoleInputs(t *testing.T) {
	layout := validLauncherLayout(t)
	options, err := buildLauncher(layout)
	if err != nil {
		t.Fatal(err)
	}
	if options.Socket != layout.Socket || options.SocketGID != layout.SocketGID ||
		options.ExecutorUID != layout.ExecutorUID || options.RunTimeout != 30*time.Second {
		t.Fatalf("launcher options = %#v", options)
	}
	if options.Journal != nil {
		t.Fatal("builder opened durable state before runtime ownership")
	}
	if !options.Policy.Valid() {
		t.Fatal("constructed policy is invalid")
	}
	if _, ok := options.Runner.(*sandbox.Engine); !ok {
		t.Fatalf("runner type = %T", options.Runner)
	}
	if options.Toolchains == nil {
		t.Fatal("launcher toolchain resolver is nil")
	}
	if options.Ready != nil {
		t.Fatal("builder installed runtime readiness callback")
	}
}

func TestOpenLauncherJournalOwnsConfiguredPrivateState(t *testing.T) {
	layout := validLauncherLayout(t)
	journal, err := openLauncherJournal(layout)
	if err != nil {
		t.Fatal(err)
	}
	if journal.MaxOutputBytes() != layout.MaxOutputBytes {
		t.Fatalf("journal output limit = %d", journal.MaxOutputBytes())
	}
	if _, err = openLauncherJournal(layout); err == nil {
		t.Fatal("second launcher journal owner was accepted")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openLauncherJournal(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
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
		{"state-relative", func(l *launcherLayout) { l.StateDirectory = "jobs" }},
		{"state-root", func(l *launcherLayout) { l.StateDirectory = "/" }},
		{"shared-identity", func(l *launcherLayout) { l.ExecutorUID = l.WorkloadUID }},
		{"workload-root", func(l *launcherLayout) { l.WorkloadUID = 0 }},
		{"workload-root-group", func(l *launcherLayout) { l.WorkloadGID = 0 }},
		{"docker-relative", func(l *launcherLayout) { l.DockerSocket = "docker.sock" }},
		{"policy-digest", func(l *launcherLayout) { l.PolicySHA256 = "bad" }},
		{"image-tag", func(l *launcherLayout) { l.Image = "loki:latest" }},
		{"gateway-image", func(l *launcherLayout) { l.GatewayImage = "loki:latest" }},
		{"gateway-binary", func(l *launcherLayout) { l.GatewayBinary = "loki" }},
		{"gateway-contract", func(l *launcherLayout) { l.GatewayExecutionContract = "execution.json" }},
		{"gateway-policy", func(l *launcherLayout) { l.GatewayEgressPolicy = "egress.json" }},
		{"gateway-port", func(l *launcherLayout) { l.GatewayProxyPort = 80 }},
		{"gateway-memory", func(l *launcherLayout) { l.GatewayMemoryBytes = 1 }},
		{"gateway-pids", func(l *launcherLayout) { l.GatewayPIDs = 1 }},
		{"gateway-tmpfs", func(l *launcherLayout) { l.GatewayTmpfsBytes = 1 }},
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
		{"output-zero", func(l *launcherLayout) { l.MaxOutputBytes = 0 }},
		{"output-high", func(l *launcherLayout) { l.MaxOutputBytes = jobs.MaxOutputBytes + 1 }},
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
